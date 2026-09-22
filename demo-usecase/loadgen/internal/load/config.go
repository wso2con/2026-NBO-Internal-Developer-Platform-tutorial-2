// Package load drives peak-hour traffic through the PUBLIC intake path.
//
// It deliberately speaks HTTP to collections-api rather than writing to Postgres the way
// `seed` does. The point of this tool is to put concurrent demand on collections-api and,
// through the C-1.5 balance check, on the ledger's connection pool - which a direct SQL
// insert would bypass entirely.
package load

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the single place this tool reads environment variables. Constraint 5: no
// hostnames, merchant ids or rates are baked into the image.
type Config struct {
	BaseURL     string
	MerchantIDs []string
	Channel     string

	Concurrency int
	// Rate is requests per second across all workers. Zero means unbounded - send as
	// fast as the system will accept, which is what a genuine peak looks like.
	Rate     int
	Duration time.Duration

	AmountMinMinor int64
	AmountMaxMinor int64

	// ClearRatio is the fraction of accepted collections that receive a `cleared`
	// channel callback. Cleared collections are what settlement later picks up, so at
	// 1.0 the load also feeds the settlement runs.
	ClearRatio float64

	ReportInterval time.Duration
	Timeout        time.Duration
}

type envReader struct{ problems []string }

func (e *envReader) str(key string, required bool, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		if required {
			e.problems = append(e.problems, key+" is required")
		}
		return def
	}
	return v
}

func (e *envReader) intVal(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		e.problems = append(e.problems, fmt.Sprintf("%s value %q is not an integer", key, raw))
		return def
	}
	return n
}

func (e *envReader) int64Val(key string, def int64) int64 {
	return int64(e.intVal(key, int(def)))
}

func (e *envReader) floatVal(key string, def float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		e.problems = append(e.problems, fmt.Sprintf("%s value %q is not a number", key, raw))
		return def
	}
	return f
}

func (e *envReader) durationVal(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		e.problems = append(e.problems, fmt.Sprintf("%s value %q is not a duration", key, raw))
		return def
	}
	return d
}

func LoadConfig() (*Config, error) {
	e := &envReader{}

	c := &Config{
		BaseURL:        strings.TrimRight(e.str("LOADGEN_BASE_URL", true, ""), "/"),
		Channel:        e.str("LOADGEN_CHANNEL", true, ""),
		Concurrency:    e.intVal("LOADGEN_CONCURRENCY", 50),
		Rate:           e.intVal("LOADGEN_RATE", 0),
		Duration:       e.durationVal("LOADGEN_DURATION", 0),
		AmountMinMinor: e.int64Val("LOADGEN_AMOUNT_MIN_MINOR", 10_000),
		AmountMaxMinor: e.int64Val("LOADGEN_AMOUNT_MAX_MINOR", 250_000),
		ClearRatio:     e.floatVal("LOADGEN_CLEAR_RATIO", 1.0),
		ReportInterval: e.durationVal("LOADGEN_REPORT_INTERVAL", 5*time.Second),
		Timeout:        e.durationVal("LOADGEN_TIMEOUT", 10*time.Second),
	}

	for _, id := range strings.Split(e.str("LOADGEN_MERCHANT_IDS", true, ""), ",") {
		if id = strings.TrimSpace(id); id != "" {
			c.MerchantIDs = append(c.MerchantIDs, id)
		}
	}

	// C-1.8 derives currency from the channel, so the merchants driven here must belong
	// to the region that channel serves. Mixing them produces float-limit currency
	// mismatches rather than load, so fail loudly instead.
	switch c.Channel {
	case "mpesa", "nibss_transfer":
	case "":
	default:
		e.problems = append(e.problems,
			fmt.Sprintf("LOADGEN_CHANNEL %q is not supported (want mpesa or nibss_transfer)", c.Channel))
	}
	if c.Concurrency < 1 {
		e.problems = append(e.problems, fmt.Sprintf("LOADGEN_CONCURRENCY must be at least 1, got %d", c.Concurrency))
	}
	if c.AmountMinMinor <= 0 || c.AmountMaxMinor < c.AmountMinMinor {
		e.problems = append(e.problems, fmt.Sprintf(
			"LOADGEN_AMOUNT_MIN_MINOR/MAX_MINOR must be positive and ordered, got %d..%d",
			c.AmountMinMinor, c.AmountMaxMinor))
	}
	if c.ClearRatio < 0 || c.ClearRatio > 1 {
		e.problems = append(e.problems, fmt.Sprintf("LOADGEN_CLEAR_RATIO must be between 0 and 1, got %v", c.ClearRatio))
	}
	if len(c.MerchantIDs) == 0 && len(e.problems) == 0 {
		e.problems = append(e.problems, "LOADGEN_MERCHANT_IDS contained no usable merchant ids")
	}

	if len(e.problems) > 0 {
		return nil, fmt.Errorf("loadgen configuration is invalid: %s", strings.Join(e.problems, "; "))
	}
	return c, nil
}
