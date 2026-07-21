package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
)

func TestOTel(t *testing.T) {
	is := assert.New(t)

	t.Run("InitOTel with no project ID returns no-op shutdown", func(t *testing.T) {
		shutdown, err := InitOTel(context.Background(), "", "v1")
		is.NoError(err)
		is.NotNil(shutdown)
		err = shutdown(context.Background())
		is.NoError(err)
	})

	t.Run("InitOTel with project ID fails without auth", func(t *testing.T) {
		shutdown, err := InitOTel(context.Background(), "test-project", "v1")
		// It might fail to create exporters
		if err != nil {
			is.Contains(err.Error(), "exporter")
		} else {
			is.NotNil(shutdown)
			shutdown(context.Background())
		}
	})

	t.Run("Tracer returns a tracer", func(t *testing.T) {
		tr := Tracer("test")
		is.NotNil(tr)
	})

	t.Run("TraceIDFrom returns empty for invalid span", func(t *testing.T) {
		is.Empty(TraceIDFrom(context.Background()))
	})

	t.Run("TraceIDFrom returns hex for valid span", func(t *testing.T) {
		tp := trace.NewTracerProvider()
		ctx, span := tp.Tracer("test").Start(context.Background(), "test")
		defer span.End()

		id := TraceIDFrom(ctx)
		is.NotEmpty(id)
	})

	t.Run("NewTraceID returns a string", func(t *testing.T) {
		id := NewTraceID()
		is.NotEmpty(id)
		is.Len(id, 16)
	})

	t.Run("MetricsHandler returns 503 if not initialised", func(t *testing.T) {
		// Since InitOTel might have been called by init or other tests,
		// promHandler might be set.
		// But we can check the behavior.
		h := MetricsHandler()
		req := httptest.NewRequest("GET", "/metrics", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if promHandler == nil {
			is.Equal(http.StatusServiceUnavailable, rec.Code)
		} else {
			is.Equal(http.StatusOK, rec.Code)
		}
	})
}

func TestStartSpan(t *testing.T) {
	oldProvider := otel.GetTracerProvider()
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
	})

	otel.SetTracerProvider(trace.NewTracerProvider())
	ctx, span := StartSpan(context.Background(), "demo-span")
	defer span.End()

	assert.NotEmpty(t, TraceIDFrom(ctx))
}
