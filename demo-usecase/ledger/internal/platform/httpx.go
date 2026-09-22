package platform

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"
)

// APIError is the shared error shape (README.md "Errors over HTTP"): a stable
// machine-readable code, a human message, and the relevant values so a caller can act
// on it programmatically rather than parsing prose.
type APIError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`

	Status int `json:"-"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// Stable error codes. Every code the services return is enumerated here and mirrored in
// the OpenAPI specs.
const (
	CodeBadRequest          = "bad_request"
	CodeUnauthorized        = "unauthorized"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeConflict            = "conflict"
	CodeUnprocessable       = "unprocessable"
	CodeInternal            = "internal_error"
	CodeUpstreamUnavailable = "upstream_unavailable"

	// Domain-specific, referenced by the PRD.
	CodeFloatLimitExceeded  = "float_limit_exceeded"  // C-1.5
	CodeLedgerNotBalanced   = "ledger_not_balanced"   // C-3.5
	CodeAppendOnlyViolation = "append_only_violation" // C-3.3
	CodeInvalidTransition   = "invalid_status_transition"
	CodeUnsupportedChannel  = "unsupported_channel"
)

func Errorf(status int, code, format string, args ...any) *APIError {
	return &APIError{Code: code, Message: fmt.Sprintf(format, args...), Status: status}
}

func (e *APIError) WithDetail(k string, v any) *APIError {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[k] = v
	return e
}

func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

// WriteError renders an APIError. Anything that is not an APIError becomes a generic
// internal error - the detail goes to the log, never to the caller.
func WriteError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	apiErr, ok := err.(*APIError)
	if !ok {
		apiErr = Errorf(http.StatusInternalServerError, CodeInternal, "internal error")
		if !logDBCapacityFailure(r, log, err) {
			LoggerFrom(r.Context(), log).Error("unhandled error serving request",
				slog.String("path", r.URL.Path),
				slog.String("method", r.Method),
				slog.String("error", err.Error()))
		}
	}
	if apiErr.Status == 0 {
		apiErr.Status = http.StatusInternalServerError
	}
	if apiErr.Status >= 500 {
		LoggerFrom(r.Context(), log).Error("request failed",
			slog.String("path", r.URL.Path),
			slog.String("code", apiErr.Code),
			slog.String("error", apiErr.Message))
	}
	WriteJSON(w, apiErr.Status, apiErr)
}

// logDBCapacityFailure writes the dedicated line for a database failure that is about
// capacity, and reports whether it did. Folding this into the generic "unhandled error"
// branch would be the easier change and the wrong one: an operator reading the log needs
// the pool's numbers next to the error, and a component that is out of connections looks
// exactly like one with a slow query unless something says otherwise.
func logDBCapacityFailure(r *http.Request, log *slog.Logger, err error) bool {
	diag, ok := dbDiagFrom(r.Context())
	if !ok {
		return false
	}
	stats := diag.stats.PoolStats()
	kind := ClassifyDBFailure(err, stats)
	if kind == "" {
		return false
	}

	attrs := append([]any{
		slog.String("failure", kind),
		slog.String("path", r.URL.Path),
		slog.String("method", r.Method),
		slog.String("error", err.Error()),
	}, stats.LogAttrs()...)
	LoggerFrom(r.Context(), log).Error(DBFailureMessage(kind), attrs...)

	if diag.reg != nil {
		diag.reg.IncCounter(MetricDBCapacityErrs,
			"Requests that failed because the database would not grant a connection.",
			Labels{"failure": kind})
	}
	return true
}

// Handler is a http.HandlerFunc that may return an error, so handlers can `return err`
// instead of remembering to write-and-return at every branch.
type Handler func(http.ResponseWriter, *http.Request) error

func (h Handler) Serve(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, log, err)
		}
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// Observe is the middleware every HTTP component wraps its mux in. It establishes the
// trace context (OBS-5), builds the request-scoped logger (OBS-1), records request rate,
// error rate and duration per endpoint (OBS-2), and converts a panic into a 500 rather
// than killing the process (NFR-10).
func Observe(next http.Handler, log *slog.Logger, reg *Registry, pool PoolStatter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		tc, inherited := ParseTraceparent(r.Header.Get(TraceparentHeader))
		ctx := WithTrace(r.Context(), tc)
		reqLog := log.With(slog.String(FieldTrace, tc.TraceID))
		ctx = WithLogger(ctx, reqLog)
		ctx = WithDBDiag(ctx, pool, reg)
		r = r.WithContext(ctx)

		w.Header().Set(TraceparentHeader, tc.Header())
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if p := recover(); p != nil {
				reqLog.Error("panic serving request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Any("panic", p),
					slog.String("stack", string(debug.Stack())))
				if !rec.wrote {
					WriteJSON(rec, http.StatusInternalServerError,
						&APIError{Code: CodeInternal, Message: "internal error"})
				}
			}

			// Route pattern, not raw path - a per-id metric series is unbounded.
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			elapsed := time.Since(start)
			labels := Labels{"method": r.Method, "route": route}

			reg.ObserveHistogram("http_request_duration_seconds",
				"HTTP request duration in seconds.", DefaultDurationBuckets,
				labels, elapsed.Seconds())

			withStatus := Labels{"method": r.Method, "route": route, "status": strconv.Itoa(rec.status)}
			reg.IncCounter("http_requests_total", "Total HTTP requests.", withStatus)
			if rec.status >= 500 {
				reg.IncCounter("http_request_errors_total", "HTTP requests that failed.", labels)
			}

			if route != "unmatched" && r.URL.Path != "/healthz" && r.URL.Path != "/metrics" {
				reqLog.Info("request served",
					slog.String("method", r.Method),
					slog.String("route", route),
					slog.Int("status", rec.status),
					slog.Int64("duration_ms", elapsed.Milliseconds()),
					slog.Bool("trace_inherited", inherited))
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

// Health is the liveness/readiness endpoint. check is nil for a pure liveness probe.
func Health(service string, check func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if check != nil {
			if err := check(); err != nil {
				WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
					"status": "unhealthy", "service": service, "error": err.Error(),
				})
				return
			}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": service})
	}
}
