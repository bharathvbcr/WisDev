// Package telemetry — OTel provider bootstrap for Cloud Trace + Cloud Monitoring.
//
// Call InitOTel once in main() before serving requests. The returned shutdown
// function must be deferred; it flushes any pending spans/metrics before the
// process exits.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	cloudmonitoring "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric"
	cloudtrace "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const serviceName = "wisdev-go-orchestrator"

// promRegistry is the Prometheus registry used for the pull /metrics endpoint.
// It is initialised once by InitOTel and used by MetricsHandler.
var (
	promRegistry     *prometheus.Registry
	promRegistryOnce sync.Once
	promHandler      http.Handler
	resourceNew      = resource.New
	cloudTraceNew    = func(projectID string) (sdktrace.SpanExporter, error) {
		return cloudtrace.New(cloudtrace.WithProjectID(projectID))
	}
	cloudMonitoringNew = func(projectID string) (sdkmetric.Exporter, error) {
		return cloudmonitoring.New(cloudmonitoring.WithProjectID(projectID))
	}
	promExporterNew = func(reg *prometheus.Registry) (*promexporter.Exporter, error) {
		return promexporter.New(promexporter.WithRegisterer(reg))
	}
	traceProviderShutdown = func(tp *sdktrace.TracerProvider, ctx context.Context) error {
		return tp.Shutdown(ctx)
	}
	metricProviderShutdown = func(mp *sdkmetric.MeterProvider, ctx context.Context) error {
		return mp.Shutdown(ctx)
	}
	randRead = rand.Read
)

// ShutdownFunc flushes pending telemetry and releases exporter connections.
type ShutdownFunc func(context.Context) error

// InitOTel wires Cloud Trace and Cloud Monitoring to the global OTel providers.
// projectID is the GCP project; pass "" to read GOOGLE_CLOUD_PROJECT from env.
// serviceVersion is embedded as a resource attribute on every span/metric.
func InitOTel(ctx context.Context, projectID, serviceVersion string) (ShutdownFunc, error) {
	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if projectID == "" {
		// No GCP project available (local dev without credentials).
		// Leave the default global no-op providers in place.
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
		return func(context.Context) error { return nil }, nil
	}

	res, err := resourceNew(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
			semconv.CloudProviderKey.String("gcp"),
			semconv.CloudAccountIDKey.String(projectID),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	// ── Cloud Trace (spans) ──────────────────────────────────────────────────
	traceExporter, err := cloudTraceNew(projectID)
	if err != nil {
		return nil, fmt.Errorf("cloud trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		// BatchSpanProcessor is non-blocking; critical for low-latency request paths.
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		// Sample 100% of requests; adjust with sdktrace.TraceIDRatioBased for
		// high-traffic production deployments.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// ── Cloud Monitoring (metrics, push) ────────────────────────────────────
	metricExporter, err := cloudMonitoringNew(projectID)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("cloud monitoring exporter: %w", err)
	}

	// ── Prometheus (metrics, pull via /metrics) ───────────────────────────
	// A dedicated Prometheus registry keeps our metrics isolated from
	// any default global registry that test frameworks might register to.
	reg := prometheus.NewRegistry()
	promRegistryOnce.Do(func() {
		promRegistry = reg
		promHandler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})
	})
	promExp, err := promExporterNew(reg)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("prometheus exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		// Push to Cloud Monitoring every 60 s (minimum GCM resolution).
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		// Pull via /metrics — Prometheus scrapes this endpoint on demand.
		sdkmetric.WithReader(promExp),
		sdkmetric.WithResource(res),
	)

	// ── Global registration ──────────────────────────────────────────────────
	// W3C TraceContext propagates the traceparent header between services.
	// Baggage propagates key-value metadata (e.g. user tier) alongside spans.
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func(shutdownCtx context.Context) error {
		var errs []error
		if err := traceProviderShutdown(tp, shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("trace provider shutdown: %w", err))
		}
		if err := metricProviderShutdown(mp, shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("metric provider shutdown: %w", err))
		}
		if len(errs) > 0 {
			return fmt.Errorf("otel shutdown errors: %v", errs)
		}
		return nil
	}, nil
}

// Tracer returns a named tracer from the global provider.
// Use this instead of calling otel.Tracer() directly so all OTel surface area
// stays inside the telemetry package and callers don't import otel directly.
func Tracer(name string) oteltrace.Tracer {
	return otel.GetTracerProvider().Tracer(name)
}

// TraceIDFrom returns the trace ID associated with the current span in the context.
// Returns an empty string if no span is found or if the trace ID is invalid.
func TraceIDFrom(ctx context.Context) string {
	span := oteltrace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return ""
	}
	return span.SpanContext().TraceID().String()
}

// NewTraceID generates a random 16-character hex string suitable for a trace ID.
// Note: This is a fallback when no OTel span is present.
func NewTraceID() string {
	b := make([]byte, 8)
	if _, err := randRead(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// StartSpan begins a new span with the given name.
// It returns the context with the new span and the span itself.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, oteltrace.Span) {
	return Tracer("wisdev").Start(ctx, name, oteltrace.WithAttributes(attrs...))
}

// MetricsHandler returns the Prometheus pull endpoint that mirrors all OTel
// metric instruments registered with the global MeterProvider.
// Call this after InitOTel has been called; before that it returns a 503.
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if promHandler == nil {
			http.Error(w, "metrics not yet initialised", http.StatusServiceUnavailable)
			return
		}
		promHandler.ServeHTTP(w, r)
	})
}
