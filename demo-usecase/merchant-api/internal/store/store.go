// Package store is merchant-api's datastore. It is the system of record for merchant
// identity: nothing else creates a merchant, and no other store is authoritative for
// these fields.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mopay/merchant-api/internal/platform"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "mch_" + hex.EncodeToString(b)
}

// maskAccount keeps the last four digits only. The full payout account number is accepted
// at onboarding and never returned by a read.
func maskAccount(n string) string {
	if len(n) <= 4 {
		return "****"
	}
	return "****" + n[len(n)-4:]
}

// Validate checks an onboarding payload. Errors name the offending field and the value
// seen, so a caller can correct the request without guessing.
func Validate(r OnboardRequest) error {
	bad := func(format string, args ...any) error {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest, format, args...)
	}

	if strings.TrimSpace(r.Name) == "" {
		return bad("name is required")
	}
	if r.Country != "KE" && r.Country != "NG" {
		return bad("country %q is not supported (want KE or NG)", r.Country)
	}
	if r.DataRegion != "KE" && r.DataRegion != "NG" {
		return bad("dataRegion %q is not supported (want KE or NG)", r.DataRegion)
	}
	if strings.TrimSpace(r.PayoutBank) == "" || strings.TrimSpace(r.PayoutAccountNumber) == "" ||
		strings.TrimSpace(r.PayoutAccountName) == "" {
		return bad("payoutBank, payoutAccountNumber and payoutAccountName are all required: a merchant that cannot be paid cannot be onboarded")
	}
	if r.FloatLimitMinor < 0 {
		return bad("floatLimitMinor must not be negative, got %d", r.FloatLimitMinor)
	}
	if r.FloatLimitCurrency != platform.KES && r.FloatLimitCurrency != platform.NGN {
		return bad("floatLimitCurrency %q is not supported (want KES or NGN)", r.FloatLimitCurrency)
	}
	if len(r.FeeSchedules) == 0 {
		return bad("at least one fee schedule is required: a collection on a channel with no fee schedule cannot be settled")
	}

	seen := map[platform.Channel]bool{}
	for i, f := range r.FeeSchedules {
		if !platform.ValidChannel(f.Channel) {
			return bad("feeSchedules[%d] channel %q is not supported (supported: %v)",
				i, f.Channel, platform.SupportedChannels())
		}
		if seen[f.Channel] {
			return bad("feeSchedules contains channel %q twice: fees are one entry per merchant per channel", f.Channel)
		}
		seen[f.Channel] = true

		if f.PercentageBP < 0 || f.PercentageBP > 10000 {
			return bad("feeSchedules[%d] percentageBp must be between 0 and 10000 basis points, got %d", i, f.PercentageBP)
		}
		if f.FixedMinor < 0 {
			return bad("feeSchedules[%d] fixedMinor must not be negative, got %d", i, f.FixedMinor)
		}

		// C-1.8: a channel settles in exactly one currency. A merchant whose float limit
		// is in KES cannot collect on a Nigerian channel - the limit could never be
		// compared against the balance.
		cur, _ := platform.CurrencyForChannel(f.Channel)
		if cur != r.FloatLimitCurrency {
			return bad("feeSchedules[%d] channel %q settles in %s but floatLimitCurrency is %s: a merchant cannot hold a float limit in a currency it does not collect",
				i, f.Channel, cur, r.FloatLimitCurrency)
		}
	}
	return nil
}

// Onboard creates a merchant and its fee schedules in one transaction. A partially
// onboarded merchant - identity without fee terms - could take collections it could never
// settle, so the two are written together or not at all.
func (s *Store) Onboard(ctx context.Context, r OnboardRequest) (*Merchant, error) {
	if err := Validate(r); err != nil {
		return nil, err
	}

	id := newID()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin onboarding transaction for merchant %q: %w", r.Name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO merchants (id, name, country, data_region, payout_bank,
		                       payout_account_number, payout_account_name,
		                       float_limit_minor, float_limit_currency)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		id, r.Name, r.Country, r.DataRegion, r.PayoutBank,
		r.PayoutAccountNumber, r.PayoutAccountName,
		int64(r.FloatLimitMinor), string(r.FloatLimitCurrency)); err != nil {
		return nil, fmt.Errorf("insert merchant %s (%s): %w", id, r.Name, err)
	}

	for _, f := range r.FeeSchedules {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fee_schedules (merchant_id, channel, percentage_bp, fixed_minor)
			VALUES ($1,$2,$3,$4)`,
			id, string(f.Channel), f.PercentageBP, int64(f.FixedMinor)); err != nil {
			return nil, fmt.Errorf("insert fee schedule %s/%s for merchant %s: %w",
				id, f.Channel, r.Name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit onboarding of merchant %s (%s): %w", id, r.Name, err)
	}
	return s.Get(ctx, id)
}

const merchantColumns = `id, name, country, data_region, payout_bank,
	payout_account_number, payout_account_name, float_limit_minor,
	float_limit_currency, created_at`

func scanMerchant(row pgx.Row) (*Merchant, error) {
	var m Merchant
	var acct string
	var limit int64
	if err := row.Scan(&m.ID, &m.Name, &m.Country, &m.DataRegion, &m.PayoutBank,
		&acct, &m.PayoutAccountName, &limit, &m.FloatLimitCurrency, &m.CreatedAt); err != nil {
		return nil, err
	}
	m.PayoutAccountMask = maskAccount(acct)
	m.FloatLimitMinor = platform.Minor(limit)
	m.FeeSchedules = []FeeSchedule{}
	return &m, nil
}

// Get returns one merchant with its fee schedules. This is the operation on the
// collection intake path, so it is a single round trip - the fee schedules come back with
// the merchant rather than costing the caller a second call.
func (s *Store) Get(ctx context.Context, id string) (*Merchant, error) {
	m, err := scanMerchant(s.pool.QueryRow(ctx,
		`SELECT `+merchantColumns+` FROM merchants WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, platform.Errorf(http.StatusNotFound, platform.CodeNotFound,
			"merchant %s does not exist", id)
	}
	if err != nil {
		return nil, fmt.Errorf("read merchant %s: %w", id, err)
	}

	if err := s.attachFees(ctx, []*Merchant{m}); err != nil {
		return nil, err
	}
	return m, nil
}

// List returns a page of merchants, newest first, optionally filtered by region.
func (s *Store) List(ctx context.Context, region string, limit, offset int) ([]*Merchant, int64, error) {
	where, args := "", []any{}
	if region != "" {
		where = " WHERE data_region = $1"
		args = append(args, region)
	}

	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM merchants`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count merchants (region=%q): %w", region, err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+merchantColumns+` FROM merchants`+where+
			fmt.Sprintf(` ORDER BY created_at DESC, id ASC LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2),
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list merchants (region=%q limit=%d offset=%d): %w", region, limit, offset, err)
	}
	defer rows.Close()

	out := []*Merchant{}
	for rows.Next() {
		m, err := scanMerchant(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan merchant row (region=%q): %w", region, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate merchants (region=%q): %w", region, err)
	}

	if err := s.attachFees(ctx, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// attachFees loads fee schedules for the given merchants in one query, so a list of N
// merchants costs two round trips rather than N+1.
func (s *Store) attachFees(ctx context.Context, ms []*Merchant) error {
	if len(ms) == 0 {
		return nil
	}
	byID := make(map[string]*Merchant, len(ms))
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		byID[m.ID] = m
		ids = append(ids, m.ID)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT merchant_id, channel, percentage_bp, fixed_minor
		  FROM fee_schedules WHERE merchant_id = ANY($1) ORDER BY merchant_id, channel`, ids)
	if err != nil {
		return fmt.Errorf("read fee schedules for %d merchant(s): %w", len(ids), err)
	}
	defer rows.Close()

	for rows.Next() {
		var mid string
		var f FeeSchedule
		var fixed int64
		if err := rows.Scan(&mid, &f.Channel, &f.PercentageBP, &fixed); err != nil {
			return fmt.Errorf("scan fee schedule row: %w", err)
		}
		f.FixedMinor = platform.Minor(fixed)
		if m := byID[mid]; m != nil {
			m.FeeSchedules = append(m.FeeSchedules, f)
		}
	}
	return rows.Err()
}
