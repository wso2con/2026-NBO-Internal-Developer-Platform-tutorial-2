package platform

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Database capacity diagnostics.
//
// `ledger` is the only component in mopay that holds a Postgres connection (README hard
// constraint 8a), which makes it the only place the system's whole demand on the database
// is visible. When that demand outgrows what the database will grant, the driver reports
// it as an ordinary request error - a timeout, or a FATAL from the server - and by the
// time it reaches an HTTP handler nothing distinguishes "that query was slow" from "this
// component is configured to open more connections than exist".
//
// That difference is the entire diagnosis, and it is only recoverable here, while the
// pool's own numbers are still in reach. Everything below exists to put it in the log.

// Failure kinds. Stable strings: an alert rule and a human both match on these.
const (
	DBFailureConnectionLimit = "connection_limit"
	DBFailurePoolExhausted   = "pool_exhausted"
)

// SQLSTATEs that mean the server refused a connection on capacity grounds, not on the
// merits of the request. https://www.postgresql.org/docs/16/errcodes-appendix.html
const (
	sqlStateTooManyConnections  = "53300"
	sqlStateConfigLimitExceeded = "53400"
)

// PoolStats is the subset of a connection pool's live state that matters when the question
// is "why can this component not reach the database".
type PoolStats struct {
	Acquired             int32
	Idle                 int32
	Total                int32
	Max                  int32
	EmptyAcquireCount    int64
	CanceledAcquireCount int64
}

// SaturationPct is how much of the configured pool is currently checked out. 100 means the
// next caller waits.
func (p PoolStats) SaturationPct() int {
	if p.Max <= 0 {
		return 0
	}
	return int(float64(p.Acquired) / float64(p.Max) * 100)
}

// LogAttrs renders the pool for a log line. The configured ceiling travels with every
// record deliberately: it is the number that has to change, and it is not discoverable
// from anywhere else in the system.
func (p PoolStats) LogAttrs() []any {
	return []any{
		slog.Int("db_pool_max", int(p.Max)),
		slog.Int("db_pool_acquired", int(p.Acquired)),
		slog.Int("db_pool_idle", int(p.Idle)),
		slog.Int("db_pool_total", int(p.Total)),
		slog.Int("db_pool_saturation_pct", p.SaturationPct()),
		slog.Int64("db_pool_empty_acquires", p.EmptyAcquireCount),
		slog.Int64("db_pool_canceled_acquires", p.CanceledAcquireCount),
	}
}

// PoolStatter reports the live pool state. store.Store implements it; declaring it as an
// interface keeps this package free of a driver dependency.
type PoolStatter interface {
	PoolStats() PoolStats
}

// sqlStater is implemented by pgconn.PgError. Reading the SQLSTATE through an interface
// means the exact error code still names the failure without platform importing pgx.
type sqlStater interface {
	SQLState() string
}

// dbDiag is what a request needs to tell a capacity failure from an ordinary one.
type dbDiag struct {
	stats PoolStatter
	reg   *Registry
}

// WithDBDiag attaches database diagnostics to a request context. Observe does this once
// per request; WriteError is the only reader.
func WithDBDiag(ctx context.Context, ps PoolStatter, reg *Registry) context.Context {
	if ps == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyDBDiag, dbDiag{stats: ps, reg: reg})
}

func dbDiagFrom(ctx context.Context) (dbDiag, bool) {
	d, ok := ctx.Value(ctxKeyDBDiag).(dbDiag)
	return d, ok && d.stats != nil
}

// ClassifyDBFailure names a database failure that is about CAPACITY rather than about the
// query. It returns "" for everything else, which is most things - a wrong answer here is
// worse than no answer, because it would send a diagnosis in the wrong direction.
func ClassifyDBFailure(err error, stats PoolStats) string {
	if err == nil {
		return ""
	}

	// The server answered, and its answer was "no room". This is unambiguous.
	var st sqlStater
	if errors.As(err, &st) {
		switch st.SQLState() {
		case sqlStateTooManyConnections, sqlStateConfigLimitExceeded:
			return DBFailureConnectionLimit
		}
	}

	// A pool with nothing to hand out does not produce a database error at all: the caller
	// waits and its context expires, and the request never reaches Postgres. A slow query
	// expires identically, so only call it exhaustion when the pool's own numbers agree.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if stats.Max > 0 && stats.Acquired >= stats.Max {
			return DBFailurePoolExhausted
		}
	}

	return ""
}

// DBCapacityMarker prefixes every capacity failure message. Both kinds share it so a single
// log query matches them whatever syntax the log backend speaks, and so the phrase is
// greppable by a human reading a container log with no backend at all.
const DBCapacityMarker = "database capacity failure"

// DBFailureMessage is the log message for a kind. It states the CAUSE, not the symptom: a
// line that only reports a timeout leaves the reader to guess between a slow database, a
// slow query, and a pool sized past what the server will grant. Those have different fixes.
func DBFailureMessage(kind string) string {
	switch kind {
	case DBFailureConnectionLimit:
		return DBCapacityMarker + ": the database refused a connection because this component's pool is configured larger than the database will grant"
	case DBFailurePoolExhausted:
		return DBCapacityMarker + ": every connection in the pool is checked out and the caller timed out waiting for one"
	default:
		return ""
	}
}

// Metric names for pool capacity. OBS-2.
const (
	MetricPoolMax        = "mopay_ledger_db_pool_max"
	MetricPoolAcquired   = "mopay_ledger_db_pool_acquired"
	MetricPoolSaturation = "mopay_ledger_db_pool_saturation_pct"
	MetricDBCapacityErrs = "mopay_ledger_db_capacity_errors_total"
)

// WatchPool samples the pool on an interval and publishes it, so the pool's state is
// visible before a request fails rather than only after. It logs on the EDGE in both
// directions - saturated, then recovered - so the log carries an interval rather than a
// stream of identical lines, and so a recovery is as visible as an onset.
func WatchPool(ctx context.Context, ps PoolStatter, log *slog.Logger, reg *Registry, interval time.Duration, warnAtPct int) {
	t := time.NewTicker(interval)
	defer t.Stop()

	saturated := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		s := ps.PoolStats()
		if reg != nil {
			reg.SetGauge(MetricPoolMax, "Configured maximum size of this component's Postgres pool.", nil, float64(s.Max))
			reg.SetGauge(MetricPoolAcquired, "Connections currently checked out of the pool.", nil, float64(s.Acquired))
			reg.SetGauge(MetricPoolSaturation, "Checked-out connections as a percentage of the configured maximum.", nil, float64(s.SaturationPct()))
		}

		switch pct := s.SaturationPct(); {
		case pct >= warnAtPct && !saturated:
			saturated = true
			log.Warn("database pool saturated: the component is holding nearly every connection it is configured to open",
				append(s.LogAttrs(), slog.Int("warn_at_pct", warnAtPct))...)
		case pct < warnAtPct && saturated:
			saturated = false
			log.Info("database pool recovered", s.LogAttrs()...)
		}
	}
}
