// Package collections is the collections-side datastore.
//
// It lives in `ledger` because `ledger` is the ONLY component that holds a Postgres
// connection. collections-api and settlement-worker reach these tables over HTTP, so the
// platform can reason about one pool - one component type, replicas x DB_MAX_CONNS -
// rather than summing pools it cannot see.
package collections

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

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func maskAccount(n string) string {
	if len(n) <= 4 {
		return "****"
	}
	return "****" + n[len(n)-4:]
}

func (s *Store) Merchant(ctx context.Context, id string) (*Merchant, error) {
	var m Merchant
	var acct string
	var limit int64
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, country, data_region, payout_bank, payout_account_number,
		       payout_account_name, float_limit_minor, float_limit_currency, created_at
		  FROM merchants WHERE id = $1`, id).
		Scan(&m.ID, &m.Name, &m.Country, &m.DataRegion, &m.PayoutBank, &acct,
			&m.PayoutAccountName, &limit, &m.FloatLimitCurrency, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, platform.Errorf(http.StatusNotFound, platform.CodeNotFound,
			"merchant %s does not exist", id)
	}
	if err != nil {
		return nil, fmt.Errorf("read merchant %s: %w", id, err)
	}
	// OBS-7 / C-6.3: the full account number never leaves the datastore.
	m.PayoutAccountMask = maskAccount(acct)
	m.FloatLimitMinor = platform.Minor(limit)
	return &m, nil
}

// CreateCollectionRequest is the validated intake payload. Currency and origin country are
// absent by design - C-1.8 derives both from the channel, never from the caller.
type CreateCollectionRequest struct {
	MerchantID        string
	Channel           platform.Channel
	AmountMinor       platform.Minor
	CustomerReference string
	MerchantReference string
}

// CreateCollection persists a collection, or returns the original when the merchant
// reference has been seen before.
//
// C-1.3: idempotency is enforced by the unique constraint on
// (merchant_id, merchant_reference), not by an application-level pre-check. A pre-check
// races; the constraint does not. `existed` is true when the request was a repeat.
func (s *Store) CreateCollection(ctx context.Context, req CreateCollectionRequest) (col Collection, existed bool, err error) {
	currency, ok := platform.CurrencyForChannel(req.Channel)
	if !ok {
		return col, false, platform.Errorf(http.StatusBadRequest, platform.CodeUnsupportedChannel,
			"channel %q is not supported (supported: %v)", req.Channel, platform.SupportedChannels())
	}
	country, _ := platform.CountryForChannel(req.Channel)

	col = Collection{
		ID:                newID("col"),
		MerchantID:        req.MerchantID,
		Channel:           req.Channel,
		AmountMinor:       req.AmountMinor,
		Currency:          currency,
		CustomerReference: req.CustomerReference,
		MerchantReference: req.MerchantReference,
		OriginCountry:     string(country),
		Status:            StatusPending,
		CreatedAt:         time.Now().UTC(),
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO collections
		    (id, merchant_id, channel, amount_minor, currency, customer_reference,
		     merchant_reference, origin_country, status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		col.ID, col.MerchantID, string(col.Channel), int64(col.AmountMinor), string(col.Currency),
		col.CustomerReference, col.MerchantReference, col.OriginCountry, col.Status, col.CreatedAt)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
			pgErr.ConstraintName == "collections_merchant_reference_unique" {
			existing, findErr := s.CollectionByMerchantReference(ctx, req.MerchantID, req.MerchantReference)
			if findErr != nil {
				return col, false, findErr
			}
			return *existing, true, nil
		}
		return col, false, fmt.Errorf("insert collection for merchant %s reference %s: %w",
			req.MerchantID, req.MerchantReference, err)
	}
	return col, false, nil
}

func (s *Store) CollectionByMerchantReference(ctx context.Context, merchantID, ref string) (*Collection, error) {
	return s.scanOne(ctx, `
		SELECT id, merchant_id, channel, amount_minor, currency, customer_reference,
		       merchant_reference, origin_country, status, created_at, cleared_at,
		       settled_at, settlement_line_id
		  FROM collections WHERE merchant_id = $1 AND merchant_reference = $2`, merchantID, ref)
}

func (s *Store) Collection(ctx context.Context, id string) (*Collection, error) {
	c, err := s.scanOne(ctx, `
		SELECT id, merchant_id, channel, amount_minor, currency, customer_reference,
		       merchant_reference, origin_country, status, created_at, cleared_at,
		       settled_at, settlement_line_id
		  FROM collections WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, platform.Errorf(http.StatusNotFound, platform.CodeNotFound,
			"collection %s does not exist", id)
	}
	return c, nil
}

func (s *Store) scanOne(ctx context.Context, q string, args ...any) (*Collection, error) {
	var c Collection
	var amt int64
	var channel, currency string
	err := s.pool.QueryRow(ctx, q, args...).Scan(&c.ID, &c.MerchantID, &channel, &amt, &currency,
		&c.CustomerReference, &c.MerchantReference, &c.OriginCountry, &c.Status,
		&c.CreatedAt, &c.ClearedAt, &c.SettledAt, &c.SettlementLineID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read collection: %w", err)
	}
	c.Channel = platform.Channel(channel)
	c.Currency = platform.Currency(currency)
	c.AmountMinor = platform.Minor(amt)
	return &c, nil
}

// ApplyCallback moves a collection from pending to cleared or failed (C-1.7).
//
// Idempotent: the UPDATE is guarded on status = 'pending', so a replayed callback
// affects zero rows and reports changed=false. SCOPE.md conflict 2 records that signature
// verification is a README.md non-goal and is deliberately absent.
func (s *Store) ApplyCallback(ctx context.Context, collectionID, outcome string, at time.Time) (col Collection, changed bool, err error) {
	if outcome != StatusCleared && outcome != StatusFailed {
		return col, false, platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"callback outcome %q is not valid for collection %s (want cleared or failed)",
			outcome, collectionID)
	}

	current, err := s.Collection(ctx, collectionID)
	if err != nil {
		return col, false, err
	}

	clearedAt := at.UTC()
	var tag pgconn.CommandTag
	if outcome == StatusCleared {
		tag, err = s.pool.Exec(ctx, `
			UPDATE collections SET status = 'cleared', cleared_at = $1
			 WHERE id = $2 AND status = 'pending'`, clearedAt, collectionID)
	} else {
		tag, err = s.pool.Exec(ctx, `
			UPDATE collections SET status = 'failed'
			 WHERE id = $1 AND status = 'pending'`, collectionID)
	}
	if err != nil {
		return col, false, fmt.Errorf("apply %s callback to collection %s: %w", outcome, collectionID, err)
	}

	updated, err := s.Collection(ctx, collectionID)
	if err != nil {
		return col, false, err
	}
	if tag.RowsAffected() == 0 {
		// Replay, or the collection already moved on. Not an error (C-1.7).
		return *updated, false, nil
	}
	_ = current
	return *updated, true, nil
}

// ---------------------------------------------------------------------------
// Constraint 4 / OBS-4 - settlement lag, computed by THIS service
// ---------------------------------------------------------------------------

// SettlementStatus computes settlement lag from the datastore.
//
// Hard constraint 4: this is computed continuously by collections-api and NOT by
// settlement-worker, and it must keep being emitted when the worker is not running.
// That is the whole point - the signal that the worker is dead cannot depend on the
// worker being alive.
func (s *Store) SettlementStatus(ctx context.Context, region string, staleness time.Duration) (SettlementStatus, error) {
	st := SettlementStatus{
		Region:             region,
		StalenessThreshold: staleness.Seconds(),
	}

	err := s.pool.QueryRow(ctx, `
		SELECT MIN(c.cleared_at), COUNT(*)::bigint, COUNT(DISTINCT c.merchant_id)::bigint
		  FROM collections c
		  JOIN merchants m ON m.id = c.merchant_id
		 WHERE c.status = 'cleared' AND m.data_region = $1`, region).
		Scan(&st.OldestUnsettledAt, &st.UnsettledCount, &st.AffectedMerchants)
	if err != nil {
		return st, fmt.Errorf("compute settlement lag for region %s: %w", region, err)
	}
	if st.OldestUnsettledAt != nil {
		st.LagSeconds = time.Since(*st.OldestUnsettledAt).Seconds()
	}

	err = s.pool.QueryRow(ctx, `
		SELECT id, ended_at FROM settlement_runs
		 WHERE status = 'completed' AND region = $1
		 ORDER BY ended_at DESC NULLS LAST LIMIT 1`, region).
		Scan(&st.LastCompletedRunID, &st.LastCompletedRunAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return st, fmt.Errorf("read last completed run for region %s: %w", region, err)
	}

	// S-3.4 / C-5.3: stale when the most recent COMPLETED run is older than the threshold.
	// No run at all with unsettled work present is also stale - C-5.3 and S-6.1 both
	// need "never started" to be distinguishable from "ran and failed".
	switch {
	case st.LastCompletedRunAt != nil:
		st.Stale = time.Since(*st.LastCompletedRunAt) > staleness
	default:
		st.Stale = st.UnsettledCount > 0
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// Merchant projection
// ---------------------------------------------------------------------------

// ProjectedMerchant is merchant-api's view of a merchant, as mopay records it locally.
//
// mopay's `merchants` table is a PROJECTION, not the system of record. It exists because
// five foreign keys point at it (collections, fee_schedules, settlement_lines,
// payout_instructions, ledger_entries) and because `ledger` reads merchant payout and fee
// data inside the settlement commit transaction - turning that into an HTTP call would
// put a network hop inside a money transaction.
type ProjectedMerchant struct {
	ID                 string
	Name               string
	Country            string
	DataRegion         string
	PayoutBank         string
	PayoutAccountRef   string
	PayoutAccountName  string
	FloatLimitMinor    platform.Minor
	FloatLimitCurrency platform.Currency
	FeeSchedules       []ProjectedFeeSchedule
}

type ProjectedFeeSchedule struct {
	Channel      platform.Channel
	PercentageBP int
	FixedMinor   platform.Minor
}

// UpsertMerchantProjection refreshes the local row from merchant-api.
//
// On INSERT every field is written. On UPDATE, `payout_account_number` and `data_region`
// are deliberately left alone:
//
//   - merchant-api never returns a full payout account number - reads get a mask - so
//     writing it on update would replace a real account number with "****6789" and
//     corrupt the payout instructions settlement produces. Resolving the full number for
//     payout execution is a privileged path that does not exist yet.
//   - data_region is immutable (RES-5) and the table's trigger would reject a change.
func (s *Store) UpsertMerchantProjection(ctx context.Context, m ProjectedMerchant) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin merchant projection for %s: %w", m.ID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO merchants (id, name, country, data_region, payout_bank,
		                       payout_account_number, payout_account_name,
		                       float_limit_minor, float_limit_currency)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET
		    name                 = EXCLUDED.name,
		    country              = EXCLUDED.country,
		    payout_bank          = EXCLUDED.payout_bank,
		    payout_account_name  = EXCLUDED.payout_account_name,
		    float_limit_minor    = EXCLUDED.float_limit_minor,
		    float_limit_currency = EXCLUDED.float_limit_currency`,
		m.ID, m.Name, m.Country, m.DataRegion, m.PayoutBank,
		m.PayoutAccountRef, m.PayoutAccountName,
		int64(m.FloatLimitMinor), string(m.FloatLimitCurrency)); err != nil {
		return fmt.Errorf("project merchant %s into the local store: %w", m.ID, err)
	}

	for _, f := range m.FeeSchedules {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fee_schedules (merchant_id, channel, percentage_bp, fixed_minor)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (merchant_id, channel) DO UPDATE SET
			    percentage_bp = EXCLUDED.percentage_bp,
			    fixed_minor   = EXCLUDED.fixed_minor`,
			m.ID, string(f.Channel), f.PercentageBP, int64(f.FixedMinor)); err != nil {
			return fmt.Errorf("project fee schedule %s/%s: %w", m.ID, f.Channel, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit merchant projection for %s: %w", m.ID, err)
	}
	return nil
}
