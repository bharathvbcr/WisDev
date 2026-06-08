package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type fakeTraceExporter struct {
	exportErr   error
	shutdownErr error
}

func (e *fakeTraceExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return e.exportErr
}

func (e *fakeTraceExporter) Shutdown(context.Context) error {
	return e.shutdownErr
}

type fakeMetricExporter struct {
	shutdownErr error
}

func (e *fakeMetricExporter) Temporality(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (e *fakeMetricExporter) Aggregation(sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return nil
}

func (e *fakeMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error {
	return nil
}

func (e *fakeMetricExporter) ForceFlush(context.Context) error {
	return nil
}

func (e *fakeMetricExporter) Shutdown(context.Context) error {
	return e.shutdownErr
}

func TestInitLoggerBranches(t *testing.T) {
	oldLogger := logger
	oldDefault := slog.Default()
	t.Cleanup(func() {
		logger = oldLogger
		slog.SetDefault(oldDefault)
	})

	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	initLogger()
	assert.NotNil(t, Logger())

	t.Setenv("LOG_LEVEL", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	initLogger()
	assert.NotNil(t, Logger())
}

func TestInitOTelBranches(t *testing.T) {
	oldResourceNew := resourceNew
	oldCloudTraceNew := cloudTraceNew
	oldCloudMonitoringNew := cloudMonitoringNew
	oldPromExporterNew := promExporterNew
	oldTraceShutdown := traceProviderShutdown
	oldMetricShutdown := metricProviderShutdown
	oldRegistry := promRegistry
	oldHandler := promHandler
	t.Cleanup(func() {
		resourceNew = oldResourceNew
		cloudTraceNew = oldCloudTraceNew
		cloudMonitoringNew = oldCloudMonitoringNew
		promExporterNew = oldPromExporterNew
		traceProviderShutdown = oldTraceShutdown
		metricProviderShutdown = oldMetricShutdown
		promRegistry = oldRegistry
		promHandler = oldHandler
	})

	t.Run("env project id and successful shutdown", func(t *testing.T) {
		t.Setenv("GOOGLE_CLOUD_PROJECT", "env-project")
		resourceNew = func(ctx context.Context, opts ...resource.Option) (*resource.Resource, error) {
			return resource.New(ctx, opts...)
		}
		cloudTraceNew = func(projectID string) (sdktrace.SpanExporter, error) {
			return &fakeTraceExporter{}, nil
		}
		cloudMonitoringNew = func(projectID string) (sdkmetric.Exporter, error) {
			return &fakeMetricExporter{}, nil
		}
		promExporterNew = func(reg *prometheus.Registry) (*promexporter.Exporter, error) {
			return promexporter.New(promexporter.WithRegisterer(reg))
		}

		shutdown, err := InitOTel(context.Background(), "", "v1")
		assert.NoError(t, err)
		assert.NotNil(t, shutdown)
		assert.NoError(t, shutdown(context.Background()))
	})

	t.Run("resource error", func(t *testing.T) {
		resourceNew = func(context.Context, ...resource.Option) (*resource.Resource, error) {
			return nil, errors.New("resource boom")
		}

		shutdown, err := InitOTel(context.Background(), "project", "v1")
		assert.Nil(t, shutdown)
		assert.ErrorContains(t, err, "otel resource")
	})

	t.Run("trace exporter error", func(t *testing.T) {
		resourceNew = func(ctx context.Context, opts ...resource.Option) (*resource.Resource, error) {
			return resource.New(ctx, opts...)
		}
		cloudTraceNew = func(string) (sdktrace.SpanExporter, error) {
			return nil, errors.New("trace boom")
		}

		shutdown, err := InitOTel(context.Background(), "project", "v1")
		assert.Nil(t, shutdown)
		assert.ErrorContains(t, err, "cloud trace exporter")
	})

	t.Run("metric exporter error", func(t *testing.T) {
		cloudTraceNew = func(projectID string) (sdktrace.SpanExporter, error) {
			return &fakeTraceExporter{}, nil
		}
		cloudMonitoringNew = func(string) (sdkmetric.Exporter, error) {
			return nil, errors.New("metric boom")
		}

		shutdown, err := InitOTel(context.Background(), "project", "v1")
		assert.Nil(t, shutdown)
		assert.ErrorContains(t, err, "cloud monitoring exporter")
	})

	t.Run("prometheus exporter error", func(t *testing.T) {
		cloudMonitoringNew = func(projectID string) (sdkmetric.Exporter, error) {
			return &fakeMetricExporter{}, nil
		}
		promExporterNew = func(*prometheus.Registry) (*promexporter.Exporter, error) {
			return nil, errors.New("prom boom")
		}

		shutdown, err := InitOTel(context.Background(), "project", "v1")
		assert.Nil(t, shutdown)
		assert.ErrorContains(t, err, "prometheus exporter")
	})

	t.Run("shutdown aggregates exporter errors", func(t *testing.T) {
		cloudTraceNew = func(projectID string) (sdktrace.SpanExporter, error) {
			return &fakeTraceExporter{}, nil
		}
		cloudMonitoringNew = func(projectID string) (sdkmetric.Exporter, error) {
			return &fakeMetricExporter{shutdownErr: errors.New("metric shutdown")}, nil
		}
		promExporterNew = func(reg *prometheus.Registry) (*promexporter.Exporter, error) {
			return promexporter.New(promexporter.WithRegisterer(reg))
		}

		shutdown, err := InitOTel(context.Background(), "project", "v1")
		assert.NoError(t, err)
		assert.NotNil(t, shutdown)
		err = shutdown(context.Background())
		assert.ErrorContains(t, err, "metric provider shutdown")
	})

	t.Run("shutdown aggregates trace errors", func(t *testing.T) {
		cloudTraceNew = func(projectID string) (sdktrace.SpanExporter, error) {
			return &fakeTraceExporter{}, nil
		}
		cloudMonitoringNew = func(projectID string) (sdkmetric.Exporter, error) {
			return &fakeMetricExporter{}, nil
		}
		promExporterNew = func(reg *prometheus.Registry) (*promexporter.Exporter, error) {
			return promexporter.New(promexporter.WithRegisterer(reg))
		}
		traceProviderShutdown = func(*sdktrace.TracerProvider, context.Context) error {
			return errors.New("trace shutdown")
		}
		metricProviderShutdown = func(mp *sdkmetric.MeterProvider, ctx context.Context) error {
			return nil
		}

		shutdown, err := InitOTel(context.Background(), "project", "v1")
		assert.NoError(t, err)
		assert.NotNil(t, shutdown)
		err = shutdown(context.Background())
		assert.ErrorContains(t, err, "trace provider shutdown")
	})
}

func TestNewTraceIDFallback(t *testing.T) {
	oldRandRead := randRead
	t.Cleanup(func() {
		randRead = oldRandRead
	})

	randRead = func([]byte) (int, error) {
		return 0, errors.New("rand boom")
	}

	id := NewTraceID()
	assert.NotEmpty(t, id)
	assert.NotEqual(t, 16, len(id))
}

func TestMetricsHandlerScrapesFailureModeCounters(t *testing.T) {
	oldResourceNew := resourceNew
	oldCloudTraceNew := cloudTraceNew
	oldCloudMonitoringNew := cloudMonitoringNew
	oldPromExporterNew := promExporterNew
	oldTraceShutdown := traceProviderShutdown
	oldMetricShutdown := metricProviderShutdown
	oldRegistry := promRegistry
	oldHandler := promHandler
	oldInst := inst
	t.Cleanup(func() {
		resourceNew = oldResourceNew
		cloudTraceNew = oldCloudTraceNew
		cloudMonitoringNew = oldCloudMonitoringNew
		promExporterNew = oldPromExporterNew
		traceProviderShutdown = oldTraceShutdown
		metricProviderShutdown = oldMetricShutdown
		promRegistry = oldRegistry
		promRegistryOnce = sync.Once{}
		promHandler = oldHandler
		inst = oldInst
		instOnce = sync.Once{}
	})

	promRegistry = nil
	promRegistryOnce = sync.Once{}
	promHandler = nil
	inst = instruments{}
	instOnce = sync.Once{}
	resourceNew = func(ctx context.Context, opts ...resource.Option) (*resource.Resource, error) {
		return resource.New(ctx, opts...)
	}
	cloudTraceNew = func(projectID string) (sdktrace.SpanExporter, error) {
		return &fakeTraceExporter{}, nil
	}
	cloudMonitoringNew = func(projectID string) (sdkmetric.Exporter, error) {
		return &fakeMetricExporter{}, nil
	}
	promExporterNew = func(reg *prometheus.Registry) (*promexporter.Exporter, error) {
		return promexporter.New(promexporter.WithRegisterer(reg))
	}

	shutdown, err := InitOTel(context.Background(), "metrics-test-project", "v1")
	assert.NoError(t, err)
	assert.NotNil(t, shutdown)
	defer func() { _ = shutdown(context.Background()) }()

	RecordLLMRequest("generate", "heavy", errors.New("context deadline exceeded"), time.Millisecond)
	RecordSearchProviderRequest("openalex", errors.New("upstream 503"))
	RecordCircuitBreakerTrip("semantic_scholar", "open", errors.New("upstream 503"))
	RecordResourceRejection("wisdev_research_loop", "critical")

	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{
		"llm_request_errors_total",
		"search_provider_errors_total",
		"circuit_breaker_trips_total",
		"resource_rejections_total",
		`kind="timeout"`,
		`provider="openalex"`,
		`breaker="semantic_scholar"`,
		`task_type="wisdev_research_loop"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected /metrics scrape to contain %q, got:\n%s", want, body)
		}
	}
}

func TestInitTracingBranches(t *testing.T) {
	oldResourceNew := tracingResourceNew
	oldExporterNew := tracingExporterNew
	t.Cleanup(func() {
		tracingResourceNew = oldResourceNew
		tracingExporterNew = oldExporterNew
	})

	t.Run("resource error", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
		tracingResourceNew = func(context.Context, ...resource.Option) (*resource.Resource, error) {
			return nil, errors.New("resource boom")
		}

		shutdown, err := InitTracing("resource-error")
		assert.Nil(t, shutdown)
		assert.ErrorContains(t, err, "failed to create resource")
	})

	t.Run("exporter error", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
		tracingResourceNew = func(ctx context.Context, opts ...resource.Option) (*resource.Resource, error) {
			return resource.New(ctx, opts...)
		}
		tracingExporterNew = func(context.Context, string) (sdktrace.SpanExporter, error) {
			return nil, errors.New("exporter boom")
		}

		shutdown, err := InitTracing("exporter-error")
		assert.Nil(t, shutdown)
		assert.ErrorContains(t, err, "failed to create exporter")
	})
}
