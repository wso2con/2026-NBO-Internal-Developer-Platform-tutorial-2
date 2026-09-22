package config

import (
	"strings"
	"time"

	"github.com/mopay/settlement-worker/internal/platform"
)

// Config is the single place this component reads environment variables.
// Constraint 5: the cron schedule, the region and the ledger address are all injected -
// none of them is baked into the image.
type Config struct {
	// No Environment. The platform labels this component's logs and metrics with the
	// environment it runs them in, and the settlement run record no longer carries one -
	// nothing ever queried it, and each environment has its own database anyway.
	DataRegion string
	LogLevel   string
	// No DatabaseURL and no pool - `ledger` owns the only Postgres connection.

	LedgerBaseURL string
	LedgerTimeout time.Duration

	// RunMode is "once" (run a single settlement and exit - how a scheduled task behaves
	// on the platform) or "interval" (loop, which is what makes the demo watchable).
	RunMode     string
	RunInterval time.Duration

	// RunWindow is NFR-3's two-hour window. A run that exceeds it is flagged (S-3.3).
	RunWindow time.Duration

	// ForceMonthEnd makes a month-end run reproducible on demand rather than waiting for
	// the last business day of the month (C-2.2).
	ForceMonthEnd bool
	PeriodHours   int
}

func Load() (*Config, error) {
	e := platform.NewEnv()

	c := &Config{
		DataRegion:    e.OneOf("DATA_REGION", "KE", "NG"),
		LogLevel:      e.StringDefault("LOG_LEVEL", "info"),
		LedgerBaseURL: strings.TrimRight(e.String("LEDGER_BASE_URL"), "/"),
		LedgerTimeout: e.Duration("LEDGER_TIMEOUT", 30*time.Second),
		RunMode:       e.OneOf("RUN_MODE", "once", "interval"),
		RunInterval:   e.Duration("RUN_INTERVAL", 5*time.Minute),
		RunWindow:     e.Duration("RUN_WINDOW", 2*time.Hour),
		ForceMonthEnd: e.Bool("FORCE_MONTH_END", false),
		PeriodHours:   e.Int("SETTLEMENT_PERIOD_HOURS", 24),
	}

	if err := e.Err(); err != nil {
		return nil, err
	}
	return c, nil
}
