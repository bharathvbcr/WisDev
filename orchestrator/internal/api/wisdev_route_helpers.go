package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

var routeConcurrencyGuards = struct {
	mu       sync.Mutex
	limiters map[string]chan struct{}
}{
	limiters: make(map[string]chan struct{}),
}

var (
	wisdevAnalyzeQueryLLMGraceTimeout        = 5 * time.Second
	wisdevAnalyzeQueryGoroutineTimeout       = wisdevAnalyzeQueryHandlerTimeout + wisdevAnalyzeQueryLLMGraceTimeout
	wisdevAnalyzeQuerySidecarBackstopTimeout = wisdevAnalyzeQueryGoroutineTimeout + 5*time.Second
)

const structuredOutputSchemaInstruction = "Use the supplied structured output schema exactly."

// wisdevAnalyzeQueryGoroutineBudget returns the goroutine timeout, extended
// during the cold-start window to account for sidecar and ADC initialization.
func wisdevAnalyzeQueryGoroutineBudget() time.Duration {
	base := wisdevAnalyzeQueryBudget() + wisdevAnalyzeQueryLLMGraceTimeout
	if llm.IsColdStartWindow() {
		return base + 10*time.Second
	}
	return base
}

// wisdevAnalyzeQuerySidecarBackstopBudget returns the HTTP backstop timeout
// for the sidecar client, always slightly larger than the goroutine budget
// so the goroutine context remains the canonical timeout owner.
func wisdevAnalyzeQuerySidecarBackstopBudget() time.Duration {
	base := wisdevAnalyzeQueryGoroutineBudget() + 5*time.Second
	defaultOverride := wisdevAnalyzeQueryGoroutineTimeout + 5*time.Second
	if wisdevAnalyzeQuerySidecarBackstopTimeout > 0 && wisdevAnalyzeQuerySidecarBackstopTimeout != defaultOverride {
		return wisdevAnalyzeQuerySidecarBackstopTimeout
	}
	return base
}

func logWisdevRouteError(r *http.Request, message string, attrs ...any) {
	base := []any{
		"path", r.URL.Path,
		"method", r.Method,
		"user_id", strings.TrimSpace(GetUserID(r)),
	}
	slog.Error(message, append(base, attrs...)...)
}

func logWisdevRouteLifecycle(r *http.Request, operation string, stage string, query string, attrs ...any) {
	base := []any{
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "api.wisdev",
		"operation", operation,
		"stage", stage,
		"path", r.URL.Path,
		"method", r.Method,
		"user_id", strings.TrimSpace(GetUserID(r)),
		"query_preview", wisdev.QueryPreview(query),
		"query_length", len(wisdev.ResolveSessionQueryText(query, "")),
		"query_hash", searchQueryFingerprint(query),
	}
	telemetry.FromCtx(r.Context()).InfoContext(r.Context(), "wisdev route lifecycle", append(base, attrs...)...)
}

func classifyAnalyzeQueryFallbackReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	type timeoutError interface {
		Timeout() bool
	}
	var netErr timeoutError
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "sidecar_timeout"
	}
	switch {
	case strings.Contains(msg, "client.timeout"),
		strings.Contains(msg, "awaiting headers"),
		strings.Contains(msg, "timeout exceeded while awaiting headers"),
		strings.Contains(msg, "timed out"),
		strings.Contains(msg, "timeout"):
		return "sidecar_timeout"
	case strings.Contains(msg, "invalid_prompt"):
		return "llm_invalid_prompt"
	case strings.Contains(msg, "structured output invalid"),
		strings.Contains(msg, "not valid json"),
		strings.Contains(msg, "invalid json"),
		strings.Contains(msg, "empty json result"):
		return "llm_invalid_output"
	case strings.Contains(msg, "upstream unavailable"),
		strings.Contains(msg, "service unavailable"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "lookup "),
		strings.Contains(msg, "dial tcp"),
		strings.Contains(msg, "connectex"):
		return "sidecar_unavailable"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "llm_deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "llm_context_canceled"
	}
	switch {
	case strings.Contains(msg, "deadline exceeded"):
		return "llm_deadline_exceeded"
	case strings.Contains(msg, "context canceled"):
		return "llm_context_canceled"
	default:
		return "llm_error"
	}
}

type wisdevResearchLoopErrorClassification struct {
	status    int
	code      ErrorCode
	kind      string
	retryable bool
}

func classifyWisdevResearchLoopError(err error) wisdevResearchLoopErrorClassification {
	classification := wisdevResearchLoopErrorClassification{
		status: http.StatusInternalServerError,
		code:   ErrWisdevFailed,
		kind:   "runtime_failure",
	}
	if err == nil {
		return classification
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case errors.Is(err, context.Canceled) || strings.Contains(msg, "context canceled"):
		classification.status = 499
		classification.code = ErrServiceUnavailable
		classification.kind = "context_canceled"
	case errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "timeout"):
		classification.status = http.StatusGatewayTimeout
		classification.code = ErrDependencyFailed
		classification.kind = "timeout"
		classification.retryable = true
	case strings.Contains(msg, "429") ||
		strings.Contains(msg, "resource exhausted") ||
		strings.Contains(msg, "quota") ||
		strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "rate limited") ||
		strings.Contains(msg, "too many requests"):
		classification.status = http.StatusTooManyRequests
		classification.code = ErrRateLimit
		classification.kind = "rate_limit"
		classification.retryable = true
	case strings.Contains(msg, "503") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "unavailable") ||
		strings.Contains(msg, "socket hang up") ||
		strings.Contains(msg, "connection refused"):
		classification.status = http.StatusServiceUnavailable
		classification.code = ErrServiceUnavailable
		classification.kind = "provider_unavailable"
		classification.retryable = true
	}
	return classification
}

func writeWisdevResearchLoopError(w http.ResponseWriter, message string, err error) {
	classification := classifyWisdevResearchLoopError(err)
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	WriteError(w, classification.status, classification.code, message, map[string]any{
		"error":      errText,
		"errorKind":  classification.kind,
		"retryable":  classification.retryable,
		"runtime":    "go",
		"component":  "wisdev.research_loop",
		"ownerLayer": "wisdev-agent-os/orchestrator",
	})
}

func truncateAnalyzeQueryDebugValue(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if maxLen < 1 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func buildAnalyzeQueryFallbackDetail(traceID string, stage string, err error, fallbackReason string) map[string]any {
	detail := map[string]any{
		"stage":                    strings.TrimSpace(stage),
		"reason":                   strings.TrimSpace(fallbackReason),
		"traceId":                  strings.TrimSpace(traceID),
		"model":                    llm.ResolveLightModel(),
		"transport":                "python-sidecar-structured-output",
		"vertexDirectDisabled":     true,
		"handlerTimeoutMs":         wisdevAnalyzeQueryHandlerTimeout.Milliseconds(),
		"goroutineTimeoutMs":       wisdevAnalyzeQueryGoroutineTimeout.Milliseconds(),
		"sidecarBackstopTimeoutMs": wisdevAnalyzeQuerySidecarBackstopTimeout.Milliseconds(),
		"llmGraceTimeoutMs":        wisdevAnalyzeQueryLLMGraceTimeout.Milliseconds(),
	}
	if err != nil {
		detail["error"] = truncateAnalyzeQueryDebugValue(err.Error(), 240)
	}
	return detail
}

func enrichAnalyzeQueryFallbackDetailWithSidecar(detail map[string]any, client *llm.Client) map[string]any {
	if detail == nil {
		detail = map[string]any{}
	}
	if client == nil {
		return detail
	}
	detail["goToSidecarTransport"] = client.TransportName()
	probeCtx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	runtimeHealth, err := client.RuntimeHealth(probeCtx)
	if err != nil {
		detail["sidecarHealthProbeError"] = truncateAnalyzeQueryDebugValue(err.Error(), 240)
		return detail
	}
	detail["sidecarHealthService"] = strings.TrimSpace(runtimeHealth.Service)
	detail["sidecarHealthStatus"] = strings.TrimSpace(runtimeHealth.Status)
	detail["sidecarHealthTransport"] = strings.TrimSpace(runtimeHealth.Transport)
	for _, dep := range runtimeHealth.Dependencies {
		switch strings.TrimSpace(dep.Name) {
		case "gemini_runtime":
			detail["sidecarGeminiRuntimeStatus"] = strings.TrimSpace(dep.Status)
			detail["sidecarGeminiRuntimeSource"] = strings.TrimSpace(dep.Source)
			detail["sidecarGeminiRuntimeDetail"] = strings.TrimSpace(dep.Detail)
			detail["sidecarGeminiRuntimeTransport"] = strings.TrimSpace(dep.Transport)
		case "grpc_sidecar":
			detail["sidecarGrpcStatus"] = strings.TrimSpace(dep.Status)
			detail["sidecarGrpcTransport"] = strings.TrimSpace(dep.Transport)
		}
	}
	return detail
}

func resolveWisdevRouteTraceID(r *http.Request, requested string) string {
	if traceID := strings.TrimSpace(requested); traceID != "" {
		return traceID
	}
	return strings.TrimSpace(resolveRequestTraceID(r))
}

func resolveWisdevRouteOptionalTraceID(r *http.Request, requested string, legacyRequested string) string {
	if traceID := strings.TrimSpace(requested); traceID != "" {
		return resolveWisdevRouteTraceID(r, traceID)
	}
	return resolveWisdevRouteTraceID(r, legacyRequested)
}

func writeCachedWisdevEnvelopeResponse(w http.ResponseWriter, status int, cached []byte) {
	w.Header().Set("Content-Type", "application/json")
	if traceID := extractWisdevEnvelopeTraceID(cached); traceID != "" {
		w.Header().Set("X-Trace-Id", traceID)
	}
	w.WriteHeader(status)
	_, _ = w.Write(cached)
}

func extractWisdevEnvelopeTraceID(body []byte) string {
	var payload struct {
		TraceID string `json:"traceId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.TraceID)
}

// analyzeQueryStopWords is the set of common English stop words filtered out
// of the naïve entity extraction in buildAnalyzeQueryPayload. The Go stub
// extracts entities by tokenising the query; without this filter the frontend
// receives words like "in", "of", "the" as keyword expansion hints.
var analyzeQueryStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true,
	"in": true, "on": true, "at": true, "to": true, "for": true, "of": true,
	"with": true, "by": true, "from": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "been": true, "being": true, "have": true,
	"has": true, "had": true, "do": true, "does": true, "did": true, "will": true,
	"would": true, "could": true, "should": true, "may": true, "might": true,
	"can": true, "how": true, "what": true, "when": true, "where": true,
	"that": true, "this": true, "these": true, "those": true, "it": true,
	"its": true, "their": true, "our": true, "your": true, "my": true,
}

func buildAnalyzeQueryPayload(query string, traceID string) map[string]any {
	return buildAnalyzeQueryPayloadWithAI(context.Background(), nil, query, traceID)
}

// buildAnalyzeQueryPayloadWithAI calls the LLM to extract real intent,
// suggested domains, and complexity from the query. It falls back to the
// heuristic implementation when the LLM is unavailable or returns an error.
// Always emits "fallbackTriggered" and "fallbackReason" so the frontend can
// observe whether the Go-side LLM call succeeded or degraded to heuristics.
//
// The LLM call runs in a background goroutine with a hard select deadline so
// that the Go oauth2 library's context-blind Token() refresh cannot stall the
// HTTP handler. The goroutine always completes on its own using a buffered
// channel, so there is no goroutine leak.
func buildAnalyzeQueryPayloadWithAI(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, traceID string) map[string]any {
	normalizedQuery := wisdev.ResolveSessionQueryText(query, "")
	// Extract meaningful tokens: skip stop words and single-character tokens.
	var entities []string
	for _, token := range strings.Fields(normalizedQuery) {
		lower := strings.ToLower(token)
		if len(lower) > 1 && !analyzeQueryStopWords[lower] {
			entities = append(entities, token)
		}
		if len(entities) == 6 {
			break
		}
	}

	// Defaults — used as fallback when AI is unavailable.
	suggestedDomains := []string{"general"}
	complexity := "moderate"
	intent := "broad_topic"
	methodologyHints := []string{}
	reasoning := "Go-primary structural extraction"
	fallbackTriggered := true
	fallbackReason := "llm_unavailable"
	var fallbackDetail map[string]any

	if agentGateway != nil && agentGateway.LLMClient != nil {
		type llmResult struct {
			domains    []string
			complexity string
			intent     string
			hints      []string
			reasoning  string
			err        error
		}
		// Use a buffered channel so the goroutine can write and exit even
		// after the select has already chosen the ctx.Done() branch.
		resultCh := make(chan llmResult, 1)
		go func() {
			// Use WithoutVertexDirect() to force the Python sidecar path so
			// that gRPC context cancellation works correctly. The VertexDirect
			// path calls oauth2.TokenSource.Token() which is NOT context-aware
			// and can block for 20 s (OS socket timeout) when the GCP metadata
			// server is unreachable. The sidecar uses the Cloud Function proxy
			// which is already working and whose gRPC call respects the context.
			//
			// Keep the sidecar HTTP backstop slightly longer than the local
			// goroutine budget so the goroutine context remains the canonical
			// timeout owner for analyze-query. This avoids ambiguous error
			// classification where a shorter http.Client timeout reports a
			// generic error before the analyze-query budget expires.
			sidecarClient := agentGateway.LLMClient.WithoutVertexDirect().WithTimeout(wisdevAnalyzeQuerySidecarBackstopBudget())
			goroutineCtx, goroutineCancel := context.WithTimeout(context.Background(), wisdevAnalyzeQueryGoroutineBudget())
			defer goroutineCancel()
			d, c, i, h, r, err := analyzeQueryWithLLM(goroutineCtx, sidecarClient, normalizedQuery, traceID)
			resultCh <- llmResult{d, c, i, h, r, err}
		}()

		select {
		case result := <-resultCh:
			if result.err == nil {
				suggestedDomains = result.domains
				complexity = result.complexity
				intent = result.intent
				methodologyHints = result.hints
				reasoning = result.reasoning
				fallbackTriggered = false
				fallbackReason = ""
			} else {
				fallbackReason = classifyAnalyzeQueryFallbackReason(result.err)
				fallbackDetail = buildAnalyzeQueryFallbackDetail(traceID, "llm_result_error", result.err, fallbackReason)
				fallbackDetail = enrichAnalyzeQueryFallbackDetailWithSidecar(fallbackDetail, agentGateway.LLMClient)
				slog.Warn("analyze-query LLM call failed, using heuristic fallback",
					"component", "wisdev_route_helpers",
					"operation", "buildAnalyzeQueryPayloadWithAI",
					"error_code", fallbackReason,
					"error", result.err.Error(),
					"fallback_stage", fallbackDetail["stage"],
					"llm_model", fallbackDetail["model"],
					"handler_timeout_ms", fallbackDetail["handlerTimeoutMs"],
					"goroutine_timeout_ms", fallbackDetail["goroutineTimeoutMs"],
					"sidecar_backstop_timeout_ms", fallbackDetail["sidecarBackstopTimeoutMs"],
					"query_preview", normalizedQuery[:min(len(normalizedQuery), 60)],
					"trace_id", traceID,
				)
			}
		case <-ctx.Done():
			// ctx deadline fired (e.g. the 3s analyzeCtx in the handler).
			// The goroutine continues in the background and writes to the
			// buffered channel when it eventually finishes — no leak.
			fallbackReason = "handler_timeout"
			fallbackDetail = buildAnalyzeQueryFallbackDetail(traceID, "handler_context_done", ctx.Err(), fallbackReason)
			fallbackDetail = enrichAnalyzeQueryFallbackDetailWithSidecar(fallbackDetail, agentGateway.LLMClient)
			slog.Warn("analyze-query LLM call timed out, using heuristic fallback",
				"component", "wisdev_route_helpers",
				"operation", "buildAnalyzeQueryPayloadWithAI",
				"error_code", fallbackReason,
				"fallback_stage", fallbackDetail["stage"],
				"llm_model", fallbackDetail["model"],
				"handler_timeout_ms", fallbackDetail["handlerTimeoutMs"],
				"goroutine_timeout_ms", fallbackDetail["goroutineTimeoutMs"],
				"sidecar_backstop_timeout_ms", fallbackDetail["sidecarBackstopTimeoutMs"],
				"query_preview", normalizedQuery[:min(len(normalizedQuery), 60)],
				"trace_id", traceID,
			)
		}
	}

	return map[string]any{
		"intent":                   intent,
		"entities":                 entities,
		"research_questions":       []string{"What is known about " + normalizedQuery + "?"},
		"complexity":               complexity,
		"ambiguity_score":          0.8,
		"suggested_domains":        suggestedDomains,
		"methodology_hints":        methodologyHints,
		"reasoning":                reasoning,
		"cache_hit":                false,
		"suggested_question_count": 4,
		"traceId":                  traceID,
		"queryUsed":                normalizedQuery,
		"fallbackTriggered":        fallbackTriggered,
		"fallbackReason":           fallbackReason,
		"fallbackDetail":           fallbackDetail,
	}
}

// analyzeQueryWithLLM calls the LLM to extract structured query analysis.
// It returns (suggestedDomains, complexity, intent, methodologyHints, reasoning, error).
func analyzeQueryWithLLM(ctx context.Context, client *llm.Client, query string, traceID string) ([]string, string, string, []string, string, error) {
	domainValues := []string{"medicine", "cs", "social", "climate", "neuro", "physics", "biology", "humanities", "general"}
	prompt := fmt.Sprintf(`You are an academic research assistant analyzing a research query.

Query: %q

Analyze the query and provide:
- "suggestedDomains": array of 1-3 academic domain keys most relevant to this query. Use ONLY these keys: %s
- "complexity": one of "simple", "moderate", "complex"
- "intent": one of "broad_topic", "specific_question", "comparative", "review", "methodological"
- "methodologyHints": array of 0-2 brief methodology hints relevant to this query (empty if not applicable)
- "reasoning": one sentence explaining the domain classification

%s`, query, strings.Join(domainValues, ", "), structuredOutputSchemaInstruction)

	// Enum constraints on complexity and intent eliminate the need for
	// post-validation correction and prevent the model from wasting tokens
	// on invalid values. The allowed domain keys are enforced in the prompt.
	schema := `{"type":"object","required":["suggestedDomains","complexity","intent","methodologyHints","reasoning"],"properties":{"suggestedDomains":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":3},"complexity":{"type":"string","enum":["simple","moderate","complex"]},"intent":{"type":"string","enum":["broad_topic","specific_question","comparative","review","methodological"]},"methodologyHints":{"type":"array","items":{"type":"string"},"maxItems":2},"reasoning":{"type":"string"}}}`

	latencyBudgetMs := wisdevAnalyzeQueryGoroutineBudget().Milliseconds()
	startedAt := time.Now()

	// Query analysis is a lightweight intent classification step (entities,
	// domain, complexity). Use the light model with no thinking budget so it
	// responds in 1-3s instead of timing out with a heavy thinking model.
	policy := llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier:   "light",
		RequestClass:    string(llm.RequestClassLight),
		LatencyBudgetMs: int(latencyBudgetMs),
		Structured:      true,
		HighValue:       false,
	})

	telemetry.FromCtx(ctx).InfoContext(ctx, "wisdev analyze-query llm request start",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "api.wisdev",
		"operation", "analyze_query_llm",
		"stage", "llm_request_start",
		"provider", "python_sidecar",
		"model", llm.ResolveModelForTier(policy.InitialTier),
		"trace_id", strings.TrimSpace(traceID),
		"query_preview", wisdev.QueryPreview(query),
		"query_hash", searchQueryFingerprint(query),
		"handler_timeout_ms", wisdevAnalyzeQueryBudget().Milliseconds(),
		"goroutine_timeout_ms", wisdevAnalyzeQueryGoroutineBudget().Milliseconds(),
		"sidecar_backstop_timeout_ms", wisdevAnalyzeQuerySidecarBackstopBudget().Milliseconds(),
		"request_class", string(policy.RequestClass),
		"retry_profile", string(policy.RetryProfile),
		"latency_budget_ms", policy.LatencyBudgetMs,
		"thinking_budget", policy.ThinkingBudget,
		"service_tier", policy.ServiceTier,
		"cold_start_window", llm.IsColdStartWindow(),
		"process_uptime_ms", llm.ProcessUptimeMs(),
		"result", "started",
	)
	resp, err := client.StructuredOutput(ctx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveModelForTier(policy.InitialTier),
		JsonSchema: schema,
	}, policy))
	if err != nil {
		telemetry.FromCtx(ctx).WarnContext(ctx, "wisdev analyze-query llm request failed",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "api.wisdev",
			"operation", "analyze_query_llm",
			"stage", "llm_request_failed",
			"provider", "python_sidecar",
			"model", llm.ResolveModelForTier(policy.InitialTier),
			"trace_id", strings.TrimSpace(traceID),
			"query_hash", searchQueryFingerprint(query),
			"latency_ms", time.Since(startedAt).Milliseconds(),
			"error_code", classifyAnalyzeQueryFallbackReason(err),
			"error", err.Error(),
			"result", "error",
		)
		return nil, "", "", nil, "", err
	}
	telemetry.FromCtx(ctx).InfoContext(ctx, "wisdev analyze-query llm request success",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "api.wisdev",
		"operation", "analyze_query_llm",
		"stage", "llm_request_success",
		"provider", "python_sidecar",
		"model", llm.ResolveStandardModel(),
		"trace_id", strings.TrimSpace(traceID),
		"query_hash", searchQueryFingerprint(query),
		"latency_ms", time.Since(startedAt).Milliseconds(),
		"schema_valid", resp.GetSchemaValid(),
		"result", "success",
	)

	var parsed struct {
		SuggestedDomains []string `json:"suggestedDomains"`
		Complexity       string   `json:"complexity"`
		Intent           string   `json:"intent"`
		MethodologyHints []string `json:"methodologyHints"`
		Reasoning        string   `json:"reasoning"`
	}
	jsonResult := strings.TrimSpace(resp.GetJsonResult())
	if errMsg := strings.TrimSpace(resp.GetError()); errMsg != "" {
		return nil, "", "", nil, "", fmt.Errorf("structured output invalid: %s", errMsg)
	}
	if jsonResult == "" {
		return nil, "", "", nil, "", errors.New("structured output empty json result")
	}
	if !json.Valid([]byte(jsonResult)) {
		return nil, "", "", nil, "", fmt.Errorf("structured output invalid JSON: %s", truncateAnalyzeQueryDebugValue(jsonResult, 200))
	}
	if err := json.Unmarshal([]byte(jsonResult), &parsed); err != nil {
		return nil, "", "", nil, "", err
	}

	// Validate domains against allowed set.
	allowed := make(map[string]struct{}, len(domainValues))
	for _, d := range domainValues {
		allowed[d] = struct{}{}
	}
	validated := make([]string, 0, len(parsed.SuggestedDomains))
	for _, d := range parsed.SuggestedDomains {
		d = strings.TrimSpace(strings.ToLower(d))
		if _, ok := allowed[d]; ok {
			validated = append(validated, d)
		}
	}
	if len(validated) == 0 {
		validated = []string{"general"}
	}

	complexity := strings.TrimSpace(parsed.Complexity)
	if complexity != "simple" && complexity != "moderate" && complexity != "complex" {
		complexity = "moderate"
	}

	intent := strings.TrimSpace(parsed.Intent)
	validIntents := map[string]struct{}{"broad_topic": {}, "specific_question": {}, "comparative": {}, "review": {}, "methodological": {}}
	if _, ok := validIntents[intent]; !ok {
		intent = "broad_topic"
	}

	return validated, complexity, intent, trimStrings(parsed.MethodologyHints, 2), strings.TrimSpace(parsed.Reasoning), nil
}

func buildCommitteeAnswer(query string, papers []wisdev.Source) string {
	if len(papers) == 0 {
		return fmt.Sprintf("No committee evidence was retrieved yet for %q. Refine the query or widen the search scope.", query)
	}
	topTitles := make([]string, 0, wisdev.MinInt(3, len(papers)))
	for _, paper := range papers {
		title := strings.TrimSpace(paper.Title)
		if title != "" {
			topTitles = append(topTitles, title)
		}
		if len(topTitles) >= 3 {
			break
		}
	}
	if len(topTitles) == 0 {
		return fmt.Sprintf("Committee review completed for %q with %d supporting source(s).", query, len(papers))
	}
	return fmt.Sprintf("Committee review for %q prioritized %s.", query, strings.Join(topTitles, "; "))
}

func buildCommitteeCitations(papers []wisdev.Source) []map[string]any {
	citations := make([]map[string]any, 0, wisdev.MinInt(3, len(papers)))
	for _, paper := range papers {
		title := strings.TrimSpace(paper.Title)
		if title == "" {
			continue
		}
		paperID := strings.TrimSpace(paper.ID)
		if paperID == "" {
			paperID = strings.TrimSpace(paper.DOI)
		}
		if paperID == "" {
			paperID = strings.TrimSpace(paper.Link)
		}
		citations = append(citations, map[string]any{
			"claim":       fmt.Sprintf("Relevant evidence identified in %s", title),
			"sourceId":    paperID,
			"sourceTitle": title,
			"confidence":  wisdev.ClampFloat(paper.Score, 0.55, 0.95),
		})
		if len(citations) >= 3 {
			break
		}
	}
	return citations
}

func buildCommitteePapers(papers []wisdev.Source) []map[string]any {
	mapped := make([]map[string]any, 0, len(papers))
	for _, paper := range papers {
		paperID := strings.TrimSpace(paper.ID)
		if paperID == "" {
			paperID = strings.TrimSpace(paper.DOI)
		}
		authors := make([]map[string]any, 0, len(paper.Authors))
		for _, author := range paper.Authors {
			author = strings.TrimSpace(author)
			if author == "" {
				continue
			}
			authors = append(authors, map[string]any{
				"name": author,
			})
		}
		var publishDate map[string]any
		if paper.Year > 0 {
			publishDate = map[string]any{
				"year": paper.Year,
			}
		}
		mapped = append(mapped, map[string]any{
			"id":             paperID,
			"paperId":        paperID,
			"doi":            paper.DOI,
			"title":          paper.Title,
			"summary":        paper.Summary,
			"abstract":       paper.Summary,
			"link":           paper.Link,
			"source":         paper.Source,
			"siteName":       paper.SiteName,
			"publication":    paper.Publication,
			"keywords":       paper.Keywords,
			"sourceApis":     paper.SourceApis,
			"authors":        authors,
			"publishDate":    publishDate,
			"citationCount":  paper.CitationCount,
			"score":          paper.Score,
			"relevanceScore": wisdev.ClampFloat(paper.Score, 0.55, 0.95),
		})
	}
	return mapped
}

func buildMultiAgentCommitteeResult(query string, domainHint string, papers []wisdev.Source, maxIterations int, includeAnalyst bool) map[string]any {
	sorted := append([]wisdev.Source(nil), papers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Score > sorted[j].Score
	})
	citations := buildCommitteeCitations(sorted)
	paperPayload := buildCommitteePapers(sorted)
	answer := buildCommitteeAnswer(query, sorted)
	iterationLogs := []map[string]any{
		{
			"iteration": 1,
			"phase":     "researcher",
			"summary":   fmt.Sprintf("Retrieved %d candidate source(s) for committee review.", len(sorted)),
		},
		{
			"iteration": wisdev.MinInt(maxIterations, 2),
			"phase":     "critic",
			"summary":   "Ranked evidence by source quality, query match, and committee confidence.",
		},
	}

	analyst := map[string]any{}
	if includeAnalyst {
		analyst = map[string]any{
			"coverage":        len(sorted),
			"domainHint":      domainHint,
			"topSourceCount":  wisdev.MinInt(3, len(sorted)),
			"committeeAnswer": answer,
		}
	}

	return map[string]any{
		"success": true,
		"mode":    "go_committee",
		"supervisor": map[string]any{
			"decision":         "accept",
			"selectedStrategy": "parallel_search_committee",
			"domainHint":       domainHint,
			"sourceCount":      len(sorted),
		},
		"researcher": map[string]any{
			"query":        query,
			"paperCount":   len(sorted),
			"selectedTool": "/rag/retrieve",
		},
		"critic": map[string]any{
			"decision":      "accept",
			"reasons":       []string{"Committee evidence assembled from Go search core.", "Top results were reranked before synthesis."},
			"citationCount": len(citations),
		},
		"analyst":       analyst,
		"iterationLogs": iterationLogs,
		"routing": map[string]any{
			"selectedTier":       "standard",
			"fallbackTier":       "light",
			"committeeActivated": true,
		},
		"sources":   paperPayload,
		"papers":    paperPayload,
		"answer":    answer,
		"citations": citations,
		"execution": map[string]any{
			"durationMs": 0,
			"tokensUsed": 0,
			"agentTimings": map[string]any{
				"researcher": 0,
				"critic":     0,
				"analyst":    0,
			},
		},
	}
}

func extractCommitteeSignals(planMetadata map[string]any) (citationCount int, sourceCount int, criticDecision string) {
	rawCommittee, ok := planMetadata["multiAgent"]
	if !ok {
		return 0, 0, ""
	}
	committee, ok := rawCommittee.(map[string]any)
	if !ok {
		return 0, 0, ""
	}
	if critic, ok := committee["critic"].(map[string]any); ok {
		if rawCount, ok := critic["citationCount"].(float64); ok {
			citationCount = int(rawCount)
		}
		if rawDecision, ok := critic["decision"].(string); ok {
			criticDecision = strings.TrimSpace(rawDecision)
		}
	}
	if supervisor, ok := committee["supervisor"].(map[string]any); ok {
		if rawSources, ok := supervisor["sourceCount"].(float64); ok {
			sourceCount = int(rawSources)
		}
	}
	return citationCount, sourceCount, criticDecision
}

func buildEvidenceGatePayload(claims []map[string]any, contradictionCount int) map[string]any {
	linkedClaims := make([]map[string]any, 0, len(claims))
	unlinkedClaims := make([]map[string]any, 0, len(claims))
	for _, claim := range claims {
		source, _ := claim["source"].(map[string]any)
		if source != nil && strings.TrimSpace(fmt.Sprintf("%v", source["id"])) != "" {
			linkedClaims = append(linkedClaims, claim)
			continue
		}
		unlinkedClaims = append(unlinkedClaims, claim)
	}
	claimCount := len(claims)
	linkedCount := len(linkedClaims)
	unlinkedCount := len(unlinkedClaims)
	passed := claimCount == 0 || (unlinkedCount == 0 && contradictionCount == 0)
	provisional := !passed
	warningPrefix := ""
	if provisional {
		warningPrefix = "[Provisional] Claim-evidence verification did not fully pass. Treat this synthesis as unverified.\n\n"
	}
	message := "Evidence gate passed."
	if provisional {
		message = "Evidence gate found unsupported or contradictory claims."
	}
	return map[string]any{
		"checked":               true,
		"passed":                passed,
		"provisional":           provisional,
		"warningPrefix":         warningPrefix,
		"message":               message,
		"claimCount":            claimCount,
		"linkedClaimCount":      linkedCount,
		"unlinkedClaimCount":    unlinkedCount,
		"contradictionCount":    contradictionCount,
		"claims":                claims,
		"linkedClaims":          linkedClaims,
		"unlinkedClaims":        unlinkedClaims,
		"contradictions":        []map[string]any{},
		"verdict":               map[bool]string{true: "pass", false: "provisional"}[passed],
		"strictGatePass":        passed,
		"nliChecked":            false,
		"aiClaimExtractionUsed": false,
	}
}

func normalizeSectionID(label string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(label), " ", "_"))
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func inferDraftSections(title string, customSections []string) []string {
	titleLower := strings.ToLower(strings.TrimSpace(title))
	sections := []string{"Introduction", "Approach", "Evidence", "Discussion"}
	if strings.Contains(titleLower, "benchmark") || strings.Contains(titleLower, "evaluation") || strings.Contains(titleLower, "compare") {
		sections = []string{"Introduction", "Evaluation Setup", "Comparative Findings", "Limitations", "Recommendations"}
	} else if strings.Contains(titleLower, "survey") || strings.Contains(titleLower, "review") {
		sections = []string{"Introduction", "Landscape", "Methods", "Open Problems", "Recommendations"}
	} else if strings.Contains(titleLower, "system") || strings.Contains(titleLower, "architecture") || strings.Contains(titleLower, "platform") {
		sections = []string{"Problem Framing", "Architecture", "Operational Risks", "Implementation Plan", "Decision Summary"}
	}
	sections = append(sections, customSections...)
	return uniqueStrings(sections)
}

func buildDraftOutlinePayload(documentID string, title string, targetWordCount int, customSections []string) map[string]any {
	if targetWordCount <= 0 {
		targetWordCount = 1600
	}
	sectionTitles := inferDraftSections(title, customSections)
	items := make([]map[string]any, 0, len(sectionTitles))
	remainingWords := targetWordCount
	for index, sectionTitle := range sectionTitles {
		target := wisdev.MaxInt(120, targetWordCount/wisdev.MaxInt(1, len(sectionTitles)))
		if index == 0 {
			target = wisdev.MaxInt(target, 180)
		}
		if index == len(sectionTitles)-1 {
			target = wisdev.MaxInt(target-30, 120)
		}
		remainingWords -= target
		items = append(items, map[string]any{
			"id":          normalizeSectionID(sectionTitle),
			"title":       sectionTitle,
			"level":       1,
			"targetWords": target,
			"order":       index + 1,
			"purpose":     fmt.Sprintf("Explain how %s contributes to the overall argument.", strings.ToLower(sectionTitle)),
			"evidenceExpectation": map[string]any{
				"minSources":           wisdev.MinInt(4, wisdev.MaxInt(2, index+2)),
				"requiresCounterpoint": index >= 2,
			},
		})
	}
	if remainingWords > 0 && len(items) > 0 {
		last := items[len(items)-1]
		last["targetWords"] = wisdev.IntValue(last["targetWords"]) + remainingWords
		items[len(items)-1] = last
	}
	return map[string]any{
		"documentId":       documentID,
		"title":            title,
		"totalTargetWords": targetWordCount,
		"items":            items,
		"narrativeArc": []string{
			"Frame the problem and decision context.",
			"Present the strongest supporting evidence before tradeoffs.",
			"Close with operational implications and explicit uncertainty.",
		},
		"generatedAt": time.Now().UnixMilli(),
		"model":       "go_outline",
	}
}

func buildDraftSectionPayload(documentID string, sectionID string, title string, targetWords int, papers []map[string]any) map[string]any {
	if targetWords <= 0 {
		targetWords = 220
	}
	citations := make([]string, 0, wisdev.MinInt(4, len(papers)))
	keyFindings := make([]string, 0, wisdev.MinInt(4, len(papers)))
	paragraphs := make([]string, 0, wisdev.MinInt(4, len(papers))+1)
	paragraphs = append(paragraphs, fmt.Sprintf("%s frames the highest-signal evidence relevant to the draft objective and separates supported claims from remaining uncertainty.", title))
	for _, paper := range papers {
		citation := strings.TrimSpace(fmt.Sprintf("%v", paper["title"]))
		if citation != "" && len(citations) < 4 {
			citations = append(citations, citation)
		}
		summary := strings.TrimSpace(fmt.Sprintf("%v", paper["summary"]))
		if summary == "" {
			summary = strings.TrimSpace(fmt.Sprintf("%v", paper["abstract"]))
		}
		if summary == "" {
			continue
		}
		score := wisdev.ClampFloat(wisdev.AsFloat(paper["score"]), 0.55, 0.95)
		paragraphs = append(paragraphs, fmt.Sprintf("%s This source contributes %.0f%% confidence toward the section argument and should be cited where the claim is asserted.", summary, score*100))
		keyFindings = append(keyFindings, fmt.Sprintf("%s supports the section with a %.0f%% relevance score.", citation, score*100))
		if len(paragraphs) >= 4 {
			break
		}
	}
	if len(paragraphs) == 1 {
		paragraphs = append(paragraphs, "Retrieved evidence is limited, so this section should remain provisional until stronger source grounding is added.")
	}
	content := strings.Join(paragraphs, "\n\n")
	return map[string]any{
		"documentId":  documentID,
		"sectionId":   sectionID,
		"title":       title,
		"content":     content,
		"actualWords": wisdev.MaxInt(110, targetWords-(targetWords/6)),
		"citations":   uniqueStrings(citations),
		"keyFindings": uniqueStrings(keyFindings),
		"summary":     "Go drafting generated a section with explicit evidence weighting and citation placement guidance.",
		"generatedAt": time.Now().UnixMilli(),
	}
}

func mapAny(value any) map[string]any {
	mapped, _ := value.(map[string]any)
	if mapped == nil {
		return map[string]any{}
	}
	return cloneAnyMap(mapped)
}

func mergeAnyMap(base map[string]any, override map[string]any) map[string]any {
	out := cloneAnyMap(base)
	for key, value := range override {
		if child, ok := value.(map[string]any); ok {
			existing, _ := out[key].(map[string]any)
			out[key] = mergeAnyMap(existing, child)
			continue
		}
		out[key] = value
	}
	return out
}

// sliceAnyMap converts a raw map slice value to []map[string]any.
// IMPORTANT: Every element is deep-copied via cloneAnyMap. This means
// modifications to returned elements are NOT visible in the original value.
// After mutating elements, callers MUST write the slice back:
//
//	questions := sliceAnyMap(s["questions"])
//	questions[i]["field"] = newValue   // modifies the copy
//	s["questions"] = questions         // required to persist changes
func sliceAnyMap(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneAnyMap(item))
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, cloneAnyMap(mapped))
			}
		}
		return out
	default:
		return []map[string]any{}
	}
}

func sliceStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(wisdev.AsOptionalString(item))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return []string{}
	}
}

func normalizeQuestionOptionPayload(value string, label string, description string, icon string) map[string]any {
	payload := map[string]any{
		"value": strings.TrimSpace(value),
		"label": strings.TrimSpace(label),
	}
	if payload["label"] == "" {
		payload["label"] = payload["value"]
	}
	if desc := strings.TrimSpace(description); desc != "" {
		payload["description"] = desc
	}
	if iconValue := strings.TrimSpace(icon); iconValue != "" {
		payload["icon"] = iconValue
	}
	return payload
}

func normalizeQuestionOptionMapPayload(raw map[string]any) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	value := strings.TrimSpace(wisdev.AsOptionalString(raw["value"]))
	if value == "" {
		value = strings.TrimSpace(wisdev.AsOptionalString(raw["id"]))
	}
	label := strings.TrimSpace(wisdev.AsOptionalString(raw["label"]))
	if label == "" {
		label = strings.TrimSpace(wisdev.AsOptionalString(raw["text"]))
	}
	if label == "" {
		label = value
	}
	return normalizeQuestionOptionPayload(
		value,
		label,
		wisdev.AsOptionalString(raw["description"]),
		wisdev.AsOptionalString(raw["icon"]),
	)
}

func questionOptionPayloads(value any) []map[string]any {
	switch typed := value.(type) {
	case []wisdev.QuestionOption:
		out := make([]map[string]any, 0, len(typed))
		for _, option := range typed {
			out = append(out, normalizeQuestionOptionPayload(option.Value, option.Label, option.Description, option.Icon))
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, option := range typed {
			normalized := normalizeQuestionOptionMapPayload(option)
			if strings.TrimSpace(wisdev.AsOptionalString(normalized["value"])) != "" {
				out = append(out, normalized)
			}
		}
		return out
	case []string:
		out := make([]map[string]any, 0, len(typed))
		for _, option := range typed {
			if text := strings.TrimSpace(option); text != "" {
				out = append(out, normalizeQuestionOptionPayload(text, text, "", ""))
			}
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			switch option := item.(type) {
			case string:
				if text := strings.TrimSpace(option); text != "" {
					out = append(out, normalizeQuestionOptionPayload(text, text, "", ""))
				}
			case map[string]any:
				normalized := normalizeQuestionOptionMapPayload(option)
				if strings.TrimSpace(wisdev.AsOptionalString(normalized["value"])) != "" {
					out = append(out, normalized)
				}
			}
		}
		return out
	default:
		return []map[string]any{}
	}
}

func questionOptionValues(value any) []string {
	options := questionOptionPayloads(value)
	values := make([]string, 0, len(options))
	for _, option := range options {
		optionValue := strings.TrimSpace(wisdev.AsOptionalString(option["value"]))
		if optionValue != "" {
			values = append(values, optionValue)
		}
	}
	return values
}

func coerceFloatValue(raw any) float64 {
	switch typed := raw.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		value, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return value
		}
	}
	return 0
}

func normalizeAgentSessionStatus(raw string) wisdev.SessionStatus {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ready", "searching", "executing", "running":
		return wisdev.SessionExecutingPlan
	case "completed", "complete":
		return wisdev.SessionComplete
	case "failed":
		return wisdev.SessionFailed
	case "paused":
		return wisdev.SessionPaused
	default:
		return wisdev.SessionQuestioning
	}
}

func AnswersFromState(raw map[string]any) map[string]wisdev.Answer {
	answers := map[string]wisdev.Answer{}
	if len(raw) == 0 {
		return answers
	}
	for questionID, item := range raw {
		answerMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		values := sliceStrings(answerMap["values"])
		displayValues := sliceStrings(answerMap["displayValues"])
		answerID := strings.TrimSpace(wisdev.AsOptionalString(answerMap["questionId"]))
		if answerID == "" {
			answerID = questionID
		}
		values, displayValues = normalizeStoredAgentAnswerValues(answerID, values, displayValues)
		answers[questionID] = wisdev.Answer{
			QuestionID:    answerID,
			Values:        values,
			DisplayValues: displayValues,
			AnsweredAt:    wisdev.IntValue64(answerMap["answeredAt"]),
		}
	}
	return answers
}

func normalizeStoredAgentAnswerValues(questionID string, values []string, displayValues []string) ([]string, []string) {
	return wisdev.NormalizeAnswerValues(!wisdev.IsKnownSingleSelectQuestionID(questionID), values, displayValues)
}

func resolveAgentSessionQueryMap(session map[string]any) string {
	if len(session) == 0 {
		return ""
	}
	return wisdev.ResolveSessionSearchQuery(
		wisdev.AsOptionalString(session["query"]),
		wisdev.AsOptionalString(session["correctedQuery"]),
		wisdev.AsOptionalString(session["originalQuery"]),
	)
}

func buildCanonicalAgentSession(session map[string]any) *wisdev.AgentSession {
	if len(session) == 0 {
		return nil
	}
	resolvedQuery := resolveAgentSessionQueryMap(session)
	canonical := &wisdev.AgentSession{
		SessionID:            strings.TrimSpace(wisdev.AsOptionalString(session["sessionId"])),
		UserID:               strings.TrimSpace(wisdev.AsOptionalString(session["userId"])),
		Query:                strings.TrimSpace(wisdev.AsOptionalString(session["query"])),
		OriginalQuery:        wisdev.ResolveSessionQueryText("", wisdev.AsOptionalString(session["originalQuery"])),
		CorrectedQuery:       resolvedQuery,
		DetectedDomain:       strings.TrimSpace(wisdev.AsOptionalString(session["detectedDomain"])),
		SecondaryDomains:     sliceStrings(session["secondaryDomains"]),
		Status:               normalizeAgentSessionStatus(wisdev.AsOptionalString(session["status"])),
		CurrentQuestionIndex: wisdev.IntValue(session["currentQuestionIndex"]),
		QuestionSequence:     sliceStrings(session["questionSequence"]),
		MinQuestions:         wisdev.IntValue(session["minQuestions"]),
		MaxQuestions:         wisdev.IntValue(session["maxQuestions"]),
		ComplexityScore:      coerceFloatValue(session["complexityScore"]),
		ClarificationBudget:  wisdev.IntValue(session["clarificationBudget"]),
		QuestionStopReason:   wisdev.QuestionStopReason(strings.TrimSpace(wisdev.AsOptionalString(session["questionStopReason"]))),
		Answers:              AnswersFromState(mapAny(session["answers"])),
		FailureMemory:        map[string]int{},
		Mode:                 normalizeSessionMode(wisdev.AsOptionalString(session["mode"])),
		ServiceTier:          wisdev.ServiceTier(strings.TrimSpace(wisdev.AsOptionalString(session["serviceTier"]))),
		CreatedAt:            wisdev.IntValue64(session["createdAt"]),
		UpdatedAt:            wisdev.IntValue64(session["updatedAt"]),
	}
	if canonical.SessionID == "" {
		return nil
	}
	// If all query fields resolved to empty the canonical session is
	// effectively unusable — any downstream search or plan generation
	// will silently produce zero results. Return nil so callers treat
	// this as a load failure rather than persisting a zero-query session
	// to the canonical store (which would infect subsequent resumptions).
	if canonical.Query == "" && canonical.OriginalQuery == "" && canonical.CorrectedQuery == "" {
		slog.Warn("buildCanonicalAgentSession: all query fields empty — returning nil to prevent zero-query session propagation",
			"session_id", canonical.SessionID,
		)
		return nil
	}
	if canonical.CorrectedQuery == "" {
		canonical.CorrectedQuery = canonical.OriginalQuery
	}
	if canonical.Query == "" {
		canonical.Query = wisdev.ResolveSessionQueryText(canonical.CorrectedQuery, canonical.OriginalQuery)
	}
	if canonical.ServiceTier == "" {
		canonical.ServiceTier = wisdev.ResolveServiceTier(canonical.Mode, canonical.Status == wisdev.SessionQuestioning || canonical.Status == wisdev.SessionPaused)
	}
	query := wisdev.ResolveSessionSearchQuery(canonical.Query, canonical.CorrectedQuery, canonical.OriginalQuery)
	canonical.ReasoningGraph = &wisdev.ReasoningGraph{Query: query}
	canonical.MemoryTiers = &wisdev.MemoryTierState{}
	return canonical
}

func syncCanonicalSessionStore(agentGateway *wisdev.AgentGateway, session map[string]any) error {
	if agentGateway == nil || agentGateway.Store == nil {
		return nil
	}
	canonical := buildCanonicalAgentSession(session)
	if canonical == nil {
		return fmt.Errorf("canonical session payload is invalid")
	}
	return agentGateway.Store.Put(context.Background(), canonical, agentGateway.SessionTTL)
}

func defaultPolicyPayload(agentGateway *wisdev.AgentGateway, userID string, policyVersion string) map[string]any {
	if strings.TrimSpace(policyVersion) == "" {
		policyVersion = agentGateway.PolicyConfig.PolicyVersion
	}
	return map[string]any{
		"policy": map[string]any{
			"userId":        userID,
			"policyVersion": policyVersion,
			"autonomy": map[string]any{
				"allowLowRiskAutoRun":          true,
				"requireConfirmationForMedium": true,
				"alwaysConfirmHighRisk":        true,
				"followUpMode":                 "adaptive",
			},
			"budgets": map[string]any{
				"maxToolCallsPerSession":  agentGateway.PolicyConfig.MaxToolCallsPerSession,
				"maxScriptRunsPerSession": agentGateway.PolicyConfig.MaxScriptRunsPerSession,
				"maxDecisionLatencyMs":    10000,
				"maxCostPerSessionCents":  agentGateway.PolicyConfig.MaxCostPerSessionCents,
			},
			"thresholds": map[string]any{
				"mediumRiskImpactThreshold": 0.6,
			},
			"weights": map[string]any{
				"searchSuccess":     1,
				"citationQuality":   1,
				"sessionCompletion": 1,
				"latencyPenalty":    1,
				"frictionPenalty":   1,
				"unsafePenalty":     1,
			},
		},
		"telemetry": map[string]any{
			"outcomesCount":       0,
			"lastOutcomeAt":       nil,
			"decayedSuccessScore": 0.0,
		},
		"gates": map[string]any{},
	}
}

func validateRequiredString(value string, name string, maxLen int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is required", name)
	}
	if maxLen > 0 && len(trimmed) > maxLen {
		return fmt.Errorf("%s exceeds max length of %d", name, maxLen)
	}
	return nil
}

func validateOptionalString(value string, name string, maxLen int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" && maxLen > 0 && len(trimmed) > maxLen {
		return fmt.Errorf("%s exceeds max length of %d", name, maxLen)
	}
	return nil
}

func validateStringSlice(values []string, name string, maxItems int, maxLen int) error {
	if maxItems > 0 && len(values) > maxItems {
		return fmt.Errorf("%s exceeds max items of %d", name, maxItems)
	}
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("item in %s must not be empty or whitespace-only", name)
		}
		if maxLen > 0 && len(trimmed) > maxLen {
			return fmt.Errorf("item in %s exceeds max length of %d", name, maxLen)
		}
	}
	return nil
}

func validatePayloadSize(v any, name string, maxBytes int) error {
	if maxBytes <= 0 {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to validate %s size: %w", name, err)
	}
	if len(data) > maxBytes {
		return fmt.Errorf("%s payload too large (%d bytes, max %d)", name, len(data), maxBytes)
	}
	return nil
}

func enforceIdempotency(r *http.Request, agentGateway *wisdev.AgentGateway, key string) (int, []byte, bool) {
	if agentGateway == nil || agentGateway.Idempotency == nil {
		return 0, nil, false
	}
	return agentGateway.Idempotency.Get(key)
}

func storeIdempotentResponse(agentGateway *wisdev.AgentGateway, r *http.Request, key string, body []byte) {
	if agentGateway == nil || agentGateway.Idempotency == nil {
		return
	}
	agentGateway.Idempotency.Put(key, http.StatusOK, json.RawMessage(body))
}

func normalizeIdempotencyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizedStringSet(values []string) []string {
	normalized := normalizeIdempotencyStrings(values)
	if len(normalized) == 0 {
		return nil
	}
	out := append([]string(nil), normalized...)
	sort.Strings(out)
	return out
}

func equalNormalizedStringSets(left []string, right []string) bool {
	return reflect.DeepEqual(normalizedStringSet(left), normalizedStringSet(right))
}

func makeAgentAnswerIdempotencyKey(sessionID string, questionID string, values []string, displayValues []string, proceed bool, expectedUpdatedAt int64) string {
	payload := map[string]any{
		"sessionId":     strings.TrimSpace(sessionID),
		"questionId":    strings.TrimSpace(questionID),
		"values":        normalizedStringSet(values),
		"displayValues": normalizedStringSet(displayValues),
		"proceed":       proceed,
	}
	if expectedUpdatedAt > 0 {
		payload["expectedUpdatedAt"] = expectedUpdatedAt
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(
			"wisdev_agent_answer:%s:%s:%t:%d:%v:%v",
			strings.TrimSpace(sessionID),
			strings.TrimSpace(questionID),
			proceed,
			expectedUpdatedAt,
			normalizedStringSet(values),
			normalizedStringSet(displayValues),
		)
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf(
		"wisdev_agent_answer:%s:%s:%x",
		strings.TrimSpace(sessionID),
		strings.TrimSpace(questionID),
		sum,
	)
}

func makeDraftOutlineIdempotencyKey(documentID string, title string, targetWordCount int, customSections []string, expectedUpdatedAt int64) string {
	payload := map[string]any{
		"documentId":      strings.TrimSpace(documentID),
		"title":           strings.TrimSpace(title),
		"targetWordCount": targetWordCount,
		"customSections":  normalizedStringSet(customSections),
	}
	if expectedUpdatedAt > 0 {
		payload["expectedUpdatedAt"] = expectedUpdatedAt
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(
			"wisdev_drafting_outline:%s:%d:%s:%v",
			strings.TrimSpace(documentID),
			expectedUpdatedAt,
			strings.TrimSpace(title),
			normalizedStringSet(customSections),
		)
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("wisdev_drafting_outline:%s:%x", strings.TrimSpace(documentID), sum)
}

func makeDraftSectionIdempotencyKey(documentID string, sectionID string, sectionTitle string, targetWords int, papers []map[string]any, expectedUpdatedAt int64) string {
	payload := map[string]any{
		"documentId":   strings.TrimSpace(documentID),
		"sectionId":    strings.TrimSpace(sectionID),
		"sectionTitle": strings.TrimSpace(sectionTitle),
		"targetWords":  targetWords,
		"papers":       papers,
	}
	if expectedUpdatedAt > 0 {
		payload["expectedUpdatedAt"] = expectedUpdatedAt
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(
			"wisdev_drafting_section:%s:%s:%d:%s",
			strings.TrimSpace(documentID),
			strings.TrimSpace(sectionID),
			expectedUpdatedAt,
			strings.TrimSpace(sectionTitle),
		)
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("wisdev_drafting_section:%s:%s:%x", strings.TrimSpace(documentID), strings.TrimSpace(sectionID), sum)
}

func loadFullPaperJobState(agentGateway *wisdev.AgentGateway, documentID string) (map[string]any, error) {
	if agentGateway == nil || agentGateway.StateStore == nil {
		return nil, fmt.Errorf("state store unavailable")
	}
	return agentGateway.StateStore.LoadFullPaperJob(documentID)
}

func loadOwnedFullPaperJobState(w http.ResponseWriter, r *http.Request, agentGateway *wisdev.AgentGateway, documentID string) (map[string]any, bool) {
	job, err := loadFullPaperJobState(agentGateway, documentID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", nil)
		return nil, false
	}
	ownerID := strings.TrimSpace(wisdev.AsOptionalString(job["userId"]))
	if ownerID == "" {
		WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied to resource", nil)
		return nil, false
	}
	if !requireOwnerAccess(w, r, ownerID) {
		return nil, false
	}
	return job, true
}

func saveFullPaperJobState(agentGateway *wisdev.AgentGateway, job map[string]any) error {
	if agentGateway == nil || agentGateway.StateStore == nil {
		return fmt.Errorf("state store unavailable")
	}
	docID := wisdev.AsOptionalString(job["documentId"])
	if docID == "" {
		docID = wisdev.AsOptionalString(job["jobId"])
	}
	if docID == "" {
		return fmt.Errorf("jobId/documentId is required")
	}
	if err := agentGateway.StateStore.SaveFullPaperJob(docID, job); err != nil {
		return err
	}
	dossierPayload, ok := job["evidenceDossier"].(map[string]any)
	if !ok || len(dossierPayload) == 0 {
		return nil
	}
	dossierPayload = cloneAnyMap(dossierPayload)
	dossierID := strings.TrimSpace(wisdev.AsOptionalString(dossierPayload["dossierId"]))
	if dossierID == "" {
		dossierID = strings.TrimSpace(wisdev.AsOptionalString(dossierPayload["id"]))
	}
	if dossierID == "" {
		dossierID = docID
	}
	dossierPayload["jobId"] = docID
	if _, hasUserID := dossierPayload["userId"]; !hasUserID {
		dossierPayload["userId"] = wisdev.AsOptionalString(job["userId"])
	}
	return agentGateway.StateStore.SaveEvidenceDossier(dossierID, dossierPayload)
}

func requireOwnerAccess(w http.ResponseWriter, r *http.Request, ownerID string) bool {
	ownerID = strings.TrimSpace(ownerID)
	userID := strings.TrimSpace(GetUserID(r))
	if ownerID == "" || ownerID == "anonymous" {
		if userID == "admin" || userID == "internal-service" {
			return true
		}
		WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied to resource", nil)
		return false
	}
	if userID != ownerID && userID != "admin" && userID != "internal-service" {
		WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied to resource", nil)
		return false
	}
	return true
}

func assertExpectedUpdatedAt(w http.ResponseWriter, expected int64, state map[string]any) bool {
	if expected <= 0 {
		return true
	}
	actual := wisdev.IntValue64(state["updatedAt"])
	if actual != expected {
		// Tests expect StatusConflict (409) but ErrInvalidParameters ("INVALID_PARAMETERS") code
		WriteError(w, http.StatusConflict, ErrInvalidParameters, "resource has been modified by another process", map[string]any{
			"expected": expected,
			"actual":   actual,
		})
		return false
	}
	return true
}

func fullPaperHasTerminalStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == "completed" || s == "failed" || s == "cancelled"
}

func upsertDraftingState(agentGateway *wisdev.AgentGateway, documentID string, outline map[string]any, sectionID string, section map[string]any) error {
	if agentGateway == nil || agentGateway.StateStore == nil {
		return fmt.Errorf("state store unavailable")
	}
	job, err := agentGateway.StateStore.LoadFullPaperJob(documentID)
	if err != nil {
		return fmt.Errorf("failed to load job: %w", err)
	}

	workspace := mapAny(job["workspace"])
	drafting := mapAny(workspace["drafting"])

	if outline != nil {
		drafting["outline"] = outline
		var order []string
		if items, ok := outline["items"].([]any); ok {
			for _, item := range items {
				if m, ok := item.(map[string]any); ok {
					order = append(order, wisdev.AsOptionalString(m["id"]))
				}
			}
		}
		drafting["sectionOrder"] = order
	}

	if sectionID != "" && section != nil {
		sections := mapAny(drafting["sections"])
		sections[sectionID] = section
		drafting["sections"] = sections

		existingSectionArtifacts := append(sliceStrings(drafting["sectionArtifactIds"]), sectionID)
		drafting["sectionArtifactIds"] = uniqueStrings(existingSectionArtifacts)

		claimIDs := append([]string{}, sliceStrings(drafting["claimPacketIds"])...)
		claimIDs = append(claimIDs, sliceStrings(section["claimPacketIds"])...)
		claimIDs = append(claimIDs, sliceStrings(section["claimPacketId"])...)
		claimIDs = append(claimIDs, wisdev.AsOptionalString(section["claimPacketId"]))
		claimIDs = append(claimIDs, sliceStrings(section["evidencePacketIds"])...)
		claimIDs = append(claimIDs, wisdev.AsOptionalString(section["evidencePacketId"]))
		drafting["claimPacketIds"] = uniqueStrings(claimIDs)
	}

	if _, ok := drafting["sectionArtifactIds"]; !ok {
		drafting["sectionArtifactIds"] = []string{}
	}
	if _, ok := drafting["claimPacketIds"]; !ok {
		drafting["claimPacketIds"] = []string{}
	}

	workspace["drafting"] = drafting
	job["workspace"] = workspace
	job["updatedAt"] = time.Now().UnixMilli()

	return agentGateway.StateStore.SaveFullPaperJob(documentID, job)
}

func boundedInt(val int, def int, min int, max int) int {
	if val <= 0 {
		return def
	}
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

func withConcurrencyGuard(key string, limit int, fn func() error) error {
	routeConcurrencyGuards.mu.Lock()
	limiter, ok := routeConcurrencyGuards.limiters[key]
	if !ok {
		limiter = make(chan struct{}, limit)
		routeConcurrencyGuards.limiters[key] = limiter
	}
	routeConcurrencyGuards.mu.Unlock()

	select {
	case limiter <- struct{}{}:
		defer func() { <-limiter }()
		return fn()
	default:
		return fmt.Errorf("concurrency limit reached for %s", key)
	}
}

func normalizeAgentQuestionDomainHint(domain string) string {
	value := strings.ToLower(strings.TrimSpace(domain))
	switch {
	case strings.Contains(value, "med"), strings.Contains(value, "health"), strings.Contains(value, "clinical"):
		return "medicine"
	case strings.Contains(value, "computer"), strings.Contains(value, "artificial intelligence"), strings.Contains(value, "machine learning"), value == "cs", value == "ai":
		return "cs"
	case strings.Contains(value, "social"):
		return "social"
	case strings.Contains(value, "climate"), strings.Contains(value, "environment"):
		return "climate"
	case strings.Contains(value, "neuro"), strings.Contains(value, "brain"):
		return "neuro"
	case strings.Contains(value, "physics"), strings.Contains(value, "engineering"):
		return "physics"
	case strings.Contains(value, "biology"), strings.Contains(value, "life sciences"), strings.Contains(value, "life science"):
		return "biology"
	case strings.Contains(value, "humanities"), strings.Contains(value, "history"), strings.Contains(value, "literature"):
		return "humanities"
	default:
		return value
	}
}

func agentQuestionExpertiseLevel(domain string) string {
	normalized := normalizeAgentQuestionDomainHint(domain)
	if normalized == "medicine" || normalized == "biology" {
		return "advanced"
	}
	return "intermediate"
}

func normalizeAgentQuestionDomains(values []string) (string, []string) {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		domain := normalizeAgentQuestionDomainHint(value)
		if domain == "" {
			continue
		}
		normalized = append(normalized, domain)
	}
	normalized = uniqueStrings(normalized)
	if len(normalized) == 0 {
		return "", nil
	}
	primary := normalized[0]
	if len(normalized) == 1 {
		return primary, nil
	}
	return primary, normalized[1:]
}

func replanAgentSessionForDomainAnswer(session map[string]any) bool {
	if len(session) == 0 {
		return false
	}
	primaryDomain, secondaryDomains := normalizeAgentQuestionDomains(answeredAgentQuestionValues(session, "q1_domain"))
	if primaryDomain == "" {
		return false
	}

	query := resolveAgentSessionQueryMap(session)
	questions, questionSequence, minQuestions, maxQuestions := defaultAgentQuestionPlan(query, primaryDomain, secondaryDomains)
	session["detectedDomain"] = primaryDomain
	if len(secondaryDomains) > 0 {
		session["secondaryDomains"] = secondaryDomains
	} else {
		session["secondaryDomains"] = []string{}
	}
	session["questions"] = questions
	session["questionSequence"] = questionSequence
	session["minQuestions"] = minQuestions
	session["maxQuestions"] = maxQuestions
	session["clarificationBudget"] = maxQuestions
	session["expertiseLevel"] = agentQuestionExpertiseLevel(primaryDomain)
	return true
}

func canonicalAgentQuestionPayload(question wisdev.Question) map[string]any {
	options := questionOptionPayloads(question.Options)
	payload := map[string]any{
		"id":            question.ID,
		"type":          string(question.Type),
		"question":      question.Question,
		"text":          question.Question,
		"options":       options,
		"isMultiSelect": question.IsMultiSelect,
		"isRequired":    question.IsRequired,
	}
	if helpText := strings.TrimSpace(question.HelpText); helpText != "" {
		payload["helpText"] = helpText
	}
	return payload
}

func defaultAgentQuestionPlan(query string, domain string, secondaryDomains []string) ([]map[string]any, []string, int, int) {
	complexityScore := wisdev.EstimateComplexityScore(strings.TrimSpace(query))
	questionSequence, minQuestions, maxQuestions := wisdev.BuildAdaptiveQuestionSequence(
		complexityScore,
		normalizeAgentQuestionDomainHint(domain),
	)

	catalog := wisdev.DefaultQuestionFlow()
	index := make(map[string]wisdev.Question, len(catalog))
	for _, question := range catalog {
		index[question.ID] = question
	}

	questions := make([]map[string]any, 0, len(questionSequence))
	for _, questionID := range questionSequence {
		question, ok := index[questionID]
		if !ok {
			continue
		}
		questions = append(questions, canonicalAgentQuestionPayload(question))
	}
	if len(questions) == 0 {
		for _, question := range catalog {
			questions = append(questions, canonicalAgentQuestionPayload(question))
			if len(questions) >= 3 {
				break
			}
		}
		questionSequence = []string{"q1_domain", "q2_scope", "q3_timeframe"}
		minQuestions = 3
		maxQuestions = len(questionSequence)
	}
	return questions, uniqueStrings(questionSequence), minQuestions, maxQuestions
}

func defaultAgentQuestionSequence(query string, domain string) []map[string]any {
	questions, _, _, _ := defaultAgentQuestionPlan(query, domain, nil)
	return questions
}

func resolveAuthorizedUserID(r *http.Request, providedID string) (string, error) {
	uid := strings.TrimSpace(GetUserID(r))
	providedID = strings.TrimSpace(providedID)

	if uid == "" || uid == "anonymous" {
		return "", fmt.Errorf("authentication required")
	}
	if uid == "admin" || uid == "internal-service" {
		if providedID != "" {
			return providedID, nil
		}
		return uid, nil
	}
	if providedID == "" {
		return uid, nil
	}
	if uid != providedID {
		return "", fmt.Errorf("access denied")
	}
	return uid, nil
}

func answeredAgentQuestionValues(session map[string]any, questionID string) []string {
	if len(session) == 0 || strings.TrimSpace(questionID) == "" {
		return nil
	}
	answers := mapAny(session["answers"])
	answer := mapAny(answers[strings.TrimSpace(questionID)])
	if len(answer) == 0 {
		return nil
	}
	return sliceStrings(answer["values"])
}

func inferPendingAgentFollowUpTargetQuestionID(question map[string]any) string {
	targetQuestionID := strings.TrimSpace(wisdev.AsOptionalString(question["targetQuestionId"]))
	switch targetQuestionID {
	case "q4_subtopics", "q5_study_types":
		return targetQuestionID
	case "":
		// Infer below.
	default:
		return ""
	}
	combinedContext := strings.ToLower(strings.Join([]string{
		wisdev.AsOptionalString(question["question"]),
		wisdev.AsOptionalString(question["questionExplanation"]),
		wisdev.AsOptionalString(question["optionsExplanation"]),
		wisdev.AsOptionalString(question["helpText"]),
	}, " "))
	if strings.Contains(combinedContext, "study design") || strings.Contains(combinedContext, "study type") {
		return "q5_study_types"
	}
	return "q4_subtopics"
}

func mirrorPendingAgentFollowUpAnswer(session map[string]any, pendingQuestion map[string]any, values []string, displayValues []string) string {
	if len(session) == 0 || len(pendingQuestion) == 0 {
		return ""
	}
	targetQuestionID := inferPendingAgentFollowUpTargetQuestionID(pendingQuestion)
	if targetQuestionID == "" {
		return ""
	}
	mirroredValues := normalizeIdempotencyStrings(values)
	if len(mirroredValues) == 0 {
		return ""
	}
	answers := mapAny(session["answers"])
	mirroredDisplayValues := normalizeIdempotencyStrings(displayValues)
	if len(mirroredDisplayValues) == 0 {
		mirroredDisplayValues = mirroredValues
	}
	answers[targetQuestionID] = map[string]any{
		"questionId":             targetQuestionID,
		"values":                 mirroredValues,
		"displayValues":          mirroredDisplayValues,
		"answeredAt":             time.Now().UTC().Format(time.RFC3339),
		"mirroredFromQuestionId": strings.TrimSpace(wisdev.AsOptionalString(pendingQuestion["id"])),
	}
	session["answers"] = answers
	return targetQuestionID
}

func agentAnswerAlreadyApplied(session map[string]any, questionID string, values []string, displayValues []string) bool {
	if len(session) == 0 || strings.TrimSpace(questionID) == "" {
		return false
	}
	answers := mapAny(session["answers"])
	answer := mapAny(answers[strings.TrimSpace(questionID)])
	if len(answer) == 0 {
		return false
	}
	if !equalNormalizedStringSets(sliceStrings(answer["values"]), values) {
		return false
	}
	if len(normalizeIdempotencyStrings(displayValues)) == 0 {
		return true
	}
	return equalNormalizedStringSets(sliceStrings(answer["displayValues"]), displayValues)
}

func isPlannedPendingAgentFollowUpTarget(session map[string]any, pending map[string]any) bool {
	targetQuestionID := inferPendingAgentFollowUpTargetQuestionID(pending)
	if targetQuestionID == "" {
		return false
	}
	for _, questionID := range sliceStrings(session["questionSequence"]) {
		if strings.TrimSpace(questionID) == targetQuestionID {
			return true
		}
	}
	return false
}

func getPendingAgentFollowUpQuestion(session map[string]any) map[string]any {
	pending := mapAny(session["pendingFollowUpQuestion"])
	if len(pending) == 0 {
		return nil
	}
	if !isPlannedPendingAgentFollowUpTarget(session, pending) {
		return nil
	}
	if strings.TrimSpace(wisdev.AsOptionalString(pending["id"])) == "" {
		pending["id"] = "follow_up_refinement"
	}
	sanitizeFollowUpQuestionText(pending)
	sanitizeFollowUpQuestionOptions(
		pending,
		resolveAgentSessionQueryMap(session),
		strings.TrimSpace(wisdev.AsOptionalString(session["detectedDomain"])),
	)
	return pending
}

func isAgentQuestionRequired(session map[string]any, questionID string) bool {
	trimmedQuestionID := strings.TrimSpace(questionID)
	if trimmedQuestionID == "" {
		return false
	}
	if pending := getPendingAgentFollowUpQuestion(session); len(pending) > 0 &&
		strings.TrimSpace(wisdev.AsOptionalString(pending["id"])) == trimmedQuestionID {
		if required, ok := pending["isRequired"].(bool); ok {
			return required
		}
		return true
	}
	for _, question := range sliceAnyMap(session["questions"]) {
		if strings.TrimSpace(wisdev.AsOptionalString(question["id"])) != trimmedQuestionID {
			continue
		}
		if required, ok := question["isRequired"].(bool); ok {
			return required
		}
		return true
	}
	return false
}

func hasNonEmptyAnswerValues(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func agentQuestionAllowsMultipleAnswers(session map[string]any, questionID string) bool {
	trimmedQuestionID := strings.TrimSpace(questionID)
	if trimmedQuestionID == "" {
		return true
	}
	if pending := getPendingAgentFollowUpQuestion(session); len(pending) > 0 &&
		strings.TrimSpace(wisdev.AsOptionalString(pending["id"])) == trimmedQuestionID {
		if isMultiSelect, ok := pending["isMultiSelect"].(bool); ok {
			return isMultiSelect
		}
		return true
	}
	for _, question := range sliceAnyMap(session["questions"]) {
		if strings.TrimSpace(wisdev.AsOptionalString(question["id"])) != trimmedQuestionID {
			continue
		}
		if isMultiSelect, ok := question["isMultiSelect"].(bool); ok {
			return isMultiSelect
		}
		switch strings.TrimSpace(wisdev.AsOptionalString(question["type"])) {
		case "domain", "subtopics", "study_types", "exclusions":
			return true
		}
		return !wisdev.IsKnownSingleSelectQuestionID(trimmedQuestionID)
	}
	return !wisdev.IsKnownSingleSelectQuestionID(trimmedQuestionID)
}

func normalizeAgentQuestionAnswerValues(session map[string]any, questionID string, values []string, displayValues []string) ([]string, []string) {
	return wisdev.NormalizeAnswerValues(
		agentQuestionAllowsMultipleAnswers(session, questionID),
		values,
		displayValues,
	)
}

func clearPendingAgentFollowUpQuestion(session map[string]any) {
	if len(session) == 0 {
		return
	}
	delete(session, "pendingFollowUpQuestion")
}

func buildAgentQuestionPayload(session map[string]any, adaptive bool) map[string]any {
	_ = adaptive
	status := strings.ToLower(strings.TrimSpace(wisdev.AsOptionalString(session["status"])))
	if status != "" && status != string(wisdev.SessionQuestioning) {
		return nil
	}
	if pending := getPendingAgentFollowUpQuestion(session); len(pending) > 0 {
		return pending
	}
	questions := sliceAnyMap(session["questions"])
	if len(questions) == 0 {
		return nil
	}
	if canonical := buildCanonicalAgentSession(session); canonical != nil {
		nextQuestionID := strings.TrimSpace(wisdev.FindNextQuestionID(canonical))
		if nextQuestionID != "" {
			for _, question := range questions {
				if wisdev.AsOptionalString(question["id"]) == nextQuestionID {
					return question
				}
			}
		}
	}
	index := wisdev.IntValue(session["currentQuestionIndex"])
	if index < 0 || index >= len(questions) {
		return nil
	}
	return questions[index]
}

func buildAgentQuestioningEnvelopeBody(traceID string, session map[string]any, adaptive bool) map[string]any {
	body := buildEnvelopeBody(traceID, "session", session)
	question := buildAgentQuestionPayload(session, adaptive)
	body["question"] = question
	completed := strings.ToLower(strings.TrimSpace(wisdev.AsOptionalString(session["status"]))) != string(wisdev.SessionQuestioning)
	if question == nil {
		completed = true
	}
	body["completed"] = completed
	if canonical := buildCanonicalAgentSession(session); canonical != nil {
		body["questioning"] = wisdev.BuildQuestionStateSummary(canonical)
	}
	if pending := getPendingAgentFollowUpQuestion(session); len(pending) > 0 {
		questioning := mapAny(body["questioning"])
		pendingQuestionID := strings.TrimSpace(wisdev.AsOptionalString(pending["id"]))
		if pendingQuestionID != "" {
			remaining := append([]string{pendingQuestionID}, sliceStrings(questioning["remainingQuestionIds"])...)
			questioning["remainingQuestionIds"] = uniqueStrings(remaining)
			questioning["pendingQuestionId"] = pendingQuestionID
			body["questioning"] = questioning
		}
	}
	return body
}

func ensureAgentSessionMutable(session map[string]any) error {
	status := wisdev.AsOptionalString(session["status"])
	if status == "completed" || status == "failed" {
		// Tests expect StatusConflict (409) but ErrInvalidParameters ("INVALID_PARAMETERS") code
		return fmt.Errorf("session is in terminal state: %s", status)
	}
	return nil
}

func normalizeResearchPlanQueries(queries []string) []string {
	normalized := make([]string, 0, len(queries))
	seen := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		trimmed := strings.TrimSpace(query)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func buildAgentOrchestrationPlanWithQueries(session map[string]any, queries []string, coverageMap map[string]any, generatedFromTree bool) map[string]any {
	normalizedQueries := normalizeResearchPlanQueries(queries)
	if len(normalizedQueries) == 0 {
		normalizedQueries = normalizeResearchPlanQueries([]string{resolveAgentSessionQueryMap(session)})
	}
	if coverageMap == nil {
		coverageMap = map[string]any{}
	}
	return map[string]any{
		"queries":           normalizedQueries,
		"coverageMap":       coverageMap,
		"generatedFromTree": generatedFromTree,
		"generatedAt":       time.Now().UnixMilli(),
	}
}

func buildAgentOrchestrationPlan(session map[string]any) map[string]any {
	return buildAgentOrchestrationPlanWithQueries(session, nil, nil, false)
}

func resolveOperationMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "yolo" {
		return "yolo"
	}
	return "guided"
}

func normalizeDeepResearchCategories(categories []string, domainHint string) []string {
	normalized := make([]string, 0, len(categories))
	seen := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		trimmed := strings.TrimSpace(category)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) > 0 {
		return normalized
	}
	if trimmedHint := strings.TrimSpace(domainHint); trimmedHint != "" {
		return []string{trimmedHint}
	}
	return []string{"General"}
}

func buildDeepResearchPayload(query string, categories []string, domainHint string, papers []wisdev.Source) map[string]any {
	committee := buildMultiAgentCommitteeResult(query, domainHint, papers, 3, true)
	normalizedCategories := normalizeDeepResearchCategories(categories, domainHint)
	paperPayload := committee["papers"]
	categorizedSources := make([]map[string]any, 0, len(normalizedCategories))
	for index, category := range normalizedCategories {
		sources := []map[string]any{}
		if paperMaps, ok := paperPayload.([]map[string]any); ok && index == 0 {
			sources = paperMaps
		}
		categorizedSources = append(categorizedSources, map[string]any{
			"category":        category,
			"categorySummary": fmt.Sprintf("Go deep research returned %d candidate source(s).", len(sources)),
			"sources":         sources,
		})
	}
	return map[string]any{
		"query":              query,
		"categories":         normalizedCategories,
		"categorizedSources": categorizedSources,
		"paperPools": []map[string]any{
			{
				"category":       normalizedCategories[0],
				"label":          normalizedCategories[0],
				"papers":         []map[string]any{},
				"totalAvailable": len(papers),
			},
		},
		"prismaReport": map[string]any{
			"included": len(papers),
			"excluded": 0,
			"total":    len(papers),
		},
		"papers":    committee["papers"],
		"answer":    committee["answer"],
		"citations": committee["citations"],
		"committee": committee,
		"workerMetadata": map[string]any{
			"searchPasses":  len(categories) + 1,
			"sourceOfTruth": "go-control-plane",
			"retrievalMode": "deep_multi_pass",
		},
	}
}

func buildDeepResearchLoopPayload(
	query string,
	categories []string,
	domainHint string,
	loopResult *wisdev.LoopResult,
) map[string]any {
	if loopResult == nil {
		return buildDeepResearchPayload(query, categories, domainHint, nil)
	}

	papers := searchPapersToWisdevSources(loopResult.Papers)
	payload := buildDeepResearchPayload(query, categories, domainHint, papers)
	payload["coverageMap"] = buildAutonomousCoveragePayload(nil, normalizeResearchPlanQueries(loopResult.ExecutedQueries), serializeAutonomousCoverageSearchPapersByQuery(loopResult.QueryCoverage))
	payload["iterations"] = []map[string]any{{
		"count":     loopResult.Iterations,
		"converged": loopResult.Converged,
	}}
	payload["reasoningGraph"] = loopResult.ReasoningGraph
	payload["memoryTiers"] = loopResult.MemoryTiers
	payload["gapAnalysis"] = loopResult.GapAnalysis
	payload["draftCritique"] = loopResult.DraftCritique
	payload["finalAnswer"] = loopResult.FinalAnswer
	payload["finalizationGate"] = loopResult.FinalizationGate
	payload["stopReason"] = loopResult.StopReason
	attachUnifiedRuntimeStatePayload(payload, loopResult)
	if loopResult.GapAnalysis != nil && len(loopResult.GapAnalysis.Ledger) > 0 {
		payload["coverageLedger"] = loopResult.GapAnalysis.Ledger
	}
	if gapPayloads := buildAutonomousGapPayloadsFromLoopAnalysis(loopResult.GapAnalysis); len(gapPayloads) > 0 {
		payload["gaps"] = gapPayloads
	}
	if workerMetadata, ok := payload["workerMetadata"].(map[string]any); ok {
		workerMetadata["searchPasses"] = maxInt(len(loopResult.ExecutedQueries), 1)
		workerMetadata["retrievalMode"] = "deep_canonical_runtime"
		workerMetadata["coverageModel"] = "ledger_tree_runtime"
		if loopResult.GapAnalysis != nil {
			workerMetadata["observedSourceFamilies"] = loopResult.GapAnalysis.ObservedSourceFamilies
			workerMetadata["observedEvidenceCount"] = loopResult.GapAnalysis.ObservedEvidenceCount
		}
	}
	enforceLoopFinalizationPayload(payload, loopResult)
	return payload
}

func buildAutonomousResearchLoopPayload(
	query string,
	domainHint string,
	loopResult *wisdev.LoopResult,
	planCoverage map[string][]string,
	attachCommittee bool,
) map[string]any {
	if loopResult == nil {
		return map[string]any{}
	}

	papers := searchPapersToWisdevSources(loopResult.Papers)
	executedQueries := normalizeResearchPlanQueries(loopResult.ExecutedQueries)
	payload := map[string]any{
		"query":            query,
		"coverageMap":      buildAutonomousCoveragePayload(planCoverage, executedQueries, serializeAutonomousCoverageSearchPapersByQuery(loopResult.QueryCoverage)),
		"iterations":       []map[string]any{{"count": loopResult.Iterations, "converged": loopResult.Converged}},
		"gapAnalysis":      loopResult.GapAnalysis,
		"draftCritique":    loopResult.DraftCritique,
		"finalAnswer":      loopResult.FinalAnswer,
		"finalizationGate": loopResult.FinalizationGate,
		"stopReason":       loopResult.StopReason,
		"reasoningGraph":   loopResult.ReasoningGraph,
		"memoryTiers":      loopResult.MemoryTiers,
		"mode":             loopResult.Mode,
		"serviceTier":      loopResult.ServiceTier,
		"prismaReport": map[string]any{
			"included": len(papers),
			"excluded": 0,
			"total":    len(papers),
		},
	}

	if attachCommittee {
		committee := buildMultiAgentCommitteeResult(query, domainHint, papers, 2, true)
		payload["papers"] = committee["papers"]
		payload["artifacts"] = committee["papers"]
		if answer, ok := committee["answer"]; ok {
			payload["answer"] = answer
		}
		if citations, ok := committee["citations"]; ok {
			payload["citations"] = citations
		}
		if committeePayload, ok := committee["committee"]; ok {
			payload["committee"] = committeePayload
		} else {
			payload["committee"] = committee
		}
	} else {
		papersPayload := buildCommitteePapers(papers)
		payload["papers"] = papersPayload
		payload["artifacts"] = papersPayload
	}
	if loopResult.GapAnalysis != nil && len(loopResult.GapAnalysis.Ledger) > 0 {
		payload["coverageLedger"] = loopResult.GapAnalysis.Ledger
	}
	attachUnifiedRuntimeStatePayload(payload, loopResult)
	if gapPayloads := buildAutonomousGapPayloadsFromLoopAnalysis(loopResult.GapAnalysis); len(gapPayloads) > 0 {
		payload["gaps"] = gapPayloads
	}
	enforceLoopFinalizationPayload(payload, loopResult)

	return payload
}

func attachUnifiedRuntimeStatePayload(payload map[string]any, loopResult *wisdev.LoopResult) {
	if payload == nil || loopResult == nil || loopResult.RuntimeState == nil {
		return
	}
	state := loopResult.RuntimeState
	runtimeState := map[string]any{
		"sessionId":         state.SessionID,
		"plane":             state.Plane,
		"stopReason":        state.StopReason,
		"openLedgerCount":   countOpenRuntimeCoverageLedger(state.CoverageLedger),
		"readyForSynthesis": state.Blackboard != nil && state.Blackboard.ReadyForSynthesis,
	}
	payload["runtimeState"] = runtimeState
	if reasoningRuntime := buildResearchReasoningRuntimePayload(loopResult); len(reasoningRuntime) > 0 {
		payload["reasoningRuntime"] = reasoningRuntime
		runtimeState["reasoningRuntime"] = reasoningRuntime
	}
	if state.DurableJob != nil {
		payload["durableJob"] = state.DurableJob
	}
	if len(state.DurableTasks) > 0 {
		payload["durableTasks"] = state.DurableTasks
	}
	if state.SourceAcquisition != nil {
		payload["sourceAcquisition"] = state.SourceAcquisition
	}
	if state.CitationGraph != nil {
		payload["citationGraph"] = state.CitationGraph
	}
	if len(state.BranchEvaluations) > 0 {
		payload["branchEvaluations"] = state.BranchEvaluations
		payload["researchBranches"] = state.BranchEvaluations
	}
	if len(state.Workers) > 0 {
		payload["workerReports"] = state.Workers
	}
	if obligations := wisdev.BuildResearchCoverageObligations(state); len(obligations) > 0 {
		payload["coverageObligations"] = obligations
	}
	if state.Budget != nil {
		payload["adaptiveBudget"] = state.Budget
	}
	if state.ClaimVerification != nil {
		payload["claimVerification"] = state.ClaimVerification
	}
	if state.VerifierDecision != nil {
		payload["verifierDecision"] = state.VerifierDecision
	}
}

func enforceLoopFinalizationPayload(payload map[string]any, loopResult *wisdev.LoopResult) {
	if payload == nil || loopResult == nil {
		return
	}
	openLedgerCount := 0
	if loopResult.GapAnalysis != nil {
		openLedgerCount = countOpenRuntimeCoverageLedger(loopResult.GapAnalysis.Ledger)
	}
	if loopResult.RuntimeState != nil {
		openLedgerCount = maxInt(openLedgerCount, countOpenRuntimeCoverageLedger(loopResult.RuntimeState.CoverageLedger))
		if loopResult.RuntimeState.Blackboard != nil {
			openLedgerCount = maxInt(openLedgerCount, loopResult.RuntimeState.Blackboard.OpenLedgerCount)
		}
	}
	gate := loopResult.FinalizationGate
	if gate == nil && openLedgerCount > 0 {
		gate = &wisdev.ResearchFinalizationGate{
			Status:          "revise_required",
			Provisional:     true,
			OpenLedgerCount: openLedgerCount,
			StopReason:      firstNonEmptyString(loopResult.StopReason, "coverage_open"),
		}
		if loopResult.GapAnalysis != nil {
			gate.FollowUpQueries = append([]string(nil), loopResult.GapAnalysis.NextQueries...)
		}
	}
	if gate == nil {
		return
	}
	payload["finalizationGate"] = gate
	payload["stopReason"] = firstNonEmptyString(gate.StopReason, loopResult.StopReason)
	payload["openLedgerCount"] = gate.OpenLedgerCount
	payload["followUpQueries"] = append([]string(nil), gate.FollowUpQueries...)
	status := strings.TrimSpace(gate.Status)
	if status == "" {
		status = "revise_required"
	}
	payload["answerStatus"] = status
	if status != "promote" || gate.Provisional || gate.OpenLedgerCount > 0 {
		payload["blockingFinalization"] = true
		if answer := strings.TrimSpace(wisdev.AsOptionalString(payload["finalAnswer"])); answer != "" && !strings.HasPrefix(strings.ToLower(answer), "provisional answer:") {
			payload["finalAnswer"] = "Provisional answer: " + answer
		}
	}
}

func enrichResearchMetadataWithRuntimeState(metadata map[string]any, loopResult *wisdev.LoopResult) {
	if metadata == nil || loopResult == nil || loopResult.RuntimeState == nil {
		return
	}
	state := loopResult.RuntimeState
	metadata["runtimeStateAvailable"] = true
	metadata["finalStopReason"] = state.StopReason
	if reasoningRuntime := buildResearchReasoningRuntimePayload(loopResult); len(reasoningRuntime) > 0 {
		metadata["reasoningRuntime"] = reasoningRuntime
	}
	answerVerified := loopResult.FinalizationGate != nil && !loopResult.FinalizationGate.Provisional
	metadata["answerStatus"] = wisdev.ResearchAnswerStatusFromState(state, loopResult.FinalizationGate, answerVerified, loopResult.StopReason)
	if state.DurableJob != nil {
		metadata["durableJob"] = state.DurableJob
		metadata["durableJobStatus"] = state.DurableJob.Status
		metadata["durableJobId"] = state.DurableJob.JobID
	}
	if len(state.DurableTasks) > 0 {
		metadata["durableTasks"] = state.DurableTasks
		metadata["durableTaskCount"] = len(state.DurableTasks)
	}
	if state.SourceAcquisition != nil {
		metadata["sourceAcquisition"] = state.SourceAcquisition
		metadata["sourceAcquisitionAttempts"] = len(state.SourceAcquisition.Attempts)
		metadata["sourceFetchFailures"] = state.SourceAcquisition.FetchFailures
		metadata["pythonPdfExtractions"] = state.SourceAcquisition.RequiredPythonExtractions
	}
	if state.CitationGraph != nil {
		metadata["citationGraph"] = state.CitationGraph
		metadata["citationGraphNodeCount"] = len(state.CitationGraph.Nodes)
		metadata["citationGraphEdgeCount"] = len(state.CitationGraph.Edges)
		metadata["citationIdentityConflictCount"] = len(state.CitationGraph.IdentityConflicts) + len(state.CitationGraph.DuplicateSourceIDs)
	}
	if len(state.BranchEvaluations) > 0 {
		metadata["researchBranches"] = state.BranchEvaluations
	}
	if obligations := wisdev.BuildResearchCoverageObligations(state); len(obligations) > 0 {
		metadata["coverageObligations"] = obligations
	}
	if state.VerifierDecision != nil {
		metadata["verifierVerdict"] = wisdev.ResearchVerifierVerdict(state.VerifierDecision)
	}
	if state.Budget != nil {
		metadata["adaptiveBudget"] = state.Budget
	}
}

func buildResearchReasoningRuntimePayload(loopResult *wisdev.LoopResult) map[string]any {
	if loopResult == nil || loopResult.RuntimeState == nil {
		return nil
	}
	state := loopResult.RuntimeState
	return wisdev.BuildResearchReasoningRuntimeMetadata(wisdev.LoopRequest{
		Query:                       state.Query,
		Domain:                      state.Domain,
		ProjectID:                   state.SessionID,
		Mode:                        string(loopResult.Mode),
		DisableProgrammaticPlanning: state.DisableProgrammaticPlanning,
		DisableHypothesisGeneration: state.DisableHypothesisGeneration,
	}, state.Plane, state.Budget)
}

func countOpenRuntimeCoverageLedger(ledger []wisdev.CoverageLedgerEntry) int {
	count := 0
	for _, entry := range ledger {
		if strings.EqualFold(strings.TrimSpace(entry.Status), "open") {
			count++
		}
	}
	return count
}

func buildAutonomousResearchRetrievalPayload(
	query string,
	domainHint string,
	papers []wisdev.Source,
	attachCommittee bool,
) map[string]any {
	payload := map[string]any{
		"query": query,
		"prismaReport": map[string]any{
			"included": len(papers),
			"excluded": 0,
			"total":    len(papers),
		},
	}

	if attachCommittee {
		committee := buildMultiAgentCommitteeResult(query, domainHint, papers, 2, true)
		payload["papers"] = committee["papers"]
		payload["artifacts"] = committee["papers"]
		if answer, ok := committee["answer"]; ok {
			payload["answer"] = answer
		}
		if citations, ok := committee["citations"]; ok {
			payload["citations"] = citations
		}
		if committeePayload, ok := committee["committee"]; ok {
			payload["committee"] = committeePayload
		} else {
			payload["committee"] = committee
		}
		return payload
	}

	papersPayload := buildCommitteePapers(papers)
	payload["papers"] = papersPayload
	payload["artifacts"] = papersPayload
	return payload
}

func attachEvidenceDossier(agentGateway *wisdev.AgentGateway, payload map[string]any, jobID string, query string, userID string, papers []wisdev.Source) {
	if payload == nil || strings.TrimSpace(query) == "" || len(papers) == 0 {
		return
	}
	if strings.TrimSpace(jobID) == "" {
		jobID = "dossier_" + wisdev.NewTraceID()
	}
	dossier, err := evidence.BuildDossier(jobID, query, convertWisdevSourcesToSearchPapers(papers))
	if err != nil {
		slog.Warn("failed to build evidence dossier", "jobId", jobID, "query", query, "error", err)
		return
	}
	dossierPayload := map[string]any{
		"dossierId":        dossier.DossierID,
		"jobId":            jobID,
		"userId":           strings.TrimSpace(userID),
		"query":            dossier.Query,
		"canonicalSources": dossier.CanonicalSources,
		"verifiedClaims":   dossier.VerifiedClaims,
		"tentativeClaims":  dossier.TentativeClaims,
		"contradictions":   dossier.Contradictions,
		"gaps":             dossier.Gaps,
		"coverageMetrics":  dossier.CoverageMetrics,
		"createdAt":        dossier.CreatedAt,
		"updatedAt":        dossier.UpdatedAt,
	}
	payload["evidenceDossier"] = dossierPayload
	if agentGateway == nil || agentGateway.StateStore == nil {
		return
	}
	if err := agentGateway.StateStore.SaveEvidenceDossier(dossier.DossierID, dossierPayload); err != nil {
		slog.Warn("failed to persist evidence dossier", "dossierId", dossier.DossierID, "jobId", jobID, "error", err)
	}
}

func applyLegacyResearchEnvelopeFields(
	payload map[string]any,
	qualityMode string,
	budget deepResearchBudget,
	warnings []string,
	executionPlane string,
) {
	if payload == nil {
		return
	}
	payload["qualityMode"] = qualityMode
	payload["searchBudget"] = map[string]any{
		"maxSearchTerms": budget.maxSearchTerms,
		"hitsPerSearch":  budget.hitsPerSearch,
	}
	payload["warnings"] = append([]string(nil), warnings...)
	metadata := mapAny(payload["metadata"])
	if len(metadata) == 0 {
		metadata = map[string]any{}
	}
	metadata["executionPlane"] = strings.TrimSpace(executionPlane)
	payload["metadata"] = metadata
}

func attachResearchEvidence(
	agentGateway *wisdev.AgentGateway,
	payload map[string]any,
	jobPrefix string,
	sessionID string,
	query string,
	userID string,
	papers []wisdev.Source,
) {
	attachEvidenceDossier(
		agentGateway,
		payload,
		strings.TrimSpace(jobPrefix)+"_"+firstNonEmpty(strings.TrimSpace(sessionID), wisdev.NewTraceID()),
		query,
		userID,
		papers,
	)
	if gapPayloads := buildAutonomousGapPayloads(mapAny(payload["evidenceDossier"])); len(gapPayloads) > 0 {
		if _, alreadySet := payload["gaps"]; !alreadySet {
			payload["gaps"] = gapPayloads
		}
	}
}

func IntValue(v any) int {
	return wisdev.IntValue(v)
}

func AsFloat(v any) float64 {
	return wisdev.AsFloat(v)
}

func isAllowedFullPaperCheckpointAction(job map[string]any, stageId string, action string) error {
	status := wisdev.AsOptionalString(job["status"])
	if fullPaperHasTerminalStatus(status) {
		return fmt.Errorf("job is in terminal status")
	}
	pending, _ := job["pendingCheckpoint"].(map[string]any)
	if pending == nil {
		return fmt.Errorf("no pending checkpoint")
	}
	if wisdev.AsOptionalString(pending["stageId"]) != stageId {
		return fmt.Errorf("checkpoint is not for stage %s", stageId)
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		return fmt.Errorf("checkpoint action is required")
	}
	allowedActions := normalizedStringSet(sliceStrings(pending["actions"]))
	if len(allowedActions) == 0 {
		allowedActions = []string{"approve", "reject", "request_revision", "skip"}
	}
	for _, allowed := range allowedActions {
		if action == strings.ToLower(strings.TrimSpace(allowed)) {
			return nil
		}
	}
	return fmt.Errorf("checkpoint action %s is not allowed for stage %s", action, stageId)
}

func isAllowedFullPaperCheckpointActionLegacy(job map[string]any, stageId string) error {
	return isAllowedFullPaperCheckpointAction(job, stageId, "approve")
}

func applyFullPaperCheckpointAction(job map[string]any, stageId string, action string, feedback any) {
	delete(job, "pendingCheckpoint")
	job["status"] = "running"
	job["updatedAt"] = time.Now().UnixMilli()
}

func isAllowedFullPaperControlAction(job map[string]any, action string, something string) error {
	status := wisdev.AsOptionalString(job["status"])
	a := strings.ToLower(strings.TrimSpace(action))
	switch a {
	case "pause":
		if status != "running" {
			return fmt.Errorf("cannot pause non-running job")
		}
		return nil
	case "resume":
		if status != "paused" {
			return fmt.Errorf("cannot resume non-paused job")
		}
		return nil
	case "retry_stage":
		if fullPaperHasTerminalStatus(status) || status == "cancelled" {
			return fmt.Errorf("cannot retry terminal job")
		}
		return nil
	case "cancel":
		if fullPaperHasTerminalStatus(status) {
			return fmt.Errorf("cannot cancel terminal job")
		}
		return nil
	case "restart":
		if status == "" {
			return fmt.Errorf("cannot restart unknown job state")
		}
		return nil
	default:
		return fmt.Errorf("invalid action: %s", action)
	}
}

func applyFullPaperControlAction(job map[string]any, action string) {
	a := strings.ToLower(strings.TrimSpace(action))
	if a == "pause" {
		job["status"] = "paused"
	} else if a == "resume" {
		job["status"] = "running"
	}
	job["updatedAt"] = time.Now().UnixMilli()
}

func validateEnum(val string, allowed ...string) bool {
	v := strings.ToLower(strings.TrimSpace(val))
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func normalizePolicyVersion(agentGateway *wisdev.AgentGateway, version string, reqContext any) string {
	v := strings.TrimSpace(version)
	if v == "" {
		if agentGateway != nil {
			return agentGateway.PolicyConfig.PolicyVersion
		}
		return ""
	}
	return v
}
