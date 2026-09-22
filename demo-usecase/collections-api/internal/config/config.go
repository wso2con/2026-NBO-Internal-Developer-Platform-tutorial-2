package config

import (
	"strings"
	"time"

	"github.com/mopay/collections-api/internal/platform"
)

// Config is the single place this service reads environment variables (README.md "Config").
// Constraint 5: no hostnames, thresholds, regions or schedules are baked into the image.
type Config struct {
	// No Environment. The platform labels this component's logs and metrics with the
	// environment it is running them in; restating it here would be a second source of
	// truth that can disagree. DataRegion is a different kind of value - a query
	// predicate (RES-1), not a label - and stays.
	DataRegion string
	Port       int
	LogLevel   string
	// No DatabaseURL and no pool. `ledger` is the only component that holds a Postgres
	// connection; this service reaches the data through it (LedgerBaseURL below).

	// LedgerBaseURL is injected, never hardcoded (constraint 5). On the platform this
	// comes from service discovery; under Compose it is the service name.
	LedgerBaseURL string
	LedgerTimeout time.Duration

	HouseAccountID string

	// merchant-api is the system of record for merchant identity and lives in a
	// SEPARATE project. MerchantCacheTTL of 0 disables caching, so the cross-project
	// call happens on every collection - visible in the platform topology, wasteful in
	// production.
	MerchantAPIBaseURL string
	MerchantAPITimeout time.Duration
	MerchantCacheTTL   time.Duration

	// Constraint 4: settlement lag is computed continuously by THIS service, not by
	// settlement-worker, and must keep being emitted when the worker is not running.
	LagRefreshInterval time.Duration

	// C-5.2 / S-3.4. Default 26h per the PRD.
	StalenessThreshold time.Duration
	LagAlertThreshold  time.Duration

	// CORSAllowedOrigins lists the browser origins permitted to call this API. The
	// console is a separate origin from the API in every environment, so this is
	// configuration, not a constant (constraint 5). "*" is accepted for local dev only.
	CORSAllowedOrigins []string

	ShutdownGrace time.Duration
	QueryTimeout  time.Duration
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func Load() (*Config, error) {
	e := platform.NewEnv()

	c := &Config{
		DataRegion:         e.OneOf("DATA_REGION", "KE", "NG"),
		Port:               e.Int("PORT", 8080),
		LogLevel:           e.StringDefault("LOG_LEVEL", "info"),
		LedgerBaseURL:      strings.TrimRight(e.String("LEDGER_BASE_URL"), "/"),
		LedgerTimeout:      e.Duration("LEDGER_TIMEOUT", 5*time.Second),
		HouseAccountID:     e.StringDefault("HOUSE_ACCOUNT_ID", "mopay_house"),
		MerchantAPIBaseURL: strings.TrimRight(e.String("MERCHANT_API_BASE_URL"), "/"),
		MerchantAPITimeout: e.Duration("MERCHANT_API_TIMEOUT", 3*time.Second),
		MerchantCacheTTL:   e.Duration("MERCHANT_CACHE_TTL", 60*time.Second),
		LagRefreshInterval: e.Duration("LAG_REFRESH_INTERVAL", 15*time.Second),
		StalenessThreshold: e.Duration("SETTLEMENT_STALENESS_THRESHOLD", 26*time.Hour),
		LagAlertThreshold:  e.Duration("SETTLEMENT_LAG_ALERT_THRESHOLD", 26*time.Hour),
		CORSAllowedOrigins: splitList(e.StringDefault("CORS_ALLOWED_ORIGINS", "")),
		ShutdownGrace:      e.Duration("SHUTDOWN_GRACE", 10*time.Second),
		QueryTimeout:       e.Duration("QUERY_TIMEOUT", 30*time.Second),
	}

	if err := e.Err(); err != nil {
		return nil, err
	}
	return c, nil
}
