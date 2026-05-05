package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
)

type modelsClient interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
	EmbedContent(ctx context.Context, model string, contents []*genai.Content, config *genai.EmbedContentConfig) (*genai.EmbedContentResponse, error)
	GenerateImages(ctx context.Context, model string, prompt string, config *genai.GenerateImagesConfig) (*genai.GenerateImagesResponse, error)
}

// VertexClient wraps the Google GenAI Go SDK.
type VertexClient struct {
	client           modelsClient
	backend          string
	credentialSource string
}

const stableGenAIAPIVersion = "v1"

const vertexGenerateContentMaxConcurrency = 2

var vertexGenerateContentSlots = make(chan struct{}, vertexGenerateContentMaxConcurrency)

const vertexProviderRateLimitBackoff = 60 * time.Second

var (
	vertexProviderRateLimitMu    sync.Mutex
	vertexProviderRateLimitUntil time.Time
	vertexProviderCooldownLogMu  sync.Mutex
	vertexProviderCooldownLogAt  = map[string]time.Time{}
)

var newModelsClient = func(ctx context.Context, cfg *genai.ClientConfig) (modelsClient, error) {
	client, err := genai.NewClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return client.Models, nil
}

var resolveProjectID = resilience.ResolveGoogleCloudProjectIDWithSource
var resolveSecret = resilience.GetSecretFromManager

// NewVertexClient creates a GenAI client. It prefers Vertex AI when a project
// is configured, then falls back to a Secret Manager-backed Gemini API key for
// local development environments where ADC is incomplete.
func NewVertexClient(ctx context.Context, projectID, location string) (*VertexClient, error) {
	projectID = strings.TrimSpace(projectID)
	projectSource := "argument"
	if projectID == "" {
		projectID, projectSource = resolveProjectID(ctx)
	}
	if location == "" {
		location = "us-central1"
	}

	if projectID != "" {
		slog.Info("initializing google genai client", "preferred_backend", "vertex_ai", "project_id", projectID, "project_source", projectSource, "location", location)
		vertexCfg := &genai.ClientConfig{
			Project:  projectID,
			Location: location,
			Backend:  genai.BackendVertexAI,
			HTTPOptions: genai.HTTPOptions{
				APIVersion: stableGenAIAPIVersion,
			},
		}
		client, err := newModelsClient(ctx, vertexCfg)
		if err == nil {
			return &VertexClient{
				client:           client,
				backend:          "vertex_ai",
				credentialSource: "vertex_ai:" + projectSource,
			}, nil
		}
		slog.Warn("vertex ai client initialization failed", "project_id", projectID, "project_source", projectSource, "location", location, "error", err)

		apiKey, apiKeySource, keyErr := ResolveGoogleAPIKey(ctx, projectID)
		if keyErr == nil && apiKey != "" {
			slog.Warn("attempting google genai fallback backend", "fallback_backend", "gemini_api_secret", "project_id", projectID, "api_key_source", apiKeySource)
			fallbackClient, fallbackErr := newModelsClient(ctx, &genai.ClientConfig{
				APIKey:  apiKey,
				Backend: genai.BackendGeminiAPI,
				HTTPOptions: genai.HTTPOptions{
					APIVersion: stableGenAIAPIVersion,
				},
			})
			if fallbackErr == nil {
				slog.Warn("using google genai fallback backend", "backend", "gemini_api_secret", "project_id", projectID, "api_key_source", apiKeySource)
				return &VertexClient{
					client:           fallbackClient,
					backend:          "gemini_api_secret",
					credentialSource: apiKeySource,
				}, nil
			}
			slog.Error("google genai fallback backend failed", "backend", "gemini_api_secret", "project_id", projectID, "api_key_source", apiKeySource, "error", fallbackErr)

			return nil, fmt.Errorf(
				"failed to create Vertex AI genai client: %w; GOOGLE_API_KEY fallback failed: %v",
				err,
				fallbackErr,
			)
		}

		if keyErr != nil {
			slog.Warn("google genai fallback secret unavailable", "project_id", projectID, "project_source", projectSource, "error", keyErr)
			return nil, fmt.Errorf(
				"failed to create Vertex AI genai client: %w; GOOGLE_API_KEY fallback unavailable: %v",
				err,
				keyErr,
			)
		}

		slog.Error("google genai fallback secret missing", "project_id", projectID, "project_source", projectSource, "fallback_secrets", []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"})
		return nil, fmt.Errorf("failed to create Vertex AI genai client: %w", err)
	}

	slog.Info("initializing google genai client", "preferred_backend", "gemini_api_secret", "project_source", projectSource, "location", location)
	apiKey, apiKeySource, err := ResolveGoogleAPIKey(ctx, projectID)
	if err != nil {
		slog.Error("google genai api key resolution failed", "project_source", projectSource, "error", err)
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}
	if apiKey == "" {
		slog.Error("google genai configuration missing", "project_source", projectSource, "fallback_secrets", []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"})
		return nil, fmt.Errorf("failed to create genai client: GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT is not set and no GOOGLE_API_KEY or GEMINI_API_KEY secret was available")
	}

	client, err := newModelsClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{
			APIVersion: stableGenAIAPIVersion,
		},
	})
	if err != nil {
		slog.Error("gemini api client initialization failed", "backend", "gemini_api_secret", "api_key_source", apiKeySource, "error", err)
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}

	return &VertexClient{
		client:           client,
		backend:          "gemini_api_secret",
		credentialSource: apiKeySource,
	}, nil
}

func (v *VertexClient) BackendName() string {
	if v == nil || strings.TrimSpace(v.backend) == "" {
		return "vertex_ai"
	}
	return v.backend
}

func (v *VertexClient) CredentialSource() string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(v.credentialSource)
}

func ResolveGoogleAPIKey(ctx context.Context, projectID string) (string, string, error) {
	var firstErr error

	if strings.TrimSpace(projectID) != "" {
		for _, secretName := range []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"} {
			value, err := resolveSecret(ctx, secretName)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed, "secret_manager:" + secretName, nil
			}
		}
	}

	for _, envName := range []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"} {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			return value, "env:" + envName, nil
		}
	}

	return "", "", firstErr
}

// GenerateText generates a text completion using Gemini.
// Retries once on transient failures with jitter backoff, respecting context cancellation.
// Applies thinking budget when a supported model is detected (Gemini 2.5+/3+).
func (v *VertexClient) GenerateText(ctx context.Context, modelID, prompt, systemPrompt string, temperature float32, maxTokens int32) (string, error) {
	if modelID == "" {
		modelID = ResolveStandardModel()
	}

	config := &genai.GenerateContentConfig{
		Temperature:     &temperature,
		MaxOutputTokens: maxTokens,
	}
	// Apply a conservative thinking budget for text generation to avoid
	// unbounded thinking time that causes timeouts. Light models get 0
	// (disabled), standard models get a bounded budget.
	textThinkingBudget := defaultTextThinkingBudget(modelID)
	if tc := thinkingConfigForModel(modelID, textThinkingBudget); tc != nil {
		config.ThinkingConfig = tc
	}

	if systemPrompt != "" {
		config.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: systemPrompt}},
		}
	}

	contents := []*genai.Content{
		{
			Role:  "user",
			Parts: []*genai.Part{{Text: prompt}},
		},
	}

	sleepOrCancel := func(delay time.Duration) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-time.After(delay):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	const maxAttempts = 2
	var lastErr error
	startedAt := time.Now()
	for attempt := range maxAttempts {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if remaining := vertexProviderRateLimitRemaining(time.Now()); remaining > 0 {
			err := fmt.Errorf("vertex text generation rate limited; retry after %s", remaining.Round(time.Millisecond))
			if shouldLogVertexProviderCooldownSkip("generate_text", modelID, "", v.backend, time.Now()) {
				slog.Warn("vertex text generation skipped during provider cooldown",
					"component", "llm.vertex",
					"operation", "generate_text",
					"stage", "provider_cooldown",
					"model", modelID,
					"attempt", attempt+1,
					"retry_after_ms", remaining.Milliseconds(),
					"backend", v.backend,
				)
			}
			return "", err
		}
		attemptStart := time.Now()
		ctxRemaining := int64(-1)
		if dl, ok := ctx.Deadline(); ok {
			ctxRemaining = time.Until(dl).Milliseconds()
			if ctxRemaining <= 0 {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return "", ctxErr
				}
				return "", context.DeadlineExceeded
			}
		}
		slog.Info("vertex text generation attempt",
			"component", "llm.vertex",
			"operation", "generate_text",
			"stage", "attempt_start",
			"model", modelID,
			"attempt", attempt+1,
			"thinking_budget", textThinkingBudget,
			"ctx_deadline_set", ctxRemaining >= 0,
			"ctx_remaining_ms", ctxRemaining,
			"elapsed_ms", time.Since(startedAt).Milliseconds(),
			"cold_start", IsColdStartWindow(),
			"process_uptime_ms", ProcessUptimeMs(),
			"backend", v.backend,
		)
		releaseSlot, limiterWait, limiterErr := acquireVertexGenerateContentSlot(ctx)
		if limiterErr != nil {
			return "", limiterErr
		}
		if remaining := vertexProviderRateLimitRemaining(time.Now()); remaining > 0 {
			releaseSlot()
			err := fmt.Errorf("vertex text generation rate limited; retry after %s", remaining.Round(time.Millisecond))
			if shouldLogVertexProviderCooldownSkip("generate_text", modelID, "", v.backend, time.Now()) {
				slog.Warn("vertex text generation skipped during provider cooldown",
					"component", "llm.vertex",
					"operation", "generate_text",
					"stage", "provider_cooldown",
					"model", modelID,
					"attempt", attempt+1,
					"retry_after_ms", remaining.Milliseconds(),
					"limiter_wait_ms", limiterWait.Milliseconds(),
					"backend", v.backend,
				)
			}
			return "", err
		}
		var resp *genai.GenerateContentResponse
		var err error
		func() {
			defer releaseSlot()
			resp, err = v.client.GenerateContent(ctx, modelID, contents, config)
		}()
		attemptLatencyMs := time.Since(attemptStart).Milliseconds()
		if err != nil {
			lastErr = fmt.Errorf("generate content failed: %w", err)
			errClass := classifyVertexError(err)
			slog.Warn("vertex text generation attempt failed",
				"component", "llm.vertex",
				"operation", "generate_text",
				"stage", "attempt_failed",
				"model", modelID,
				"attempt", attempt+1,
				"attempt_latency_ms", attemptLatencyMs,
				"limiter_wait_ms", limiterWait.Milliseconds(),
				"error_class", errClass,
				"error", err.Error(),
			)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", lastErr
			}
			if remaining, opened := recordVertexProviderRateLimitIfNeeded(errClass, time.Now()); opened {
				slog.Warn("vertex text generation provider cooldown opened",
					"component", "llm.vertex",
					"operation", "generate_text",
					"stage", "provider_cooldown_opened",
					"model", modelID,
					"retry_after_ms", remaining.Milliseconds(),
					"backend", v.backend,
				)
				break
			}
			if !isRetryableVertexErrorClass(errClass) {
				break
			}
			if attempt < maxAttempts-1 {
				retryDelay := vertexRetryDelay(errClass)
				if ok, retryRemainingMs := hasVertexRetryBudget(ctx, retryDelay); !ok {
					slog.Warn("vertex text generation retry skipped",
						"component", "llm.vertex",
						"operation", "generate_text",
						"stage", "retry_budget_exhausted",
						"model", modelID,
						"attempt", attempt+1,
						"error_class", errClass,
						"retry_delay_ms", retryDelay.Milliseconds(),
						"ctx_remaining_ms", retryRemainingMs,
						"backend", v.backend,
					)
					break
				}
				if sleepErr := sleepOrCancel(retryDelay); sleepErr != nil {
					return "", sleepErr
				}
			}
			continue
		}

		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
			lastErr = fmt.Errorf("no candidates returned (model may have blocked output)")
			if attempt < maxAttempts-1 {
				retryDelay := vertexRetryDelay("empty_candidates")
				if ok, retryRemainingMs := hasVertexRetryBudget(ctx, retryDelay); !ok {
					slog.Warn("vertex text generation retry skipped",
						"component", "llm.vertex",
						"operation", "generate_text",
						"stage", "retry_budget_exhausted",
						"model", modelID,
						"attempt", attempt+1,
						"error_class", "empty_candidates",
						"retry_delay_ms", retryDelay.Milliseconds(),
						"ctx_remaining_ms", retryRemainingMs,
						"backend", v.backend,
					)
					break
				}
				if sleepErr := sleepOrCancel(retryDelay); sleepErr != nil {
					return "", sleepErr
				}
			}
			continue
		}

		var textBuilder strings.Builder
		for _, part := range resp.Candidates[0].Content.Parts {
			textBuilder.WriteString(part.Text)
		}
		text := strings.TrimSpace(textBuilder.String())
		if text == "" {
			lastErr = fmt.Errorf("vertex text generation returned empty text")
			if attempt < maxAttempts-1 {
				retryDelay := vertexRetryDelay("empty_text")
				if ok, retryRemainingMs := hasVertexRetryBudget(ctx, retryDelay); !ok {
					slog.Warn("vertex text generation retry skipped",
						"component", "llm.vertex",
						"operation", "generate_text",
						"stage", "retry_budget_exhausted",
						"model", modelID,
						"attempt", attempt+1,
						"error_class", "empty_text",
						"retry_delay_ms", retryDelay.Milliseconds(),
						"ctx_remaining_ms", retryRemainingMs,
						"backend", v.backend,
					)
					break
				}
				if sleepErr := sleepOrCancel(retryDelay); sleepErr != nil {
					return "", sleepErr
				}
			}
			continue
		}
		return text, nil
	}
	return "", lastErr
}

// defaultTextThinkingBudget returns a bounded thinking budget for text
// generation. This prevents the model from spending unbounded thinking
// time on simple text completions, which is the primary cause of timeouts.
func defaultTextThinkingBudget(modelID string) *int32 {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.HasPrefix(normalized, "gemini-2.5-flash-lite"),
		strings.HasPrefix(normalized, "gemini-3") && strings.Contains(normalized, "lite"):
		v := int32(0) // disable thinking for lite models
		return &v
	case strings.HasPrefix(normalized, "gemini-2.5-flash"),
		strings.HasPrefix(normalized, "gemini-3") && strings.Contains(normalized, "flash"):
		v := int32(1024) // bounded thinking for flash
		return &v
	case strings.HasPrefix(normalized, "gemini-2.5-pro"),
		strings.HasPrefix(normalized, "gemini-3") && strings.Contains(normalized, "pro"):
		v := int32(2048) // moderate thinking for pro
		return &v
	}
	return nil // no thinking config for unknown models
}

// EmbedText generates an embedding for a single text.
func (v *VertexClient) EmbedText(ctx context.Context, modelID, text, taskType string) ([]float32, error) {
	if modelID == "" {
		modelID = "text-embedding-005"
	}

	config := &genai.EmbedContentConfig{
		TaskType: taskType,
	}

	contents := []*genai.Content{
		{
			Parts: []*genai.Part{{Text: text}},
		},
	}

	res, err := v.client.EmbedContent(ctx, modelID, contents, config)
	if err != nil {
		return nil, fmt.Errorf("embed content failed: %w", err)
	}

	if len(res.Embeddings) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return res.Embeddings[0].Values, nil
}

// EmbedBatch generates embeddings for a batch of texts.
func (v *VertexClient) EmbedBatch(ctx context.Context, modelID string, texts []string, taskType string) ([][]float32, error) {
	if modelID == "" {
		modelID = "text-embedding-005"
	}

	config := &genai.EmbedContentConfig{
		TaskType: taskType,
	}

	var contents []*genai.Content
	for _, t := range texts {
		contents = append(contents, &genai.Content{
			Parts: []*genai.Part{{Text: t}},
		})
	}

	res, err := v.client.EmbedContent(ctx, modelID, contents, config)
	if err != nil {
		return nil, fmt.Errorf("batch embed content failed: %w", err)
	}

	var results [][]float32
	for _, e := range res.Embeddings {
		results = append(results, e.Values)
	}

	return results, nil
}

// generateStructuredWithTokens is the single canonical implementation of
// Gemini native controlled generation with response_mime_type + response_json_schema.
// It retries once on ALL failure modes with a 200–400ms jitter sleep (BUG5 fix:
// empty-candidates and invalid-JSON paths now also back off before retrying).
// ctx.Err() is checked at the top of each iteration and inside every sleep so
// cancellation is always caught promptly (BUG8 fix).
// Returns (jsonText, promptTokens, candidateTokens, error).
// GenerateStructured calls Gemini with native controlled generation using
// response_mime_type + response_json_schema. It is a thin wrapper around
// generateStructuredWithTokens for callers that only need the JSON string.
func (v *VertexClient) GenerateStructured(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32) (string, error) {
	result, _, _, err := v.generateStructuredWithTokens(
		ctx,
		modelID,
		prompt,
		systemPrompt,
		jsonSchemaStr,
		temperature,
		maxTokens,
		"",
		nil,
		string(RequestClassStandard),
		string(RetryProfileStandard),
	)
	return result, err
}

func (v *VertexClient) GenerateStructuredWithPolicy(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32, serviceTier string, thinkingBudget *int32, requestClass string, retryProfile string) (string, error) {
	result, _, _, err := v.generateStructuredWithTokens(ctx, modelID, prompt, systemPrompt, jsonSchemaStr, temperature, maxTokens, serviceTier, thinkingBudget, requestClass, retryProfile)
	return result, err
}

func thinkingConfigForModel(modelID string, thinkingBudget *int32) *genai.ThinkingConfig {
	if thinkingBudget == nil {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	if strings.HasPrefix(normalized, "gemini-3") {
		config := &genai.ThinkingConfig{}
		switch {
		case *thinkingBudget <= 0:
			config.ThinkingLevel = genai.ThinkingLevelMinimal
		case *thinkingBudget <= 1024:
			config.ThinkingLevel = genai.ThinkingLevelLow
		case *thinkingBudget <= 8192:
			config.ThinkingLevel = genai.ThinkingLevelMedium
		default:
			config.ThinkingLevel = genai.ThinkingLevelHigh
		}
		return config
	}
	if strings.HasPrefix(normalized, "gemini-2.5") {
		return &genai.ThinkingConfig{ThinkingBudget: thinkingBudget}
	}
	return nil
}

func prepareSchemaForVertex(raw any) any {
	switch node := raw.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(node)+1)
		for key, value := range node {
			normalized[key] = prepareSchemaForVertex(value)
		}
		if _, hasOrdering := normalized["propertyOrdering"]; !hasOrdering {
			if props, ok := normalized["properties"].(map[string]any); ok && len(props) > 0 {
				propertyOrdering := make([]string, 0, len(props))
				for key := range props {
					propertyOrdering = append(propertyOrdering, key)
				}
				sort.Strings(propertyOrdering)
				normalized["propertyOrdering"] = propertyOrdering
			}
		}
		return normalized
	case []any:
		normalized := make([]any, len(node))
		for idx, value := range node {
			normalized[idx] = prepareSchemaForVertex(value)
		}
		return normalized
	default:
		return raw
	}
}

func acquireVertexGenerateContentSlot(ctx context.Context) (func(), time.Duration, error) {
	startedAt := time.Now()
	select {
	case vertexGenerateContentSlots <- struct{}{}:
		return func() { <-vertexGenerateContentSlots }, time.Since(startedAt), nil
	case <-ctx.Done():
		return nil, time.Since(startedAt), ctx.Err()
	}
}

func vertexRetryDelay(errorClass string) time.Duration {
	switch errorClass {
	case "rate_limit":
		return time.Duration(1200+rand.Intn(800)) * time.Millisecond
	default:
		return time.Duration(200+rand.Intn(200)) * time.Millisecond
	}
}

func vertexProviderRateLimitRemaining(now time.Time) time.Duration {
	vertexProviderRateLimitMu.Lock()
	defer vertexProviderRateLimitMu.Unlock()
	if now.Before(vertexProviderRateLimitUntil) {
		return time.Until(vertexProviderRateLimitUntil)
	}
	return 0
}

// VertexProviderRateLimitRemaining reports the active process-wide Vertex
// cooldown. Callers that can use deterministic fallbacks should check this
// before starting optional LLM fan-out.
func VertexProviderRateLimitRemaining() time.Duration {
	return vertexProviderRateLimitRemaining(time.Now())
}

func shouldLogVertexProviderCooldownSkip(operation, modelID, requestClass, backend string, now time.Time) bool {
	key := strings.Join([]string{
		strings.TrimSpace(operation),
		strings.TrimSpace(modelID),
		strings.TrimSpace(requestClass),
		strings.TrimSpace(backend),
	}, "|")
	vertexProviderCooldownLogMu.Lock()
	defer vertexProviderCooldownLogMu.Unlock()
	if last, ok := vertexProviderCooldownLogAt[key]; ok && now.Sub(last) < 10*time.Second {
		return false
	}
	vertexProviderCooldownLogAt[key] = now
	return true
}

func recordVertexProviderRateLimit(now time.Time) time.Duration {
	until := now.Add(vertexProviderRateLimitBackoff)
	vertexProviderRateLimitMu.Lock()
	if until.After(vertexProviderRateLimitUntil) {
		vertexProviderRateLimitUntil = until
	}
	remaining := time.Until(vertexProviderRateLimitUntil)
	vertexProviderRateLimitMu.Unlock()
	return remaining
}

func recordVertexProviderRateLimitIfNeeded(errClass string, now time.Time) (time.Duration, bool) {
	if strings.TrimSpace(errClass) != "rate_limit" {
		return 0, false
	}
	return recordVertexProviderRateLimit(now), true
}

func resetVertexStructuredRateLimitForTest() {
	vertexProviderRateLimitMu.Lock()
	vertexProviderRateLimitUntil = time.Time{}
	vertexProviderRateLimitMu.Unlock()
	vertexProviderCooldownLogMu.Lock()
	vertexProviderCooldownLogAt = map[string]time.Time{}
	vertexProviderCooldownLogMu.Unlock()
}

func hasVertexRetryBudget(ctx context.Context, delay time.Duration) (bool, int64) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return true, -1
	}
	remaining := time.Until(deadline)
	return remaining > delay+time.Duration(minProviderLatencyBudgetMs)*time.Millisecond, remaining.Milliseconds()
}

func (v *VertexClient) generateStructuredWithTokens(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32, serviceTier string, thinkingBudget *int32, requestClass string, retryProfile string) (result string, inputTokens, outputTokens int32, err error) {
	if modelID == "" {
		modelID = ResolveStandardModel()
	}
	serviceTier = strings.TrimSpace(serviceTier)
	requestClass = strings.TrimSpace(requestClass)
	if requestClass == "" {
		requestClass = string(RequestClassStandard)
	}
	retryProfile = strings.TrimSpace(retryProfile)
	if retryProfile == "" {
		retryProfile = string(RetryProfileStandard)
	}
	var schemaValue any
	if jsonSchemaStr != "" {
		if parseErr := json.Unmarshal([]byte(jsonSchemaStr), &schemaValue); parseErr != nil {
			return "", 0, 0, fmt.Errorf("invalid json_schema: %w", parseErr)
		}
		schemaValue = prepareSchemaForVertex(schemaValue)
	}
	config := &genai.GenerateContentConfig{
		Temperature:        &temperature,
		MaxOutputTokens:    maxTokens,
		ResponseMIMEType:   "application/json",
		ResponseJsonSchema: schemaValue,
	}
	// The current Vertex GenerateContent path rejects serviceTier, so keep the
	// requested tier in policy/logging but do not forward it into the native SDK
	// request until the upstream API supports it again.
	if thinkingConfig := thinkingConfigForModel(modelID, thinkingBudget); thinkingConfig != nil {
		config.ThinkingConfig = thinkingConfig
	}
	if systemPrompt != "" {
		config.SystemInstruction = &genai.Content{Parts: []*genai.Part{{Text: systemPrompt}}}
	}
	contents := []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: prompt}}}}

	sleepOrCancel := func(delay time.Duration) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-time.After(delay):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	const maxAttempts = 2
	startedAt := time.Now()
	var lastErr error
	for attempt := range maxAttempts {
		if ctx.Err() != nil {
			return "", 0, 0, ctx.Err()
		}
		if remaining := vertexProviderRateLimitRemaining(time.Now()); remaining > 0 {
			err := fmt.Errorf("vertex structured output rate limited; retry after %s", remaining.Round(time.Millisecond))
			if shouldLogVertexProviderCooldownSkip("structured_output", modelID, requestClass, v.backend, time.Now()) {
				slog.Warn("vertex structured output skipped during provider cooldown",
					"model", modelID,
					"attempt", attempt+1,
					"request_class", requestClass,
					"retry_after_ms", remaining.Milliseconds(),
					"backend", v.backend,
				)
			}
			return "", 0, 0, err
		}
		attemptStart := time.Now()
		deadline, hasDeadline := ctx.Deadline()
		remainingMs := int64(-1)
		if hasDeadline {
			remainingMs = time.Until(deadline).Milliseconds()
			if remainingMs <= 0 {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return "", 0, 0, ctxErr
				}
				return "", 0, 0, context.DeadlineExceeded
			}
		}
		slog.Info("vertex structured output attempt",
			"model", modelID,
			"attempt", attempt+1,
			"max_attempts", maxAttempts,
			"service_tier", serviceTier,
			"thinking_budget", thinkingBudget,
			"request_class", requestClass,
			"retry_profile", retryProfile,
			"ctx_deadline_set", hasDeadline,
			"ctx_remaining_ms", remainingMs,
			"elapsed_ms", time.Since(startedAt).Milliseconds(),
			"backend", v.backend,
		)
		releaseSlot, limiterWait, limiterErr := acquireVertexGenerateContentSlot(ctx)
		if limiterErr != nil {
			return "", 0, 0, limiterErr
		}
		if remaining := vertexProviderRateLimitRemaining(time.Now()); remaining > 0 {
			releaseSlot()
			err := fmt.Errorf("vertex structured output rate limited; retry after %s", remaining.Round(time.Millisecond))
			if shouldLogVertexProviderCooldownSkip("structured_output", modelID, requestClass, v.backend, time.Now()) {
				slog.Warn("vertex structured output skipped during provider cooldown",
					"model", modelID,
					"attempt", attempt+1,
					"request_class", requestClass,
					"retry_after_ms", remaining.Milliseconds(),
					"limiter_wait_ms", limiterWait.Milliseconds(),
					"backend", v.backend,
				)
			}
			return "", 0, 0, err
		}
		var resp *genai.GenerateContentResponse
		var callErr error
		func() {
			defer releaseSlot()
			resp, callErr = v.client.GenerateContent(ctx, modelID, contents, config)
		}()
		attemptLatencyMs := time.Since(attemptStart).Milliseconds()
		if callErr != nil {
			lastErr = fmt.Errorf("generate structured content failed: %w", callErr)
			errorClass := classifyVertexError(callErr)
			slog.Warn("vertex structured output attempt failed",
				"model", modelID,
				"attempt", attempt+1,
				"error", callErr.Error(),
				"attempt_latency_ms", attemptLatencyMs,
				"limiter_wait_ms", limiterWait.Milliseconds(),
				"error_class", errorClass,
				"backend", v.backend,
			)
			if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
				return "", 0, 0, lastErr
			}
			if remaining, opened := recordVertexProviderRateLimitIfNeeded(errorClass, time.Now()); opened {
				slog.Warn("vertex structured output provider cooldown opened",
					"model", modelID,
					"request_class", requestClass,
					"retry_after_ms", remaining.Milliseconds(),
					"backend", v.backend,
				)
				break
			}
			if !isRetryableVertexErrorClass(errorClass) {
				break
			}
			if attempt < maxAttempts-1 {
				retryDelay := vertexRetryDelay(errorClass)
				if ok, retryRemainingMs := hasVertexRetryBudget(ctx, retryDelay); !ok {
					slog.Warn("vertex structured output retry skipped",
						"model", modelID,
						"attempt", attempt+1,
						"error_class", errorClass,
						"retry_delay_ms", retryDelay.Milliseconds(),
						"ctx_remaining_ms", retryRemainingMs,
						"backend", v.backend,
					)
					break
				}
				if sleepErr := sleepOrCancel(retryDelay); sleepErr != nil {
					return "", 0, 0, sleepErr
				}
			}
			continue
		}
		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
			lastErr = fmt.Errorf("no structured candidates returned (model may have blocked output)")
			slog.Warn("vertex structured output empty candidates",
				"model", modelID,
				"attempt", attempt+1,
				"attempt_latency_ms", attemptLatencyMs,
				"limiter_wait_ms", limiterWait.Milliseconds(),
				"backend", v.backend,
			)
			if attempt < maxAttempts-1 {
				retryDelay := vertexRetryDelay("empty_candidates")
				if ok, retryRemainingMs := hasVertexRetryBudget(ctx, retryDelay); !ok {
					slog.Warn("vertex structured output retry skipped",
						"model", modelID,
						"attempt", attempt+1,
						"error_class", "empty_candidates",
						"retry_delay_ms", retryDelay.Milliseconds(),
						"ctx_remaining_ms", retryRemainingMs,
						"backend", v.backend,
					)
					break
				}
				if sleepErr := sleepOrCancel(retryDelay); sleepErr != nil {
					return "", 0, 0, sleepErr
				}
			}
			continue
		}
		var sb strings.Builder
		for _, part := range resp.Candidates[0].Content.Parts {
			sb.WriteString(part.Text)
		}
		text := strings.TrimSpace(sb.String())
		if text == "" {
			lastErr = fmt.Errorf("gemini structured output returned empty text")
			if attempt < maxAttempts-1 {
				retryDelay := vertexRetryDelay("empty_text")
				if ok, retryRemainingMs := hasVertexRetryBudget(ctx, retryDelay); !ok {
					slog.Warn("vertex structured output retry skipped",
						"model", modelID,
						"attempt", attempt+1,
						"error_class", "empty_text",
						"retry_delay_ms", retryDelay.Milliseconds(),
						"ctx_remaining_ms", retryRemainingMs,
						"backend", v.backend,
					)
					break
				}
				if sleepErr := sleepOrCancel(retryDelay); sleepErr != nil {
					return "", 0, 0, sleepErr
				}
			}
			continue
		}
		if !json.Valid([]byte(text)) {
			lastErr = fmt.Errorf("gemini structured output is not valid JSON: %.200s", text)
			if attempt < maxAttempts-1 {
				retryDelay := vertexRetryDelay("invalid_json")
				if ok, retryRemainingMs := hasVertexRetryBudget(ctx, retryDelay); !ok {
					slog.Warn("vertex structured output retry skipped",
						"model", modelID,
						"attempt", attempt+1,
						"error_class", "invalid_json",
						"retry_delay_ms", retryDelay.Milliseconds(),
						"ctx_remaining_ms", retryRemainingMs,
						"backend", v.backend,
					)
					break
				}
				if sleepErr := sleepOrCancel(retryDelay); sleepErr != nil {
					return "", 0, 0, sleepErr
				}
			}
			continue
		}
		var inTok, outTok int32
		if resp.UsageMetadata != nil {
			inTok = resp.UsageMetadata.PromptTokenCount
			outTok = resp.UsageMetadata.CandidatesTokenCount
		}
		slog.Info("vertex structured output success",
			"model", modelID,
			"attempt", attempt+1,
			"attempt_latency_ms", attemptLatencyMs,
			"total_latency_ms", time.Since(startedAt).Milliseconds(),
			"input_tokens", inTok,
			"output_tokens", outTok,
			"result_bytes", len(text),
			"limiter_wait_ms", limiterWait.Milliseconds(),
			"backend", v.backend,
		)
		return text, inTok, outTok, nil
	}
	slog.Error("vertex structured output exhausted all attempts",
		"model", modelID,
		"max_attempts", maxAttempts,
		"total_latency_ms", time.Since(startedAt).Milliseconds(),
		"last_error", lastErr.Error(),
		"backend", v.backend,
	)
	return "", 0, 0, lastErr
}

// classifyVertexError categorizes a Vertex API error for structured logging.
func classifyVertexError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "parameter is not supported in vertex ai") ||
		strings.Contains(msg, "extra inputs are not permitted") ||
		strings.Contains(msg, "unexpected keyword argument"):
		return "unsupported_parameter"
	case strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "context canceled"):
		return "timeout"
	case strings.Contains(msg, "429") || strings.Contains(msg, "resource exhausted") ||
		strings.Contains(msg, "rate limit") || strings.Contains(msg, "rate_limit"):
		return "rate_limit"
	case strings.Contains(msg, "503") || strings.Contains(msg, "unavailable"):
		return "unavailable"
	case strings.Contains(msg, "400") || strings.Contains(msg, "invalid"):
		return "invalid_request"
	default:
		return "unknown"
	}
}

func isRetryableVertexErrorClass(errClass string) bool {
	switch strings.TrimSpace(errClass) {
	case "timeout", "rate_limit", "unavailable", "unknown":
		return true
	default:
		return false
	}
}

func (v *VertexClient) GenerateImages(ctx context.Context, modelID, prompt string, count int, aspectRatio string) ([]genai.Image, error) {
	if modelID == "" {
		modelID = "imagen-3.0-generate-001"
	}

	config := &genai.GenerateImagesConfig{
		NumberOfImages: int32(count),
		AspectRatio:    aspectRatio,
	}

	resp, err := v.client.GenerateImages(ctx, modelID, prompt, config)
	if err != nil {
		return nil, fmt.Errorf("generate images failed: %w", err)
	}

	var results []genai.Image
	for _, img := range resp.GeneratedImages {
		if img.Image != nil {
			results = append(results, *img.Image)
		}
	}

	return results, nil
}
