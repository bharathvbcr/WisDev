package telemetry

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type metricSnapshot struct {
	name       string
	attrs      map[string]string
	attributes map[string]any
}

func TestMetrics(t *testing.T) {
	is := assert.New(t)

	t.Run("RecordLLMRequest records metrics without panic", func(t *testing.T) {
		// Should not panic even if providers are no-op
		RecordLLMRequest("chat", "gpt-4", nil, 100*time.Millisecond)
		RecordLLMRequest("chat", "gpt-4", errors.New("fail"), 50*time.Millisecond)
	})

	t.Run("RecordSearchProviderRequest records metrics without panic", func(t *testing.T) {
		RecordSearchProviderRequest("arxiv", nil)
		RecordSearchProviderRequest("arxiv", errors.New("fail"))
	})

	t.Run("statusLabel helper", func(t *testing.T) {
		is.Equal("success", statusLabel(nil))
		is.Equal("error", statusLabel(errors.New("err")))
	})

	t.Run("statusFromCode helper", func(t *testing.T) {
		is.Equal("2xx_3xx", statusFromCode(200))
		is.Equal("2xx_3xx", statusFromCode(301))
		is.Equal("4xx", statusFromCode(400))
		is.Equal("4xx", statusFromCode(404))
		is.Equal("5xx", statusFromCode(500))
		is.Equal("5xx", statusFromCode(503))
	})

	t.Run("extractErrorKind helper", func(t *testing.T) {
		is.Equal("none", extractErrorKind(nil))
		is.Equal("timeout", extractErrorKind(errors.New("context deadline exceeded")))
		is.Equal("rate_limit", extractErrorKind(errors.New("Error 429: quota exceeded")))
		is.Equal("system_overload", extractErrorKind(errors.New("circuit breaker open")))
		is.Equal("unknown", extractErrorKind(errors.New("some other error")))
	})
}

func TestFailureModeCounters(t *testing.T) {
	snapshots := []metricSnapshot{}
	restore := captureFailureMetricsForTest(func(name string, attrs map[string]string, attributes map[string]any) {
		snapshots = append(snapshots, metricSnapshot{name: name, attrs: attrs, attributes: attributes})
	})
	defer restore()

	RecordLLMRequest("generate", "heavy", contextDeadlineErr(), time.Second)
	RecordLLMRequest("generate", "heavy", errors.New("upstream returned 429"), time.Second)
	RecordLLMRequest("generate", "heavy", errors.New("upstream 503"), time.Second)
	RecordSearchProviderRequest("openalex", errors.New("upstream 503"))
	RecordCircuitBreakerTrip("semantic_scholar", "open", errors.New("upstream 503"))
	RecordResourceRejection("wisdev_research_loop", "critical")

	assertMetric := func(name string, kind string) {
		t.Helper()
		for _, snapshot := range snapshots {
			if snapshot.name == name && snapshot.attrs["kind"] == kind {
				return
			}
		}
		t.Fatalf("expected metric %s with kind %s in %#v", name, kind, snapshots)
	}

	assertMetric("llm_request_errors_total", "timeout")
	assertMetric("llm_request_errors_total", "rate_limit")
	assertMetric("llm_request_errors_total", "upstream_5xx")
	assertMetric("search_provider_errors_total", "upstream_5xx")
	assertMetric("circuit_breaker_trips_total", "upstream_5xx")
	assertMetric("resource_rejections_total", "system_overload")
}

func contextDeadlineErr() error {
	return errors.New("context deadline exceeded")
}

func TestMetricsMiddleware(t *testing.T) {
	is := assert.New(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mw := MetricsMiddleware(handler)

	req := httptest.NewRequest("POST", "/search/parallel", nil)
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	is.Equal(http.StatusNoContent, rec.Code)
}

func TestMetricsMiddleware_PreservesFlusher(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := w.(http.Flusher)
		assert.True(t, ok)
		w.WriteHeader(http.StatusOK)
	})

	mw := MetricsMiddleware(handler)
	req := httptest.NewRequest(http.MethodGet, "/wisdev/job/test/stream", nil)
	rec := httptest.NewRecorder()

	mw.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
