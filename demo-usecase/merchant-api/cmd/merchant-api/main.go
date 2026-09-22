// Command merchant-api is the system of record for merchant identity.
//
// Unlike ledger, this service is meant to be FOUND. It is an exposed endpoint described
// by api/merchants.openapi.yaml, it holds its own datastore, and other projects on the
// platform are expected to discover its contract and build against it.
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

	"github.com/mopay/merchant-api/internal/api"
	"github.com/mopay/merchant-api/internal/config"
	"github.com/mopay/merchant-api/internal/platform"
	"github.com/mopay/merchant-api/internal/store"
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
		fmt.Fprintf(os.Stderr, "merchant-api failed to start: %v\n", err)
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

func run() error {
	// Config is validated before anything else, and a bad value names itself.
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := platform.NewLogger("merchant-api", cfg.Environment, cfg.LogLevel)
	reg := platform.NewRegistry(platform.Labels{"environment": cfg.Environment, "service": "merchant-api"})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := openPool(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	st := store.New(pool)
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           api.New(cfg, st, log, reg).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("merchant-api listening", "port", cfg.Port)
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
	log.Info("merchant-api stopped cleanly")
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
	poolCfg.ConnConfig.RuntimeParams["application_name"] = "merchant-api"

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
