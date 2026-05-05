package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
)

type timeoutErr struct{}

func (e timeoutErr) Error() string { return "timeout test" }
func (e timeoutErr) Timeout() bool { return true }

func TestWisdevRouteTraceIDResolution(t *testing.T) {
	t.Run("resolveWisdevRouteTraceID prefers requested trace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev?traceId=req&trace_id=legacy", nil)
		req.Header.Set("X-Trace-Id", "header-trace")
		assert.Equal(t, "requested-trace", resolveWisdevRouteTraceID(req, " requested-trace "))
	})

	t.Run("resolveWisdevRouteTraceID falls back to request headers and query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev?traceId=query-trace&trace_id=legacy-trace", nil)
		req.Header.Set("X-Trace-Id", "header-trace")
		assert.Equal(t, "header-trace", resolveWisdevRouteTraceID(req, ""))

		reqNoHeader := httptest.NewRequest(http.MethodGet, "/wisdev?traceId=query-trace&trace_id=legacy-trace", nil)
		assert.Equal(t, "query-trace", resolveWisdevRouteTraceID(reqNoHeader, ""))

		reqLegacy := httptest.NewRequest(http.MethodGet, "/wisdev?trace_id=legacy-trace", nil)
		assert.Equal(t, "legacy-trace", resolveWisdevRouteTraceID(reqLegacy, ""))
	})

	t.Run("resolveWisdevRouteOptionalTraceID picks requested or legacy fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev?traceId=query-trace", nil)
		assert.Equal(t, "requested-trace", resolveWisdevRouteOptionalTraceID(req, " requested-trace ", " legacy-trace "))
		assert.Equal(t, "legacy-trace", resolveWisdevRouteOptionalTraceID(req, " ", " legacy-trace "))
		assert.Equal(t, "query-trace", resolveWisdevRouteOptionalTraceID(req, " ", " "))
	})
}

func TestWisdevEnvelopeTraceHandling(t *testing.T) {
	t.Run("extractWisdevEnvelopeTraceID parses and trims", func(t *testing.T) {
		trace := extractWisdevEnvelopeTraceID([]byte(`{"traceId":"   abc-123   "}`))
		assert.Equal(t, "abc-123", trace)

		invalid := extractWisdevEnvelopeTraceID([]byte(`invalid json`))
		assert.Empty(t, invalid)
	})

	t.Run("writeCachedWisdevEnvelopeResponse sets trace header when available", func(t *testing.T) {
		rr := httptest.NewRecorder()
		payload := []byte(`{"traceId":"cached-trace","ok":true}`)
		writeCachedWisdevEnvelopeResponse(rr, http.StatusAccepted, payload)

		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		assert.Equal(t, http.StatusAccepted, rr.Code)
		assert.Equal(t, "cached-trace", rr.Header().Get("X-Trace-Id"))
		assert.JSONEq(t, string(payload), rr.Body.String())
	})

	t.Run("writeCachedWisdevEnvelopeResponse omits trace header for missing trace", func(t *testing.T) {
		rr := httptest.NewRecorder()
		payload := []byte(`{"status":"ok"}`)
		writeCachedWisdevEnvelopeResponse(rr, http.StatusOK, payload)

		assert.Empty(t, rr.Header().Get("X-Trace-Id"))
		assert.Equal(t, string(payload), rr.Body.String())
	})
}

func TestAnalyzeQueryFallbackReason(t *testing.T) {
	t.Run("classifyAnalyzeQueryFallbackReason returns expected mappings", func(t *testing.T) {
		assert.Equal(t, "", classifyAnalyzeQueryFallbackReason(nil))
		assert.Equal(t, "sidecar_timeout", classifyAnalyzeQueryFallbackReason(timeoutErr{}))
		assert.Equal(t, "sidecar_timeout", classifyAnalyzeQueryFallbackReason(errors.New("client.timeout")))
		assert.Equal(t, "llm_invalid_prompt", classifyAnalyzeQueryFallbackReason(errors.New("invalid_prompt detected")))
		assert.Equal(t, "llm_invalid_output", classifyAnalyzeQueryFallbackReason(errors.New("structured output invalid")))
		assert.Equal(t, "sidecar_unavailable", classifyAnalyzeQueryFallbackReason(errors.New("no such host: localhost")))
		assert.Equal(t, "llm_deadline_exceeded", classifyAnalyzeQueryFallbackReason(errors.New("deadline exceeded while processing")))
		assert.Equal(t, "llm_context_canceled", classifyAnalyzeQueryFallbackReason(context.Canceled))
		assert.Equal(t, "llm_error", classifyAnalyzeQueryFallbackReason(errors.New("unknown failure")))
	})
}

func TestAnalyzeQueryFallbackDetail(t *testing.T) {
	t.Run("buildAnalyzeQueryFallbackDetail trims and includes default budget metadata", func(t *testing.T) {
		detail := buildAnalyzeQueryFallbackDetail("  trace-1 ", " llm_result_error ", errors.New("something bad"), " sidecar_timeout ")
		assert.Equal(t, "llm_result_error", detail["stage"])
		assert.Equal(t, "sidecar_timeout", detail["reason"])
		assert.Equal(t, "trace-1", detail["traceId"])
		assert.Equal(t, wisdevAnalyzeQueryHandlerTimeout.Milliseconds(), detail["handlerTimeoutMs"])
		assert.Equal(t, true, detail["vertexDirectDisabled"])
	})

	t.Run("buildAnalyzeQueryFallbackDetail omits missing error", func(t *testing.T) {
		detail := buildAnalyzeQueryFallbackDetail("trace-2", "handler_context_done", nil, "llm_error")
		assert.Equal(t, "handler_context_done", detail["stage"])
		_, hasError := detail["error"]
		assert.False(t, hasError)
	})

	t.Run("enrichAnalyzeQueryFallbackDetailWithSidecar captures runtime health", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/health":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"service":   "python_sidecar",
					"status":    "ok",
					"transport": "http-json+grpc-protobuf",
					"dependencies": []map[string]any{
						{
							"name":      "gemini_runtime",
							"status":    "configured",
							"source":    "native",
							"detail":    "initialized",
							"transport": "vertex-sdk-or-proxy",
						},
						{
							"name":      "grpc_sidecar",
							"status":    "ok",
							"transport": "grpc+protobuf",
						},
					},
				})
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

		client := llm.NewClient()
		detail := enrichAnalyzeQueryFallbackDetailWithSidecar(map[string]any{
			"traceId": "trace-3",
		}, client)

		assert.Equal(t, "http-json", detail["goToSidecarTransport"])
		assert.Equal(t, "python_sidecar", detail["sidecarHealthService"])
		assert.Equal(t, "ok", detail["sidecarHealthStatus"])
		assert.Equal(t, "http-json+grpc-protobuf", detail["sidecarHealthTransport"])
		assert.Equal(t, "configured", detail["sidecarGeminiRuntimeStatus"])
		assert.Equal(t, "native", detail["sidecarGeminiRuntimeSource"])
		assert.Equal(t, "initialized", detail["sidecarGeminiRuntimeDetail"])
		assert.Equal(t, "vertex-sdk-or-proxy", detail["sidecarGeminiRuntimeTransport"])
		assert.Equal(t, "ok", detail["sidecarGrpcStatus"])
		assert.Equal(t, "grpc+protobuf", detail["sidecarGrpcTransport"])
	})

	t.Run("enrichAnalyzeQueryFallbackDetailWithSidecar records health probe errors", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		t.Cleanup(server.Close)
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

		client := llm.NewClient()
		detail := enrichAnalyzeQueryFallbackDetailWithSidecar(map[string]any{}, client)

		_, hasError := detail["sidecarHealthProbeError"]
		assert.True(t, hasError)
		assert.NotEmpty(t, detail["sidecarHealthProbeError"])
		assert.NotContains(t, detail, "sidecarHealthService")
	})

	t.Run("enrichAnalyzeQueryFallbackDetailWithSidecar handles nil detail and nil client", func(t *testing.T) {
		detail := enrichAnalyzeQueryFallbackDetailWithSidecar(nil, nil)
		assert.Equal(t, map[string]any{}, detail)
	})
}

func TestCommitteeAndEvidenceHelpers(t *testing.T) {
	t.Run("extractCommitteeSignals handles malformed committee metadata safely", func(t *testing.T) {
		emptyCitationCount, emptySourceCount, emptyDecision := extractCommitteeSignals(nil)
		assert.Equal(t, 0, emptyCitationCount)
		assert.Equal(t, 0, emptySourceCount)
		assert.Empty(t, emptyDecision)
	})

	t.Run("buildEvidenceGatePayload handles unsupported claims and contradictions", func(t *testing.T) {
		claims := []map[string]any{
			{"source": map[string]any{"id": "p1"}},
			{"source": map[string]any{"id": "  "}},
			{"source": map[string]any{"id": ""}},
		}
		result := buildEvidenceGatePayload(claims, 2)

		assert.False(t, result["passed"].(bool))
		assert.True(t, result["provisional"].(bool))
		assert.Equal(t, 3, result["claimCount"])
		assert.Equal(t, 1, result["linkedClaimCount"])
		assert.Equal(t, 2, result["unlinkedClaimCount"])
		assert.Equal(t, 2, result["contradictionCount"])
		assert.Equal(t, "Evidence gate found unsupported or contradictory claims.", result["message"])
		assert.Equal(t, "[Provisional] Claim-evidence verification did not fully pass. Treat this synthesis as unverified.\n\n", result["warningPrefix"])
		assert.Equal(t, "provisional", result["verdict"])
		assert.False(t, result["strictGatePass"].(bool))
	})
}

func TestWisDev_PriorityQueryHelpers(t *testing.T) {
	t.Run("wisdevAnalyzeQueryGoroutineBudget adapts to cold-start status", func(t *testing.T) {
		base := wisdevAnalyzeQueryBudget() + wisdevAnalyzeQueryLLMGraceTimeout
		expected := base
		if llm.IsColdStartWindow() {
			expected += 10 * time.Second
		}
		assert.Equal(t, expected, wisdevAnalyzeQueryGoroutineBudget())
	})

	t.Run("wisdevAnalyzeQuerySidecarBackstopBudget is dynamic when override differs", func(t *testing.T) {
		defaultBackoff := wisdevAnalyzeQuerySidecarBackstopTimeout
		defer func() { wisdevAnalyzeQuerySidecarBackstopTimeout = defaultBackoff }()

		defaultOverride := wisdevAnalyzeQueryGoroutineTimeout + 5*time.Second
		wisdevAnalyzeQuerySidecarBackstopTimeout = defaultOverride
		assert.Equal(t, wisdevAnalyzeQueryGoroutineBudget()+5*time.Second, wisdevAnalyzeQuerySidecarBackstopBudget())

		customBackoff := wisdevAnalyzeQuerySidecarBackstopTimeout + 7*time.Second
		wisdevAnalyzeQuerySidecarBackstopTimeout = customBackoff
		assert.Equal(t, customBackoff, wisdevAnalyzeQuerySidecarBackstopBudget())
	})

	t.Run("truncateAnalyzeQueryDebugValue trims whitespace and enforces byte length", func(t *testing.T) {
		assert.Equal(t, "", truncateAnalyzeQueryDebugValue("   ", 10))
		assert.Equal(t, "abc", truncateAnalyzeQueryDebugValue("  abc  ", 5))
		assert.Equal(t, "abcdef", truncateAnalyzeQueryDebugValue(" abcdef ", 0))
		assert.Equal(t, "abcdefghij", truncateAnalyzeQueryDebugValue("abcdefghijklmnopqrstuvwxyz", 10))
	})

	t.Run("writeWisdevResearchLoopError maps error classes to API responses", func(t *testing.T) {
		readError := func(rec *httptest.ResponseRecorder) APIError {
			var payload APIError
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
			return payload
		}

		t.Run("timeout", func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeWisdevResearchLoopError(rec, "analyze query timed out", context.DeadlineExceeded)
			resp := readError(rec)
			assert.Equal(t, http.StatusGatewayTimeout, resp.Error.Status)
			assert.Equal(t, ErrDependencyFailed, resp.Error.Code)
			assert.Equal(t, "timeout", resp.Error.Details["errorKind"])
			assert.Equal(t, true, resp.Error.Details["retryable"])
		})

		t.Run("rate limited", func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeWisdevResearchLoopError(rec, "rate limited retry later", errors.New("ERROR 429: quota exhausted"))
			resp := readError(rec)
			assert.Equal(t, http.StatusTooManyRequests, resp.Error.Status)
			assert.Equal(t, ErrRateLimit, resp.Error.Code)
			assert.Equal(t, "rate_limit", resp.Error.Details["errorKind"])
			assert.Equal(t, true, resp.Error.Details["retryable"])
		})

		t.Run("context canceled", func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeWisdevResearchLoopError(rec, "request canceled", context.Canceled)
			resp := readError(rec)
			assert.Equal(t, 499, resp.Error.Status)
			assert.Equal(t, ErrServiceUnavailable, resp.Error.Code)
			assert.Equal(t, "context_canceled", resp.Error.Details["errorKind"])
			assert.Equal(t, false, resp.Error.Details["retryable"])
		})

		t.Run("generic failure", func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeWisdevResearchLoopError(rec, "unexpected failure", errors.New("unhandled boom "+strconv.Itoa(1)))
			resp := readError(rec)
			assert.Equal(t, http.StatusInternalServerError, resp.Error.Status)
			assert.Equal(t, ErrWisdevFailed, resp.Error.Code)
			assert.Equal(t, "runtime_failure", resp.Error.Details["errorKind"])
			assert.Equal(t, false, resp.Error.Details["retryable"])
			assert.Equal(t, "unhandled boom 1", resp.Error.Details["error"])
		})
	})
}
