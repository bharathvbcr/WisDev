package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/sdk/trace"
)

func TestLogger(t *testing.T) {
	is := assert.New(t)

	t.Run("Logger returns global logger", func(t *testing.T) {
		l := Logger()
		is.NotNil(l)
	})

	t.Run("FromCtx returns logger with trace ID if span exists", func(t *testing.T) {
		ctx := context.Background()

		// No span
		l1 := FromCtx(ctx)
		is.NotNil(l1)

		// With span
		tp := trace.NewTracerProvider()
		tracer := tp.Tracer("test")
		ctx, span := tracer.Start(ctx, "test-span")
		defer span.End()

		l2 := FromCtx(ctx)
		is.NotNil(l2)
	})

	t.Run("LogError logs structured error", func(t *testing.T) {
		ctx := context.Background()
		err := errors.New("test error")
		// This should not panic
		LogError(ctx, ErrInternal, err, "extra", "info")
	})
}
func TestGCPHandler(t *testing.T) {
	is := assert.New(t)

	t.Run("Handle injects GCP trace fields", func(t *testing.T) {
		inner := &mockSlogHandler{}
		h := newGCPHandler(inner, "test-project")

		is.True(h.Enabled(context.Background(), slog.LevelInfo))

		tp := trace.NewTracerProvider()
		ctx, span := tp.Tracer("test").Start(context.Background(), "test")
		defer span.End()

		record := slog.NewRecord(time.Now(), slog.LevelInfo, "test message", 0)
		err := h.Handle(ctx, record)
		is.NoError(err)
		is.True(inner.handled)
	})

	t.Run("WithAttrs and WithGroup", func(t *testing.T) {
		inner := &mockSlogHandler{}
		h := newGCPHandler(inner, "test-project")

		h2 := h.WithAttrs([]slog.Attr{slog.String("k", "v")})
		is.NotNil(h2)

		h3 := h.WithGroup("test-group")
		is.NotNil(h3)
	})
}

type mockSlogHandler struct {
	handled bool
}

func (m *mockSlogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (m *mockSlogHandler) Handle(context.Context, slog.Record) error {
	m.handled = true
	return nil
}
func (m *mockSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return m }
func (m *mockSlogHandler) WithGroup(name string) slog.Handler       { return m }
func TestRequestLogger(t *testing.T) {
	is := assert.New(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte("ok"))
	})

	mw := RequestLogger(handler)

	req := httptest.NewRequest("GET", "/test-path", nil)
	req.Header.Set("User-Agent", "test-agent")
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	is.Equal(http.StatusAccepted, rec.Code)
	is.Equal("ok", rec.Body.String())
}

func TestRequestLogger_ExternalProxyProbeMetadata(t *testing.T) {
	var buf bytes.Buffer
	original := logger
	originalDefault := slog.Default()
	testLogger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger = testLogger
	slog.SetDefault(testLogger)
	t.Cleanup(func() {
		logger = original
		slog.SetDefault(originalDefault)
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/external?readiness=1", nil)
	req.Header.Set("User-Agent", "test-agent")
	rec := httptest.NewRecorder()

	RequestLogger(handler).ServeHTTP(rec, req)

	var payload map[string]any
	err := json.Unmarshal(buf.Bytes(), &payload)
	assert.NoError(t, err)
	assert.Equal(t, "/api/proxy/external", payload["path"])
	assert.Equal(t, true, payload["proxy_readiness"])
	assert.Equal(t, false, payload["proxy_has_url"])
	assert.Equal(t, false, payload["proxy_has_provider"])
}

func TestResponseWriter(t *testing.T) {
	is := assert.New(t)
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, status: http.StatusOK}

	rw.WriteHeader(http.StatusNotFound)
	is.Equal(http.StatusNotFound, rw.status)
	is.Equal(http.StatusNotFound, rec.Code)
}

func TestRequestLogger_PreservesFlusher(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := w.(http.Flusher)
		assert.True(t, ok)
		w.WriteHeader(http.StatusOK)
	})

	mw := RequestLogger(handler)
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
