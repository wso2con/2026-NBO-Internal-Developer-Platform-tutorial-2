package seeder

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mopay/seed/internal/platform"
)

type Options struct {
	Days           int
	PerDay         int
	FailureRatePct int
	Seed           int64
	HouseAccountID string
	// Region restricts seeding to one of KE / NG. "" means both, which is right for a
	// single shared environment and wrong for a regional one: the ledger filters runs
	// with WHERE r.region = $1, so rows for the other region are invisible to the app
	// and are exactly the cross-border data a residency boundary is meant to exclude.
	Region string
}

func DefaultOptions() Options {
	// PRD §12: 90 days, ~2,000 collections per day, 3-5% failure rate.
	return Options{Days: 90, PerDay: 2000, FailureRatePct: 4, Seed: 20260910, HouseAccountID: "mopay_house"}
}

type Stats struct {
	Merchants   int
	Collections int
	LedgerRows  int
	Runs        int
	Lines       int
	Unsettled   int
	Duration    time.Duration
}

type collection struct {
	id        string
	merchant  string
	channel   string
	amount    platform.Minor
	currency  string
	custRef   string
	merchRef  string
	country   string
	status    string
	createdAt time.Time
	clearedAt *time.Time
	settledAt *time.Time
	lineID    *string
	day       int
}

type ledgerRow struct {
	id        string
	merchant  string
	direction string
	amount    platform.Minor
	currency  string
	createdAt time.Time
	eventType string
	eventID   string
	groupID   string
}

// Baseline generates PRD §12. It is idempotent: if merchants already exist it reports and
// makes no change, so running it twice cannot double anything.
func Baseline(ctx context.Context, pool *pgxpool.Pool, opt Options, log *slog.Logger) (Stats, error) {
	var st Stats
	start := time.Now()

	var existing int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM collections`).Scan(&existing); err != nil {
		return st, fmt.Errorf("check whether the database is already seeded: %w", err)
	}
	if existing > 0 {
		log.Info("database already contains collections; baseline is a no-op",
			slog.Int("existing_collections", existing),
			slog.String("hint", "use `seed reset` to truncate and reseed"))
		return st, nil
	}

	rng := rand.New(rand.NewSource(opt.Seed))
	specs := Normalised()
	if opt.Region != "" {
		filtered := specs[:0]
		for _, s := range specs {
			if string(s.Country) == opt.Region {
				filtered = append(filtered, s)
			}
		}
		specs = filtered
	}
	totalWeight := 0
	for _, s := range specs {
		totalWeight += s.Weight
	}

	// Midnight UTC today is the anchor. Day 0 is today and stays unsettled, so there is
	// always live work for a settlement run to pick up.
	today := time.Now().UTC().Truncate(24 * time.Hour)

	now := time.Now().UTC()
	cols := make([]collection, 0, opt.Days*opt.PerDay)
	seq := 0
	for day := opt.Days - 1; day >= 0; day-- {
		dayStart := today.AddDate(0, 0, -day)

		// Day 0 is today and is only partly elapsed. Spreading it over a full 24h from
		// midnight would date most of it in the future, which makes settlement lag
		// negative and the whole dataset untrustworthy.
		spanSeconds := 24 * 60 * 60
		if elapsed := int(now.Sub(dayStart).Seconds()); elapsed < spanSeconds {
			spanSeconds = elapsed
		}
		if spanSeconds < 1 {
			spanSeconds = 1
		}

		for i := 0; i < opt.PerDay; i++ {
			m := pickMerchant(rng, specs, totalWeight)
			seq++

			created := dayStart.Add(time.Duration(rng.Intn(spanSeconds)) * time.Second)
			amount := amountFor(rng, m)

			c := collection{
				id:        fmt.Sprintf("col_seed_%07d", seq),
				merchant:  m.ID,
				channel:   string(m.Channel),
				amount:    amount,
				currency:  string(m.Currency),
				custRef:   fmt.Sprintf("cust_%s_%05d", m.Country, rng.Intn(60000)),
				merchRef:  fmt.Sprintf("%s-ref-%07d", m.ID, seq),
				country:   string(m.Country),
				createdAt: created,
				day:       day,
			}

			switch {
			case rng.Intn(100) < opt.FailureRatePct:
				// PRD §12: a realistic 3-5% failure rate.
				c.status = "failed"
			case day == 0:
				// Today's cleared collections are the unsettled float. They drive the
				// float limit (C-1.5) and settlement lag (OBS-4), and give the worker
				// something real to settle.
				cleared := clampToNow(created.Add(time.Duration(rng.Intn(600))*time.Second), now)
				c.status = "cleared"
				c.clearedAt = &cleared
			default:
				cleared := clampToNow(created.Add(time.Duration(rng.Intn(600))*time.Second), now)
				c.status = "settled"
				c.clearedAt = &cleared
			}
			cols = append(cols, c)
		}
	}

	// The two "near float limit" merchants (PRD §12) get a limit just above the unsettled
	// balance they actually accumulated, so the next collection is the one that trips
	// C-1.5. Computed from the generated data rather than guessed.
	unsettledByMerchant := map[string]platform.Minor{}
	for _, c := range cols {
		if c.status == "cleared" {
			unsettledByMerchant[c.merchant] += c.amount
		}
	}
	for i := range specs {
		bal := unsettledByMerchant[specs[i].ID]
		if bal <= 0 {
			continue // keep the declared fallback
		}
		hb := specs[i].HeadroomBP
		if hb <= 0 {
			hb = 30000
		}
		// NearLimit merchants get ~2% headroom (HeadroomBP 10200), so the very next
		// collection trips C-1.5. Everyone else gets a limit sized to their own volume.
		specs[i].FloatLimit = bal * platform.Minor(hb) / 10000
	}

	// The house account must exist before any ledger row references it.
	houseRegion := opt.Region
	if houseRegion == "" {
		houseRegion = "KE"
	}
	if err := ensureHouseAccount(ctx, pool, opt.HouseAccountID, houseRegion); err != nil {
		return st, err
	}
	if err := insertMerchants(ctx, pool, specs); err != nil {
		return st, err
	}
	st.Merchants = len(specs)

	// Settlement runs: one per region per day for every day except today (PRD §12).
	runs, lines, settledLineByDayMerchant := buildRuns(rng, specs, cols, today, opt)
	if err := insertRuns(ctx, pool, runs); err != nil {
		return st, err
	}
	st.Runs = len(runs)

	// Attach settled collections to their line.
	for i := range cols {
		if cols[i].status != "settled" {
			continue
		}
		key := dayMerchantKey{day: cols[i].day, merchant: cols[i].merchant}
		if lineID, ok := settledLineByDayMerchant[key]; ok {
			settled := cols[i].clearedAt.AddDate(0, 0, 1).Truncate(24 * time.Hour).Add(90 * time.Minute)
			cols[i].settledAt = &settled
			cols[i].lineID = &lineID
		} else {
			// No line covers it - leave it cleared rather than claiming it settled.
			cols[i].status = "cleared"
		}
	}

	if err := insertCollections(ctx, pool, cols); err != nil {
		return st, err
	}
	st.Collections = len(cols)

	if err := insertLines(ctx, pool, lines); err != nil {
		return st, err
	}
	st.Lines = len(lines)

	rows := buildLedger(cols, lines, opt.HouseAccountID)
	if err := insertLedger(ctx, pool, rows); err != nil {
		return st, err
	}
	st.LedgerRows = len(rows)

	for _, c := range cols {
		if c.status == "cleared" {
			st.Unsettled++
		}
	}
	st.Duration = time.Since(start)
	return st, nil
}

// clampToNow keeps a generated timestamp from landing in the future.
func clampToNow(t, now time.Time) time.Time {
	if t.After(now) {
		return now
	}
	return t
}

func amountFor(rng *rand.Rand, m *MerchantSpec) platform.Minor {
	// Nigerian naira amounts run an order of magnitude larger than Kenyan shilling ones.
	base := 200
	if m.Country == platform.NG {
		base = 2000
	}
	return platform.Minor((base + rng.Intn(base*12)) * 100)
}

type dayMerchantKey struct {
	day      int
	merchant string
}

type runRow struct {
	id            string
	runType       string
	status        string
	startedAt     time.Time
	endedAt       *time.Time
	rowCount      int64
	failureReason *string
	periodStart   time.Time
	periodEnd     time.Time
	region        string
}

type lineRow struct {
	id        string
	runID     string
	merchant  string
	gross     platform.Minor
	fees      platform.Minor
	net       platform.Minor
	currency  string
	rowCount  int64
	createdAt time.Time
}

func buildRuns(rng *rand.Rand, specs []MerchantSpec, cols []collection, today time.Time, opt Options) ([]runRow, []lineRow, map[dayMerchantKey]string) {
	byRegion := map[string][]MerchantSpec{}
	for _, s := range specs {
		byRegion[string(s.Country)] = append(byRegion[string(s.Country)], s)
	}

	// Totals per (day, merchant) for every collection that is going to settle.
	type agg struct {
		gross platform.Minor
		fees  platform.Minor
		count int64
	}
	totals := map[dayMerchantKey]*agg{}
	feeByMerchant := map[string]MerchantSpec{}
	for _, s := range specs {
		feeByMerchant[s.ID] = s
	}
	for _, c := range cols {
		if c.status != "settled" {
			continue
		}
		k := dayMerchantKey{day: c.day, merchant: c.merchant}
		a := totals[k]
		if a == nil {
			a = &agg{}
			totals[k] = a
		}
		spec := feeByMerchant[c.merchant]
		a.gross += c.amount
		a.fees += platform.FeeFor(c.amount, spec.PercentageBP, spec.FixedMinor)
		a.count++
	}

	var runs []runRow
	var lines []lineRow
	lineFor := map[dayMerchantKey]string{}

	// PRD §12 wants at least one failed run with a reason and at least one that exceeded
	// its window. Pick two specific days so the dataset is deterministic.
	failedDay := 17
	overranDay := 34

	for region, members := range byRegion {
		for day := opt.Days - 1; day >= 1; day-- {
			periodStart := today.AddDate(0, 0, -day)
			periodEnd := periodStart.AddDate(0, 0, 1)
			// Settlement happens at 01:30 local the following day (NFR-3's window).
			startedAt := periodEnd.Add(90 * time.Minute)

			runID := fmt.Sprintf("run_seed_%s_%03d", region, day)
			var rowCount int64
			for _, m := range members {
				if a := totals[dayMerchantKey{day: day, merchant: m.ID}]; a != nil {
					rowCount += a.count
				}
			}

			duration := time.Duration(4+rng.Intn(25)) * time.Minute
			if day == overranDay {
				// S-3.3 / NFR-3: a run that exceeded the two-hour window.
				duration = 2*time.Hour + 37*time.Minute
			}
			ended := startedAt.Add(duration)

			r := runRow{
				id: runID, runType: "nightly", status: "completed",
				startedAt: startedAt, endedAt: &ended, rowCount: rowCount,
				periodStart: periodStart, periodEnd: periodEnd, region: region,
			}
			if periodStart.Day() == 1 {
				// A month boundary sits inside the range, so a month-end run is
				// exercisable (PRD §12).
				r.runType = "monthEnd"
			}
			runs = append(runs, r)

			for _, m := range members {
				k := dayMerchantKey{day: day, merchant: m.ID}
				a := totals[k]
				if a == nil {
					continue
				}
				lineID := fmt.Sprintf("sl_seed_%s_%03d", m.ID, day)
				lines = append(lines, lineRow{
					id: lineID, runID: runID, merchant: m.ID,
					gross: a.gross, fees: a.fees, net: a.gross - a.fees,
					currency: string(m.Currency), rowCount: a.count, createdAt: ended,
				})
				lineFor[k] = lineID
			}

			if day == failedDay {
				// A failed run recorded alongside the successful one for that day: the
				// first attempt failed, a later attempt settled the rows. S-3.2 renders
				// the reason inline, so it has to be a real sentence.
				failStart := startedAt.Add(-45 * time.Minute)
				failEnd := failStart.Add(6 * time.Minute)
				reason := fmt.Sprintf(
					"settlement run run_seed_%s_%03d_a1 failed for merchant %s at row 1841: ledger write did not balance (group tg_99a2, sum -250.00 %s)",
					region, day, members[0].ID, members[0].Currency)
				runs = append(runs, runRow{
					id:        fmt.Sprintf("run_seed_%s_%03d_a1", region, day),
					runType:   "nightly",
					status:    "failed",
					startedAt: failStart, endedAt: &failEnd, rowCount: 0,
					failureReason: &reason,
					periodStart:   periodStart, periodEnd: periodEnd, region: region,
				})
			}
		}
	}
	return runs, lines, lineFor
}

// buildLedger produces the balancing entries for everything (PRD §12: "Ledger entries
// must exist and balance for everything").
func buildLedger(cols []collection, lines []lineRow, house string) []ledgerRow {
	out := make([]ledgerRow, 0, len(cols)*2+len(lines)*4)
	n := 0
	add := func(merchant, dir string, amt platform.Minor, cur string, at time.Time, evType, evID, grp string) {
		n++
		out = append(out, ledgerRow{
			id: fmt.Sprintf("le_seed_%08d", n), merchant: merchant, direction: dir,
			amount: amt, currency: cur, createdAt: at,
			eventType: evType, eventID: evID, groupID: grp,
		})
	}

	for _, c := range cols {
		if c.status == "failed" || c.status == "pending" || c.clearedAt == nil {
			continue // money we never held produces no ledger entry
		}
		grp := "tg_col_" + c.id
		add(c.merchant, "credit", c.amount, c.currency, *c.clearedAt, "collection", c.id, grp)
		add(house, "debit", c.amount, c.currency, *c.clearedAt, "collection", c.id, grp)
	}

	for _, l := range lines {
		if l.fees > 0 {
			grp := "tg_fee_" + l.id
			add(l.merchant, "debit", l.fees, l.currency, l.createdAt, "fee", l.id, grp)
			add(house, "credit", l.fees, l.currency, l.createdAt, "fee", l.id, grp)
		}
		if l.net > 0 {
			grp := "tg_stl_" + l.id
			add(l.merchant, "debit", l.net, l.currency, l.createdAt, "settlement", l.id, grp)
			add(house, "credit", l.net, l.currency, l.createdAt, "settlement", l.id, grp)
		}
	}
	return out
}

var _ = pgx.CopyFromSlice
