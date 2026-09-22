package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mopay/ledger/internal/platform"
)

type Store struct {
	pool           *pgxpool.Pool
	houseAccountID string
}

func New(pool *pgxpool.Pool, houseAccountID string) *Store {
	return &Store{pool: pool, houseAccountID: houseAccountID}
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// PoolStats exposes the pool's live state for diagnostics. `ledger` holds the only
// Postgres connection in mopay, so these numbers are the system's entire demand on the
// database - and the only place the configured ceiling can be compared with what is
// actually in use.
func (s *Store) PoolStats() platform.PoolStats {
	st := s.pool.Stat()
	return platform.PoolStats{
		Acquired:             st.AcquiredConns(),
		Idle:                 st.IdleConns(),
		Total:                st.TotalConns(),
		Max:                  st.MaxConns(),
		EmptyAcquireCount:    st.EmptyAcquireCount(),
		CanceledAcquireCount: st.CanceledAcquireCount(),
	}
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// insufficientPrivilege is Postgres SQLSTATE 42501. Seeing it on ledger_entries or
// settlement_lines means the append-only grant in migration 002 is doing its job.
const insufficientPrivilege = "42501"

func isPrivilegeError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == insufficientPrivilege
}

// ---------------------------------------------------------------------------
// C-3.5 / hard constraint 2 - every ledger write must balance
// ---------------------------------------------------------------------------

// ValidateGroup enforces the sum-zero rule. The error names the group id and the actual
// sum, because a caller that cannot see the imbalance cannot fix it (README.md "Logging").
func ValidateGroup(g EntryGroup) error {
	if g.TransactionGroupID == "" {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"transactionGroupId is required")
	}
	if len(g.Entries) < 2 {
		return platform.Errorf(http.StatusBadRequest, platform.CodeLedgerNotBalanced,
			"ledger write for group %s has %d entries: a balancing write needs at least 2",
			g.TransactionGroupID, len(g.Entries))
	}
	switch g.EventType {
	case EventCollection, EventFee, EventSettlement, EventCorrection:
	default:
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"ledger write for group %s has unknown eventType %q (want collection, fee, settlement or correction)",
			g.TransactionGroupID, g.EventType)
	}
	if g.EventID == "" {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"ledger write for group %s is missing eventId: C-3.4 requires every entry to reference its business event",
			g.TransactionGroupID)
	}

	// Sum per currency. A group mixing currencies cannot balance meaningfully, and
	// silently netting KES against NGN would be a residency and correctness bug.
	sums := map[platform.Currency]platform.Minor{}
	for i, e := range g.Entries {
		if e.MerchantID == "" {
			return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
				"ledger write for group %s entry %d is missing merchantId", g.TransactionGroupID, i)
		}
		if e.Direction != DirectionDebit && e.Direction != DirectionCredit {
			return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
				"ledger write for group %s entry %d has direction %q (want debit or credit)",
				g.TransactionGroupID, i, e.Direction)
		}
		if e.AmountMinor <= 0 {
			return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
				"ledger write for group %s entry %d has amountMinor %d: amounts are positive, direction carries the sign",
				g.TransactionGroupID, i, e.AmountMinor)
		}
		if e.Currency != platform.KES && e.Currency != platform.NGN {
			return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
				"ledger write for group %s entry %d has currency %q (want KES or NGN)",
				g.TransactionGroupID, i, e.Currency)
		}
		sums[e.Currency] += e.Signed()
	}

	if len(sums) > 1 {
		return platform.Errorf(http.StatusUnprocessableEntity, platform.CodeLedgerNotBalanced,
			"ledger write for group %s mixes currencies %v: a transaction group is single-currency",
			g.TransactionGroupID, currencyList(sums))
	}
	for cur, sum := range sums {
		if sum != 0 {
			return platform.Errorf(http.StatusUnprocessableEntity, platform.CodeLedgerNotBalanced,
				"ledger write did not balance (group %s, sum %s %s)",
				g.TransactionGroupID, sum.String(), cur).
				WithDetail("transactionGroupId", g.TransactionGroupID).
				WithDetail("sumMinor", int64(sum)).
				WithDetail("currency", string(cur))
		}
	}
	return nil
}

func currencyList(m map[platform.Currency]platform.Minor) []string {
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, string(c))
	}
	return out
}

// WriteGroup appends a balanced group. Idempotent on transactionGroupId: a repeat returns
// the entries already stored rather than duplicating them, so a caller that retried after
// a timeout does not double-post.
func (s *Store) WriteGroup(ctx context.Context, g EntryGroup) ([]Entry, error) {
	if err := ValidateGroup(g); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin ledger write for group %s: %w", g.TransactionGroupID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := entriesByGroup(ctx, tx, g.TransactionGroupID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return existing, nil
	}

	now := time.Now().UTC()
	out := make([]Entry, 0, len(g.Entries))
	for _, e := range g.Entries {
		e.ID = newID("le")
		e.CreatedAt = now
		e.EventType = g.EventType
		e.EventID = g.EventID
		e.TransactionGroupID = g.TransactionGroupID

		_, err := tx.Exec(ctx, `
			INSERT INTO ledger_entries
			    (id, merchant_id, direction, amount_minor, currency, created_at,
			     event_type, event_id, transaction_group_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			e.ID, e.MerchantID, e.Direction, int64(e.AmountMinor), string(e.Currency),
			e.CreatedAt, e.EventType, e.EventID, e.TransactionGroupID)
		if err != nil {
			if isPrivilegeError(err) {
				return nil, platform.Errorf(http.StatusInternalServerError, platform.CodeAppendOnlyViolation,
					"ledger insert refused by database for group %s: %v", g.TransactionGroupID, err)
			}
			return nil, fmt.Errorf("insert ledger entry for group %s merchant %s: %w",
				g.TransactionGroupID, e.MerchantID, err)
		}
		out = append(out, e)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit ledger write for group %s: %w", g.TransactionGroupID, err)
	}
	return out, nil
}

func entriesByGroup(ctx context.Context, q pgx.Tx, groupID string) ([]Entry, error) {
	rows, err := q.Query(ctx, `
		SELECT id, merchant_id, direction, amount_minor, currency, created_at,
		       event_type, event_id, transaction_group_id
		  FROM ledger_entries WHERE transaction_group_id = $1 ORDER BY id`, groupID)
	if err != nil {
		return nil, fmt.Errorf("read ledger entries for group %s: %w", groupID, err)
	}
	defer rows.Close()
	return scanEntries(rows, groupID)
}

func scanEntries(rows pgx.Rows, ctxLabel string) ([]Entry, error) {
	var out []Entry
	for rows.Next() {
		var e Entry
		var amt int64
		var cur, dir string
		if err := rows.Scan(&e.ID, &e.MerchantID, &dir, &amt, &cur, &e.CreatedAt,
			&e.EventType, &e.EventID, &e.TransactionGroupID); err != nil {
			return nil, fmt.Errorf("scan ledger entry (%s): %w", ctxLabel, err)
		}
		e.Direction = dir
		e.AmountMinor = platform.Minor(amt)
		e.Currency = platform.Currency(cur)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// C-3.2 - balance, including as of a past timestamp
// ---------------------------------------------------------------------------

func (s *Store) Balance(ctx context.Context, merchantID string, asOf time.Time) (Balance, error) {
	b := Balance{MerchantID: merchantID, AsOf: asOf.UTC()}

	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount_minor
		                         ELSE -amount_minor END), 0)::bigint,
		       COUNT(*)::bigint,
		       COALESCE(MAX(currency), '')
		  FROM ledger_entries
		 WHERE merchant_id = $1 AND created_at <= $2`,
		merchantID, b.AsOf).Scan(&b.AmountMinor, &b.EntryCount, &b.Currency)
	if err != nil {
		return b, fmt.Errorf("compute balance for merchant %s as of %s: %w",
			merchantID, b.AsOf.Format(time.RFC3339), err)
	}
	if b.Currency == "" {
		// No entries yet. Fall back to the merchant's configured float limit currency
		// so the caller still gets an explicit currency (README.md "Money").
		_ = s.pool.QueryRow(ctx,
			`SELECT float_limit_currency FROM merchants WHERE id = $1`, merchantID).Scan(&b.Currency)
	}
	return b, nil
}

// MerchantExists lets the API return 404 rather than a zero balance for an unknown id.
func (s *Store) MerchantExists(ctx context.Context, merchantID string) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM merchants WHERE id = $1`, merchantID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up merchant %s: %w", merchantID, err)
	}
	return true, nil
}

// EntriesByEvent returns the entries produced by one business event (C-3.4).
// It is how collections-api satisfies C-1.9 - collection detail including its ledger
// entries - without reading the ledger's tables directly.
func (s *Store) EntriesByEvent(ctx context.Context, eventType, eventID string) ([]Entry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, merchant_id, direction, amount_minor, currency, created_at,
		       event_type, event_id, transaction_group_id
		  FROM ledger_entries
		 WHERE event_type = $1 AND event_id = $2
		 ORDER BY created_at, id`, eventType, eventID)
	if err != nil {
		return nil, fmt.Errorf("read ledger entries for %s %s: %w", eventType, eventID, err)
	}
	defer rows.Close()

	entries, err := scanEntries(rows, eventType+" "+eventID)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []Entry{}
	}
	return entries, nil
}
