// Command loadgen drives peak-hour intake traffic at collections-api.
//
// It exists to reproduce a load-dependent failure on demand: at rest the stack is
// healthy, and only concurrent demand grows the ledger's connection pool far enough to
// collide with the database's shared max_connections.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mopay/loadgen/internal/load"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen failed to start: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := load.LoadConfig()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	duration := "until interrupted"
	if cfg.Duration > 0 {
		duration = cfg.Duration.String()
	}
	rate := "unbounded"
	if cfg.Rate > 0 {
		rate = fmt.Sprintf("%d/s", cfg.Rate)
	}
	fmt.Printf("loadgen -> %s  channel=%s  merchants=%d  concurrency=%d  rate=%s  for %s\n\n",
		cfg.BaseURL, cfg.Channel, len(cfg.MerchantIDs), cfg.Concurrency, rate, duration)

	stats := load.NewStats()
	go load.Report(ctx, stats, cfg.ReportInterval)

	started := time.Now()
	load.NewRunner(cfg, stats).Run(ctx)
	fmt.Print(stats.Final(time.Since(started)))
	return nil
}
