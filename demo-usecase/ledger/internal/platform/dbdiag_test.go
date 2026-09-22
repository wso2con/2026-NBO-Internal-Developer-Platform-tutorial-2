package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// pgErr stands in for pgconn.PgError, which ClassifyDBFailure reads through an interface.
type pgErr struct{ code string }

func (e *pgErr) Error() string    { return "server error " + e.code }
func (e *pgErr) SQLState() string { return e.code }

func TestClassifyDBFailure(t *testing.T) {
	full := PoolStats{Acquired: 60, Max: 60}
	spare := PoolStats{Acquired: 3, Max: 60}

	tests := []struct {
		name  string
		err   error
		stats PoolStats
		want  string
	}{
		{"no error", nil, full, ""},

		{"server refused on connection count", &pgErr{sqlStateTooManyConnections}, spare, DBFailureConnectionLimit},
		{"server refused on a configuration limit", &pgErr{sqlStateConfigLimitExceeded}, spare, DBFailureConnectionLimit},
		{"wrapped server refusal still classifies",
			fmt.Errorf("read balance: %w", &pgErr{sqlStateTooManyConnections}), spare, DBFailureConnectionLimit},

		// A unique-violation is a problem with the write, not with capacity. Naming it a
		// capacity failure would point an operator at the pool size for a bug in a query.
		{"an ordinary server error is not a capacity failure", &pgErr{"23505"}, full, ""},

		{"timeout with every connection checked out", context.DeadlineExceeded, full, DBFailurePoolExhausted},
		{"wrapped timeout with every connection checked out",
			fmt.Errorf("acquire: %w", context.DeadlineExceeded), full, DBFailurePoolExhausted},
		{"cancellation with every connection checked out", context.Canceled, full, DBFailurePoolExhausted},

		// The negative that matters: a slow query times out exactly like a starved pool.
		// Only the pool's own numbers separate them.
		{"timeout with connections to spare is not exhaustion", context.DeadlineExceeded, spare, ""},
		{"timeout with an unconfigured pool is not exhaustion", context.DeadlineExceeded, PoolStats{}, ""},

		{"an unrelated error is not a capacity failure", errors.New("no rows in result set"), full, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyDBFailure(tc.err, tc.stats); got != tc.want {
				t.Errorf("ClassifyDBFailure() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDBFailureMessageCoversEveryKind(t *testing.T) {
	for _, kind := range []string{DBFailureConnectionLimit, DBFailurePoolExhausted} {
		msg := DBFailureMessage(kind)
		if msg == "" {
			t.Errorf("kind %q has no message; a classified failure would log an empty line", kind)
		}
		// The alert rule matches on this marker. A kind that drops it is invisible to the
		// alert while still looking correct in a log.
		if !strings.Contains(msg, DBCapacityMarker) {
			t.Errorf("kind %q message %q is missing the %q marker the alert rule matches on",
				kind, msg, DBCapacityMarker)
		}
	}
	if DBFailureMessage("") != "" {
		t.Error(`DBFailureMessage("") should be empty`)
	}
}

func TestSaturationPct(t *testing.T) {
	tests := []struct {
		stats PoolStats
		want  int
	}{
		{PoolStats{Acquired: 60, Max: 60}, 100},
		{PoolStats{Acquired: 30, Max: 60}, 50},
		{PoolStats{Acquired: 0, Max: 60}, 0},
		{PoolStats{}, 0}, // no pool configured: must not divide by zero
	}
	for _, tc := range tests {
		if got := tc.stats.SaturationPct(); got != tc.want {
			t.Errorf("PoolStats%+v.SaturationPct() = %d, want %d", tc.stats, got, tc.want)
		}
	}
}

func TestDBDiagIsAbsentWithoutAStatter(t *testing.T) {
	// WriteError must fall back to its generic branch rather than panic when nothing
	// attached a pool - the health and metrics handlers are served outside Observe.
	if _, ok := dbDiagFrom(context.Background()); ok {
		t.Error("dbDiagFrom reported diagnostics on a bare context")
	}
	if _, ok := dbDiagFrom(WithDBDiag(context.Background(), nil, nil)); ok {
		t.Error("WithDBDiag(nil) should attach nothing")
	}
}
