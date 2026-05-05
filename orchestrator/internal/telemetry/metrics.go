// Package telemetry — OTel metric instruments for HTTP, LLM, and search operations.
//
// Instruments are lazily initialised on first use via sync.Once so they can be
// called before InitOTel() (e.g. in tests) and still produce no-ops safely.
package telemetry

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "wisdev"

// ── instrument bundle ────────────────────────────────────────────────────────

type instruments struct {
	httpReqTotal            metric.Int64Counter
	httpReqDuration         metric.Float64Histogram
	llmReqTotal             metric.Int64Counter
	llmReqDuration          metric.Float64Histogram
	searchReqTotal          metric.Int64Counter
	llmErrorTotal           metric.Int64Counter
	searchErrorTotal        metric.Int64Counter
	circuitBreakerTripTotal metric.Int64Counter
	resourceRejectionTotal  metric.Int64Counter
}

var (
	inst                    instruments
	instOnce                sync.Once
	failureMetricRecorderMu sync.Mutex
	failureMetricRecorder   func(name string, attrs map[string]string, attributes map[string]any)
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
		llmErrorTotal, _ := m.Int64Counter("llm_request_errors_total",
			metric.WithDescription("Total number of failed LLM requests by failure kind"),
		)
		searchErrorTotal, _ := m.Int64Counter("search_provider_errors_total",
			metric.WithDescription("Total number of failed search-provider requests by failure kind"),
		)
		circuitBreakerTripTotal, _ := m.Int64Counter("circuit_breaker_trips_total",
			metric.WithDescription("Total number of circuit breaker trip events by failure kind"),
		)
		resourceRejectionTotal, _ := m.Int64Counter("resource_rejections_total",
			metric.WithDescription("Total number of resource governor graceful rejections"),
		)

		inst = instruments{
			httpReqTotal:            httpTotal,
			httpReqDuration:         httpDuration,
			llmReqTotal:             llmTotal,
			llmReqDuration:          llmDuration,
			searchReqTotal:          searchTotal,
			llmErrorTotal:           llmErrorTotal,
			searchErrorTotal:        searchErrorTotal,
			circuitBreakerTripTotal: circuitBreakerTripTotal,
			resourceRejectionTotal:  resourceRejectionTotal,
		}
	})
	return inst
}

// ── public recording helpers ─────────────────────────────────────────────────

// RecordLLMRequest increments the LLM request counter and records latency.
func RecordLLMRequest(operation, model string, err error, duration time.Duration) {
	status := statusLabel(err)
	kind := extractErrorKind(err)
	i := getInstruments()
	attrs := metric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("model", model),
		attribute.String("status", status),
		attribute.String("kind", kind),
		attribute.String("error_kind", kind),
	)
	i.llmReqTotal.Add(context.Background(), 1, attrs)
	if err != nil {
		errorAttrs := metric.WithAttributes(
			attribute.String("operation", operation),
			attribute.String("model", model),
			attribute.String("kind", kind),
			attribute.String("error_kind", kind),
		)
		i.llmErrorTotal.Add(context.Background(), 1, errorAttrs)
		captureFailureMetric("llm_request_errors_total", map[string]string{"operation": operation, "model": model, "kind": kind, "error_kind": kind}, nil)
	}
	i.llmReqDuration.Record(context.Background(), duration.Seconds(),
		metric.WithAttributes(
			attribute.String("operation", operation),
			attribute.String("model", model),
		),
	)
}

// RecordLLMBudgetRequest records an LLM request with full budget context for
// diagnosing timeout vs budget exhaustion issues.
func RecordLLMBudgetRequest(operation, model string, err error, duration time.Duration, budgetMs int64, coldStart bool) {
	status := statusLabel(err)
	kind := extractErrorKind(err)
	i := getInstruments()
	attrs := metric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("model", model),
		attribute.String("status", status),
		attribute.String("kind", kind),
		attribute.String("error_kind", kind),
		attribute.Bool("cold_start", coldStart),
	)
	i.llmReqTotal.Add(context.Background(), 1, attrs)
	if err != nil {
		errorAttrs := metric.WithAttributes(
			attribute.String("operation", operation),
			attribute.String("model", model),
			attribute.String("kind", kind),
			attribute.String("error_kind", kind),
			attribute.Bool("cold_start", coldStart),
		)
		i.llmErrorTotal.Add(context.Background(), 1, errorAttrs)
		captureFailureMetric("llm_request_errors_total", map[string]string{"operation": operation, "model": model, "kind": kind, "error_kind": kind}, map[string]any{"cold_start": coldStart})
	}
	i.llmReqDuration.Record(context.Background(), duration.Seconds(),
		metric.WithAttributes(
			attribute.String("operation", operation),
			attribute.String("model", model),
			attribute.Bool("cold_start", coldStart),
			attribute.Int64("budget_ms", budgetMs),
		),
	)
}

// RecordSearchProviderRequest increments the search provider request counter.
func RecordSearchProviderRequest(provider string, err error) {
	i := getInstruments()
	kind := extractErrorKind(err)
	i.searchReqTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("provider", provider),
			attribute.String("status", statusLabel(err)),
			attribute.String("kind", kind),
			attribute.String("error_kind", kind),
		),
	)
	if err != nil {
		i.searchErrorTotal.Add(context.Background(), 1,
			metric.WithAttributes(
				attribute.String("provider", provider),
				attribute.String("kind", kind),
				attribute.String("error_kind", kind),
			),
		)
		captureFailureMetric("search_provider_errors_total", map[string]string{"provider": provider, "kind": kind, "error_kind": kind}, nil)
	}
}

// RecordCircuitBreakerTrip records a circuit breaker transition into an
// operator-visible open/reject state.
func RecordCircuitBreakerTrip(name, state string, err error) {
	kind := extractErrorKind(err)
	i := getInstruments()
	i.circuitBreakerTripTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("breaker", name),
			attribute.String("state", state),
			attribute.String("kind", kind),
			attribute.String("error_kind", kind),
		),
	)
	captureFailureMetric("circuit_breaker_trips_total", map[string]string{"breaker": name, "state": state, "kind": kind, "error_kind": kind}, nil)
}

// RecordResourceRejection records graceful load shedding before the process is
// pushed into an unbounded failure mode.
func RecordResourceRejection(taskType, status string) {
	i := getInstruments()
	i.resourceRejectionTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("task_type", taskType),
			attribute.String("status", status),
			attribute.String("kind", "system_overload"),
		),
	)
	captureFailureMetric("resource_rejections_total", map[string]string{"task_type": taskType, "status": status, "kind": "system_overload"}, nil)
}

// ── HTTP metrics middleware ───────────────────────────────────────────────────

// MetricsMiddleware records HTTP request count and latency via OTel.
// Place it after the OTel HTTP middleware (otelhttp.NewHandler) so that the
// span context is available for potential future exemplar linking.
func MetricsMiddleware(next http.Handler) http.Handler {
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

		dur := time.Since(start).Seconds()
		i := getInstruments()
		statusStr := statusFromCode(base.status)
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

func extractErrorKind(err error) string {
	if err == nil {
		return "none"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "timeout") {
		return "timeout"
	}
	if strings.Contains(msg, "429") || strings.Contains(msg, "quota") || strings.Contains(msg, "rate") || strings.Contains(msg, "resource exhausted") {
		return "rate_limit"
	}
	if strings.Contains(msg, "circuit breaker") || strings.Contains(msg, "concurrency") {
		return "system_overload"
	}
	if strings.Contains(msg, "system resources exhausted") || strings.Contains(msg, "gracefully rejecting") {
		return "system_overload"
	}
	if strings.Contains(msg, " 5") || strings.Contains(msg, "upstream error") || strings.Contains(msg, "503") || strings.Contains(msg, "502") || strings.Contains(msg, "500") || strings.Contains(msg, "504") {
		return "upstream_5xx"
	}
	return "unknown"
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

func captureFailureMetric(name string, attrs map[string]string, attributes map[string]any) {
	failureMetricRecorderMu.Lock()
	recorder := failureMetricRecorder
	failureMetricRecorderMu.Unlock()
	if recorder == nil {
		return
	}
	recorder(name, attrs, attributes)
}

func captureFailureMetricsForTest(recorder func(name string, attrs map[string]string, attributes map[string]any)) func() {
	failureMetricRecorderMu.Lock()
	previous := failureMetricRecorder
	failureMetricRecorder = recorder
	failureMetricRecorderMu.Unlock()
	return func() {
		failureMetricRecorderMu.Lock()
		failureMetricRecorder = previous
		failureMetricRecorderMu.Unlock()
	}
}
