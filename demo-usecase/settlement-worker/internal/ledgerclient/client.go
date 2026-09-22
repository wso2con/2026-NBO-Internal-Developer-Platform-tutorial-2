// Package ledgerclient is settlement-worker's outbound path to the ledger.
package ledgerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/mopay/settlement-worker/internal/platform"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, timeout time.Duration) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: timeout}}
}

type Entry struct {
	ID          string            `json:"id"`
	MerchantID  string            `json:"merchantId"`
	Direction   string            `json:"direction"`
	AmountMinor platform.Minor    `json:"amountMinor"`
	Currency    platform.Currency `json:"currency"`
}

type FeeSchedule struct {
	Channel      platform.Channel `json:"channel"`
	PercentageBP int              `json:"percentageBp"`
	FixedMinor   platform.Minor   `json:"fixedMinor"`
}

// PendingCollection mirrors the ledger's pending-settlements element.
type PendingCollection struct {
	CollectionID      string            `json:"collectionId"`
	MerchantID        string            `json:"merchantId"`
	MerchantName      string            `json:"merchantName"`
	Channel           platform.Channel  `json:"channel"`
	AmountMinor       platform.Minor    `json:"amountMinor"`
	Currency          platform.Currency `json:"currency"`
	MerchantReference string            `json:"merchantReference"`
	OriginCountry     string            `json:"originCountry"`
	CreatedAt         time.Time         `json:"createdAt"`
	ClearedAt         *time.Time        `json:"clearedAt"`
	LedgerEntries     []Entry           `json:"ledgerEntries"`
	FeeSchedule       *FeeSchedule      `json:"feeSchedule"`
}

type PendingResponse struct {
	Region   string              `json:"region"`
	RowCount int                 `json:"rowCount"`
	Items    []PendingCollection `json:"items"`
}

type CommitRequest struct {
	RunID         string            `json:"runId"`
	MerchantID    string            `json:"merchantId"`
	CollectionIDs []string          `json:"collectionIds"`
	GrossMinor    platform.Minor    `json:"grossMinor"`
	FeesMinor     platform.Minor    `json:"feesMinor"`
	NetMinor      platform.Minor    `json:"netMinor"`
	Currency      platform.Currency `json:"currency"`
	SettledAt     time.Time         `json:"settledAt"`
}

type SettlementLine struct {
	ID         string         `json:"id"`
	RunID      string         `json:"runId"`
	MerchantID string         `json:"merchantId"`
	NetMinor   platform.Minor `json:"netMinor"`
	RowCount   int64          `json:"rowCount"`
}

type CommitResult struct {
	Line           SettlementLine `json:"line"`
	AlreadySettled bool           `json:"alreadySettled"`
	PayoutID       string         `json:"payoutId"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode ledger request for %s: %w", path, err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build ledger request %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	platform.InjectTrace(ctx, req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call ledger %s %s: %w", method, path, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		var apiErr platform.APIError
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Code != "" {
			return fmt.Errorf("ledger %s %s returned %d %s: %s",
				method, path, resp.StatusCode, apiErr.Code, apiErr.Message)
		}
		return fmt.Errorf("ledger %s %s returned HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode ledger response for %s %s: %w", method, path, err)
		}
	}
	return nil
}

// Pending reads the whole period.
//
// README hard constraint 3: this read is unpaginated by design and the worker holds
// the entire result set. Do NOT add pagination, streaming, batching or chunking here -
// full-period reconciliation (C-2.2) requires the complete set.
func (c *Client) Pending(ctx context.Context, region string, start, end time.Time) (PendingResponse, error) {
	q := url.Values{}
	q.Set("region", region)
	q.Set("periodStart", start.UTC().Format(time.RFC3339))
	q.Set("periodEnd", end.UTC().Format(time.RFC3339))

	var out PendingResponse
	err := c.do(ctx, http.MethodGet, "/internal/settlements/pending?"+q.Encode(), nil, &out)
	return out, err
}

func (c *Client) Commit(ctx context.Context, req CommitRequest) (CommitResult, error) {
	var out CommitResult
	err := c.do(ctx, http.MethodPost, "/internal/settlements/commit", req, &out)
	return out, err
}

// ---------------------------------------------------------------------------
// Settlement-run persistence
// ---------------------------------------------------------------------------
//
// These used to be direct SQL in settlement-worker. `ledger` is now the only component
// that holds a Postgres connection, so the worker asks it instead.

type InsertRunRequest struct {
	RunID       string    `json:"runId"`
	RunType     string    `json:"runType"`
	Region      string    `json:"region"`
	PeriodStart time.Time `json:"periodStart"`
	PeriodEnd   time.Time `json:"periodEnd"`
	StartedAt   time.Time `json:"startedAt"`
}

func (c *Client) InsertRun(ctx context.Context, r InsertRunRequest) error {
	return c.do(ctx, http.MethodPost, "/internal/settlements/runs/insert", r, nil)
}

type FinishRunRequest struct {
	RunID         string `json:"runId"`
	Status        string `json:"status"`
	RowCount      int64  `json:"rowCount"`
	FailureReason string `json:"failureReason,omitempty"`
}

func (c *Client) FinishRun(ctx context.Context, r FinishRunRequest) error {
	return c.do(ctx, http.MethodPost, "/internal/settlements/runs/finish", r, nil)
}

type MonthlyStatement struct {
	MerchantID string            `json:"merchantId"`
	Name       string            `json:"name"`
	GrossMinor platform.Minor    `json:"grossMinor"`
	FeesMinor  platform.Minor    `json:"feesMinor"`
	NetMinor   platform.Minor    `json:"netMinor"`
	RowCount   int64             `json:"rowCount"`
	Currency   platform.Currency `json:"currency"`
}

func (c *Client) MonthlyStatements(ctx context.Context, region string, periodStart, periodEnd time.Time) ([]MonthlyStatement, error) {
	var out struct {
		Statements []MonthlyStatement `json:"statements"`
	}
	err := c.do(ctx, http.MethodPost, "/internal/settlements/statements", map[string]any{
		"region": region, "periodStart": periodStart, "periodEnd": periodEnd,
	}, &out)
	return out.Statements, err
}
