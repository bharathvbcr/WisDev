package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/stackconfig"
)

const (
	openAIAPIStyle       = "openai"
	ollamaNativeAPIStyle = "ollama-native"
)

// OpenAICompatibleClient calls OpenAI-compatible chat completion endpoints such
// as Ollama's /v1/chat/completions or the native /api/chat API.
type OpenAICompatibleClient struct {
	openAIBaseURL string
	ollamaRootURL string
	apiStyle      string
	defaultModel  string
	apiKey        string
	httpClient    *http.Client
}

// NewOpenAICompatibleClient constructs a client for local or remote
// OpenAI-compatible inference servers.
func NewOpenAICompatibleClient(baseURL, defaultModel, apiKey string) *OpenAICompatibleClient {
	openAIBaseURL, ollamaRootURL, apiStyle := normalizeOpenAICompatibleBaseURL(baseURL)
	model := strings.TrimSpace(defaultModel)
	if model == "" {
		model = "llama3.1"
	}
	return &OpenAICompatibleClient{
		openAIBaseURL: openAIBaseURL,
		ollamaRootURL: ollamaRootURL,
		apiStyle:      apiStyle,
		defaultModel:  model,
		apiKey:        strings.TrimSpace(apiKey),
		httpClient:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// NewOpenAICompatibleClientFromEnv returns a configured client when
// WISDEV_LLM_BASE_URL is set, or nil when local OpenAI-compatible inference
// is not configured.
func NewOpenAICompatibleClientFromEnv() *OpenAICompatibleClient {
	baseURL, model, apiKey, ok := ResolveOpenAICompatibleConfig()
	if !ok {
		return nil
	}
	return NewOpenAICompatibleClient(baseURL, model, apiKey)
}

// ResolveOpenAICompatibleConfig reads canonical WisDev local LLM env vars.
func ResolveOpenAICompatibleConfig() (baseURL, model, apiKey string, ok bool) {
	provider := strings.ToLower(strings.TrimSpace(stackconfig.ResolveEnv("WISDEV_LLM_PROVIDER")))
	baseURL = strings.TrimSpace(stackconfig.ResolveEnv("WISDEV_LLM_BASE_URL"))
	model = strings.TrimSpace(stackconfig.ResolveEnvWithFallback("WISDEV_LLM_MODEL", "llama3.1"))
	apiKey = strings.TrimSpace(stackconfig.ResolveEnv("WISDEV_LLM_API_KEY"))

	switch provider {
	case "vertex", "gemini":
		return "", "", "", false
	case "ollama", "openai-compatible", "openai_compatible", "openai":
		if baseURL == "" {
			baseURL = "http://127.0.0.1:11434/v1"
		}
		return baseURL, model, apiKey, true
	}

	if baseURL == "" {
		return "", "", "", false
	}
	return baseURL, model, apiKey, true
}

func normalizeOpenAICompatibleBaseURL(raw string) (openAIBaseURL, ollamaRootURL, apiStyle string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", "", openAIAPIStyle
	}
	if strings.HasSuffix(raw, "/v1") {
		return raw, strings.TrimSuffix(raw, "/v1"), openAIAPIStyle
	}
	if strings.Contains(raw, ":11434") || strings.Contains(strings.ToLower(raw), "ollama") {
		return raw + "/v1", raw, ollamaNativeAPIStyle
	}
	return raw + "/v1", raw, openAIAPIStyle
}

func (c *OpenAICompatibleClient) BackendName() string {
	if c == nil {
		return ""
	}
	if c.looksLikeOllama() {
		return "ollama"
	}
	return "openai_compatible"
}

// looksLikeOllama reports whether the configured endpoint is an Ollama
// server, regardless of whether requests go through the OpenAI-compatible
// /v1 surface or the native API.
func (c *OpenAICompatibleClient) looksLikeOllama() bool {
	if c == nil {
		return false
	}
	if c.apiStyle == ollamaNativeAPIStyle {
		return true
	}
	root := strings.ToLower(c.ollamaRootURL)
	return strings.Contains(root, ":11434") || strings.Contains(root, "ollama")
}

func (c *OpenAICompatibleClient) CredentialSource() string {
	if c == nil {
		return ""
	}
	if c.apiKey != "" {
		return "env:WISDEV_LLM_API_KEY"
	}
	return "openai_compatible:" + c.openAIBaseURL
}

func (c *OpenAICompatibleClient) DefaultModel() string {
	if c == nil {
		return ""
	}
	return c.defaultModel
}

// HealthCheck probes the configured inference server.
func (c *OpenAICompatibleClient) HealthCheck(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("openai compatible client is not configured")
	}
	if c.apiStyle == ollamaNativeAPIStyle {
		return c.healthCheckOllama(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.openAIBaseURL+"/models", nil)
	if err != nil {
		return err
	}
	c.applyAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("openai compatible health check failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *OpenAICompatibleClient) healthCheckOllama(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ollamaRootURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ollama health check failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// ResolveLocalModelForTier maps canonical model tiers to local model env vars.
func ResolveLocalModelForTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "light":
		if model := strings.TrimSpace(stackconfig.ResolveEnv("WISDEV_LLM_MODEL_LIGHT")); model != "" {
			return model
		}
	case "heavy":
		if model := strings.TrimSpace(stackconfig.ResolveEnv("WISDEV_LLM_MODEL_HEAVY")); model != "" {
			return model
		}
	case "standard", "balanced", "":
		if model := strings.TrimSpace(stackconfig.ResolveEnv("WISDEV_LLM_MODEL_STANDARD")); model != "" {
			return model
		}
	}
	return strings.TrimSpace(stackconfig.ResolveEnvWithFallback("WISDEV_LLM_MODEL", "llama3.1"))
}

func inferLocalModelTierFromRequested(requested string) string {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "standard"
	}
	if requested == strings.TrimSpace(ResolveLightModel()) {
		return "light"
	}
	if requested == strings.TrimSpace(ResolveHeavyModel()) {
		return "heavy"
	}
	if requested == strings.TrimSpace(ResolveStandardModel()) {
		return "standard"
	}
	lower := strings.ToLower(requested)
	if strings.Contains(lower, "lite") || strings.Contains(lower, "flash-lite") {
		return "light"
	}
	if strings.Contains(lower, "pro") || strings.Contains(lower, "heavy") || strings.Contains(lower, "ultra") {
		return "heavy"
	}
	return "standard"
}

func (c *OpenAICompatibleClient) resolveModel(requested string) string {
	requested = strings.TrimSpace(requested)
	if requested != "" && !looksLikeManagedGeminiModel(requested) {
		return requested
	}
	if model := ResolveLocalModelForTier(inferLocalModelTierFromRequested(requested)); model != "" {
		return model
	}
	return c.defaultModel
}

// LiveModel resolves the model actually serving requests on a running
// inference server: the model currently loaded into Ollama memory when one
// is, otherwise the server catalog entry matching the configured model
// (e.g. configured "llama3.1" resolves to the live tag "llama3.1:8b").
func (c *OpenAICompatibleClient) LiveModel(ctx context.Context) (string, bool) {
	if c == nil {
		return "", false
	}
	if c.looksLikeOllama() {
		if name, ok := c.ollamaLoadedModel(ctx); ok {
			return name, true
		}
	}
	if ok, name := c.ModelAvailable(ctx); ok && strings.TrimSpace(name) != "" {
		return name, true
	}
	return "", false
}

// ollamaLoadedModel returns the first model currently loaded into memory on
// a running Ollama server (GET /api/ps), if any.
func (c *OpenAICompatibleClient) ollamaLoadedModel(ctx context.Context) (string, bool) {
	if c.ollamaRootURL == "" {
		return "", false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ollamaRootURL+"/api/ps", nil)
	if err != nil {
		return "", false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	var parsed struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", false
	}
	for _, model := range parsed.Models {
		if name := strings.TrimSpace(model.Name); name != "" {
			return name, true
		}
	}
	return "", false
}

// ModelAvailable reports whether the configured default model is present on the server.
func (c *OpenAICompatibleClient) ModelAvailable(ctx context.Context) (bool, string) {
	if c == nil {
		return false, "client not configured"
	}
	model := strings.TrimSpace(c.defaultModel)
	if model == "" {
		return false, "model not configured"
	}
	names, err := c.listModelNames(ctx)
	if err != nil {
		return false, err.Error()
	}
	prefix := strings.Split(model, ":")[0]
	for _, name := range names {
		if name == model || strings.HasPrefix(name, prefix+":") || strings.HasPrefix(name, prefix) {
			return true, name
		}
	}
	return false, "model " + model + " not found on server"
}

func (c *OpenAICompatibleClient) listModelNames(ctx context.Context) ([]string, error) {
	if c.apiStyle == ollamaNativeAPIStyle {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ollamaRootURL+"/api/tags", nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("ollama tags probe failed: HTTP %d", resp.StatusCode)
		}
		var parsed struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			return nil, err
		}
		names := make([]string, 0, len(parsed.Models))
		for _, model := range parsed.Models {
			if name := strings.TrimSpace(model.Name); name != "" {
				names = append(names, name)
			}
		}
		return names, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.openAIBaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai models probe failed: HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(parsed.Data))
	for _, model := range parsed.Data {
		if id := strings.TrimSpace(model.ID); id != "" {
			names = append(names, id)
		}
	}
	return names, nil
}

func looksLikeManagedGeminiModel(model string) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	if lower == "" {
		return false
	}
	return strings.HasPrefix(lower, "gemini") ||
		strings.HasPrefix(lower, "models/gemini") ||
		strings.Contains(lower, "gemini-")
}

func (c *OpenAICompatibleClient) generateStructuredWithTokens(
	ctx context.Context,
	modelID, prompt, systemPrompt, jsonSchemaStr string,
	temperature float32,
	maxTokens int32,
	_ string,
	_ *int32,
	requestClass string,
	_ string,
) (result string, inputTokens, outputTokens int32, err error) {
	if c == nil {
		return "", 0, 0, fmt.Errorf("openai compatible client is not configured")
	}
	modelID = c.resolveModel(modelID)
	if strings.TrimSpace(jsonSchemaStr) != "" {
		var schemaProbe json.RawMessage
		if parseErr := json.Unmarshal([]byte(jsonSchemaStr), &schemaProbe); parseErr != nil {
			return "", 0, 0, fmt.Errorf("invalid json_schema: %w", parseErr)
		}
	}
	if maxTokens <= 0 {
		maxTokens = defaultOutputTokens(2048)
	}
	maxTokens = liftStructuredOutputTokens(maxTokens)
	if temperature <= 0 {
		temperature = 0.2
	}

	startedAt := time.Now()
	var text string
	if c.apiStyle == ollamaNativeAPIStyle {
		text, inputTokens, outputTokens, err = c.generateStructuredOllamaNative(ctx, modelID, prompt, systemPrompt, jsonSchemaStr, temperature, maxTokens)
	} else {
		text, inputTokens, outputTokens, err = c.generateStructuredOpenAI(ctx, modelID, prompt, systemPrompt, jsonSchemaStr, temperature, maxTokens)
	}
	if err != nil {
		slog.Warn("openai compatible structured output failed",
			"component", "llm.openai_compatible",
			"operation", "structured_output",
			"model", modelID,
			"request_class", requestClass,
			"backend", c.BackendName(),
			"latency_ms", time.Since(startedAt).Milliseconds(),
			"error", err.Error(),
		)
		return "", 0, 0, err
	}
	if !json.Valid([]byte(text)) {
		return "", 0, 0, fmt.Errorf("openai compatible structured output is not valid JSON: %.200s", text)
	}
	if inputTokens == 0 {
		inputTokens = int32(len(prompt+systemPrompt) / 4)
	}
	if outputTokens == 0 {
		outputTokens = int32(len(text) / 4)
	}
	slog.Info("openai compatible structured output success",
		"component", "llm.openai_compatible",
		"operation", "structured_output",
		"model", modelID,
		"request_class", requestClass,
		"backend", c.BackendName(),
		"latency_ms", time.Since(startedAt).Milliseconds(),
		"input_tokens", inputTokens,
		"output_tokens", outputTokens,
		"result_bytes", len(text),
	)
	return text, inputTokens, outputTokens, nil
}

func (c *OpenAICompatibleClient) generateStructuredOpenAI(
	ctx context.Context,
	modelID, prompt, systemPrompt, jsonSchemaStr string,
	temperature float32,
	maxTokens int32,
) (string, int32, int32, error) {
	messages := make([]map[string]string, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": systemPrompt})
	}
	userPrompt := strings.TrimSpace(prompt)
	if strings.TrimSpace(jsonSchemaStr) != "" {
		userPrompt = userPrompt + "\n\nRespond with valid JSON that matches this schema:\n" + jsonSchemaStr
	}
	messages = append(messages, map[string]string{"role": "user", "content": userPrompt})

	payload := map[string]any{
		"model":       modelID,
		"messages":    messages,
		"temperature": temperature,
		"max_tokens":  maxTokens,
		"stream":      false,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.openAIBaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("openai compatible request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", 0, 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, 0, fmt.Errorf("openai compatible error: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int32 `json:"prompt_tokens"`
			CompletionTokens int32 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", 0, 0, fmt.Errorf("openai compatible response decode failed: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", 0, 0, fmt.Errorf("openai compatible response returned no choices")
	}
	text := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if text == "" {
		return "", 0, 0, fmt.Errorf("openai compatible structured output returned empty text")
	}
	return text, parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, nil
}

func (c *OpenAICompatibleClient) generateStructuredOllamaNative(
	ctx context.Context,
	modelID, prompt, systemPrompt, jsonSchemaStr string,
	temperature float32,
	maxTokens int32,
) (string, int32, int32, error) {
	messages := make([]map[string]string, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": systemPrompt})
	}
	userPrompt := strings.TrimSpace(prompt)
	if strings.TrimSpace(jsonSchemaStr) != "" {
		userPrompt = userPrompt + "\n\nRespond with valid JSON that matches this schema:\n" + jsonSchemaStr
	}
	messages = append(messages, map[string]string{"role": "user", "content": userPrompt})

	format := any("json")
	if strings.TrimSpace(jsonSchemaStr) != "" {
		var schemaObject map[string]any
		if err := json.Unmarshal([]byte(jsonSchemaStr), &schemaObject); err == nil && len(schemaObject) > 0 {
			format = schemaObject
		}
	}
	payload := map[string]any{
		"model":    modelID,
		"messages": messages,
		"stream":   false,
		"format":   format,
		"options": map[string]any{
			"temperature": temperature,
			"num_predict": maxTokens,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ollamaRootURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", 0, 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, 0, fmt.Errorf("ollama error: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		PromptEvalCount int32 `json:"prompt_eval_count"`
		EvalCount       int32 `json:"eval_count"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", 0, 0, fmt.Errorf("ollama response decode failed: %w", err)
	}
	text := strings.TrimSpace(parsed.Message.Content)
	if text == "" {
		return "", 0, 0, fmt.Errorf("ollama structured output returned empty text")
	}
	return text, parsed.PromptEvalCount, parsed.EvalCount, nil
}

func (c *OpenAICompatibleClient) applyAuth(req *http.Request) {
	if c == nil || req == nil || c.apiKey == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
}
