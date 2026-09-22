// Command ledger is mopay's append-only double-entry ledger (PRD §5.5).
//
// It is an internal service: only collections-api and settlement-worker call it
// (README hard constraint 8).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mopay/ledger/internal/api"
	"github.com/mopay/ledger/internal/collections"
	"github.com/mopay/ledger/internal/config"
	"github.com/mopay/ledger/internal/platform"
	"github.com/mopay/ledger/internal/store"
)

func main() {
	// The runtime image is distroless: no shell, no curl. A container healthcheck
	// therefore re-execs this binary with -healthcheck and checks the exit code.
	healthcheck := flag.Bool("healthcheck", false, "probe the local health endpoint and exit")
	flag.Parse()
	if *healthcheck {
		os.Exit(probe())
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ledger failed to start: %v\n", err)
		os.Exit(1)
	}
}

// probe returns 0 when the local health endpoint reports ok.
func probe() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: HTTP %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

// How often the pool is sampled, and the saturation at which that sampling becomes a
// warning. 90% is chosen so the warning precedes the failure rather than accompanying it.
const (
	poolSampleInterval = 10 * time.Second
	poolWarnAtPct      = 90
)

func run() error {
	// Config is validated before anything else, and a bad value names itself.
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := platform.NewLogger("ledger", cfg.LogLevel)
	reg := platform.NewRegistry(platform.Labels{"service": "ledger"})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := openPool(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	// The configured ceiling, stated once at startup. It is not derivable from anything
	// else in the log stream, and it is the number that has to change when the pool and
	// the database disagree about how many connections exist.
	log.Info("database pool configured",
		"db_max_conns", cfg.DBMaxConns,
		"db_min_conns", cfg.DBMinConns)

	st := store.New(pool, cfg.HouseAccountID)
	cs := collections.New(pool)

	// Publish the pool continuously, not only when a request fails. An exhausted pool is
	// load-dependent: it is healthy at rest and fails under concurrency, so a signal that
	// only appears at the moment of failure arrives too late to be diagnostic.
	go platform.WatchPool(ctx, st, log, reg, poolSampleInterval, poolWarnAtPct)

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           api.New(cfg, st, cs, log, reg).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("ledger listening",
			"port", cfg.Port, "data_region", cfg.DataRegion, "house_account_id", cfg.HouseAccountID)
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
	log.Info("ledger stopped cleanly")
	return nil
}

// openPool connects and waits for Postgres to accept queries. Compose starts containers
// in parallel, so a service that exits on first connection failure produces a flaky stack.
func openPool(ctx context.Context, cfg *config.Config, log interface{ Info(string, ...any) }) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL is not a valid Postgres URL: %w", err)
	}
	// Constraint 5: pool size is injected. Note that MaxConns is a per-COMPONENT
	// ceiling, while Postgres enforces max_connections across every component at once -
	// the two are sized together or not at all.
	poolCfg.MaxConns = cfg.DBMaxConns
	poolCfg.MinConns = cfg.DBMinConns

	// Name the connections so pg_stat_activity attributes them to a component. Without
	// this every pool looks identical from the database side, and "who is holding the
	// connections" - the first question anyone asks when they run out - is unanswerable.
	poolCfg.ConnConfig.RuntimeParams["application_name"] = "ledger"

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create Postgres pool: %w", err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			log.Info("connected to postgres", "attempt", attempt)
			return pool, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf("postgres did not accept a connection within 60s (%d attempts): %w", attempt, err)
		}
		time.Sleep(time.Second)
	}
}
