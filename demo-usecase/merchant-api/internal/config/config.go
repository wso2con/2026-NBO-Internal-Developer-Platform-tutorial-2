package config

import (
	"strings"
	"time"

	"github.com/mopay/merchant-api/internal/platform"
)

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Config is the single place this service reads environment variables.
// Constraint 5: nothing environment-specific is baked into the image.
//
// Note the absence of DATA_REGION. merchant-api is the system of record for merchant
// IDENTITY, which names the region a merchant's transaction data lives in but does not
// itself hold transaction data. Under hard constraint 7 ("prod-ke and prod-ng share
// nothing") a production deployment still wants one instance per region; that is a
// deployment decision, recorded in api/README.md, not a code one.
type Config struct {
	Environment string
	Port        int
	LogLevel    string
	DatabaseURL string

	// Sized per environment, and against the SERVER's max_connections - see the note in
	// docker-compose.yml. merchant-api has its own Postgres, so it draws from its own
	// budget and cannot starve mopay's components.
	DBMaxConns int32
	DBMinConns int32

	// CORSAllowedOrigins lists the browser origins permitted to call this API. The
	// console calls merchant-api directly to list merchants, and it is a different
	// origin in every environment, so this is configuration (constraint 5).
	CORSAllowedOrigins []string

	ShutdownGrace time.Duration
	QueryTimeout  time.Duration
}

func Load() (*Config, error) {
	e := platform.NewEnv()

	c := &Config{
		Environment:        e.String("ENVIRONMENT"),
		Port:               e.Int("PORT", 8080),
		LogLevel:           e.StringDefault("LOG_LEVEL", "info"),
		DatabaseURL:        e.String("DATABASE_URL"),
		DBMaxConns:         int32(e.Int("DB_MAX_CONNS", 10)),
		DBMinConns:         int32(e.Int("DB_MIN_CONNS", 0)),
		CORSAllowedOrigins: splitList(e.StringDefault("CORS_ALLOWED_ORIGINS", "")),
		ShutdownGrace:      e.Duration("SHUTDOWN_GRACE", 10*time.Second),
		QueryTimeout:       e.Duration("QUERY_TIMEOUT", 30*time.Second),
	}

	if err := e.Err(); err != nil {
		return nil, err
	}
	return c, nil
}
