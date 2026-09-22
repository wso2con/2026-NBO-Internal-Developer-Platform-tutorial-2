package config

import (
	"time"

	"github.com/mopay/ledger/internal/platform"
)

// DefaultPoolConns is both the default minimum and the default maximum size of the
// Postgres pool. `ledger` holds the only pool in the system, so this one number - times
// the replica count - is the whole system's demand on the database.
const DefaultPoolConns = 10

// Config is the single place in this service that reads environment variables
// (README.md "Config"). Constraint 5: nothing environment-specific is baked into the image.
type Config struct {
	// No Environment. The platform already knows which environment it is running this
	// component in and labels its logs and metrics accordingly; asking the service to
	// restate it adds a second source of truth that can be wrong. DataRegion is a
	// different matter - it is a query predicate, not a label (RES-1).
	DataRegion     string
	Port           int
	LogLevel       string
	DatabaseURL    string
	HouseAccountID string

	// `ledger` is the ONLY component that holds a Postgres connection, so this pool is
	// the whole system's demand on the database:
	//
	//     replicas x DB_MAX_CONNS  <=  postgres max_connections
	//
	// Constraint 5: both are injected, not baked into the image. This component supplies
	// the MECHANISM - a default and an environment override. It deliberately enforces no
	// ceiling of its own: limiting what DB_MAX_CONNS may be set to, and checking it
	// against the replica count and the database's max_connections, is a PLATFORM policy
	// applied by OpenChoreo. A component that polices its own limits leaves the platform
	// nothing to enforce.
	DBMaxConns int32
	DBMinConns int32

	ShutdownGrace time.Duration
	QueryTimeout  time.Duration
}

func Load() (*Config, error) {
	e := platform.NewEnv()

	c := &Config{
		DataRegion:     e.OneOf("DATA_REGION", "KE", "NG"),
		Port:           e.Int("PORT", 8080),
		LogLevel:       e.StringDefault("LOG_LEVEL", "info"),
		DatabaseURL:    e.String("DATABASE_URL"),
		HouseAccountID: e.StringDefault("HOUSE_ACCOUNT_ID", "mopay_house"),
		DBMaxConns:     int32(e.Int("DB_MAX_CONNS", DefaultPoolConns)),
		DBMinConns:     int32(e.Int("DB_MIN_CONNS", DefaultPoolConns)),
		ShutdownGrace:  e.Duration("SHUTDOWN_GRACE", 10*time.Second),
		QueryTimeout:   e.Duration("QUERY_TIMEOUT", 30*time.Second),
	}

	if err := e.Err(); err != nil {
		return nil, err
	}
	return c, nil
}
