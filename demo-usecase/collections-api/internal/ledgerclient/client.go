// Package ledgerclient is collections-api's outbound path to the ledger.
// Hard constraint 8: only collections-api and settlement-worker may call the ledger.
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

	"github.com/mopay/collections-api/internal/platform"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, timeout time.Duration) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: timeout}}
}

type Entry struct {
	ID                 string            `json:"id"`
	MerchantID         string            `json:"merchantId"`
	Direction          string            `json:"direction"`
	AmountMinor        platform.Minor    `json:"amountMinor"`
	Currency           platform.Currency `json:"currency"`
	CreatedAt          time.Time         `json:"createdAt"`
	EventType          string            `json:"eventType"`
	EventID            string            `json:"eventId"`
	TransactionGroupID string            `json:"transactionGroupId"`
}

type EntryGroup struct {
	TransactionGroupID string  `json:"transactionGroupId"`
	EventType          string  `json:"eventType"`
	EventID            string  `json:"eventId"`
	Entries            []Entry `json:"entries"`
}

type Balance struct {
	MerchantID  string            `json:"merchantId"`
	AmountMinor platform.Minor    `json:"amountMinor"`
	Currency    platform.Currency `json:"currency"`
	AsOf        time.Time         `json:"asOf"`
	EntryCount  int64             `json:"entryCount"`
}

// do issues a request with the trace context propagated (OBS-5), so a collection is one
// trace across collections-api and ledger.
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

// Balance reads a merchant's position. The float-limit check (C-1.5) depends on this
// call, and a failure here must fail the collection closed - never open.
func (c *Client) Balance(ctx context.Context, merchantID string) (Balance, error) {
	var b Balance
	err := c.do(ctx, http.MethodGet, "/internal/ledger/balances/"+url.PathEscape(merchantID), nil, &b)
	return b, err
}

// EntriesForCollection backs C-1.9 / S-2: collection detail showing its ledger entries.
func (c *Client) EntriesForCollection(ctx context.Context, collectionID string) ([]Entry, error) {
	q := url.Values{}
	q.Set("eventType", "collection")
	q.Set("eventId", collectionID)

	var out struct {
		Entries []Entry `json:"entries"`
	}
	if err := c.do(ctx, http.MethodGet, "/internal/ledger/entries?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

func (c *Client) WriteGroup(ctx context.Context, g EntryGroup) error {
	return c.do(ctx, http.MethodPost, "/internal/ledger/entries", g, nil)
}

func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/healthz", nil, nil)
}
