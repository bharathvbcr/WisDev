package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

const httpClientDeadlineHeadroom = 500 * time.Millisecond

type generateHTTPRequest struct {
	Prompt          string            `json:"prompt"`
	SystemPrompt    string            `json:"systemPrompt,omitempty"`
	Model           string            `json:"model,omitempty"`
	Temperature     float32           `json:"temperature,omitempty"`
	MaxTokens       int32             `json:"maxTokens,omitempty"`
	StopSequences   []string          `json:"stopSequences,omitempty"`
	ThinkingBudget  *int32            `json:"thinkingBudget,omitempty"`
	ServiceTier     string            `json:"serviceTier,omitempty"`
	RetryProfile    string            `json:"retryProfile,omitempty"`
	RequestClass    string            `json:"requestClass,omitempty"`
	LatencyBudgetMs int32             `json:"latencyBudgetMs,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type generateHTTPResponse struct {
	Text         string `json:"text"`
	ModelUsed    string `json:"modelUsed"`
	InputTokens  int32  `json:"inputTokens"`
	OutputTokens int32  `json:"outputTokens"`
	FinishReason string `json:"finishReason"`
	LatencyMs    int64  `json:"latencyMs"`
}

type generateChunkHTTPResponse struct {
	Delta        string `json:"delta"`
	Done         bool   `json:"done"`
	FinishReason string `json:"finishReason"`
}

type structuredHTTPRequest struct {
	Prompt          string            `json:"prompt"`
	SystemPrompt    string            `json:"systemPrompt,omitempty"`
	JSONSchema      string            `json:"jsonSchema,omitempty"`
	Model           string            `json:"model,omitempty"`
	Temperature     float32           `json:"temperature,omitempty"`
	MaxTokens       int32             `json:"maxTokens,omitempty"`
	ThinkingBudget  *int32            `json:"thinkingBudget,omitempty"`
	ServiceTier     string            `json:"serviceTier,omitempty"`
	RetryProfile    string            `json:"retryProfile,omitempty"`
	RequestClass    string            `json:"requestClass,omitempty"`
	LatencyBudgetMs int32             `json:"latencyBudgetMs,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type structuredHTTPResponse struct {
	JSONResult   string `json:"jsonResult"`
	ModelUsed    string `json:"modelUsed"`
	InputTokens  int32  `json:"inputTokens"`
	OutputTokens int32  `json:"outputTokens"`
	SchemaValid  bool   `json:"schemaValid"`
	Error        string `json:"error"`
	LatencyMs    int64  `json:"latencyMs"`
}

type embedHTTPRequest struct {
	Text            string            `json:"text"`
	Model           string            `json:"model,omitempty"`
	TaskType        string            `json:"taskType,omitempty"`
	LatencyBudgetMs int32             `json:"latencyBudgetMs,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type embedHTTPResponse struct {
	Embedding  []float32 `json:"embedding"`
	TokenCount int32     `json:"tokenCount"`
	ModelUsed  string    `json:"modelUsed"`
	LatencyMs  int64     `json:"latencyMs"`
}

type embedBatchHTTPRequest struct {
	Texts           []string          `json:"texts"`
	Model           string            `json:"model,omitempty"`
	TaskType        string            `json:"taskType,omitempty"`
	LatencyBudgetMs int32             `json:"latencyBudgetMs,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type embedVectorHTTPResponse struct {
	Values     []float32 `json:"values"`
	TokenCount int32     `json:"tokenCount"`
}

type embedBatchHTTPResponse struct {
	Embeddings []embedVectorHTTPResponse `json:"embeddings"`
	ModelUsed  string                    `json:"modelUsed"`
	LatencyMs  int64                     `json:"latencyMs"`
}

type healthHTTPResponse struct {
	OK              bool     `json:"ok"`
	Version         string   `json:"version"`
	ModelsAvailable []string `json:"modelsAvailable"`
	Error           string   `json:"error"`
}

type runtimeDependencyHTTPResponse struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	Detail    string `json:"detail"`
}

type runtimeHealthHTTPResponse struct {
	Service      string                          `json:"service"`
	Status       string                          `json:"status"`
	Transport    string                          `json:"transport"`
	Dependencies []runtimeDependencyHTTPResponse `json:"dependencies"`
}

type streamErrorPayload struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Status  int    `json:"status,omitempty"`
	TraceID string `json:"traceId,omitempty"`
}

type generateStreamEvent struct {
	Chunk *generateChunkHTTPResponse `json:"chunk,omitempty"`
	Error *streamErrorPayload        `json:"error,omitempty"`
}

func (c *Client) generateHTTP(ctx context.Context, req *llmpb.GenerateRequest) (*llmpb.GenerateResponse, error) {
	var resp generateHTTPResponse
	if err := c.postJSON(ctx, "/llm/generate", generateHTTPRequest{
		Prompt:          req.GetPrompt(),
		SystemPrompt:    req.GetSystemPrompt(),
		Model:           req.GetModel(),
		Temperature:     req.GetTemperature(),
		MaxTokens:       req.GetMaxTokens(),
		StopSequences:   append([]string(nil), req.GetStopSequences()...),
		ThinkingBudget:  req.ThinkingBudget,
		ServiceTier:     req.GetServiceTier(),
		RetryProfile:    req.GetRetryProfile(),
		RequestClass:    req.GetRequestClass(),
		LatencyBudgetMs: req.GetLatencyBudgetMs(),
		Metadata:        cloneStringMap(req.GetMetadata()),
	}, &resp); err != nil {
		return nil, err
	}
	return &llmpb.GenerateResponse{
		Text:         resp.Text,
		ModelUsed:    resp.ModelUsed,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		FinishReason: resp.FinishReason,
		LatencyMs:    resp.LatencyMs,
	}, nil
}

func (c *Client) generateStreamHTTP(ctx context.Context, req *llmpb.GenerateRequest) (llmpb.LLMService_GenerateStreamClient, error) {
	httpReq, err := c.newJSONRequest(ctx, http.MethodPost, "/llm/generate/stream", generateHTTPRequest{
		Prompt:          req.GetPrompt(),
		SystemPrompt:    req.GetSystemPrompt(),
		Model:           req.GetModel(),
		Temperature:     req.GetTemperature(),
		MaxTokens:       req.GetMaxTokens(),
		StopSequences:   append([]string(nil), req.GetStopSequences()...),
		ThinkingBudget:  req.ThinkingBudget,
		ServiceTier:     req.GetServiceTier(),
		RetryProfile:    req.GetRetryProfile(),
		RequestClass:    req.GetRequestClass(),
		LatencyBudgetMs: req.GetLatencyBudgetMs(),
		Metadata:        cloneStringMap(req.GetMetadata()),
	})
	if err != nil {
		return nil, err
	}

	resp, err := (&http.Client{Timeout: c.httpTimeoutFor(ctx)}).Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		defer resp.Body.Close()
		return nil, decodeHTTPError(resp, "python llm stream")
	}
	return newLLMHTTPStream(ctx, resp), nil
}

func (c *Client) structuredOutputHTTP(ctx context.Context, req *llmpb.StructuredRequest) (*llmpb.StructuredResponse, error) {
	var resp structuredHTTPResponse
	if err := c.postJSON(ctx, "/llm/structured-output", structuredHTTPRequest{
		Prompt:          req.GetPrompt(),
		SystemPrompt:    req.GetSystemPrompt(),
		JSONSchema:      req.GetJsonSchema(),
		Model:           req.GetModel(),
		Temperature:     req.GetTemperature(),
		MaxTokens:       req.GetMaxTokens(),
		ThinkingBudget:  req.ThinkingBudget,
		ServiceTier:     req.GetServiceTier(),
		RetryProfile:    req.GetRetryProfile(),
		RequestClass:    req.GetRequestClass(),
		LatencyBudgetMs: req.GetLatencyBudgetMs(),
		Metadata:        cloneStringMap(req.GetMetadata()),
	}, &resp); err != nil {
		return nil, err
	}
	return &llmpb.StructuredResponse{
		JsonResult:   resp.JSONResult,
		ModelUsed:    resp.ModelUsed,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		SchemaValid:  resp.SchemaValid,
		Error:        resp.Error,
		LatencyMs:    resp.LatencyMs,
	}, nil
}

func (c *Client) embedHTTP(ctx context.Context, req *llmpb.EmbedRequest) (*llmpb.EmbedResponse, error) {
	var resp embedHTTPResponse
	if err := c.postJSON(ctx, "/llm/embed", embedHTTPRequest{
		Text:            req.GetText(),
		Model:           req.GetModel(),
		TaskType:        req.GetTaskType(),
		LatencyBudgetMs: req.GetLatencyBudgetMs(),
		Metadata:        cloneStringMap(req.GetMetadata()),
	}, &resp); err != nil {
		return nil, err
	}
	return &llmpb.EmbedResponse{
		Embedding:  resp.Embedding,
		TokenCount: resp.TokenCount,
		ModelUsed:  resp.ModelUsed,
		LatencyMs:  resp.LatencyMs,
	}, nil
}

func (c *Client) embedBatchHTTP(ctx context.Context, req *llmpb.EmbedBatchRequest) (*llmpb.EmbedBatchResponse, error) {
	var resp embedBatchHTTPResponse
	if err := c.postJSON(ctx, "/llm/embed/batch", embedBatchHTTPRequest{
		Texts:           append([]string(nil), req.GetTexts()...),
		Model:           req.GetModel(),
		TaskType:        req.GetTaskType(),
		LatencyBudgetMs: req.GetLatencyBudgetMs(),
		Metadata:        cloneStringMap(req.GetMetadata()),
	}, &resp); err != nil {
		return nil, err
	}

	embeddings := make([]*llmpb.EmbedVector, 0, len(resp.Embeddings))
	for _, vector := range resp.Embeddings {
		embeddings = append(embeddings, &llmpb.EmbedVector{
			Values:     append([]float32(nil), vector.Values...),
			TokenCount: vector.TokenCount,
		})
	}

	return &llmpb.EmbedBatchResponse{
		Embeddings: embeddings,
		ModelUsed:  resp.ModelUsed,
		LatencyMs:  resp.LatencyMs,
	}, nil
}

func (c *Client) healthHTTP(ctx context.Context) (*llmpb.HealthResponse, error) {
	var resp healthHTTPResponse
	if err := c.getJSON(ctx, "/llm/health", &resp); err != nil {
		return nil, err
	}
	return &llmpb.HealthResponse{
		Ok:              resp.OK,
		Version:         resp.Version,
		ModelsAvailable: append([]string(nil), resp.ModelsAvailable...),
		Error:           resp.Error,
	}, nil
}

func (c *Client) runtimeHealthHTTP(ctx context.Context) (*runtimeHealthHTTPResponse, error) {
	var resp runtimeHealthHTTPResponse
	if err := c.getJSON(ctx, "/health", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) postJSON(ctx context.Context, route string, payload any, out any) error {
	req, err := c.newJSONRequest(ctx, http.MethodPost, route, payload)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: c.httpTimeoutFor(ctx)}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return decodeHTTPError(resp, "python llm")
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) getJSON(ctx context.Context, route string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.buildURL(route), nil)
	if err != nil {
		return err
	}
	c.applyHTTPHeaders(ctx, req.Header)
	resp, err := (&http.Client{Timeout: c.httpTimeoutFor(ctx)}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return decodeHTTPError(resp, "python llm")
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) newJSONRequest(ctx context.Context, method, route string, payload any) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.buildURL(route), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.applyHTTPHeaders(ctx, req.Header)
	return req, nil
}

func (c *Client) httpTimeoutFor(ctx context.Context) time.Duration {
	if c == nil {
		return 0
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return time.Millisecond
		}
		ctxTimeout := remaining + httpClientDeadlineHeadroom
		if c.timeout > 0 && c.timeout < ctxTimeout {
			return c.timeout
		}
		return ctxTimeout
	}
	return c.timeout
}

func (c *Client) buildURL(route string) string {
	base := strings.TrimRight(c.httpBaseURL, "/")
	path := strings.TrimLeft(route, "/")
	return base + "/" + path
}

func decodeHTTPError(resp *http.Response, surface string) error {
	body, _ := io.ReadAll(resp.Body)
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Errorf("%s returned %d", surface, resp.StatusCode)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		fallbackCode := firstNonEmptyHTTPErrorString(payload["kind"])
		if message, code := decodeHTTPErrorValue(payload["error"], fallbackCode); message != "" {
			if code != "" {
				return fmt.Errorf("%s returned %d: %s (%s)", surface, resp.StatusCode, message, code)
			}
			return fmt.Errorf("%s returned %d: %s", surface, resp.StatusCode, message)
		}
		if message, code := decodeHTTPErrorValue(payload["detail"], fallbackCode); message != "" {
			if code != "" {
				return fmt.Errorf("%s returned %d: %s (%s)", surface, resp.StatusCode, message, code)
			}
			return fmt.Errorf("%s returned %d: %s", surface, resp.StatusCode, message)
		}
	}

	return fmt.Errorf("%s returned %d: %s", surface, resp.StatusCode, trimmed)
}

func decodeHTTPErrorValue(value any, fallbackCode string) (string, string) {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), strings.TrimSpace(fallbackCode)
	case map[string]any:
		nestedCode := firstNonEmptyHTTPErrorString(typed["code"], typed["kind"], fallbackCode)
		if nested, ok := typed["error"]; ok {
			if message, code := decodeHTTPErrorValue(nested, nestedCode); message != "" {
				return message, code
			}
		}
		if nested, ok := typed["detail"]; ok {
			if message, code := decodeHTTPErrorValue(nested, nestedCode); message != "" {
				return message, code
			}
		}
		message := firstNonEmptyHTTPErrorString(typed["message"])
		if message != "" {
			return message, nestedCode
		}
	}
	return "", ""
}

func firstNonEmptyHTTPErrorString(values ...any) string {
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type llmHTTPStream struct {
	ctx     context.Context
	resp    *http.Response
	decoder *json.Decoder
	header  metadata.MD
	trailer metadata.MD
	closed  bool
}

func newLLMHTTPStream(ctx context.Context, resp *http.Response) *llmHTTPStream {
	return &llmHTTPStream{
		ctx:     ctx,
		resp:    resp,
		decoder: json.NewDecoder(resp.Body),
		header:  httpHeaderToMetadata(resp.Header),
	}
}

func (s *llmHTTPStream) Recv() (*llmpb.GenerateChunk, error) {
	if s.closed {
		return nil, io.EOF
	}

	var event generateStreamEvent
	if err := s.decoder.Decode(&event); err != nil {
		_ = s.closeBody()
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, err
	}

	if event.Error != nil {
		_ = s.closeBody()
		code := strings.TrimSpace(event.Error.Code)
		message := strings.TrimSpace(event.Error.Message)
		if code != "" {
			return nil, fmt.Errorf("python llm stream failed: %s (%s)", message, code)
		}
		if message != "" {
			return nil, fmt.Errorf("python llm stream failed: %s", message)
		}
		return nil, errors.New("python llm stream failed")
	}
	if event.Chunk == nil {
		_ = s.closeBody()
		return nil, errors.New("python llm stream emitted an empty event")
	}

	return &llmpb.GenerateChunk{
		Delta:        event.Chunk.Delta,
		Done:         event.Chunk.Done,
		FinishReason: event.Chunk.FinishReason,
	}, nil
}

func (s *llmHTTPStream) Header() (metadata.MD, error) {
	return s.header, nil
}

func (s *llmHTTPStream) Trailer() metadata.MD {
	return s.trailer
}

func (s *llmHTTPStream) CloseSend() error {
	return s.closeBody()
}

func (s *llmHTTPStream) Context() context.Context {
	return s.ctx
}

func (s *llmHTTPStream) SendMsg(any) error {
	return errors.New("sendmsg is unsupported for python llm http stream")
}

func (s *llmHTTPStream) RecvMsg(m any) error {
	chunk, err := s.Recv()
	if err != nil {
		return err
	}
	target, ok := m.(*llmpb.GenerateChunk)
	if !ok {
		return errors.New("python llm http stream expected *llm.GenerateChunk")
	}
	// Use proto.Reset + proto.Merge instead of a struct-value copy (*target = *chunk).
	// GenerateChunk embeds protoimpl.MessageState which contains a sync.Mutex;
	// copying the struct value copies the mutex, which go vet flags as a data race risk.
	proto.Reset(target)
	proto.Merge(target, chunk)
	return nil
}

func (s *llmHTTPStream) closeBody() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.resp != nil {
		s.trailer = httpHeaderToMetadata(s.resp.Trailer)
		if s.resp.Body == nil {
			return nil
		}
		return s.resp.Body.Close()
	}
	return nil
}

func httpHeaderToMetadata(headers http.Header) metadata.MD {
	md := metadata.MD{}
	for key, values := range headers {
		lower := strings.ToLower(strings.TrimSpace(key))
		if lower == "" {
			continue
		}
		md[lower] = append([]string(nil), values...)
	}
	return md
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
