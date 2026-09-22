// Command seed generates and manages the development dataset (PRD §12).
//
//	seed baseline   12 merchants, 90 days of collections, runs and balancing ledger entries
//	seed volume     extra cleared, unsettled collections at a multiple of nightly volume
//	seed reset      truncate and reseed from scratch
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mopay/seed/internal/platform"
	"github.com/mopay/seed/internal/seeder"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed failed: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: seed <command> [flags]

commands:
  baseline   generate the PRD §12 dataset (no-op if already seeded)
  volume     add cleared, unsettled collections for one region at a volume multiple
  reset      truncate every seeded table, then run baseline

flags for volume:
  --region KE|NG      required
  --multiplier N      multiple of nightly volume (default 40, per NFR-4)
  --period current-month|today   (default current-month)
`)
}

func run() error {
	if len(os.Args) < 2 {
		usage()
		return fmt.Errorf("no command given")
	}
	cmd := os.Args[1]

	// Every variable is read BEFORE the validation gate below. platform.Env records
	// failures rather than returning them, so a read placed after the gate has its
	// error collected and never checked - which is how an invalid SEED_REGION was
	// silently accepted.
	e := platform.NewEnv()
	databaseURL := e.String("DATABASE_URL")
	house := e.StringDefault("HOUSE_ACCOUNT_ID", "mopay_house")
	seedDays := e.Int("SEED_DAYS", 0)
	seedPerDay := e.Int("SEED_PER_DAY", 0)
	seedRegion := e.OneOfDefault("SEED_REGION", "", "KE", "NG")
	if err := e.Err(); err != nil {
		return err
	}

	log := platform.NewLogger("seed", e.StringDefault("LOG_LEVEL", "info"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("DATABASE_URL is not a valid Postgres URL: %w", err)
	}
	defer pool.Close()

	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := pool.Ping(ctx); err == nil {
			break
		} else if time.Now().After(deadline) {
			return fmt.Errorf("postgres did not accept a connection within 60s: %w", err)
		}
		time.Sleep(time.Second)
	}

	opt := seeder.DefaultOptions()
	opt.HouseAccountID = house
	// Restrict the dataset to one region. Unset seeds both, which is right for a single
	// shared environment. A regional environment must set it: the ledger filters runs by
	// region, so the other region's rows would be invisible to the app and would put data
	// across a boundary that is supposed to be sealed.
	opt.Region = seedRegion
	// PRD §12 sizes the dataset at 90 days x 2,000/day, which is right for a realistic
	// full dataset and far too much for a quick run. Both are overridable so the size is a
	// setting rather than an edit.
	if seedDays > 0 {
		opt.Days = seedDays
	}
	if seedPerDay > 0 {
		opt.PerDay = seedPerDay
	}

	switch cmd {
	case "baseline", "reset":
		if cmd == "reset" {
			log.Info("truncating seeded tables")
			if err := seeder.Reset(ctx, pool); err != nil {
				return err
			}
		}
		st, err := seeder.Baseline(ctx, pool, opt, log)
		if err != nil {
			return err
		}
		if st.Collections > 0 {
			log.Info("baseline seeded",
				"merchants", st.Merchants,
				"collections", st.Collections,
				"unsettled_collections", st.Unsettled,
				"ledger_entries", st.LedgerRows,
				"settlement_runs", st.Runs,
				"settlement_lines", st.Lines,
				"duration_ms", st.Duration.Milliseconds())

			if _, err := seeder.VerifyBalanced(ctx, pool); err != nil {
				return fmt.Errorf("seeded ledger does not balance: %w", err)
			}
			log.Info("verified: every ledger transaction group sums to zero")
		}
		return nil

	case "volume":
		fs := flag.NewFlagSet("volume", flag.ExitOnError)
		region := fs.String("region", "", "KE or NG")
		multiplier := fs.Int("multiplier", 40, "multiple of nightly volume (NFR-4)")
		period := fs.String("period", "current-month", "current-month or today")
		if err := fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		if *region != "KE" && *region != "NG" {
			return fmt.Errorf("--region is required and must be KE or NG, got %q", *region)
		}

		st, err := seeder.Volume(ctx, pool, *region, *multiplier, *period, opt, log)
		if err != nil {
			return err
		}
		// The build plan asks for both numbers to be reported and written down: they are
		// the inputs to the capacity work that follows.
		log.Info("volume seeded",
			"region", *region,
			"multiplier", *multiplier,
			"period", *period,
			"rows_added", st.RowsAdded,
			"pending_rows_total", st.PendingRows,
			"bytes_per_record", st.BytesPerRecord,
			"pending_response_bytes", st.PendingResponseBytes,
			"pending_response_mib", fmt.Sprintf("%.1f", float64(st.PendingResponseBytes)/(1024*1024)))
		return nil

	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}
