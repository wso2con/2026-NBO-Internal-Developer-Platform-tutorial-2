// Package store is collections-api's view of its data.
//
// It holds NO database connection. `ledger` is the only component with a Postgres pool,
// so every method here is an HTTP call to ledger's /internal/collections surface. The
// method signatures are unchanged from when this package owned a pgxpool, so the handlers
// above it did not have to change.
//
// Why: with exactly one component talking to Postgres, the platform can reason about
// connection usage as `replicas x DB_MAX_CONNS` for a single component type, and cap it.
// Three independent pools cannot be capped that way - you can only cap each in isolation
// and hope the sum fits.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mopay/collections-api/internal/platform"
)

type Store struct {
	base string
	http *http.Client
}

func New(ledgerBaseURL string, timeout time.Duration) *Store {
	return &Store{base: ledgerBaseURL, http: &http.Client{Timeout: timeout}}
}

// call posts args to one of ledger's data endpoints and decodes the result.
//
// Errors are reconstructed as platform.APIError so the code and status ledger produced -
// not_found, invalid_status_transition and so on - survive the hop and reach the caller
// unchanged. Without this a 404 from the datastore would surface as a 500.
func (s *Store) call(ctx context.Context, path string, args any, out any) error {
	body, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("encode request for ledger %s: %w", path, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ledger request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	platform.InjectTrace(ctx, req)

	resp, err := s.http.Do(req)
	if err != nil {
		return platform.Errorf(http.StatusServiceUnavailable, platform.CodeUpstreamUnavailable,
			"call ledger %s: %v", path, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read ledger response for %s: %w", path, err)
	}

	if resp.StatusCode >= 300 {
		var apiErr platform.APIError
		if json.Unmarshal(payload, &apiErr) == nil && apiErr.Code != "" {
			apiErr.Status = resp.StatusCode
			return &apiErr
		}
		return platform.Errorf(http.StatusServiceUnavailable, platform.CodeUpstreamUnavailable,
			"ledger %s returned HTTP %d", path, resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode ledger response for %s: %w", path, err)
	}
	return nil
}

// Ping reports whether the data surface is reachable. collections-api no longer has a
// database to check, so its readiness is ledger's readiness.
func (s *Store) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("ledger health: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ledger health returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *Store) Merchant(ctx context.Context, id string) (*Merchant, error) {
	var m Merchant
	if err := s.call(ctx, "/internal/collections/merchant", map[string]string{"id": id}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) UpsertMerchantProjection(ctx context.Context, m ProjectedMerchant) error {
	return s.call(ctx, "/internal/collections/project-merchant", m, nil)
}

func (s *Store) CreateCollection(ctx context.Context, req CreateCollectionRequest) (Collection, bool, error) {
	var out struct {
		Collection Collection `json:"collection"`
		Existed    bool       `json:"existed"`
	}
	if err := s.call(ctx, "/internal/collections/create", req, &out); err != nil {
		return Collection{}, false, err
	}
	return out.Collection, out.Existed, nil
}

func (s *Store) CollectionByMerchantReference(ctx context.Context, merchantID, ref string) (*Collection, error) {
	var out struct {
		Collection *Collection `json:"collection"`
	}
	if err := s.call(ctx, "/internal/collections/by-reference",
		map[string]string{"merchantId": merchantID, "reference": ref}, &out); err != nil {
		return nil, err
	}
	return out.Collection, nil
}

func (s *Store) Collection(ctx context.Context, id string) (*Collection, error) {
	var out struct {
		Collection *Collection `json:"collection"`
	}
	if err := s.call(ctx, "/internal/collections/get", map[string]string{"id": id}, &out); err != nil {
		return nil, err
	}
	return out.Collection, nil
}

func (s *Store) ApplyCallback(ctx context.Context, collectionID, outcome string, at time.Time) (Collection, bool, error) {
	var out struct {
		Collection Collection `json:"collection"`
		Changed    bool       `json:"changed"`
	}
	if err := s.call(ctx, "/internal/collections/callback", map[string]any{
		"collectionId": collectionID, "outcome": outcome, "at": at,
	}, &out); err != nil {
		return Collection{}, false, err
	}
	return out.Collection, out.Changed, nil
}

func (s *Store) SearchCollections(ctx context.Context, p SearchParams) ([]Collection, int64, error) {
	var out struct {
		Items []Collection `json:"items"`
		Total int64        `json:"total"`
	}
	if err := s.call(ctx, "/internal/collections/search", p, &out); err != nil {
		return nil, 0, err
	}
	return out.Items, out.Total, nil
}

func (s *Store) Aggregates(ctx context.Context, merchantID string, from, to time.Time) (Aggregates, error) {
	var a Aggregates
	err := s.call(ctx, "/internal/collections/aggregates", map[string]any{
		"merchantId": merchantID, "from": from, "to": to,
	}, &a)
	return a, err
}

func (s *Store) ListRuns(ctx context.Context, merchantID, region string, limit int) ([]SettlementRun, error) {
	var out struct {
		Runs []SettlementRun `json:"runs"`
	}
	if err := s.call(ctx, "/internal/collections/runs", map[string]any{
		"merchantId": merchantID, "region": region, "limit": limit,
	}, &out); err != nil {
		return nil, err
	}
	return out.Runs, nil
}

func (s *Store) Statement(ctx context.Context, merchantID, month string) (Statement, error) {
	var st Statement
	err := s.call(ctx, "/internal/collections/statement", map[string]string{
		"merchantId": merchantID, "month": month,
	}, &st)
	return st, err
}

func (s *Store) SettlementStatus(ctx context.Context, region string, staleness time.Duration) (SettlementStatus, error) {
	var st SettlementStatus
	err := s.call(ctx, "/internal/collections/settlement-status", map[string]any{
		"region":           region,
		"stalenessSeconds": int64(staleness / time.Second),
	}, &st)
	return st, err
}
