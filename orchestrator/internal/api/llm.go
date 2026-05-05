package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// generateErrorKind classifies an LLM error so the frontend can decide whether
// to retry, show a user-facing message, or trip its own circuit breaker.
type generateErrorKind string

const (
	generateErrTransient generateErrorKind = "transient"  // 502/503/timeout — worth retrying
	generateErrRateLimit generateErrorKind = "rate_limit" // 429 / quota
	generateErrPermanent generateErrorKind = "permanent"  // bad request, auth, etc.
	generateErrTimeout   generateErrorKind = "timeout"    // handler deadline exceeded
)

type LLMHandler struct {
	llmClient generateClient
}

type generateClient interface {
	Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error)
}

type embedClient interface {
	Embed(ctx context.Context, req *llmv1.EmbedRequest) (*llmv1.EmbedResponse, error)
}

type embedBatchClient interface {
	EmbedBatch(ctx context.Context, req *llmv1.EmbedBatchRequest) (*llmv1.EmbedBatchResponse, error)
}

type structuredOutputClient interface {
	StructuredOutput(ctx context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error)
}

func NewLLMHandler(llmClient generateClient) *LLMHandler {
	return &LLMHandler{llmClient: llmClient}
}

func writeGenerateError(w http.ResponseWriter, status int, kind generateErrorKind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": msg,
		"kind":  string(kind),
	})
}

func classifyLLMError(err error) generateErrorKind {
	if err == nil {
		return ""
	}
	if kind, ok := classifyTypedLLMError(err); ok {
		return kind
	}
	switch status.Code(err) {
	case codes.DeadlineExceeded, codes.Canceled:
		return generateErrTimeout
	case codes.ResourceExhausted:
		return generateErrRateLimit
	case codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange, codes.Unimplemented:
		return generateErrPermanent
	case codes.Unavailable, codes.Internal, codes.Aborted:
		return generateErrTransient
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "deadline exceeded") {
		return generateErrTimeout
	}
	if strings.Contains(msg, "429") || strings.Contains(msg, "quota") || strings.Contains(msg, "rate") {
		return generateErrRateLimit
	}
	if strings.Contains(msg, "unavailable") || strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") || strings.Contains(msg, "timeout") {
		return generateErrTransient
	}
	// Only classify as permanent for errors that are definitively non-retriable
	// with a different model (auth failures, malformed requests).
	if strings.Contains(msg, "unauthenticated") || strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "invalid argument") || strings.Contains(msg, "bad request") ||
		strings.Contains(msg, "(permanent)") || strings.Contains(msg, "invalid_embed_request") ||
		strings.Contains(msg, "invalid_embed_batch_request") || strings.Contains(msg, "invalid_prompt") {
		return generateErrPermanent
	}
	// Default: treat as transient so the chain continues to the next tier.
	return generateErrTransient
}

func classifyTypedLLMError(err error) (generateErrorKind, bool) {
	for _, text := range llmErrorTexts(err) {
		code := extractTypedLLMErrorCode(text)
		if code == "" {
			continue
		}
		kind := classifyTypedLLMErrorCode(code)
		if kind == "" {
			continue
		}
		return kind, true
	}
	return "", false
}

func llmErrorTexts(err error) []string {
	texts := []string{strings.TrimSpace(err.Error())}
	if st, ok := status.FromError(err); ok {
		if message := strings.TrimSpace(st.Message()); message != "" && message != texts[0] {
			texts = append(texts, message)
		}
	}
	return texts
}

func extractTypedLLMErrorCode(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	var envelope struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err == nil {
		if code := strings.ToUpper(strings.TrimSpace(envelope.Error.Code)); code != "" {
			return code
		}
		if code := strings.ToUpper(strings.TrimSpace(envelope.Code)); code != "" {
			return code
		}
	}

	upper := strings.ToUpper(text)
	for _, code := range []string{
		"STRUCTURED_FAILED",
		"STRUCTURED_TIMEOUT",
		"GENERATE_TIMEOUT",
		"EMBED_TIMEOUT",
		"EMBED_BATCH_TIMEOUT",
		"INVALID_PROMPT",
		"INVALID_JSON_SCHEMA",
		"MISSING_JSON_SCHEMA",
		"INVALID_EMBED_REQUEST",
		"INVALID_EMBED_BATCH_REQUEST",
		"STRUCTURED_OUTPUT_REQUIRES_NATIVE_RUNTIME",
		"UNAUTHORIZED",
		"RATE_LIMIT",
		"RESOURCE_EXHAUSTED",
	} {
		if strings.Contains(upper, code) {
			return code
		}
	}

	return ""
}

func classifyTypedLLMErrorCode(code string) generateErrorKind {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "STRUCTURED_TIMEOUT", "GENERATE_TIMEOUT", "EMBED_TIMEOUT", "EMBED_BATCH_TIMEOUT", "TIMEOUT":
		return generateErrTimeout
	case "RATE_LIMIT", "RESOURCE_EXHAUSTED":
		return generateErrRateLimit
	case "INVALID_PROMPT", "INVALID_JSON_SCHEMA", "MISSING_JSON_SCHEMA", "INVALID_EMBED_REQUEST",
		"INVALID_EMBED_BATCH_REQUEST", "STRUCTURED_OUTPUT_REQUIRES_NATIVE_RUNTIME", "UNAUTHORIZED":
		return generateErrPermanent
	case "STRUCTURED_FAILED":
		return generateErrTransient
	default:
		return ""
	}
}

func (h *LLMHandler) HandleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeGenerateError(w, http.StatusMethodNotAllowed, generateErrPermanent, "Method not allowed")
		return
	}
	var body struct {
		Prompt         string          `json:"prompt"`
		SystemPrompt   string          `json:"systemPrompt"`
		Tier           string          `json:"tier"`
		TaskType       string          `json:"taskType"`
		MaxTokens      int             `json:"maxTokens"`
		Temperature    float32         `json:"temperature"`
		ResponseFormat string          `json:"responseFormat"`
		RetryProfile   string          `json:"retryProfile"`
		JsonSchema     json.RawMessage `json:"jsonSchema"`
		RoutingIntent  struct {
			LatencyBudgetMs int `json:"latencyBudgetMs"`
		} `json:"routingIntent"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "Invalid request body")
		return
	}

	if body.Prompt == "" {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "Prompt is required")
		return
	}

	structuredRequested := strings.EqualFold(strings.TrimSpace(body.ResponseFormat), "json_object") ||
		len(strings.TrimSpace(string(body.JsonSchema))) > 0
	trimmedSchema := strings.TrimSpace(string(body.JsonSchema))
	if structuredRequested && trimmedSchema == "" {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "jsonSchema is required for structured output")
		return
	}
	retryProfile := strings.TrimSpace(body.RetryProfile)

	policy := llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier:   body.Tier,
		TaskType:        body.TaskType,
		Structured:      structuredRequested,
		HighValue:       structuredRequested,
		LatencyBudgetMs: body.RoutingIntent.LatencyBudgetMs,
	})
	requestedTier := policy.InitialTier
	// Set an outer deadline on the request context that matches the policy.
	// During cold start this is ColdStartOuterDeadline (90s); otherwise
	// DefaultProviderOuterDeadline (55s). This ensures we always write a
	// response before any upstream proxy times out.
	ctx, cancel := context.WithTimeout(r.Context(), policy.OuterDeadline)
	defer cancel()
	logger := telemetry.FromCtx(ctx)
	fallbackChain := strings.Join(policy.FallbackChain, " -> ")
	var structuredClient structuredOutputClient
	if structuredRequested {
		var ok bool
		structuredClient, ok = h.llmClient.(structuredOutputClient)
		if !ok || structuredClient == nil {
			writeGenerateError(w, http.StatusServiceUnavailable, generateErrPermanent, "Structured generation backend unavailable")
			return
		}
	}
	if retryProfile == "" {
		retryProfile = string(policy.RetryProfile)
	}

	logger.InfoContext(ctx, "llm generate request accepted",
		"component", "api.llm",
		"operation", "handle_generate",
		"stage", "request_start",
		"requested_tier", requestedTier,
		"request_class", string(policy.RequestClass),
		"retry_profile", string(policy.RetryProfile),
		"fallback_chain", fallbackChain,
		"service_tier", policy.ServiceTier,
		"thinking_budget", policy.ThinkingBudget,
		"task_type", body.TaskType,
		"structured_requested", structuredRequested,
		"response_format", strings.TrimSpace(body.ResponseFormat),
		"transport_timeout_ms", policy.TransportTimeout.Milliseconds(),
		"latency_budget_ms", policy.LatencyBudgetMs,
		"outer_deadline_ms", policy.OuterDeadline.Milliseconds(),
		"cold_start", policy.ColdStart,
		"process_uptime_ms", llm.ProcessUptimeMs(),
		"max_tokens", body.MaxTokens,
	)

	var lastError error
	var lastKind generateErrorKind
	providerDeadline := time.Now().Add(time.Duration(policy.LatencyBudgetMs) * time.Millisecond)

	for i, tier := range policy.FallbackChain {
		if ctx.Err() != nil {
			lastError = fmt.Errorf("request deadline exceeded after %d tier attempt(s): %w", i, ctx.Err())
			lastKind = generateErrTimeout
			break
		}

		remainingBudget := time.Until(providerDeadline)
		if remainingBudget <= 0 {
			lastError = fmt.Errorf("provider transport budget exhausted after %d tier attempt(s)", i)
			lastKind = generateErrTimeout
			break
		}

		model := h.resolveModel(tier)
		logger.InfoContext(ctx, "llm generate attempt start",
			"component", "api.llm",
			"operation", "handle_generate",
			"stage", "attempt_start",
			"attempt", i+1,
			"transport_timeout_ms", remainingBudget.Milliseconds(),
			"latency_budget_ms", policy.LatencyBudgetMs,
			"selected_tier", tier,
			"model", model,
			"request_class", string(policy.RequestClass),
			"retry_profile", retryProfile,
			"service_tier", policy.ServiceTier,
			"thinking_budget", policy.ThinkingBudget,
			"structured_requested", structuredRequested,
		)
		attemptCtx, attemptCancel := context.WithTimeout(ctx, remainingBudget)
		var (
			respText  string
			respModel string
			err       error
		)
		if structuredRequested {
			req := &llmv1.StructuredRequest{
				Prompt:          body.Prompt,
				SystemPrompt:    body.SystemPrompt,
				JsonSchema:      trimmedSchema,
				Model:           model,
				MaxTokens:       int32(body.MaxTokens),
				Temperature:     body.Temperature,
				ServiceTier:     policy.ServiceTier,
				RetryProfile:    retryProfile,
				RequestClass:    string(policy.RequestClass),
				LatencyBudgetMs: int32(remainingBudget.Milliseconds()),
			}
			if policy.ThinkingBudget != nil {
				req.ThinkingBudget = policy.ThinkingBudget
			}
			resp, structuredErr := structuredClient.StructuredOutput(attemptCtx, req)
			attemptCancel()
			if structuredErr == nil && resp != nil && (!resp.SchemaValid || strings.TrimSpace(resp.Error) != "") {
				structuredErr = fmt.Errorf("structured output invalid: %s", strings.TrimSpace(resp.Error))
			}
			if structuredErr == nil && resp != nil {
				respText = resp.JsonResult
				respModel = resp.ModelUsed
				if strings.TrimSpace(respText) == "" {
					structuredErr = fmt.Errorf("structured output returned empty text")
				}
			}
			err = structuredErr
		} else {
			req := &llmv1.GenerateRequest{
				Prompt:          body.Prompt,
				SystemPrompt:    body.SystemPrompt,
				Model:           model,
				MaxTokens:       int32(body.MaxTokens),
				Temperature:     body.Temperature,
				ServiceTier:     policy.ServiceTier,
				RetryProfile:    retryProfile,
				RequestClass:    string(policy.RequestClass),
				LatencyBudgetMs: int32(remainingBudget.Milliseconds()),
			}
			if policy.ThinkingBudget != nil {
				req.ThinkingBudget = policy.ThinkingBudget
			}
			resp, generateErr := h.llmClient.Generate(attemptCtx, req)
			attemptCancel()
			if generateErr == nil && resp != nil {
				respText = resp.Text
				respModel = resp.ModelUsed
				if strings.TrimSpace(respText) == "" {
					generateErr = fmt.Errorf("llm generate returned empty text")
				}
			}
			err = generateErr
		}

		if err == nil {
			logger.InfoContext(ctx, "llm generate attempt success",
				"component", "api.llm",
				"operation", "handle_generate",
				"stage", "attempt_success",
				"attempt", i+1,
				"transport_timeout_ms", remainingBudget.Milliseconds(),
				"selected_tier", tier,
				"model", respModel,
				"structured_requested", structuredRequested,
				"provider_result", "success",
			)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text":            respText,
				"model":           respModel,
				"selectedTier":    tier,
				"fallbackApplied": i > 0,
				"requestedTier":   requestedTier,
			})
			return
		}

		lastError = err
		lastKind = classifyLLMError(err)
		logger.WarnContext(ctx, "llm generate attempt failed",
			"component", "api.llm",
			"operation", "handle_generate",
			"stage", "attempt_failed",
			"attempt", i+1,
			"transport_timeout_ms", remainingBudget.Milliseconds(),
			"selected_tier", tier,
			"model", model,
			"error_kind", lastKind,
			"error_code", string(lastKind),
			"error", err.Error(),
			"structured_requested", structuredRequested,
			"provider_result", "failure",
		)

		if lastKind == generateErrPermanent || lastKind == generateErrTimeout {
			break
		}
	}

	// Return a structured JSON error body so the frontend can classify it.
	httpStatus := http.StatusBadGateway
	if lastKind == generateErrTimeout {
		httpStatus = http.StatusGatewayTimeout
	} else if lastKind == generateErrRateLimit {
		httpStatus = http.StatusTooManyRequests
	}
	logger.WarnContext(ctx, "llm generate request failed",
		"component", "api.llm",
		"operation", "handle_generate",
		"stage", "request_failed",
		"requested_tier", requestedTier,
		"request_class", string(policy.RequestClass),
		"fallback_chain", fallbackChain,
		"service_tier", policy.ServiceTier,
		"thinking_budget", policy.ThinkingBudget,
		"latency_budget_ms", policy.LatencyBudgetMs,
		"cold_start", policy.ColdStart,
		"process_uptime_ms", llm.ProcessUptimeMs(),
		"error_kind", lastKind,
		"error_code", string(lastKind),
		"error", fmt.Sprint(lastError),
	)
	writeGenerateError(w, httpStatus, lastKind, fmt.Sprintf("Generation failed: %v", lastError))
}

func (h *LLMHandler) HandleEmbed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeGenerateError(w, http.StatusMethodNotAllowed, generateErrPermanent, "Method not allowed")
		return
	}

	embedder, ok := h.llmClient.(embedClient)
	if !ok || embedder == nil {
		writeGenerateError(w, http.StatusServiceUnavailable, generateErrTransient, "Embedding backend unavailable")
		return
	}

	var body struct {
		Text            string            `json:"text"`
		Model           string            `json:"model"`
		TaskType        string            `json:"taskType"`
		LatencyBudgetMs int32             `json:"latencyBudgetMs"`
		Metadata        map[string]string `json:"metadata"`
		Content         struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "Invalid request body")
		return
	}

	text := strings.TrimSpace(body.Text)
	if text == "" && strings.EqualFold(body.Content.Kind, "text") {
		text = strings.TrimSpace(body.Content.Text)
	}
	if text == "" {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "Text is required")
		return
	}

	resp, err := embedder.Embed(r.Context(), &llmv1.EmbedRequest{
		Text:            text,
		Model:           strings.TrimSpace(body.Model),
		TaskType:        strings.TrimSpace(body.TaskType),
		LatencyBudgetMs: body.LatencyBudgetMs,
		Metadata:        body.Metadata,
	})
	if err != nil {
		kind := classifyLLMError(err)
		status := http.StatusBadGateway
		switch kind {
		case generateErrRateLimit:
			status = http.StatusTooManyRequests
		case generateErrTimeout:
			status = http.StatusGatewayTimeout
		case generateErrPermanent:
			status = http.StatusBadRequest
		}
		writeGenerateError(w, status, kind, fmt.Sprintf("Embedding failed: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"embedding":  resp.Embedding,
		"tokenCount": resp.TokenCount,
		"modelUsed":  resp.ModelUsed,
		"latencyMs":  resp.LatencyMs,
	})
}

func (h *LLMHandler) HandleEmbedBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeGenerateError(w, http.StatusMethodNotAllowed, generateErrPermanent, "Method not allowed")
		return
	}

	embedder, ok := h.llmClient.(embedBatchClient)
	if !ok || embedder == nil {
		writeGenerateError(w, http.StatusServiceUnavailable, generateErrTransient, "Embedding backend unavailable")
		return
	}

	var body struct {
		Texts           []string          `json:"texts"`
		Model           string            `json:"model"`
		TaskType        string            `json:"taskType"`
		LatencyBudgetMs int32             `json:"latencyBudgetMs"`
		Metadata        map[string]string `json:"metadata"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "Invalid request body")
		return
	}
	if len(body.Texts) == 0 {
		writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "Texts are required")
		return
	}

	texts := make([]string, 0, len(body.Texts))
	for _, text := range body.Texts {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			writeGenerateError(w, http.StatusBadRequest, generateErrPermanent, "Texts must not be blank")
			return
		}
		texts = append(texts, trimmed)
	}

	resp, err := embedder.EmbedBatch(r.Context(), &llmv1.EmbedBatchRequest{
		Texts:           texts,
		Model:           strings.TrimSpace(body.Model),
		TaskType:        strings.TrimSpace(body.TaskType),
		LatencyBudgetMs: body.LatencyBudgetMs,
		Metadata:        body.Metadata,
	})
	if err != nil {
		kind := classifyLLMError(err)
		status := http.StatusBadGateway
		switch kind {
		case generateErrRateLimit:
			status = http.StatusTooManyRequests
		case generateErrTimeout:
			status = http.StatusGatewayTimeout
		case generateErrPermanent:
			status = http.StatusBadRequest
		}
		writeGenerateError(w, status, kind, fmt.Sprintf("Batch embedding failed: %v", err))
		return
	}

	embeddings := make([]map[string]any, 0, len(resp.Embeddings))
	for _, vector := range resp.Embeddings {
		embeddings = append(embeddings, map[string]any{
			"values":     vector.GetValues(),
			"tokenCount": vector.GetTokenCount(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"embeddings": embeddings,
		"modelUsed":  resp.ModelUsed,
		"latencyMs":  resp.LatencyMs,
	})
}

// resolveModel maps a tier name to a model ID using the canonical config in
// wisdev_models.json via llm.Resolve*Model(). This is the single authoritative
// path — model IDs must not be hardcoded anywhere else in the API layer.
func (h *LLMHandler) resolveModel(tier string) string {
	switch strings.ToLower(tier) {
	case "heavy":
		return llm.ResolveHeavyModel()
	case "light":
		return llm.ResolveLightModel()
	case "standard", "balanced", "default", "":
		return llm.ResolveStandardModel()
	default:
		return llm.ResolveStandardModel()
	}
}
