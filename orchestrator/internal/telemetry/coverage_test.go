package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type recordingHandler struct {
	record  slog.Record
	handled bool
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.handled = true
	h.record = r
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordingHandler) WithGroup(string) slog.Handler { return h }

type delegatingHandler struct {
	attrsCalled bool
	groupCalled bool
}

func (h *delegatingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *delegatingHandler) Handle(context.Context, slog.Record) error { return nil }

func (h *delegatingHandler) WithAttrs([]slog.Attr) slog.Handler {
	h.attrsCalled = true
	return h
}

func (h *delegatingHandler) WithGroup(string) slog.Handler {
	h.groupCalled = true
	return h
}

func recordAttrs(r slog.Record) map[string]any {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	return attrs
}

func TestGCPHandlerHandle(t *testing.T) {
	t.Run("injects trace metadata when a span is present", func(t *testing.T) {
		inner := &recordingHandler{}
		h := newGCPHandler(inner, "test-project")

		traceID, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
		assert.NoError(t, err)
		spanID, err := oteltrace.SpanIDFromHex("0123456789abcdef")
		assert.NoError(t, err)

		ctx := oteltrace.ContextWithSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
			TraceID:    traceID,
			SpanID:     spanID,
			TraceFlags: oteltrace.FlagsSampled,
		}))

		rec := slog.NewRecord(time.Now(), slog.LevelInfo, "message", 0)
		rec.AddAttrs(slog.String("request_id", "abc123"))

		err = h.Handle(ctx, rec)
		assert.NoError(t, err)
		assert.True(t, inner.handled)

		attrs := recordAttrs(inner.record)
		assert.Equal(t, "abc123", attrs["request_id"])
		assert.Equal(t, fmt.Sprintf("projects/%s/traces/%s", "test-project", traceID.String()), attrs["logging.googleapis.com/trace"])
		assert.Equal(t, spanID.String(), attrs["logging.googleapis.com/spanId"])
		assert.Equal(t, true, attrs["logging.googleapis.com/traceSampled"])
	})

	t.Run("passes through records without a span", func(t *testing.T) {
		inner := &recordingHandler{}
		h := newGCPHandler(inner, "test-project")

		rec := slog.NewRecord(time.Now(), slog.LevelInfo, "message", 0)
		rec.AddAttrs(slog.String("request_id", "abc123"))

		err := h.Handle(context.Background(), rec)
		assert.NoError(t, err)
		assert.True(t, inner.handled)

		attrs := recordAttrs(inner.record)
		assert.Equal(t, "abc123", attrs["request_id"])
		_, hasTrace := attrs["logging.googleapis.com/trace"]
		_, hasSpan := attrs["logging.googleapis.com/spanId"]
		_, hasSampled := attrs["logging.googleapis.com/traceSampled"]
		assert.False(t, hasTrace)
		assert.False(t, hasSpan)
		assert.False(t, hasSampled)
	})

	t.Run("delegates WithAttrs and WithGroup", func(t *testing.T) {
		inner := &delegatingHandler{}
		h := newGCPHandler(inner, "test-project")

		assert.NotNil(t, h.WithAttrs([]slog.Attr{slog.String("k", "v")}))
		assert.NotNil(t, h.WithGroup("group"))
		assert.True(t, inner.attrsCalled)
		assert.True(t, inner.groupCalled)
	})
}

func TestRequestLogger_DefaultStatus(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	mw := RequestLogger(handler)
	req := httptest.NewRequest(http.MethodGet, "/test-path", nil)
	req.Header.Set("User-Agent", "test-agent")
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestMetricsMiddleware_DefaultStatus(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	mw := MetricsMiddleware(handler)
	req := httptest.NewRequest(http.MethodPost, "/search/parallel", nil)
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestMetricsHandlerBranches(t *testing.T) {
	old := promHandler
	t.Cleanup(func() {
		promHandler = old
	})

	promHandler = nil
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	MetricsHandler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "metrics not yet initialised")

	promHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	rec = httptest.NewRecorder()

	MetricsHandler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTeapot, rec.Code)
}
