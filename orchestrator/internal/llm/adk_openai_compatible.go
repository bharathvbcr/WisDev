package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"strings"
	"time"

	adkmodel "google.golang.org/adk/model"
	"google.golang.org/genai"
)

type adkOpenAICompatibleModel struct {
	client *OpenAICompatibleClient
	name   string
}

// NewADKOpenAICompatibleModel wraps an OpenAI-compatible client as an ADK model.LLM.
func NewADKOpenAICompatibleModel(client *OpenAICompatibleClient) (adkmodel.LLM, error) {
	if client == nil {
		return nil, fmt.Errorf("openai compatible client is required")
	}
	return &adkOpenAICompatibleModel{
		client: client,
		name:   client.DefaultModel(),
	}, nil
}

func (m *adkOpenAICompatibleModel) Name() string {
	if m == nil {
		return ""
	}
	return m.name
}

func (m *adkOpenAICompatibleModel) GenerateContent(
	ctx context.Context,
	req *adkmodel.LLMRequest,
	stream bool,
) iter.Seq2[*adkmodel.LLMResponse, error] {
	if stream {
		return m.generateStream(ctx, req)
	}
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		resp, err := m.generate(ctx, req)
		yield(resp, err)
	}
}

func (m *adkOpenAICompatibleModel) generateStream(ctx context.Context, req *adkmodel.LLMRequest) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		resp, err := m.generate(ctx, req)
		if err != nil {
			yield(nil, err)
			return
		}
		if resp != nil {
			resp.TurnComplete = true
		}
		yield(resp, nil)
	}
}

func (m *adkOpenAICompatibleModel) generate(ctx context.Context, req *adkmodel.LLMRequest) (*adkmodel.LLMResponse, error) {
	if m == nil || m.client == nil {
		return nil, fmt.Errorf("adk openai compatible model is not configured")
	}
	if req == nil {
		return nil, fmt.Errorf("adk llm request is required")
	}

	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = m.client.resolveModel("")
	} else {
		modelID = m.client.resolveModel(modelID)
	}

	messages, err := adkContentsToChatMessages(req)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		messages = append(messages, openAIChatMessage{Role: "user", Content: " "})
	}

	temperature := float32(0.2)
	maxTokens := int32(2048)
	if req.Config != nil {
		if req.Config.Temperature != nil {
			temperature = *req.Config.Temperature
		}
		if req.Config.MaxOutputTokens > 0 {
			maxTokens = req.Config.MaxOutputTokens
		}
	}

	tools := adkToolsToOpenAI(req)
	startedAt := time.Now()
	result, err := m.client.generateADKChatCompletion(ctx, modelID, messages, tools, temperature, maxTokens)
	if err != nil {
		slog.Warn("adk openai compatible generate failed",
			"component", "llm.adk_openai_compatible",
			"operation", "generate_content",
			"model", modelID,
			"backend", m.client.BackendName(),
			"latency_ms", time.Since(startedAt).Milliseconds(),
			"error", err.Error(),
		)
		return nil, err
	}

	slog.Info("adk openai compatible generate success",
		"component", "llm.adk_openai_compatible",
		"operation", "generate_content",
		"model", modelID,
		"backend", m.client.BackendName(),
		"latency_ms", time.Since(startedAt).Milliseconds(),
		"tool_calls", len(result.ToolCalls),
	)

	return openAIChatResultToLLMResponse(result, modelID), nil
}

type openAIChatMessage struct {
	Role       string
	Content    string
	ToolCalls  []openAIChatToolCall
	ToolCallID string
}

type openAIChatToolCall struct {
	ID       string
	Name     string
	Arguments string
}

type openAIChatResult struct {
	Content   string
	ToolCalls []openAIChatToolCall
	Usage     struct {
		PromptTokens     int32
		CompletionTokens int32
	}
}

func (c *OpenAICompatibleClient) generateADKChatCompletion(
	ctx context.Context,
	modelID string,
	messages []openAIChatMessage,
	tools []map[string]any,
	temperature float32,
	maxTokens int32,
) (*openAIChatResult, error) {
	if c == nil {
		return nil, fmt.Errorf("openai compatible client is not configured")
	}
	payloadMessages := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		entry := map[string]any{
			"role":    message.Role,
			"content": message.Content,
		}
		if message.ToolCallID != "" {
			entry["tool_call_id"] = message.ToolCallID
		}
		if len(message.ToolCalls) > 0 {
			toolCalls := make([]map[string]any, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				toolCalls = append(toolCalls, map[string]any{
					"id":   call.ID,
					"type": "function",
					"function": map[string]any{
						"name":      call.Name,
						"arguments": call.Arguments,
					},
				})
			}
			entry["tool_calls"] = toolCalls
		}
		payloadMessages = append(payloadMessages, entry)
	}

	payload := map[string]any{
		"model":       modelID,
		"messages":    payloadMessages,
		"temperature": temperature,
		"max_tokens":  maxTokens,
		"stream":      false,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.openAIBaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("adk openai compatible request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("adk openai compatible error: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int32 `json:"prompt_tokens"`
			CompletionTokens int32 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("adk openai compatible response decode failed: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("adk openai compatible response returned no choices")
	}

	choice := parsed.Choices[0].Message
	result := &openAIChatResult{
		Content: strings.TrimSpace(choice.Content),
	}
	for _, call := range choice.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, openAIChatToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	result.Usage.PromptTokens = parsed.Usage.PromptTokens
	result.Usage.CompletionTokens = parsed.Usage.CompletionTokens
	if result.Content == "" && len(result.ToolCalls) == 0 {
		return nil, fmt.Errorf("adk openai compatible response returned empty content")
	}
	return result, nil
}

func adkContentsToChatMessages(req *adkmodel.LLMRequest) ([]openAIChatMessage, error) {
	messages := make([]openAIChatMessage, 0, len(req.Contents)+1)
	if req.Config != nil && req.Config.SystemInstruction != nil {
		if text := genaiContentText(req.Config.SystemInstruction); text != "" {
			messages = append(messages, openAIChatMessage{Role: "system", Content: text})
		}
	}
	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		role := "user"
		switch content.Role {
		case genai.RoleModel:
			role = "assistant"
		case genai.RoleUser:
			role = "user"
		default:
			role = strings.ToLower(string(content.Role))
		}

		var textParts []string
		var toolCalls []openAIChatToolCall
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
			if part.FunctionCall != nil {
				argsJSON, err := json.Marshal(part.FunctionCall.Args)
				if err != nil {
					return nil, fmt.Errorf("encode function call args: %w", err)
				}
				toolCalls = append(toolCalls, openAIChatToolCall{
					ID:        strings.TrimSpace(part.FunctionCall.ID),
					Name:      part.FunctionCall.Name,
					Arguments: string(argsJSON),
				})
			}
			if part.FunctionResponse != nil {
				respJSON, err := json.Marshal(part.FunctionResponse.Response)
				if err != nil {
					return nil, fmt.Errorf("encode function response: %w", err)
				}
				messages = append(messages, openAIChatMessage{
					Role:       "tool",
					ToolCallID: strings.TrimSpace(part.FunctionResponse.ID),
					Content:    string(respJSON),
				})
			}
		}
		if len(toolCalls) > 0 || len(textParts) > 0 {
			messages = append(messages, openAIChatMessage{
				Role:      role,
				Content:   strings.Join(textParts, "\n"),
				ToolCalls: toolCalls,
			})
		}
	}
	return messages, nil
}

func adkToolsToOpenAI(req *adkmodel.LLMRequest) []map[string]any {
	if req == nil || req.Config == nil {
		return nil
	}
	tools := make([]map[string]any, 0)
	for _, tool := range req.Config.Tools {
		if tool == nil {
			continue
		}
		for _, decl := range tool.FunctionDeclarations {
			if decl == nil || strings.TrimSpace(decl.Name) == "" {
				continue
			}
			parameters := decl.Parameters
			if parameters == nil {
				parameters = &genai.Schema{Type: genai.TypeObject}
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        decl.Name,
					"description": decl.Description,
					"parameters":  parameters,
				},
			})
		}
	}
	return tools
}

func openAIChatResultToLLMResponse(result *openAIChatResult, modelVersion string) *adkmodel.LLMResponse {
	parts := make([]*genai.Part, 0, 1+len(result.ToolCalls))
	if result.Content != "" {
		parts = append(parts, genai.NewPartFromText(result.Content))
	}
	for _, call := range result.ToolCalls {
		var args map[string]any
		_ = json.Unmarshal([]byte(call.Arguments), &args)
		if args == nil {
			args = map[string]any{}
		}
		parts = append(parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				ID:   call.ID,
				Name: call.Name,
				Args: args,
			},
		})
	}
	return &adkmodel.LLMResponse{
		Content: &genai.Content{
			Role:  genai.RoleModel,
			Parts: parts,
		},
		ModelVersion: modelVersion,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     result.Usage.PromptTokens,
			CandidatesTokenCount: result.Usage.CompletionTokens,
			TotalTokenCount:      result.Usage.PromptTokens + result.Usage.CompletionTokens,
		},
		FinishReason: genai.FinishReasonStop,
	}
}

func genaiContentText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	parts := make([]string, 0, len(content.Parts))
	for _, part := range content.Parts {
		if part != nil && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

