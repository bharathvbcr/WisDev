// Package telemetry — structured logging with GCP Cloud Logging trace correlation.
//
// Every log record produced via FromCtx() or the global slog default will have
// these extra fields when an active OTel span is present in the context:
//
//	"logging.googleapis.com/trace"        → "projects/<project>/traces/<traceID>"
//	"logging.googleapis.com/spanId"       → hex span ID
//	"logging.googleapis.com/traceSampled" → true/false
//
// These are the canonical field names Cloud Logging uses to surface the
// "View in Trace" button in Log Explorer.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// ============================================================
// GCP trace-correlated slog handler
// ============================================================

type gcpHandler struct {
	inner   slog.Handler
	project string
}

// newGCPHandler wraps any slog.Handler and injects GCP trace correlation fields
// into every log record that has an active OTel span in its context.
func newGCPHandler(inner slog.Handler, projectID string) slog.Handler {
	return &gcpHandler{inner: inner, project: projectID}
}

func (h *gcpHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *gcpHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &gcpHandler{inner: h.inner.WithAttrs(attrs), project: h.project}
}

func (h *gcpHandler) WithGroup(name string) slog.Handler {
	return &gcpHandler{inner: h.inner.WithGroup(name), project: h.project}
}

// Handle injects OTel span context as GCP Cloud Logging trace fields so that
// every log line in Cloud Logging can be correlated with a Cloud Trace span.
func (h *gcpHandler) Handle(ctx context.Context, r slog.Record) error {
	span := trace.SpanFromContext(ctx)
	if span != nil && span.SpanContext().IsValid() {
		sc := span.SpanContext()
		// Clone the record so we don't mutate the caller's copy.
		r2 := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
		r.Attrs(func(a slog.Attr) bool {
			r2.AddAttrs(a)
			return true
		})
		traceField := fmt.Sprintf("projects/%s/traces/%s", h.project, sc.TraceID().String())
		r2.AddAttrs(
			slog.String("logging.googleapis.com/trace", traceField),
			slog.String("logging.googleapis.com/spanId", sc.SpanID().String()),
			slog.Bool("logging.googleapis.com/traceSampled", sc.IsSampled()),
		)
		return h.inner.Handle(ctx, r2)
	}
	return h.inner.Handle(ctx, r)
}

// ============================================================
// Logger initialisation
// ============================================================

var logger *slog.Logger

func init() {
	initLogger()
}

func initLogger() {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}

	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")

	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     level,
		AddSource: false,
	})

	var handler slog.Handler
	if projectID != "" {
		// Wrap the JSON handler with GCP trace correlation when a project is known.
		handler = newGCPHandler(jsonHandler, projectID)
	} else {
		handler = jsonHandler
	}

	logger = slog.New(handler)
	slog.SetDefault(logger)
}

// Logger returns the global structured logger.
func Logger() *slog.Logger { return logger }

// FromCtx returns a logger pre-populated with the OTel trace ID from ctx.
// Prefer this over the global slog functions inside request handlers so that
// trace correlation is automatic.
func FromCtx(ctx context.Context) *slog.Logger {
	span := trace.SpanFromContext(ctx)
	if span != nil && span.SpanContext().IsValid() {
		return logger.With("trace_id", span.SpanContext().TraceID().String())
	}
	return logger
}

func requestLogAttrs(r *http.Request, status int64, latencyMs int64) []any {
	attrs := []any{
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"latency_ms", latencyMs,
		"user_agent", r.UserAgent(),
	}

	if r.URL.Path == "/api/proxy/external" {
		query := r.URL.Query()
		attrs = append(attrs,
			"proxy_has_url", query.Get("url") != "",
			"proxy_has_provider", query.Get("provider") != "",
			"proxy_readiness", query.Get("readiness") == "1",
		)
	}

	return attrs
}

// ============================================================
// Request logging middleware
// ============================================================

// RequestLogger logs every HTTP request with method, path, status, latency, and
// OTel trace ID. It must be placed AFTER the OTel HTTP middleware so that the
// span is already in the context when it runs.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		base := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		writer := http.ResponseWriter(base)
		if flusher, ok := w.(http.Flusher); ok {
			writer = &flushResponseWriter{
				responseWriter: base,
				flusher:        flusher,
			}
		}

		next.ServeHTTP(writer, r)

		ctx := r.Context()
		log := FromCtx(ctx)
		log.InfoContext(ctx, "request", requestLogAttrs(r, int64(base.status), time.Since(start).Milliseconds())...)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

type flushResponseWriter struct {
	*responseWriter
	flusher http.Flusher
}

func (rw *flushResponseWriter) Flush() {
	rw.flusher.Flush()
}

// ============================================================
// Structured error taxonomy
// ============================================================

// ErrorCode categorises errors for structured logging and alerting.
type ErrorCode string

const (
	ErrProviderTimeout  ErrorCode = "PROVIDER_TIMEOUT"
	ErrLLMRateLimit     ErrorCode = "LLM_RATE_LIMIT"
	ErrLLMUnavailable   ErrorCode = "LLM_UNAVAILABLE"
	ErrLLMBudgetExhaust ErrorCode = "LLM_BUDGET_EXHAUSTED"
	ErrLLMColdStart     ErrorCode = "LLM_COLD_START_TIMEOUT"
	ErrLLMThinkingLimit ErrorCode = "LLM_THINKING_LIMIT"
	ErrCacheMiss        ErrorCode = "CACHE_MISS"
	ErrCacheUnavailable ErrorCode = "CACHE_UNAVAILABLE"
	ErrInvalidRequest   ErrorCode = "INVALID_REQUEST"
	ErrUnauthorized     ErrorCode = "UNAUTHORIZED"
	ErrInternal         ErrorCode = "INTERNAL_ERROR"
	ErrProviderError    ErrorCode = "PROVIDER_ERROR"
)

// LogError logs a structured error with full OTel trace context.
func LogError(ctx context.Context, code ErrorCode, err error, extra ...any) {
	args := append([]any{
		"error_code", string(code),
		"error", err.Error(),
	}, extra...)
	FromCtx(ctx).ErrorContext(ctx, "request_error", args...)
}

// LogLLMBudgetEvent logs a structured LLM budget event with context propagation.
// Use this at every significant stage of an LLM call (start, attempt, success, failure)
// to provide full observability into budget utilization and timeout causes.
func LogLLMBudgetEvent(ctx context.Context, event string, extra ...any) {
	FromCtx(ctx).InfoContext(ctx, event, extra...)
}
