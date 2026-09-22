// Package merchantclient talks to merchant-api, the system of record for merchant
// identity.
//
// merchant-api lives in a DIFFERENT project with its own datastore, so every call here
// crosses a project boundary. Two consequences shape this package:
//
//   - It is on the collection intake path, so results are cached for a short TTL.
//     MERCHANT_CACHE_TTL=0 disables the cache, which makes the cross-project call visible
//     on every single collection - useful for showing the dependency in the platform's
//     topology, wasteful otherwise.
//   - It must not become a second way for intake to fail. C-1.5 already fails closed on
//     the ledger, deliberately. Adding a second hard dependency to money intake would be
//     a downgrade, so the CALLER is expected to fall back to its local projection when
//     this client returns a transport error. Only a definitive 404 means "this merchant
//     cannot transact".
package merchantclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/mopay/collections-api/internal/platform"
)

type FeeSchedule struct {
	Channel      platform.Channel `json:"channel"`
	PercentageBP int              `json:"percentageBp"`
	FixedMinor   platform.Minor   `json:"fixedMinor"`
}

type Merchant struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Country            string            `json:"country"`
	DataRegion         string            `json:"dataRegion"`
	PayoutBank         string            `json:"payoutBank"`
	PayoutAccountMask  string            `json:"payoutAccountMask"`
	PayoutAccountName  string            `json:"payoutAccountName"`
	FloatLimitMinor    platform.Minor    `json:"floatLimitMinor"`
	FloatLimitCurrency platform.Currency `json:"floatLimitCurrency"`
	FeeSchedules       []FeeSchedule     `json:"feeSchedules"`
	CreatedAt          time.Time         `json:"createdAt"`
}

type entry struct {
	m       *Merchant
	expires time.Time
}

type Client struct {
	base string
	http *http.Client
	ttl  time.Duration

	mu    sync.RWMutex
	cache map[string]entry
}

func New(baseURL string, timeout, ttl time.Duration) *Client {
	return &Client{
		base:  baseURL,
		http:  &http.Client{Timeout: timeout},
		ttl:   ttl,
		cache: map[string]entry{},
	}
}

// serviceToken mints a dev-mode token scoped to the merchant being fetched. merchant-api
// treats `service` as a merchant-SCOPED role, so a token for merchant A cannot read
// merchant B - the scoping holds across the project boundary, not just inside mopay.
func serviceToken(merchantID string) string {
	b, _ := json.Marshal(map[string]string{
		"sub":        "collections-api",
		"merchantId": merchantID,
		"role":       "service",
	})
	return "dev." + base64.RawURLEncoding.EncodeToString(b)
}

// ErrNotFound means merchant-api answered definitively: no such merchant. It is NOT a
// transport failure and must not be retried or fallen back from.
var ErrNotFound = fmt.Errorf("merchant does not exist")

func (c *Client) cached(id string) *Merchant {
	if c.ttl <= 0 {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.cache[id]; ok && time.Now().Before(e.expires) {
		return e.m
	}
	return nil
}

func (c *Client) store(id string, m *Merchant) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	c.cache[id] = entry{m: m, expires: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

// Get returns a merchant, from cache when possible. The bool reports whether the answer
// came from cache, so the caller can tell a real cross-project call from a local hit.
func (c *Client) Get(ctx context.Context, merchantID string) (*Merchant, bool, error) {
	if m := c.cached(merchantID); m != nil {
		return m, true, nil
	}

	url := c.base + "/v1/merchants/" + merchantID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("build merchant-api request for %s: %w", merchantID, err)
	}
	req.Header.Set("Authorization", "Bearer "+serviceToken(merchantID))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("call merchant-api GET /v1/merchants/%s: %w", merchantID, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false, fmt.Errorf("read merchant-api response for %s: %w", merchantID, err)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, false, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		var apiErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &apiErr)
		return nil, false, fmt.Errorf("merchant-api returned HTTP %d for merchant %s (code %q): %s",
			resp.StatusCode, merchantID, apiErr.Code, apiErr.Message)
	}

	var m Merchant
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, false, fmt.Errorf("merchant-api response for %s is not valid JSON: %w", merchantID, err)
	}

	c.store(merchantID, &m)
	return &m, false, nil
}

// Health is the readiness probe's view of the dependency.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("merchant-api health: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("merchant-api health returned HTTP %d", resp.StatusCode)
	}
	return nil
}
