package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestTraceIDFrom(t *testing.T) {
	is := assert.New(t)

	t.Run("empty context", func(t *testing.T) {
		is.Empty(TraceIDFrom(context.Background()))
	})

	t.Run("with invalid span", func(t *testing.T) {
		ctx := context.Background()
		// noop span is invalid by default
		_, span := noop.NewTracerProvider().Tracer("test").Start(ctx, "test")
		is.Empty(TraceIDFrom(ctx))
		_ = span
	})
}

func TestNewTraceID(t *testing.T) {
	is := assert.New(t)
	id1 := NewTraceID()
	id2 := NewTraceID()
	is.NotEmpty(id1)
	is.NotEmpty(id2)
	is.NotEqual(id1, id2)
	is.Len(id1, 16)
}

func TestMetricsHandler(t *testing.T) {
	is := assert.New(t)

	t.Run("unavailable before init", func(t *testing.T) {
		promHandler = nil // Ensure reset for test
		handler := MetricsHandler()
		req := httptest.NewRequest("GET", "/metrics", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		is.Equal(http.StatusServiceUnavailable, w.Code)
	})
}
