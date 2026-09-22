package platform

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Env is the one place per service that reads environment variables (README.md "Config").
// It accumulates problems rather than failing on the first one, so a misconfigured
// deployment reports every missing variable at once instead of one per restart.
type Env struct {
	problems []string
}

func NewEnv() *Env { return &Env{} }

func (e *Env) fail(key, why string) {
	e.problems = append(e.problems, fmt.Sprintf("%s: %s", key, why))
}

// String returns a required variable. Empty or unset is a failure.
func (e *Env) String(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		e.fail(key, "required but not set")
	}
	return v
}

func (e *Env) StringDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// OneOf returns a required variable constrained to a fixed set.
func (e *Env) OneOf(key string, allowed ...string) string {
	v := e.String(key)
	if v == "" {
		return v
	}
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	e.fail(key, fmt.Sprintf("value %q is not one of [%s]", v, strings.Join(allowed, ", ")))
	return v
}

func (e *Env) Int(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		e.fail(key, fmt.Sprintf("value %q is not an integer", raw))
		return def
	}
	return n
}

func (e *Env) Duration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		e.fail(key, fmt.Sprintf("value %q is not a duration (want e.g. 30s, 26h)", raw))
		return def
	}
	if d <= 0 {
		e.fail(key, fmt.Sprintf("value %q must be positive", raw))
		return def
	}
	return d
}

func (e *Env) Bool(key string, def bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		e.fail(key, fmt.Sprintf("value %q is not a boolean", raw))
		return def
	}
	return b
}

// Err names every missing or invalid variable. Services call this at startup and exit
// non-zero, so a bad config is loud and immediate rather than a nil-pointer later.
func (e *Env) Err() error {
	if len(e.problems) == 0 {
		return nil
	}
	sort.Strings(e.problems)
	return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(e.problems, "\n  - "))
}
