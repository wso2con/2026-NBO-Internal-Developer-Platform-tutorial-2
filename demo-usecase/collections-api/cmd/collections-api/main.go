// Command collections-api is mopay's collections intake and read API (PRD §5.1, §5.2).
//
// It also owns the continuously-updated settlement lag metric (README.md hard
// constraint 4): the lag signal must keep being emitted when settlement-worker is not
// running, so it cannot live in the worker.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mopay/collections-api/internal/api"
	"github.com/mopay/collections-api/internal/config"
	"github.com/mopay/collections-api/internal/ledgerclient"
	"github.com/mopay/collections-api/internal/merchantclient"
	"github.com/mopay/collections-api/internal/platform"
	"github.com/mopay/collections-api/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "collections-api failed to start: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Config is validated before anything else, and a bad value names itself.
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := platform.NewLogger("collections-api", cfg.LogLevel)
	reg := platform.NewRegistry(platform.Labels{"service": "collections-api"})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// collections-api holds NO database connection. `ledger` is the only component with
	// a Postgres pool; everything here goes through it over HTTP.
	st := store.New(cfg.LedgerBaseURL, cfg.LedgerTimeout)
	lc := ledgerclient.New(cfg.LedgerBaseURL, cfg.LedgerTimeout)
	mc := merchantclient.New(cfg.MerchantAPIBaseURL, cfg.MerchantAPITimeout, cfg.MerchantCacheTTL)
	a := api.New(cfg, st, lc, mc, log, reg)

	// Hard constraint 4: the lag loop runs here, in-process, independent of the worker.
	go a.StartLagLoop(ctx)

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           a.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("collections-api listening",
			"port", cfg.Port,
			"data_region", cfg.DataRegion,
			"ledger_base_url", cfg.LedgerBaseURL,
			"lag_refresh_interval", cfg.LagRefreshInterval.String(),
			"staleness_threshold", cfg.StalenessThreshold.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server on port %d stopped: %w", cfg.Port, err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received; draining")
	}

	// NFR-10: all state is in the datastore, so a clean drain is all that is required.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown exceeded %s: %w", cfg.ShutdownGrace, err)
	}
	log.Info("collections-api stopped cleanly")
	return nil
}
