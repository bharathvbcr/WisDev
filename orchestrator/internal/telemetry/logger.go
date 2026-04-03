// Package telemetry — structured logging with GCP Structured Logging trace correlation.
//
// Every log record produced via FromCtx() or the global slog default will have
// these extra fields when an active OTel span is present in the context:
//
//	"trace.id"        → "traces/<traceID>"
//	"trace.span_id"       → hex span ID
//	"trace.idSampled" → true/false
//
// These are the canonical field names Structured Logging uses to surface the
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

// Handle injects OTel span context as GCP Structured Logging trace fields so that
// every log line in Structured Logging can be correlated with a OpenTelemetry span.
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
			slog.String("trace.id", traceField),
			slog.String("trace.span_id", sc.SpanID().String()),
			slog.Bool("trace.idSampled", sc.IsSampled()),
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
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}

	projectID := os.Getenv("VERTEX_PROJECT")

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

// ============================================================
// Request logging middleware
// ============================================================

// RequestLogger logs every HTTP request with method, path, status, latency, and
// OTel trace ID. It must be placed AFTER the OTel HTTP middleware so that the
// span is already in the context when it runs.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rw, r)

		ctx := r.Context()
		log := FromCtx(ctx)
		log.InfoContext(ctx, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"latency_ms", time.Since(start).Milliseconds(),
			"user_agent", r.UserAgent(),
		)
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

// ============================================================
// Structured error taxonomy
// ============================================================

// ErrorCode categorises errors for structured logging and alerting.
type ErrorCode string

const (
	ErrProviderTimeout  ErrorCode = "PROVIDER_TIMEOUT"
	ErrLLMRateLimit     ErrorCode = "LLM_RATE_LIMIT"
	ErrLLMUnavailable   ErrorCode = "LLM_UNAVAILABLE"
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
