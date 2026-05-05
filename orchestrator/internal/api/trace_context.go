package api

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
)

func requestTraceIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}

	if traceID := requestTraceIDFromContext(r.Context()); traceID != "" {
		return traceID
	}

	if traceID := strings.TrimSpace(r.Header.Get("X-Trace-Id")); traceID != "" {
		return traceID
	}

	if traceparent := strings.TrimSpace(r.Header.Get("traceparent")); traceparent != "" {
		if traceID := parseTraceparentTraceID(traceparent); traceID != "" {
			return traceID
		}
	}

	if r.URL != nil {
		if traceID := strings.TrimSpace(r.URL.Query().Get("traceId")); traceID != "" {
			return traceID
		}
		if traceID := strings.TrimSpace(r.URL.Query().Get("trace_id")); traceID != "" {
			return traceID
		}
	}

	return ""
}

func requestTraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	if traceID := telemetry.TraceIDFrom(ctx); strings.TrimSpace(traceID) != "" {
		return traceID
	}

	if value, ok := ctx.Value(ctxRequestTraceID).(string); ok {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}

	return ""
}

func parseTraceparentTraceID(traceparent string) string {
	parts := strings.Split(strings.TrimSpace(traceparent), "-")
	if len(parts) != 4 {
		return ""
	}

	traceID := strings.ToLower(strings.TrimSpace(parts[1]))
	if len(traceID) != 32 {
		return ""
	}

	id, err := trace.TraceIDFromHex(traceID)
	if err != nil || !id.IsValid() {
		return ""
	}

	return traceID
}
