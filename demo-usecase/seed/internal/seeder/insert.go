package seeder

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mopay/seed/internal/platform"
)

// Bulk inserts use COPY. 180,000 collections plus ~350,000 ledger entries is far too
// much for row-at-a-time INSERT if `make seed` is to finish in seconds.

// ensureHouseAccount recreates the ledger's counterparty row.
//
// Migration 003 creates it, but `seed reset` TRUNCATEs merchants, which takes it with it -
// and migrations do not re-run. Without this row every double-entry write fails its
// foreign key and clearing a collection breaks. See migration 003 for the sign convention.
func ensureHouseAccount(ctx context.Context, pool *pgxpool.Pool, houseID, region string) error {
	currency := "KES"
	if region == "NG" {
		currency = "NGN"
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO merchants
		    (id, name, country, data_region, payout_bank, payout_account_number,
		     payout_account_name, float_limit_minor, float_limit_currency)
		VALUES ($1, 'mopay house account', $2, $2, 'n/a', 'n/a', 'n/a', 0, $3)
		ON CONFLICT (id) DO NOTHING`, houseID, region, currency)
	if err != nil {
		return fmt.Errorf("ensure house account %s exists in region %s: %w", houseID, region, err)
	}
	return nil
}

func insertMerchants(ctx context.Context, pool *pgxpool.Pool, specs []MerchantSpec) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin merchant insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for i, m := range specs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO merchants
			    (id, name, country, data_region, payout_bank, payout_account_number,
			     payout_account_name, float_limit_minor, float_limit_currency)
			VALUES ($1,$2,$3,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (id) DO NOTHING`,
			m.ID, m.Name, string(m.Country), m.Bank,
			fmt.Sprintf("%010d", 1000000000+i*7919),
			m.Name, int64(m.FloatLimit), string(m.Currency)); err != nil {
			return fmt.Errorf("insert merchant %s: %w", m.ID, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO fee_schedules (merchant_id, channel, percentage_bp, fixed_minor)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (merchant_id, channel) DO NOTHING`,
			m.ID, string(m.Channel), m.PercentageBP, int64(m.FixedMinor)); err != nil {
			return fmt.Errorf("insert fee schedule for merchant %s channel %s: %w",
				m.ID, m.Channel, err)
		}
	}
	return tx.Commit(ctx)
}

func insertRuns(ctx context.Context, pool *pgxpool.Pool, runs []runRow) error {
	_, err := pool.CopyFrom(ctx,
		pgx.Identifier{"settlement_runs"},
		[]string{"id", "run_type", "status", "started_at", "ended_at",
			"row_count", "failure_reason", "period_start", "period_end", "region"},
		pgx.CopyFromSlice(len(runs), func(i int) ([]any, error) {
			r := runs[i]
			return []any{r.id, r.runType, r.status, r.startedAt, r.endedAt,
				r.rowCount, r.failureReason, r.periodStart, r.periodEnd, r.region}, nil
		}))
	if err != nil {
		return fmt.Errorf("bulk insert %d settlement runs: %w", len(runs), err)
	}
	return nil
}

func insertCollections(ctx context.Context, pool *pgxpool.Pool, cols []collection) error {
	_, err := pool.CopyFrom(ctx,
		pgx.Identifier{"collections"},
		[]string{"id", "merchant_id", "channel", "amount_minor", "currency",
			"customer_reference", "merchant_reference", "origin_country", "status",
			"created_at", "cleared_at", "settled_at", "settlement_line_id"},
		pgx.CopyFromSlice(len(cols), func(i int) ([]any, error) {
			c := cols[i]
			return []any{c.id, c.merchant, c.channel, int64(c.amount), c.currency,
				c.custRef, c.merchRef, c.country, c.status,
				c.createdAt, c.clearedAt, c.settledAt, c.lineID}, nil
		}))
	if err != nil {
		return fmt.Errorf("bulk insert %d collections: %w", len(cols), err)
	}
	return nil
}

func insertLines(ctx context.Context, pool *pgxpool.Pool, lines []lineRow) error {
	_, err := pool.CopyFrom(ctx,
		pgx.Identifier{"settlement_lines"},
		[]string{"id", "run_id", "merchant_id", "gross_minor", "fees_minor", "net_minor",
			"currency", "row_count", "created_at"},
		pgx.CopyFromSlice(len(lines), func(i int) ([]any, error) {
			l := lines[i]
			return []any{l.id, l.runID, l.merchant, int64(l.gross), int64(l.fees),
				int64(l.net), l.currency, l.rowCount, l.createdAt}, nil
		}))
	if err != nil {
		return fmt.Errorf("bulk insert %d settlement lines: %w", len(lines), err)
	}
	return nil
}

func insertLedger(ctx context.Context, pool *pgxpool.Pool, rows []ledgerRow) error {
	_, err := pool.CopyFrom(ctx,
		pgx.Identifier{"ledger_entries"},
		[]string{"id", "merchant_id", "direction", "amount_minor", "currency",
			"created_at", "event_type", "event_id", "transaction_group_id"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{r.id, r.merchant, r.direction, int64(r.amount), r.currency,
				r.createdAt, r.eventType, r.eventID, r.groupID}, nil
		}))
	if err != nil {
		return fmt.Errorf("bulk insert %d ledger entries: %w", len(rows), err)
	}
	return nil
}

// Reset truncates every seeded table. Requires the owner role, not the application role -
// the application role cannot DELETE from ledger_entries or settlement_lines by design
// (hard constraint 1), which is exactly the behaviour we want in the services.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		TRUNCATE payout_instructions, settlement_lines, settlement_runs,
		         ledger_entries, collections, fee_schedules, merchants
		RESTART IDENTITY CASCADE`)
	if err != nil {
		return fmt.Errorf("truncate seeded tables: %w", err)
	}
	return nil
}

// VerifyBalanced checks that every transaction group sums to zero (C-3.5). The seeder
// writes ledger rows directly, bypassing the service, so it verifies its own output.
func VerifyBalanced(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	rows, err := pool.Query(ctx, `
		SELECT transaction_group_id,
		       SUM(CASE WHEN direction = 'credit' THEN amount_minor ELSE -amount_minor END)::bigint AS s
		  FROM ledger_entries
		 GROUP BY transaction_group_id HAVING SUM(CASE WHEN direction = 'credit'
		       THEN amount_minor ELSE -amount_minor END) <> 0
		 LIMIT 20`)
	if err != nil {
		return 0, fmt.Errorf("verify ledger groups balance: %w", err)
	}
	defer rows.Close()

	bad := 0
	for rows.Next() {
		var group string
		var sum int64
		if err := rows.Scan(&group, &sum); err != nil {
			return bad, err
		}
		bad++
		return bad, fmt.Errorf("ledger group %s does not balance (sum %s)",
			group, platform.Minor(sum))
	}
	return bad, rows.Err()
}
