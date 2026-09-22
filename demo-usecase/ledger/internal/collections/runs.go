package collections

import (
	"context"
	"fmt"
	"time"

	"github.com/mopay/ledger/internal/platform"
)

// Settlement-run persistence. These moved here from settlement-worker when `ledger`
// became the only component holding a Postgres connection; the SQL is unchanged.

type InsertRunRequest struct {
	RunID       string    `json:"runId"`
	RunType     string    `json:"runType"`
	Region      string    `json:"region"`
	PeriodStart time.Time `json:"periodStart"`
	PeriodEnd   time.Time `json:"periodEnd"`
	StartedAt   time.Time `json:"startedAt"`
}

// InsertRun records the run before any work happens, so a run that dies mid-way is still
// visible as 'running' rather than vanishing (C-2.5, S-6.1).
func (s *Store) InsertRun(ctx context.Context, r InsertRunRequest) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO settlement_runs
		    (id, run_type, status, started_at, row_count, period_start, period_end, region)
		VALUES ($1,$2,'running',$3,0,$4,$5,$6)`,
		r.RunID, r.RunType, r.StartedAt, r.PeriodStart, r.PeriodEnd, r.Region)
	if err != nil {
		return fmt.Errorf("insert settlement run %s (%s, region %s): %w", r.RunID, r.RunType, r.Region, err)
	}
	return nil
}

type FinishRunRequest struct {
	RunID         string `json:"runId"`
	Status        string `json:"status"`
	RowCount      int64  `json:"rowCount"`
	FailureReason string `json:"failureReason,omitempty"`
}

// FinishRun closes the run record. C-2.5 requires the failure reason to be persisted, and
// README.md requires it to name the run, the merchant, the row and the cause.
func (s *Store) FinishRun(ctx context.Context, r FinishRunRequest) error {
	var reason *string
	if r.FailureReason != "" {
		reason = &r.FailureReason
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE settlement_runs
		   SET status = $1, ended_at = $2, row_count = $3, failure_reason = $4
		 WHERE id = $5`,
		r.Status, time.Now().UTC(), r.RowCount, reason, r.RunID); err != nil {
		return fmt.Errorf("record outcome %q for settlement run %s: %w", r.Status, r.RunID, err)
	}
	return nil
}

// MonthlyStatement is one merchant's month-end reconciliation total (C-2.2).
// OBS-7: merchant-level totals only - no customer references.
type MonthlyStatement struct {
	MerchantID string            `json:"merchantId"`
	Name       string            `json:"name"`
	GrossMinor platform.Minor    `json:"grossMinor"`
	FeesMinor  platform.Minor    `json:"feesMinor"`
	NetMinor   platform.Minor    `json:"netMinor"`
	RowCount   int64             `json:"rowCount"`
	Currency   platform.Currency `json:"currency"`
}

// MonthlyStatements is C-2.2's monthly statement production. Statements themselves are
// derived from the immutable settlement lines and served by collections-api (C-1.14);
// what the month-end run adds is the reconciliation pass and a per-merchant record of it.
func (s *Store) MonthlyStatements(ctx context.Context, region string, periodStart, periodEnd time.Time) ([]MonthlyStatement, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.merchant_id, m.name,
		       SUM(l.gross_minor)::bigint, SUM(l.fees_minor)::bigint,
		       SUM(l.net_minor)::bigint, SUM(l.row_count)::bigint, MAX(l.currency)
		  FROM settlement_lines l
		  JOIN settlement_runs r ON r.id = l.run_id
		  JOIN merchants m ON m.id = l.merchant_id
		 WHERE r.period_start >= $1 AND r.period_start < $2 AND m.data_region = $3
		 GROUP BY l.merchant_id, m.name ORDER BY l.merchant_id`,
		periodStart, periodEnd, region)
	if err != nil {
		return nil, fmt.Errorf("produce month-end statements for region %s over %s..%s: %w",
			region, periodStart.Format(time.RFC3339), periodEnd.Format(time.RFC3339), err)
	}
	defer rows.Close()

	out := []MonthlyStatement{}
	for rows.Next() {
		var st MonthlyStatement
		var gross, fees, net int64
		if err := rows.Scan(&st.MerchantID, &st.Name, &gross, &fees, &net, &st.RowCount, &st.Currency); err != nil {
			return nil, fmt.Errorf("read month-end statement row for region %s: %w", region, err)
		}
		st.GrossMinor, st.FeesMinor, st.NetMinor = platform.Minor(gross), platform.Minor(fees), platform.Minor(net)
		out = append(out, st)
	}
	return out, rows.Err()
}
