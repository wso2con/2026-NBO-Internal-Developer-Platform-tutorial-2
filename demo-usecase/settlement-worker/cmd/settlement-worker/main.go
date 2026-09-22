// Command settlement-worker performs nightly and month-end settlement runs (PRD §5.3).
//
// It is a scheduled task with NO inbound HTTP surface (PRD §4.1). Run state is persisted
// and read back through collections-api.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mopay/settlement-worker/internal/config"
	"github.com/mopay/settlement-worker/internal/ledgerclient"
	"github.com/mopay/settlement-worker/internal/platform"
	"github.com/mopay/settlement-worker/internal/runner"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "settlement-worker failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := platform.NewLogger("settlement-worker", cfg.LogLevel)
	reg := platform.NewRegistry(platform.Labels{"service": "settlement-worker"})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// settlement-worker holds NO database connection.
	lc := ledgerclient.New(cfg.LedgerBaseURL, cfg.LedgerTimeout)
	r := runner.New(cfg, lc, log, reg)

	// C-2.6 demonstration hook. Empty in normal operation.
	if id := os.Getenv("SETTLEMENT_FAIL_AT_MERCHANT"); id != "" {
		log.Warn("induced-failure mode is active: the run will fail when it reaches this merchant",
			"merchant_id", id)
		r.FailAtMerchant(id)
	}

	log.Info("settlement-worker starting",
		"run_mode", cfg.RunMode,
		"run_interval", cfg.RunInterval.String(),
		"data_region", cfg.DataRegion,
		"ledger_base_url", cfg.LedgerBaseURL,
		"period_hours", cfg.PeriodHours,
		"force_month_end", cfg.ForceMonthEnd)

	if cfg.RunMode == "once" {
		res, err := r.Run(ctx, time.Now().UTC())
		if err != nil {
			return err
		}
		log.Info("single run finished",
			"run_id", res.RunID, "rows", res.RowsProcessed, "merchants", res.MerchantsSettled)
		return nil
	}

	t := time.NewTicker(cfg.RunInterval)
	defer t.Stop()
	for {
		if _, err := r.Run(ctx, time.Now().UTC()); err != nil {
			// A failed run is recorded and alerted on; the worker keeps its schedule
			// rather than exiting (NFR-10).
			log.Error("settlement run failed; continuing on schedule", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			log.Info("settlement-worker stopped cleanly")
			return nil
		case <-t.C:
		}
	}
}
