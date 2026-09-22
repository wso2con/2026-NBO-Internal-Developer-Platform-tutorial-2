// Package runner performs nightly and month-end settlement runs (PRD §5.3).
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/mopay/settlement-worker/internal/config"
	"github.com/mopay/settlement-worker/internal/ledgerclient"
	"github.com/mopay/settlement-worker/internal/platform"
)

const (
	RunNightly  = "nightly"
	RunMonthEnd = "monthEnd"
)

type Runner struct {
	cfg *config.Config
	// No pool. `ledger` is the only component that holds a Postgres connection, so every
	// read and write this worker needs goes through it.
	ledger *ledgerclient.Client
	log    *slog.Logger
	reg    *platform.Registry

	// failAtMerchant induces a mid-run failure for the atomicity test (C-2.6).
	failAtMerchant string
}

func New(cfg *config.Config, lc *ledgerclient.Client, log *slog.Logger, reg *platform.Registry) *Runner {
	return &Runner{cfg: cfg, ledger: lc, log: log, reg: reg}
}

// FailAtMerchant induces a failure when the run reaches the given merchant. Used to
// demonstrate C-2.6: a failure affecting one merchant leaves no other merchant
// partially settled.
func (r *Runner) FailAtMerchant(id string) { r.failAtMerchant = id }

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// Result is what one run produced. It is also OBS-3's metric payload.
type Result struct {
	RunID            string
	RunType          string
	RowsProcessed    int64
	MerchantsSettled int64
	Failures         int64
	NetMinor         platform.Minor
	Duration         time.Duration
	FailureReason    string
}

// IsMonthEnd reports whether t is the last business day of its month (C-2.2).
// Business day = Monday to Friday.
func IsMonthEnd(t time.Time) bool {
	t = t.UTC()
	lastOfMonth := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).
		AddDate(0, 1, -1)
	for lastOfMonth.Weekday() == time.Saturday || lastOfMonth.Weekday() == time.Sunday {
		lastOfMonth = lastOfMonth.AddDate(0, 0, -1)
	}
	return t.Year() == lastOfMonth.Year() && t.YearDay() == lastOfMonth.YearDay()
}

// Run performs one settlement run over the period ending now.
func (r *Runner) Run(ctx context.Context, now time.Time) (Result, error) {
	now = now.UTC()

	runType := RunNightly
	periodStart := now.Add(-time.Duration(r.cfg.PeriodHours) * time.Hour)
	if r.cfg.ForceMonthEnd || IsMonthEnd(now) {
		// C-2.2: month-end additionally reconciles the FULL month per merchant.
		runType = RunMonthEnd
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}

	res := Result{RunID: newID("run"), RunType: runType}
	started := time.Now().UTC()

	log := r.log.With(
		slog.String("run_id", res.RunID),
		slog.String("run_type", runType),
		slog.String("region", r.cfg.DataRegion))

	if err := r.insertRun(ctx, res.RunID, runType, periodStart, now, started); err != nil {
		return res, err
	}
	log.Info("settlement run started",
		slog.String("period_start", periodStart.Format(time.RFC3339)),
		slog.String("period_end", now.Format(time.RFC3339)))

	// Hard constraint 3: the whole period in one unpaginated read. The worker holds it.
	pending, err := r.ledger.Pending(ctx, r.cfg.DataRegion, periodStart, now)
	if err != nil {
		reason := fmt.Sprintf("settlement run %s failed reading the pending set for region %s period %s..%s: %v",
			res.RunID, r.cfg.DataRegion, periodStart.Format(time.RFC3339), now.Format(time.RFC3339), err)
		r.finishRun(ctx, res.RunID, "failed", 0, reason)
		log.Error("settlement run failed reading pending settlements", slog.String("error", err.Error()))
		res.FailureReason = reason
		return res, fmt.Errorf("%s", reason)
	}

	byMerchant := groupByMerchant(pending.Items)
	log.Info("pending set read",
		slog.Int("row_count", pending.RowCount),
		slog.Int("merchant_count", len(byMerchant)))

	// Deterministic order, so an induced failure is reproducible.
	merchantIDs := make([]string, 0, len(byMerchant))
	for id := range byMerchant {
		merchantIDs = append(merchantIDs, id)
	}
	sort.Strings(merchantIDs)

	for _, merchantID := range merchantIDs {
		items := byMerchant[merchantID]

		if r.failAtMerchant != "" && merchantID == r.failAtMerchant {
			// C-2.6: stop here. Everything committed before this point is whole, and
			// nothing for THIS merchant has been written - the commit is one transaction.
			reason := fmt.Sprintf(
				"settlement run %s failed for merchant %s at row %d: induced failure (SETTLEMENT_FAIL_AT_MERCHANT)",
				res.RunID, merchantID, res.RowsProcessed+1)
			r.finishRun(ctx, res.RunID, "failed", res.RowsProcessed, reason)
			log.Error("settlement run failed",
				slog.String(platform.FieldMerchant, merchantID),
				slog.Int64("row", res.RowsProcessed+1),
				slog.String("cause", "induced failure"))
			res.Failures++
			res.FailureReason = reason
			res.Duration = time.Since(started)
			r.publishRunMetrics(res)
			return res, fmt.Errorf("%s", reason)
		}

		commit, err := r.settleMerchant(ctx, res.RunID, merchantID, items, now)
		if err != nil {
			// One merchant's failure fails the run, but C-2.6 guarantees no OTHER
			// merchant is left half-written: each merchant is one ledger transaction.
			reason := fmt.Sprintf("settlement run %s failed for merchant %s at row %d: %v",
				res.RunID, merchantID, res.RowsProcessed+1, err)
			r.finishRun(ctx, res.RunID, "failed", res.RowsProcessed, reason)
			log.Error("settlement run failed",
				slog.String(platform.FieldMerchant, merchantID),
				slog.Int64("row", res.RowsProcessed+1),
				slog.String("cause", err.Error()))
			res.Failures++
			res.FailureReason = reason
			res.Duration = time.Since(started)
			r.publishRunMetrics(res)
			return res, fmt.Errorf("%s", reason)
		}

		res.RowsProcessed += int64(len(items))
		if !commit.AlreadySettled {
			res.MerchantsSettled++
			res.NetMinor += commit.Line.NetMinor
		}
	}

	res.Duration = time.Since(started)
	r.finishRun(ctx, res.RunID, "completed", res.RowsProcessed, "")

	log.Info("settlement run completed",
		slog.Int64("rows_processed", res.RowsProcessed),
		slog.Int64("merchants_settled", res.MerchantsSettled),
		slog.Int64("duration_ms", res.Duration.Milliseconds()),
		slog.Bool("exceeded_window", res.Duration > r.cfg.RunWindow))

	if res.Duration > r.cfg.RunWindow {
		// NFR-3 / NFR-4: the two-hour window.
		log.Error("settlement run exceeded its window",
			slog.Int64("duration_ms", res.Duration.Milliseconds()),
			slog.Int64("window_ms", r.cfg.RunWindow.Milliseconds()))
	}

	if runType == RunMonthEnd {
		r.logMonthlyStatements(ctx, res.RunID, periodStart, now, log)
	}

	r.publishRunMetrics(res)
	return res, nil
}

// settleMerchant computes fees and commits one merchant atomically.
func (r *Runner) settleMerchant(ctx context.Context, runID, merchantID string, items []ledgerclient.PendingCollection, at time.Time) (ledgerclient.CommitResult, error) {
	var gross, fees platform.Minor
	currency := items[0].Currency
	ids := make([]string, 0, len(items))

	for _, it := range items {
		if it.Currency != currency {
			return ledgerclient.CommitResult{}, fmt.Errorf(
				"merchant %s has collections in more than one currency in this period (%s and %s): refusing to net across currencies",
				merchantID, currency, it.Currency)
		}
		// C-2.8: fee is per merchant AND per channel, so it is computed per collection
		// from that collection's schedule, not once for the merchant.
		if it.FeeSchedule == nil {
			return ledgerclient.CommitResult{}, fmt.Errorf(
				"merchant %s has no fee schedule for channel %s, so collection %s cannot be settled",
				merchantID, it.Channel, it.CollectionID)
		}
		// A cleared collection with no ledger entries means the money was taken but never
		// recorded. Settling it would pay out against a credit that does not exist and
		// drive the merchant's balance negative. Refuse, and name the collection.
		if len(it.LedgerEntries) == 0 {
			return ledgerclient.CommitResult{}, fmt.Errorf(
				"collection %s for merchant %s is cleared but has no ledger entries: refusing to settle an unrecorded collection (replay its channel callback to repair it)",
				it.CollectionID, merchantID)
		}
		gross += it.AmountMinor
		fees += platform.FeeFor(it.AmountMinor, it.FeeSchedule.PercentageBP, it.FeeSchedule.FixedMinor)
		ids = append(ids, it.CollectionID)
	}

	// C-2.7: idempotency is keyed on the collection, not the run. The ledger's commit
	// only moves collections still in 'cleared', so a re-run settles nothing further.
	return r.ledger.Commit(ctx, ledgerclient.CommitRequest{
		RunID:         runID,
		MerchantID:    merchantID,
		CollectionIDs: ids,
		GrossMinor:    gross,
		FeesMinor:     fees,
		NetMinor:      gross - fees,
		Currency:      currency,
		SettledAt:     at,
	})
}

func groupByMerchant(items []ledgerclient.PendingCollection) map[string][]ledgerclient.PendingCollection {
	out := map[string][]ledgerclient.PendingCollection{}
	for _, it := range items {
		out[it.MerchantID] = append(out[it.MerchantID], it)
	}
	return out
}

// publishRunMetrics satisfies OBS-3: rows processed, duration, merchants settled, failures.
func (r *Runner) publishRunMetrics(res Result) {
	labels := platform.Labels{"run_type": res.RunType, "region": r.cfg.DataRegion}
	r.reg.AddCounter("settlement_rows_processed_total", "Collections processed by settlement runs.", labels, float64(res.RowsProcessed))
	r.reg.AddCounter("settlement_merchants_settled_total", "Merchants settled.", labels, float64(res.MerchantsSettled))
	r.reg.AddCounter("settlement_failures_total", "Settlement run failures.", labels, float64(res.Failures))
	r.reg.ObserveHistogram("settlement_run_duration_seconds", "Settlement run duration.",
		[]float64{1, 5, 15, 60, 300, 1800, 7200}, labels, res.Duration.Seconds())
}
