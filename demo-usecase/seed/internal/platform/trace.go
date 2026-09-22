package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

// OBS-5: W3C trace context, propagated on every internal call.
// https://www.w3.org/TR/trace-context/
const TraceparentHeader = "traceparent"

// TraceContext is the subset of W3C trace context we carry.
type TraceContext struct {
	TraceID string // 32 hex chars
	SpanID  string // 16 hex chars
	Flags   string // 2 hex chars
}

func (tc TraceContext) Header() string {
	return "00-" + tc.TraceID + "-" + tc.SpanID + "-" + tc.Flags
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not a recoverable condition for trace generation;
		// fall back to a fixed-width zero id rather than panicking a request.
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}

func NewTraceContext() TraceContext {
	return TraceContext{TraceID: randHex(16), SpanID: randHex(8), Flags: "01"}
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// ParseTraceparent accepts an inbound header. Invalid or absent yields a fresh context,
// so a malformed upstream header never drops the trace entirely.
func ParseTraceparent(v string) (TraceContext, bool) {
	parts := strings.Split(strings.TrimSpace(v), "-")
	if len(parts) != 4 || parts[0] != "00" {
		return NewTraceContext(), false
	}
	if !isHex(parts[1], 32) || !isHex(parts[2], 16) || !isHex(parts[3], 2) {
		return NewTraceContext(), false
	}
	if parts[1] == strings.Repeat("0", 32) || parts[2] == strings.Repeat("0", 16) {
		return NewTraceContext(), false
	}
	return TraceContext{TraceID: parts[1], SpanID: parts[2], Flags: parts[3]}, true
}

func WithTrace(ctx context.Context, tc TraceContext) context.Context {
	return context.WithValue(ctx, ctxKeyTrace, tc)
}

// TraceFrom returns the trace context, minting one if the context carries none.
func TraceFrom(ctx context.Context) TraceContext {
	if tc, ok := ctx.Value(ctxKeyTrace).(TraceContext); ok && tc.TraceID != "" {
		return tc
	}
	return NewTraceContext()
}

// TraceIDFrom is the value that goes into every log line (OBS-1).
func TraceIDFrom(ctx context.Context) string { return TraceFrom(ctx).TraceID }

// InjectTrace sets traceparent on an outbound request, continuing the inbound trace with a
// new span id. This is what makes collections-api -> ledger a single trace (OBS-5).
func InjectTrace(ctx context.Context, req *http.Request) {
	parent := TraceFrom(ctx)
	child := TraceContext{TraceID: parent.TraceID, SpanID: randHex(8), Flags: parent.Flags}
	req.Header.Set(TraceparentHeader, child.Header())
}
