package load

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// devToken mints the dev-mode bearer token collections-api expects:
//
//	Authorization: Bearer dev.<base64url({"sub","merchantId","role"})>
//
// SCOPE conflict 5 replaced OIDC with these for the demo. Nothing secret is involved,
// so constraint 6 is not in play - there is no credential here to keep out of source.
func devToken(merchantID string) string {
	b, _ := json.Marshal(map[string]string{
		"sub":        "loadgen@" + merchantID,
		"merchantId": merchantID,
		"role":       "merchant",
	})
	return "dev." + base64.RawURLEncoding.EncodeToString(b)
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type createdCollection struct {
	ID string `json:"id"`
}

type Runner struct {
	cfg    *Config
	client *http.Client
	stats  *Stats
	seq    atomic.Int64
	epoch  int64
}

func NewRunner(cfg *Config, stats *Stats) *Runner {
	// The default transport keeps only 2 idle connections per host, so a concurrent
	// client spends its time in TCP handshakes rather than putting load on the API.
	// Size the idle pool to the worker count instead.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = cfg.Concurrency * 2
	tr.MaxIdleConnsPerHost = cfg.Concurrency * 2
	tr.MaxConnsPerHost = 0

	return &Runner{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout, Transport: tr},
		stats:  stats,
		epoch:  time.Now().UnixNano(),
	}
}

// Run drives traffic until the context is cancelled or the configured duration elapses.
func (r *Runner) Run(ctx context.Context) {
	if r.cfg.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.cfg.Duration)
		defer cancel()
	}

	// A single shared tick is what makes LOADGEN_RATE a rate across the whole client
	// rather than per worker.
	var limiter <-chan time.Time
	if r.cfg.Rate > 0 {
		t := time.NewTicker(time.Second / time.Duration(r.cfg.Rate))
		defer t.Stop()
		limiter = t.C
	}

	var wg sync.WaitGroup
	for i := 0; i < r.cfg.Concurrency; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			rnd := rand.New(rand.NewSource(time.Now().UnixNano() + int64(worker)))
			for {
				if ctx.Err() != nil {
					return
				}
				if limiter != nil {
					select {
					case <-ctx.Done():
						return
					case <-limiter:
					}
				}
				r.one(ctx, rnd)
			}
		}(i)
	}
	wg.Wait()
}

func (r *Runner) one(ctx context.Context, rnd *rand.Rand) {
	merchantID := r.cfg.MerchantIDs[rnd.Intn(len(r.cfg.MerchantIDs))]
	n := r.seq.Add(1)

	span := r.cfg.AmountMaxMinor - r.cfg.AmountMinMinor + 1
	amount := r.cfg.AmountMinMinor + rnd.Int63n(span)

	body, _ := json.Marshal(map[string]any{
		"channel":     r.cfg.Channel,
		"amountMinor": amount,
		// C-1.3 keys idempotency on merchantReference, so every request needs a fresh
		// one - a repeated reference returns the original and creates nothing, which
		// would look like throughput without being any.
		"merchantReference": fmt.Sprintf("lg-%d-%d", r.epoch, n),
		"customerReference": fmt.Sprintf("+2547%08d", rnd.Intn(100000000)),
	})

	r.stats.Attempted.Add(1)
	started := time.Now()
	status, payload, err := r.post(ctx, "/v1/collections", devToken(merchantID), body)
	r.stats.observeLatency(time.Since(started))

	if err != nil {
		if ctx.Err() != nil {
			return
		}
		r.stats.TransportErrors.Add(1)
		return
	}

	switch status {
	case http.StatusCreated:
		r.stats.Created.Add(1)
		var col createdCollection
		if json.Unmarshal(payload, &col) == nil && col.ID != "" {
			if rnd.Float64() < r.cfg.ClearRatio {
				r.clear(ctx, col.ID)
			}
		}
	case http.StatusOK:
		// C-1.3 replay. Should be zero here; a non-zero count means the reference
		// generator is colliding.
		r.stats.Replayed.Add(1)
	default:
		var ae apiError
		_ = json.Unmarshal(payload, &ae)
		switch ae.Code {
		case "float_limit_exceeded":
			r.stats.FloatLimitExceeded.Add(1)
		case "upstream_unavailable":
			r.stats.UpstreamUnavailable.Add(1)
		case "":
			r.stats.otherCode(fmt.Sprintf("http_%d", status))
		default:
			r.stats.otherCode(ae.Code)
		}
	}
}

// clear moves the collection to `cleared` so settlement has something to pick up
// (C-1.7). The callback endpoint is unauthenticated by design - it is the channel's
// entry point, not a merchant's.
func (r *Runner) clear(ctx context.Context, collectionID string) {
	body, _ := json.Marshal(map[string]string{
		"collectionId": collectionID,
		"outcome":      "cleared",
	})
	r.stats.CallbacksSent.Add(1)
	status, _, err := r.post(ctx, "/v1/channel-callbacks", "", body)
	if err != nil || status >= 300 {
		if ctx.Err() == nil {
			r.stats.CallbacksFailed.Add(1)
		}
	}
}

func (r *Runner) post(ctx context.Context, path, token string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, payload, nil
}

// Report prints one line per interval until the context ends.
func Report(ctx context.Context, stats *Stats, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var prev snapshot
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			var line string
			line, prev = stats.Line(now, prev, now.Sub(last))
			last = now
			fmt.Fprintln(os.Stdout, line)
		}
	}
}
