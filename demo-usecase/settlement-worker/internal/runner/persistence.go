package runner

import (
	"context"
	"log/slog"
	"time"

	"github.com/mopay/settlement-worker/internal/ledgerclient"
)

// Settlement-run persistence.
//
// settlement-worker holds NO database connection. `ledger` is the only component with a
// Postgres pool, so these go over HTTP. The behaviour - including finishRun's detached
// context - is unchanged.

// insertRun records the run before any work happens, so a run that dies mid-way is still
// visible as 'running' rather than vanishing (C-2.5, S-6.1).
func (r *Runner) insertRun(ctx context.Context, runID, runType string, periodStart, periodEnd, startedAt time.Time) error {
	return r.ledger.InsertRun(ctx, ledgerclient.InsertRunRequest{
		RunID:       runID,
		RunType:     runType,
		Region:      r.cfg.DataRegion,
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		StartedAt:   startedAt,
	})
}

// finishRun closes the run record. C-2.5 requires the failure reason to be persisted, and
// README.md requires it to name the run, the merchant, the row and the cause.
func (r *Runner) finishRun(ctx context.Context, runID, status string, rowCount int64, failureReason string) {
	// Use a detached context: a cancelled run must still record why it stopped.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	if err := r.ledger.FinishRun(ctx, ledgerclient.FinishRunRequest{
		RunID:         runID,
		Status:        status,
		RowCount:      rowCount,
		FailureReason: failureReason,
	}); err != nil {
		r.log.Error("could not record settlement run outcome",
			slog.String("run_id", runID),
			slog.String("intended_status", status),
			slog.String("error", err.Error()))
	}
}

// logMonthlyStatements is C-2.2's monthly statement production. Statements themselves are
// derived from the immutable settlement lines and served by collections-api (C-1.14);
// what the month-end run adds is the reconciliation pass and a per-merchant record of it.
func (r *Runner) logMonthlyStatements(ctx context.Context, runID string, periodStart, periodEnd time.Time, log *slog.Logger) {
	statements, err := r.ledger.MonthlyStatements(ctx, r.cfg.DataRegion, periodStart, periodEnd)
	if err != nil {
		log.Error("month-end statement production failed",
			slog.String("run_id", runID), slog.String("error", err.Error()))
		return
	}

	for _, st := range statements {
		// OBS-7: no customer references here. Merchant-level settlement totals only.
		log.Info("monthly statement produced",
			slog.String("merchant_id", st.MerchantID),
			slog.String("month", periodStart.Format("2006-01")),
			slog.Int64("gross_minor", int64(st.GrossMinor)),
			slog.Int64("fees_minor", int64(st.FeesMinor)),
			slog.Int64("net_minor", int64(st.NetMinor)),
			slog.Int64("row_count", st.RowCount),
			slog.String("currency", string(st.Currency)))
	}
	log.Info("month-end reconciliation complete",
		slog.String("month", periodStart.Format("2006-01")),
		slog.Int("statements", len(statements)))
}
