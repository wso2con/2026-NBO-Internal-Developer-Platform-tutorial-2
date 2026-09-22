package collections

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mopay/ledger/internal/platform"
)

// SearchParams is C-1.10's filter set at the scope agreed in SCOPE.md (THIN):
// merchant reference, status and date range. MerchantID is always set server-side from
// the token (C-6.2) and is never taken from the query string.
type SearchParams struct {
	MerchantID        string
	MerchantReference string
	Status            string
	From              *time.Time
	To                *time.Time
	Limit             int
	Offset            int
}

func (s *Store) SearchCollections(ctx context.Context, p SearchParams) ([]Collection, int64, error) {
	where := []string{"merchant_id = $1"}
	args := []any{p.MerchantID}

	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if p.MerchantReference != "" {
		add("merchant_reference = $%d", p.MerchantReference)
	}
	if p.Status != "" {
		add("status = $%d", p.Status)
	}
	if p.From != nil {
		add("created_at >= $%d", p.From.UTC())
	}
	if p.To != nil {
		add("created_at < $%d", p.To.UTC())
	}
	clause := strings.Join(where, " AND ")

	var total int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*)::bigint FROM collections WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count collections for merchant %s: %w", p.MerchantID, err)
	}

	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	args = append(args, p.Limit, p.Offset)
	rows, err := s.pool.Query(ctx, `
		SELECT id, merchant_id, channel, amount_minor, currency, customer_reference,
		       merchant_reference, origin_country, status, created_at, cleared_at,
		       settled_at, settlement_line_id
		  FROM collections WHERE `+clause+
		fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("search collections for merchant %s: %w", p.MerchantID, err)
	}
	defer rows.Close()

	out := []Collection{}
	for rows.Next() {
		var c Collection
		var amt int64
		var channel, currency string
		if err := rows.Scan(&c.ID, &c.MerchantID, &channel, &amt, &currency, &c.CustomerReference,
			&c.MerchantReference, &c.OriginCountry, &c.Status, &c.CreatedAt,
			&c.ClearedAt, &c.SettledAt, &c.SettlementLineID); err != nil {
			return nil, 0, fmt.Errorf("scan collection for merchant %s: %w", p.MerchantID, err)
		}
		c.Channel = platform.Channel(channel)
		c.Currency = platform.Currency(currency)
		c.AmountMinor = platform.Minor(amt)
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// Aggregates implements C-1.12 and feeds S-1.
func (s *Store) Aggregates(ctx context.Context, merchantID string, from, to time.Time) (Aggregates, error) {
	a := Aggregates{MerchantID: merchantID, From: from.UTC(), To: to.UTC(), ByChannel: []ChannelAggregate{}, Hourly: []HourlyPoint{}}

	rows, err := s.pool.Query(ctx, `
		SELECT channel,
		       COUNT(*)::bigint,
		       COALESCE(SUM(CASE WHEN status <> 'failed' THEN amount_minor ELSE 0 END),0)::bigint,
		       COUNT(*) FILTER (WHERE status = 'failed')::bigint,
		       MAX(currency)
		  FROM collections
		 WHERE merchant_id = $1 AND created_at >= $2 AND created_at < $3
		 GROUP BY channel ORDER BY channel`, merchantID, a.From, a.To)
	if err != nil {
		return a, fmt.Errorf("aggregate collections for merchant %s over %s..%s: %w",
			merchantID, a.From.Format(time.RFC3339), a.To.Format(time.RFC3339), err)
	}
	defer rows.Close()

	for rows.Next() {
		var ca ChannelAggregate
		var channel string
		var val int64
		var cur *string
		if err := rows.Scan(&channel, &ca.Count, &val, &ca.FailedCount, &cur); err != nil {
			return a, fmt.Errorf("scan aggregate row for merchant %s: %w", merchantID, err)
		}
		ca.Channel = platform.Channel(channel)
		ca.ValueMinor = platform.Minor(val)
		if cur != nil {
			a.Currency = platform.Currency(*cur)
		}
		a.Count += ca.Count
		a.ValueMinor += ca.ValueMinor
		a.FailedCount += ca.FailedCount
		a.ByChannel = append(a.ByChannel, ca)
	}
	if err := rows.Err(); err != nil {
		return a, err
	}
	if a.Count > 0 {
		a.SuccessRate = float64(a.Count-a.FailedCount) / float64(a.Count)
	}

	// 24-hour sparkline (S-1).
	hrows, err := s.pool.Query(ctx, `
		SELECT date_trunc('hour', created_at) AS h,
		       COUNT(*)::bigint,
		       COALESCE(SUM(CASE WHEN status <> 'failed' THEN amount_minor ELSE 0 END),0)::bigint
		  FROM collections
		 WHERE merchant_id = $1 AND created_at >= $2 AND created_at < $3
		 GROUP BY h ORDER BY h`, merchantID, a.From, a.To)
	if err != nil {
		return a, fmt.Errorf("build hourly series for merchant %s: %w", merchantID, err)
	}
	defer hrows.Close()
	for hrows.Next() {
		var p HourlyPoint
		var val int64
		if err := hrows.Scan(&p.Hour, &p.Count, &val); err != nil {
			return a, fmt.Errorf("scan hourly point for merchant %s: %w", merchantID, err)
		}
		p.ValueMinor = platform.Minor(val)
		a.Hourly = append(a.Hourly, p)
	}
	return a, hrows.Err()
}

// ListRuns implements C-1.13. merchantID empty means unfiltered, which only an
// operations or finance token reaches - the API layer enforces that, not this function.
func (s *Store) ListRuns(ctx context.Context, merchantID, region string, limit int) ([]SettlementRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var (
		rows interface {
			Next() bool
			Scan(...any) error
			Close()
			Err() error
		}
		err error
	)

	if merchantID != "" {
		rows, err = s.pool.Query(ctx, `
			SELECT r.id, r.run_type, r.status, r.started_at, r.ended_at,
			       COALESCE(l.row_count, 0)::bigint, r.failure_reason, r.period_start,
			       r.period_end, r.region, l.net_minor, l.currency
			  FROM settlement_runs r
			  JOIN settlement_lines l ON l.run_id = r.id AND l.merchant_id = $1
			 WHERE r.region = $2
			 ORDER BY r.started_at DESC LIMIT $3`, merchantID, region, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT r.id, r.run_type, r.status, r.started_at, r.ended_at,
			       r.row_count, r.failure_reason, r.period_start, r.period_end, r.region,
			       NULL::bigint, NULL::text
			  FROM settlement_runs r
			 WHERE r.region = $1
			 ORDER BY r.started_at DESC LIMIT $2`, region, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list settlement runs (merchant=%q region=%s): %w", merchantID, region, err)
	}
	defer rows.Close()

	out := []SettlementRun{}
	for rows.Next() {
		var r SettlementRun
		var net *int64
		var cur *string
		if err := rows.Scan(&r.ID, &r.RunType, &r.Status, &r.StartedAt, &r.EndedAt,
			&r.RowCount, &r.FailureReason, &r.PeriodStart, &r.PeriodEnd, &r.Region, &net, &cur); err != nil {
			return nil, fmt.Errorf("scan settlement run row: %w", err)
		}
		if net != nil {
			m := platform.Minor(*net)
			r.NetMinor = &m
		}
		if cur != nil {
			c := platform.Currency(*cur)
			r.Currency = &c
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Statement implements C-1.14 as JSON (SCOPE.md conflict 3 drops CSV).
// month is "YYYY-MM".
func (s *Store) Statement(ctx context.Context, merchantID, month string) (Statement, error) {
	st := Statement{MerchantID: merchantID, Month: month, ByChannel: []StatementLine{}, RunIDs: []string{}}

	start, err := time.Parse("2006-01", month)
	if err != nil {
		return st, platform.Errorf(400, platform.CodeBadRequest,
			"month %q is not in YYYY-MM form", month)
	}
	st.PeriodStart = start.UTC()
	st.PeriodEnd = start.AddDate(0, 1, 0).UTC()

	if err := s.pool.QueryRow(ctx,
		`SELECT name FROM merchants WHERE id = $1`, merchantID).Scan(&st.MerchantName); err != nil {
		return st, fmt.Errorf("read merchant %s for statement %s: %w", merchantID, month, err)
	}

	// Settlement totals come from the immutable settlement lines, not recomputed.
	rows, err := s.pool.Query(ctx, `
		SELECT l.id, l.run_id, l.gross_minor, l.fees_minor, l.net_minor, l.currency, l.row_count
		  FROM settlement_lines l
		  JOIN settlement_runs r ON r.id = l.run_id
		 WHERE l.merchant_id = $1 AND r.period_start >= $2 AND r.period_start < $3
		 ORDER BY l.created_at`, merchantID, st.PeriodStart, st.PeriodEnd)
	if err != nil {
		return st, fmt.Errorf("read settlement lines for merchant %s month %s: %w", merchantID, month, err)
	}
	defer rows.Close()

	seenRun := map[string]bool{}
	for rows.Next() {
		var id, runID, cur string
		var gross, fees, net, rc int64
		if err := rows.Scan(&id, &runID, &gross, &fees, &net, &cur, &rc); err != nil {
			return st, fmt.Errorf("scan settlement line for statement %s: %w", month, err)
		}
		st.GrossMinor += platform.Minor(gross)
		st.FeesMinor += platform.Minor(fees)
		st.NetMinor += platform.Minor(net)
		st.RowCount += rc
		st.Currency = platform.Currency(cur)
		if !seenRun[runID] {
			seenRun[runID] = true
			st.RunIDs = append(st.RunIDs, runID)
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}

	// Per-channel breakdown, over EXACTLY the collections covered by the settlement lines
	// counted above.
	//
	// It is tempting to filter these by `settled_at` in the month, but that is a
	// different set: a run for the last day of August settles on 1 September, so those
	// collections have a September settled_at while their line belongs to August. Using
	// the two bases together made the statement's headline totals disagree with its own
	// breakdown. The line is the authority, so the breakdown joins through it.
	crows, err := s.pool.Query(ctx, `
		SELECT c.channel, COUNT(*)::bigint, COALESCE(SUM(c.amount_minor),0)::bigint
		  FROM collections c
		  JOIN settlement_lines l ON l.id = c.settlement_line_id
		  JOIN settlement_runs r  ON r.id = l.run_id
		 WHERE c.merchant_id = $1 AND c.status = 'settled'
		   AND r.period_start >= $2 AND r.period_start < $3
		 GROUP BY c.channel ORDER BY c.channel`, merchantID, st.PeriodStart, st.PeriodEnd)
	if err != nil {
		return st, fmt.Errorf("read per-channel breakdown for merchant %s month %s: %w", merchantID, month, err)
	}
	defer crows.Close()
	for crows.Next() {
		var line StatementLine
		var channel string
		var gross int64
		if err := crows.Scan(&channel, &line.Count, &gross); err != nil {
			return st, fmt.Errorf("scan channel breakdown for statement %s: %w", month, err)
		}
		line.Channel = platform.Channel(channel)
		line.GrossMinor = platform.Minor(gross)
		// Apportion the month's fees across channels by gross value.
		if st.GrossMinor > 0 {
			line.FeesMinor = platform.Minor(int64(st.FeesMinor) * gross / int64(st.GrossMinor))
		}
		line.NetMinor = line.GrossMinor - line.FeesMinor
		st.ByChannel = append(st.ByChannel, line)
	}
	return st, crows.Err()
}
