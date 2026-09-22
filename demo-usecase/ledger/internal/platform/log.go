package platform

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// Log field keys. OBS-1: every line carries a trace id, plus the merchant id where
// applicable. The ENVIRONMENT is deliberately not among them: the platform labels every
// line with the environment it collected it from, and a value the service asserts about
// itself can disagree with that - a binding left on the wrong value mislabels the whole
// stream while the platform's own label stays right.
const (
	FieldTrace    = "trace_id"
	FieldMerchant = "merchant_id"
	FieldService  = "service"
)

// NewLogger returns a structured JSON logger on stdout (README.md "Logging").
// service is attached to every line; the environment comes from the platform.
func NewLogger(service, level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h).With(
		slog.String(FieldService, service),
	)
}

type ctxKey int

const (
	ctxKeyLogger ctxKey = iota
	ctxKeyTrace
	ctxKeyDBDiag
)

// WithLogger stores a request-scoped logger on the context.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyLogger, l)
}

// LoggerFrom returns the request-scoped logger, or the fallback when none is set.
func LoggerFrom(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok && l != nil {
		return l
	}
	return fallback
}
