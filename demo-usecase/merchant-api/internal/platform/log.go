package platform

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// Log field keys. OBS-1: every line carries trace id and environment, plus merchant id
// where applicable.
const (
	FieldTrace       = "trace_id"
	FieldEnvironment = "environment"
	FieldMerchant    = "merchant_id"
	FieldService     = "service"
)

// NewLogger returns a structured JSON logger on stdout (README.md "Logging").
// service and environment are attached to every line.
func NewLogger(service, environment, level string) *slog.Logger {
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
		slog.String(FieldEnvironment, environment),
	)
}

type ctxKey int

const (
	ctxKeyLogger ctxKey = iota
	ctxKeyTrace
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
