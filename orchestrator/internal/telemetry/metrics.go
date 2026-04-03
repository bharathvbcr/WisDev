// Package telemetry — OTel metric instruments for HTTP, LLM, and search operations.
//
// Instruments are lazily initialised on first use via sync.Once so they can be
// called before InitOTel() (e.g. in tests) and still produce no-ops safely.
package telemetry

import (
	"context"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "wisdev"

// ── instrument bundle ────────────────────────────────────────────────────────

type instruments struct {
	httpReqTotal    metric.Int64Counter
	httpReqDuration metric.Float64Histogram
	llmReqTotal     metric.Int64Counter
	llmReqDuration  metric.Float64Histogram
	searchReqTotal  metric.Int64Counter
}

var (
	inst     instruments
	instOnce sync.Once
)

func getInstruments() instruments {
	instOnce.Do(func() {
		m := otel.GetMeterProvider().Meter(meterName)

		httpTotal, _ := m.Int64Counter("wisdev.http.requests",
			metric.WithDescription("Total number of HTTP requests"),
		)
		httpDuration, _ := m.Float64Histogram("wisdev.http.request_duration",
			metric.WithDescription("Duration of HTTP requests in seconds"),
			metric.WithUnit("s"),
		)
		llmTotal, _ := m.Int64Counter("wisdev.llm.requests",
			metric.WithDescription("Total number of LLM sidecar requests"),
		)
		llmDuration, _ := m.Float64Histogram("wisdev.llm.request_duration",
			metric.WithDescription("Duration of LLM sidecar requests in seconds"),
			metric.WithUnit("s"),
		)
		searchTotal, _ := m.Int64Counter("wisdev.search.provider_requests",
			metric.WithDescription("Total number of academic search provider requests"),
		)

		inst = instruments{
			httpReqTotal:    httpTotal,
			httpReqDuration: httpDuration,
			llmReqTotal:     llmTotal,
			llmReqDuration:  llmDuration,
			searchReqTotal:  searchTotal,
		}
	})
	return inst
}

// ── public recording helpers ─────────────────────────────────────────────────

// RecordLLMRequest increments the LLM request counter and records latency.
func RecordLLMRequest(operation, model string, err error, duration time.Duration) {
	status := statusLabel(err)
	i := getInstruments()
	attrs := metric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("model", model),
		attribute.String("status", status),
	)
	i.llmReqTotal.Add(context.Background(), 1, attrs)
	i.llmReqDuration.Record(context.Background(), duration.Seconds(),
		metric.WithAttributes(
			attribute.String("operation", operation),
			attribute.String("model", model),
		),
	)
}

// RecordSearchProviderRequest increments the search provider request counter.
func RecordSearchProviderRequest(provider string, err error) {
	i := getInstruments()
	i.searchReqTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("provider", provider),
			attribute.String("status", statusLabel(err)),
		),
	)
}

// ── HTTP metrics middleware ───────────────────────────────────────────────────

// MetricsMiddleware records HTTP request count and latency via OTel.
// Place it after the OTel HTTP middleware (otelhttp.NewHandler) so that the
// span context is available for potential future exemplar linking.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		dur := time.Since(start).Seconds()
		i := getInstruments()
		statusStr := statusFromCode(rw.status)
		i.httpReqTotal.Add(r.Context(), 1,
			metric.WithAttributes(
				attribute.String("method", r.Method),
				attribute.String("path", r.URL.Path),
				attribute.String("status", statusStr),
			),
		)
		i.httpReqDuration.Record(r.Context(), dur,
			metric.WithAttributes(
				attribute.String("method", r.Method),
				attribute.String("path", r.URL.Path),
			),
		)
	})
}

// ── helpers ──────────────────────────────────────────────────────────────────

func statusLabel(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}

func statusFromCode(code int) string {
	switch {
	case code < 400:
		return "2xx_3xx"
	case code < 500:
		return "4xx"
	default:
		return "5xx"
	}
}
