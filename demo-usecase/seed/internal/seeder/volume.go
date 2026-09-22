package seeder

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mopay/seed/internal/platform"
)

type VolumeStats struct {
	RowsAdded            int
	PendingRows          int
	BytesPerRecord       int
	PendingResponseBytes int
}

// Volume inserts additional cleared, unsettled collections for one region at a multiple
// of nightly volume, dated inside the given period.
//
// PRD NFR-4: month-end is roughly 40x the nightly row count and must complete inside the
// same two-hour window. This is the lever that reproduces that scale on demand, so the
// baseline dataset can stay small enough to demo.
//
// It reports the resulting row count AND the serialised size a pending-settlements call
// over that period would now return, because that read is unpaginated (hard constraint 3)
// and the caller holds all of it.
func Volume(ctx context.Context, pool *pgxpool.Pool, region string, multiplier int, period string, opt Options, log *slog.Logger) (VolumeStats, error) {
	var st VolumeStats

	if multiplier <= 0 {
		return st, fmt.Errorf("--multiplier must be positive, got %d", multiplier)
	}

	now := time.Now().UTC()
	var periodStart, periodEnd time.Time
	switch period {
	case "current-month":
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		periodEnd = periodStart.AddDate(0, 1, 0)
	case "today":
		periodStart = now.Truncate(24 * time.Hour)
		periodEnd = periodStart.AddDate(0, 0, 1)
	default:
		return st, fmt.Errorf("--period must be current-month or today, got %q", period)
	}

	specs := Normalised()
	members := specs[:0]
	for _, s := range Normalised() {
		if string(s.Country) == region {
			members = append(members, s)
		}
	}
	if len(members) == 0 {
		return st, fmt.Errorf("no merchants exist for region %s", region)
	}
	totalWeight := 0
	for _, m := range members {
		totalWeight += m.Weight
	}

	// Nightly volume for this region, in proportion to its share of merchants.
	nightly := opt.PerDay * totalWeight / weightOfAll()
	target := nightly * multiplier

	rng := rand.New(rand.NewSource(opt.Seed + int64(multiplier)))
	span := periodEnd.Sub(periodStart)
	if periodEnd.After(now) {
		span = now.Sub(periodStart)
	}

	// A unique suffix so repeated volume runs do not collide on the merchant reference.
	stamp := now.UnixNano()

	cols := make([]collection, 0, target)
	ledger := make([]ledgerRow, 0, target*2)
	for i := 0; i < target; i++ {
		m := pickMerchant(rng, members, totalWeight)
		created := periodStart.Add(time.Duration(rng.Int63n(int64(span))))
		cleared := created.Add(time.Duration(rng.Intn(600)) * time.Second)
		amount := amountFor(rng, m)
		id := fmt.Sprintf("col_vol_%d_%07d", stamp, i)

		cols = append(cols, collection{
			id: id, merchant: m.ID, channel: string(m.Channel), amount: amount,
			currency: string(m.Currency),
			custRef:  fmt.Sprintf("cust_%s_%05d", m.Country, rng.Intn(60000)),
			merchRef: fmt.Sprintf("%s-vol-%d-%07d", m.ID, stamp, i),
			country:  string(m.Country), status: "cleared",
			createdAt: created, clearedAt: &cleared,
		})

		grp := "tg_col_" + id
		ledger = append(ledger,
			ledgerRow{id: fmt.Sprintf("le_vol_%d_%08d_c", stamp, i), merchant: m.ID, direction: "credit",
				amount: amount, currency: string(m.Currency), createdAt: cleared,
				eventType: "collection", eventID: id, groupID: grp},
			ledgerRow{id: fmt.Sprintf("le_vol_%d_%08d_d", stamp, i), merchant: opt.HouseAccountID, direction: "debit",
				amount: amount, currency: string(m.Currency), createdAt: cleared,
				eventType: "collection", eventID: id, groupID: grp})
	}

	if err := insertCollections(ctx, pool, cols); err != nil {
		return st, err
	}
	if err := insertLedger(ctx, pool, ledger); err != nil {
		return st, err
	}
	st.RowsAdded = len(cols)

	// How big is the unpaginated read now?
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM collections c JOIN merchants m ON m.id = c.merchant_id
		 WHERE c.status = 'cleared' AND m.data_region = $1
		   AND c.cleared_at >= $2 AND c.cleared_at < $3`,
		region, periodStart, periodEnd).Scan(&st.PendingRows); err != nil {
		return st, fmt.Errorf("count pending rows for region %s: %w", region, err)
	}

	st.BytesPerRecord = measureRecordSize()
	st.PendingResponseBytes = st.BytesPerRecord * st.PendingRows

	log.Info("pending-settlements capacity for this period",
		slog.String("region", region),
		slog.Int("rows", st.PendingRows),
		slog.Int("bytes_per_record", st.BytesPerRecord),
		slog.String("note", "this read is unpaginated by design (hard constraint 3); the caller holds the whole set"))

	return st, nil
}

func weightOfAll() int {
	total := 0
	for _, s := range MerchantSpecs() {
		total += s.Weight
	}
	return total
}

// measureRecordSize serialises one realistic pending-settlements element and returns its
// size in bytes. The build plan asks for this number explicitly; it is the input to the
// capacity work that follows this build.
func measureRecordSize() int {
	cleared := time.Date(2026, 9, 12, 1, 14, 55, 0, time.UTC)
	sample := map[string]any{
		"collectionId":      "col_seed_0174231",
		"merchantId":        "mch_004",
		"merchantName":      "Thika Road Hardware",
		"channel":           string(platform.ChannelMpesa),
		"amountMinor":       248500,
		"currency":          string(platform.KES),
		"merchantReference": "mch_004-ref-0174231",
		"originCountry":     "KE",
		"createdAt":         cleared.Add(-4 * time.Minute),
		"clearedAt":         cleared,
		"payoutBank":        "Absa Bank Kenya",
		"payoutAccountName": "Thika Road Hardware",
		"feeSchedule": map[string]any{
			"channel": "mpesa", "percentageBp": 175, "fixedMinor": 1500,
		},
		"ledgerEntries": []any{
			map[string]any{
				"id": "le_seed_00348462", "merchantId": "mch_004", "direction": "credit",
				"amountMinor": 248500, "currency": "KES", "createdAt": cleared,
				"eventType": "collection", "eventId": "col_seed_0174231",
				"transactionGroupId": "tg_col_col_seed_0174231",
			},
			map[string]any{
				"id": "le_seed_00348463", "merchantId": "mopay_house", "direction": "debit",
				"amountMinor": 248500, "currency": "KES", "createdAt": cleared,
				"eventType": "collection", "eventId": "col_seed_0174231",
				"transactionGroupId": "tg_col_col_seed_0174231",
			},
		},
	}
	b, err := json.Marshal(sample)
	if err != nil {
		return 0
	}
	return len(b)
}
