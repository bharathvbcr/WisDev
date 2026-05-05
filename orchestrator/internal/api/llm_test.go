package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type stubLLMHTTPClient struct {
	embedResp      *llmv1.EmbedResponse
	embedErr       error
	lastReq        *llmv1.EmbedRequest
	embedBatchResp *llmv1.EmbedBatchResponse
	embedBatchErr  error
	lastBatchReq   *llmv1.EmbedBatchRequest
}

type stubLLMGenerateOnlyClient struct{}

func assertError(msg string) error {
	return errors.New(msg)
}

func (s *stubLLMHTTPClient) Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
	return &llmv1.GenerateResponse{Text: "ok"}, nil
}

func (s *stubLLMHTTPClient) Embed(ctx context.Context, req *llmv1.EmbedRequest) (*llmv1.EmbedResponse, error) {
	s.lastReq = req
	if s.embedErr != nil {
		return nil, s.embedErr
	}
	if s.embedResp != nil {
		return s.embedResp, nil
	}
	return &llmv1.EmbedResponse{Embedding: []float32{0.1, 0.2}}, nil
}

func (s *stubLLMHTTPClient) EmbedBatch(ctx context.Context, req *llmv1.EmbedBatchRequest) (*llmv1.EmbedBatchResponse, error) {
	s.lastBatchReq = req
	if s.embedBatchErr != nil {
		return nil, s.embedBatchErr
	}
	if s.embedBatchResp != nil {
		return s.embedBatchResp, nil
	}
	return &llmv1.EmbedBatchResponse{
		Embeddings: []*llmv1.EmbedVector{
			{Values: []float32{0.1, 0.2}},
			{Values: []float32{0.3, 0.4}},
		},
	}, nil
}

func (s *stubLLMGenerateOnlyClient) Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
	return &llmv1.GenerateResponse{Text: "ok"}, nil
}

type scriptedLLMClient struct {
	errorsByModel           map[string]error
	respByModel             map[string]*llmv1.GenerateResponse
	callOrder               []string
	requests                []*llmv1.GenerateRequest
	structuredErrorsByModel map[string]error
	structuredRespByModel   map[string]*llmv1.StructuredResponse
	structuredCallOrder     []string
	structuredRequests      []*llmv1.StructuredRequest
}

func (s *scriptedLLMClient) Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
	if req != nil {
		s.callOrder = append(s.callOrder, req.Model)
		s.requests = append(s.requests, req)
	}
	if err := s.errorsByModel[req.Model]; err != nil {
		return nil, err
	}
	if s.respByModel[req.Model] != nil {
		return s.respByModel[req.Model], nil
	}
	return &llmv1.GenerateResponse{
		Text:      "ok",
		ModelUsed: req.Model,
	}, nil
}

func (s *scriptedLLMClient) StructuredOutput(ctx context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
	if req != nil {
		s.structuredCallOrder = append(s.structuredCallOrder, req.Model)
		s.structuredRequests = append(s.structuredRequests, req)
	}
	if err := s.structuredErrorsByModel[req.Model]; err != nil {
		return nil, err
	}
	if s.structuredRespByModel[req.Model] != nil {
		return s.structuredRespByModel[req.Model], nil
	}
	return &llmv1.StructuredResponse{
		JsonResult:  `{"ok":true}`,
		ModelUsed:   req.Model,
		SchemaValid: true,
	}, nil
}

func TestLLMHandlerResolveModel(t *testing.T) {
	handler := NewLLMHandler(nil)
	if got := handler.resolveModel("heavy"); got != "gemini-2.5-pro" {
		t.Fatalf("resolveModel(heavy) = %q, want %q", got, "gemini-2.5-pro")
	}
	if got := handler.resolveModel("light"); got != "gemini-2.5-flash-lite" {
		t.Fatalf("resolveModel(light) = %q, want %q", got, "gemini-2.5-flash-lite")
	}
	if got := handler.resolveModel("standard"); got != "gemini-2.5-flash" {
		t.Fatalf("resolveModel(standard) = %q, want %q", got, "gemini-2.5-flash")
	}
	if got := handler.resolveModel("unknown"); got != "gemini-2.5-flash" {
		t.Fatalf("resolveModel(unknown) = %q, want %q", got, "gemini-2.5-flash")
	}
}

func TestLLMHandlerHandleGenerate(t *testing.T) {
	t.Run("rejects non-post methods", func(t *testing.T) {
		handler := NewLLMHandler(&scriptedLLMClient{})
		req := httptest.NewRequest(http.MethodGet, "/llm/generate", nil)
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected status 405, got %d", rec.Code)
		}
	})

	t.Run("rejects empty prompt", func(t *testing.T) {
		handler := NewLLMHandler(&scriptedLLMClient{})
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", bytes.NewBufferString(`{"prompt":""}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("rejects invalid json body", func(t *testing.T) {
		handler := NewLLMHandler(&scriptedLLMClient{})
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("defaults to standard when no tier provided", func(t *testing.T) {
		client := &scriptedLLMClient{
			errorsByModel: map[string]error{},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","maxTokens":32,"temperature":0.2}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		if got := client.callOrder[0]; got != "gemini-2.5-flash" {
			t.Fatalf("expected first attempt model %q, got %q", "gemini-2.5-flash", got)
		}
	})

	t.Run("defaults to light for light task type", func(t *testing.T) {
		client := &scriptedLLMClient{
			errorsByModel: map[string]error{},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","taskType":"light"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if got := client.callOrder[0]; got != "gemini-2.5-flash-lite" {
			t.Fatalf("expected first attempt model %q, got %q", "gemini-2.5-flash-lite", got)
		}
	})

	t.Run("routes json_object requests through StructuredOutput and preserves schema intent", func(t *testing.T) {
		client := &scriptedLLMClient{
			errorsByModel: map[string]error{},
			structuredRespByModel: map[string]*llmv1.StructuredResponse{
				"gemini-2.5-flash": {
					JsonResult:  `{"ok":true}`,
					ModelUsed:   "gemini-2.5-flash",
					SchemaValid: true,
				},
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"extract entities","responseFormat":"json_object","jsonSchema":{"type":"object","properties":{"ok":{"type":"boolean"}}},"retryProfile":"strict_json"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		if len(client.callOrder) != 0 {
			t.Fatalf("expected Generate not to be called for structured request, got %d generate calls", len(client.callOrder))
		}
		if got := client.structuredCallOrder[0]; got != "gemini-2.5-flash" {
			t.Fatalf("expected first structured attempt model %q, got %q", "gemini-2.5-flash", got)
		}
		if got := client.structuredRequests[0].GetRetryProfile(); got != "strict_json" {
			t.Fatalf("expected retry profile %q, got %q", "strict_json", got)
		}
		if got := client.structuredRequests[0].GetRequestClass(); got != "structured_high_value" {
			t.Fatalf("expected request class %q, got %q", "structured_high_value", got)
		}
		if got := client.structuredRequests[0].GetServiceTier(); got != "priority" {
			t.Fatalf("expected service tier %q, got %q", "priority", got)
		}
		if schema := client.structuredRequests[0].GetJsonSchema(); !strings.Contains(schema, `"type":"object"`) {
			t.Fatalf("expected forwarded JSON schema, got %q", schema)
		}
		body := strings.TrimSpace(rec.Body.String())
		if !strings.Contains(body, `"text":"{\"ok\":true}"`) {
			t.Fatalf("expected structured json payload in response body: %s", body)
		}
	})

	t.Run("rejects structured requests without schema", func(t *testing.T) {
		handler := NewLLMHandler(&scriptedLLMClient{})
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"extract entities","responseFormat":"json_object"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		if !strings.Contains(rec.Body.String(), "jsonSchema is required") {
			t.Fatalf("expected missing schema error, got %s", strings.TrimSpace(rec.Body.String()))
		}
	})

	t.Run("structured requests fall back when the first tier returns invalid JSON", func(t *testing.T) {
		client := &scriptedLLMClient{
			structuredRespByModel: map[string]*llmv1.StructuredResponse{
				"gemini-2.5-flash-lite": {
					JsonResult:  `{"broken":`,
					ModelUsed:   "gemini-2.5-flash-lite",
					SchemaValid: false,
					Error:       "vertex structured output returned invalid JSON",
				},
				"gemini-2.5-flash": {
					JsonResult:  `{"ok":true}`,
					ModelUsed:   "gemini-2.5-flash",
					SchemaValid: true,
				},
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"extract entities","tier":"light","responseFormat":"json_object","jsonSchema":{"type":"object","properties":{"ok":{"type":"boolean"}}},"retryProfile":"strict_json"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		if len(client.structuredCallOrder) != 2 {
			t.Fatalf("expected two structured attempts, got %v", client.structuredCallOrder)
		}
		if got := client.structuredCallOrder[0]; got != "gemini-2.5-flash-lite" {
			t.Fatalf("expected first structured attempt model %q, got %q", "gemini-2.5-flash-lite", got)
		}
		if got := client.structuredCallOrder[1]; got != "gemini-2.5-flash" {
			t.Fatalf("expected second structured attempt model %q, got %q", "gemini-2.5-flash", got)
		}
		body := strings.TrimSpace(rec.Body.String())
		if !strings.Contains(body, `"selectedTier":"standard"`) {
			t.Fatalf("expected selectedTier standard in response: %s", body)
		}
		if !strings.Contains(body, `"fallbackApplied":true`) {
			t.Fatalf("expected fallbackApplied true in response: %s", body)
		}
	})

	t.Run("structured requests fall back when the sidecar wraps provider failure in invalid argument", func(t *testing.T) {
		client := &scriptedLLMClient{
			structuredErrorsByModel: map[string]error{
				"gemini-2.5-flash-lite": status.Error(codes.InvalidArgument, `{"error":{"code":"STRUCTURED_FAILED","message":"structured output returned invalid JSON"}}`),
			},
			structuredRespByModel: map[string]*llmv1.StructuredResponse{
				"gemini-2.5-flash": {
					JsonResult:  `{"ok":true}`,
					ModelUsed:   "gemini-2.5-flash",
					SchemaValid: true,
				},
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"extract entities","tier":"light","responseFormat":"json_object","jsonSchema":{"type":"object","properties":{"ok":{"type":"boolean"}}},"retryProfile":"strict_json"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		if len(client.structuredCallOrder) != 2 {
			t.Fatalf("expected two structured attempts, got %v", client.structuredCallOrder)
		}
		if got := client.structuredCallOrder[0]; got != "gemini-2.5-flash-lite" {
			t.Fatalf("expected first structured attempt model %q, got %q", "gemini-2.5-flash-lite", got)
		}
		if got := client.structuredCallOrder[1]; got != "gemini-2.5-flash" {
			t.Fatalf("expected second structured attempt model %q, got %q", "gemini-2.5-flash", got)
		}
		body := strings.TrimSpace(rec.Body.String())
		if !strings.Contains(body, `"selectedTier":"standard"`) {
			t.Fatalf("expected selectedTier standard in response: %s", body)
		}
		if !strings.Contains(body, `"fallbackApplied":true`) {
			t.Fatalf("expected fallbackApplied true in response: %s", body)
		}
	})

	t.Run("falls back to next tier on failures", func(t *testing.T) {
		client := &scriptedLLMClient{
			errorsByModel: map[string]error{
				"gemini-2.5-pro":   assertError("heavy failure"),
				"gemini-2.5-flash": assertError("standard failure"),
			},
			respByModel: map[string]*llmv1.GenerateResponse{
				"gemini-2.5-flash-lite": {
					Text:      "fallback success",
					ModelUsed: "gemini-2.5-flash-lite",
				},
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","tier":"heavy"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		if got := client.callOrder[2]; got != "gemini-2.5-flash-lite" {
			t.Fatalf("expected third attempt model %q, got %q", "gemini-2.5-flash-lite", got)
		}
		body := strings.TrimSpace(rec.Body.String())
		if !strings.Contains(body, `"selectedTier":"light"`) {
			t.Fatalf("expected selectedTier light in response: %s", body)
		}
		if !strings.Contains(body, `"fallbackApplied":true`) {
			t.Fatalf("expected fallbackApplied true in response: %s", body)
		}
	})

	t.Run("falls back when first attempt returns blank output", func(t *testing.T) {
		client := &scriptedLLMClient{
			respByModel: map[string]*llmv1.GenerateResponse{
				"gemini-2.5-pro": {
					Text:      "   ",
					ModelUsed: "gemini-2.5-pro",
				},
				"gemini-2.5-flash": {
					Text:      "fallback success",
					ModelUsed: "gemini-2.5-flash",
				},
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","tier":"heavy"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		if got := client.callOrder[1]; got != "gemini-2.5-flash" {
			t.Fatalf("expected second attempt model %q, got %q", "gemini-2.5-flash", got)
		}
		body := strings.TrimSpace(rec.Body.String())
		if !strings.Contains(body, `"text":"fallback success"`) {
			t.Fatalf("expected fallback body, got %s", body)
		}
	})

	t.Run("returns bad gateway after all failures", func(t *testing.T) {
		client := &scriptedLLMClient{
			errorsByModel: map[string]error{
				"gemini-2.5-flash":      assertError("standard failure"),
				"gemini-2.5-pro":        assertError("heavy failure"),
				"gemini-2.5-flash-lite": assertError("light failure"),
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","tier":"standard"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected status 502, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Generation failed") {
			t.Fatalf("expected generation failed message, got %s", strings.TrimSpace(rec.Body.String()))
		}
	})

	t.Run("applies minimum typed transport budget for short requests", func(t *testing.T) {
		client := &scriptedLLMClient{}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","routingIntent":{"latencyBudgetMs":450}}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if got := client.callOrder[0]; got != "gemini-2.5-flash" {
			t.Fatalf("expected first attempt model %q, got %q", "gemini-2.5-flash", got)
		}
		if got := client.requests[0].GetLatencyBudgetMs(); got < 3900 || got > 4100 {
			t.Fatalf("expected typed latency budget around 4000ms, got %d", got)
		}
		if got := client.requests[0].GetRequestClass(); got != "standard" {
			t.Fatalf("expected request class %q, got %q", "standard", got)
		}
		if got := client.requests[0].GetServiceTier(); got != "standard" {
			t.Fatalf("expected service tier %q, got %q", "standard", got)
		}
	})

	t.Run("caps explicit latency budget to max request budget", func(t *testing.T) {
		client := &scriptedLLMClient{}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","routingIntent":{"latencyBudgetMs":999999}}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if got := client.requests[0].GetLatencyBudgetMs(); got < 49000 || got > 50000 {
			t.Fatalf("expected capped typed latency budget between 49000ms and 50000ms, got %d", got)
		}
		if got := client.requests[0].GetRetryProfile(); got != "standard" {
			t.Fatalf("expected retry profile %q, got %q", "standard", got)
		}
	})

	t.Run("uses fallback from heavy to standard", func(t *testing.T) {
		client := &scriptedLLMClient{
			errorsByModel: map[string]error{
				"gemini-2.5-pro": assertError("heavy failure"),
			},
			respByModel: map[string]*llmv1.GenerateResponse{
				"gemini-2.5-flash": {
					Text:      "standard success",
					ModelUsed: "gemini-2.5-flash",
				},
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","tier":"heavy"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		if got := client.callOrder[1]; got != "gemini-2.5-flash" {
			t.Fatalf("expected second attempt model %q, got %q", "gemini-2.5-flash", got)
		}
	})
}

func TestLLMHandlerHandleGenerate_ErrorKinds(t *testing.T) {
	t.Run("grpc invalid argument errors stop immediately as permanent", func(t *testing.T) {
		client := &scriptedLLMClient{
			errorsByModel: map[string]error{
				"gemini-2.5-flash": status.Error(codes.InvalidArgument, `{"error":{"code":"INVALID_PROMPT","message":"prompt is required"}}`),
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"topic","tier":"standard"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected status 502, got %d", rec.Code)
		}
		if len(client.callOrder) != 1 {
			t.Fatalf("expected one attempt for grpc invalid argument, got %v", client.callOrder)
		}
		if !strings.Contains(rec.Body.String(), `"kind":"permanent"`) {
			t.Fatalf("expected permanent error kind, got %s", strings.TrimSpace(rec.Body.String()))
		}
	})

	t.Run("permanent errors stop immediately", func(t *testing.T) {
		client := &scriptedLLMClient{
			errorsByModel: map[string]error{
				"gemini-2.5-flash": assertError("permission denied"),
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","tier":"standard"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected status 502, got %d", rec.Code)
		}
		if len(client.callOrder) != 1 {
			t.Fatalf("expected one attempt for permanent error, got %v", client.callOrder)
		}
		if !strings.Contains(rec.Body.String(), `"kind":"permanent"`) {
			t.Fatalf("expected permanent error kind, got %s", strings.TrimSpace(rec.Body.String()))
		}
	})

	t.Run("rate limit errors map to 429", func(t *testing.T) {
		client := &scriptedLLMClient{
			errorsByModel: map[string]error{
				"gemini-2.5-flash":      assertError("rate limit 429"),
				"gemini-2.5-pro":        assertError("rate limit 429"),
				"gemini-2.5-flash-lite": assertError("rate limit 429"),
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","tier":"standard"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("expected status 429, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"kind":"rate_limit"`) {
			t.Fatalf("expected rate limit error kind, got %s", strings.TrimSpace(rec.Body.String()))
		}
	})

	t.Run("timeout errors map to 504", func(t *testing.T) {
		client := &scriptedLLMClient{
			errorsByModel: map[string]error{
				"gemini-2.5-flash":      assertError("context deadline exceeded"),
				"gemini-2.5-pro":        assertError("context deadline exceeded"),
				"gemini-2.5-flash-lite": assertError("context deadline exceeded"),
			},
		}
		handler := NewLLMHandler(client)
		req := httptest.NewRequest(http.MethodPost, "/llm/generate", strings.NewReader(`{"prompt":"analyze topic","tier":"standard"}`))
		rec := httptest.NewRecorder()

		handler.HandleGenerate(rec, req)

		if rec.Code != http.StatusGatewayTimeout {
			t.Fatalf("expected status 504, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"kind":"timeout"`) {
			t.Fatalf("expected timeout error kind, got %s", strings.TrimSpace(rec.Body.String()))
		}
	})
}

func TestClassifyLLMError_GrpcStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want generateErrorKind
	}{
		{
			name: "invalid argument is permanent",
			err:  status.Error(codes.InvalidArgument, "bad request"),
			want: generateErrPermanent,
		},
		{
			name: "resource exhausted is rate limit",
			err:  status.Error(codes.ResourceExhausted, "quota"),
			want: generateErrRateLimit,
		},
		{
			name: "deadline exceeded is timeout",
			err:  status.Error(codes.DeadlineExceeded, "timeout"),
			want: generateErrTimeout,
		},
		{
			name: "unavailable is transient",
			err:  status.Error(codes.Unavailable, "backend unavailable"),
			want: generateErrTransient,
		},
		{
			name: "wrapped permission denied is permanent",
			err:  errors.Join(assertError("outer"), status.Error(codes.PermissionDenied, "denied")),
			want: generateErrPermanent,
		},
		{
			name: "http json permanent marker is permanent",
			err:  assertError("python llm returned 400: bad embed request (permanent)"),
			want: generateErrPermanent,
		},
		{
			name: "http json invalid embed code is permanent",
			err:  assertError("python llm returned 400: prompt rejected (INVALID_EMBED_REQUEST)"),
			want: generateErrPermanent,
		},
		{
			name: "structured output invalid json remains transient",
			err:  assertError("structured output invalid: vertex structured output returned invalid JSON"),
			want: generateErrTransient,
		},
		{
			name: "typed structured failure overrides invalid argument wrapper",
			err:  status.Error(codes.InvalidArgument, `{"error":{"code":"STRUCTURED_FAILED","message":"structured output returned invalid JSON"}}`),
			want: generateErrTransient,
		},
		{
			name: "typed invalid prompt overrides internal wrapper",
			err:  status.Error(codes.Internal, `{"error":{"code":"INVALID_PROMPT","message":"prompt is required"}}`),
			want: generateErrPermanent,
		},
		{
			name: "http json structured failed marker is transient",
			err:  assertError("python llm returned 500: structured output returned invalid JSON (STRUCTURED_FAILED)"),
			want: generateErrTransient,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyLLMError(tc.err); got != tc.want {
				t.Fatalf("classifyLLMError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestLLMHandlerHandleEmbed(t *testing.T) {
	t.Run("rejects non-post methods", func(t *testing.T) {
		handler := NewLLMHandler(&stubLLMHTTPClient{})
		req := httptest.NewRequest(http.MethodGet, "/llm/embed", nil)
		rec := httptest.NewRecorder()

		handler.HandleEmbed(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected status 405, got %d", rec.Code)
		}
	})

	t.Run("rejects invalid json body", func(t *testing.T) {
		handler := NewLLMHandler(&stubLLMHTTPClient{})
		req := httptest.NewRequest(http.MethodPost, "/llm/embed", strings.NewReader(`{`))
		rec := httptest.NewRecorder()

		handler.HandleEmbed(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("rejects missing text from content", func(t *testing.T) {
		handler := NewLLMHandler(&stubLLMHTTPClient{})
		req := httptest.NewRequest(http.MethodPost, "/llm/embed", strings.NewReader(`{"text":"","content":{"kind":"other","text":""}}`))
		rec := httptest.NewRecorder()

		handler.HandleEmbed(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("rejects embed when backend unavailable", func(t *testing.T) {
		handler := NewLLMHandler(&stubLLMGenerateOnlyClient{})
		req := httptest.NewRequest(http.MethodPost, "/llm/embed", strings.NewReader(`{"text":"policy"}`))
		rec := httptest.NewRecorder()

		handler.HandleEmbed(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected status 503, got %d", rec.Code)
		}
	})

	t.Run("accepts text from content payload", func(t *testing.T) {
		client := &stubLLMHTTPClient{
			embedResp: &llmv1.EmbedResponse{
				Embedding:  []float32{0.1, 0.2, 0.3},
				TokenCount: 7,
				ModelUsed:  "test-model",
				LatencyMs:  12,
			},
		}
		handler := NewLLMHandler(client)

		req := httptest.NewRequest(http.MethodPost, "/llm/embed", bytes.NewBufferString(`{
			"text": "",
			"model": "standard",
			"taskType": "RETRIEVAL_QUERY",
			"latencyBudgetMs": 12000,
			"content": { "kind": "text", "text": "rlhf" }
		}`))
		rec := httptest.NewRecorder()

		handler.HandleEmbed(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if client.lastReq == nil || client.lastReq.GetText() != "rlhf" {
			t.Fatalf("expected embed request text to be sourced from content.text, got %#v", client.lastReq)
		}
		if got := client.lastReq.GetLatencyBudgetMs(); got != 12000 {
			t.Fatalf("expected embed latency budget %d, got %d", 12000, got)
		}
	})

	t.Run("rejects missing text", func(t *testing.T) {
		handler := NewLLMHandler(&stubLLMHTTPClient{})
		req := httptest.NewRequest(http.MethodPost, "/llm/embed", bytes.NewBufferString(`{"content":{"kind":"fileUri","fileUri":"gs://x","mimeType":"text/plain"}}`))
		rec := httptest.NewRecorder()

		handler.HandleEmbed(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("returns bad gateway when backend fails", func(t *testing.T) {
		client := &stubLLMHTTPClient{
			embedErr: assertError("backend failed"),
		}
		handler := NewLLMHandler(client)

		req := httptest.NewRequest(http.MethodPost, "/llm/embed", strings.NewReader(`{"text":"policy"}`))
		rec := httptest.NewRecorder()

		handler.HandleEmbed(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected status 502, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Embedding failed") {
			t.Fatalf("expected embedding failure response, got %s", strings.TrimSpace(rec.Body.String()))
		}
	})

	t.Run("maps rate limit and timeout backend errors to transport-aware statuses", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			err    error
			status int
			kind   string
		}{
			{
				name:   "rate limit",
				err:    status.Error(codes.ResourceExhausted, "quota exceeded"),
				status: http.StatusTooManyRequests,
				kind:   string(generateErrRateLimit),
			},
			{
				name:   "timeout",
				err:    status.Error(codes.DeadlineExceeded, "deadline exceeded"),
				status: http.StatusGatewayTimeout,
				kind:   string(generateErrTimeout),
			},
			{
				name:   "invalid argument is permanent bad request",
				err:    status.Error(codes.InvalidArgument, "bad embed request"),
				status: http.StatusBadRequest,
				kind:   string(generateErrPermanent),
			},
			{
				name:   "http json permanent marker stays bad request",
				err:    assertError("python llm returned 400: bad embed request (permanent)"),
				status: http.StatusBadRequest,
				kind:   string(generateErrPermanent),
			},
			{
				name:   "http json invalid embed code stays bad request",
				err:    assertError("python llm returned 400: prompt rejected (INVALID_EMBED_REQUEST)"),
				status: http.StatusBadRequest,
				kind:   string(generateErrPermanent),
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				client := &stubLLMHTTPClient{embedErr: tc.err}
				handler := NewLLMHandler(client)

				req := httptest.NewRequest(http.MethodPost, "/llm/embed", strings.NewReader(`{"text":"policy"}`))
				rec := httptest.NewRecorder()

				handler.HandleEmbed(rec, req)

				if rec.Code != tc.status {
					t.Fatalf("expected status %d, got %d", tc.status, rec.Code)
				}
				var payload map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
					t.Fatalf("expected JSON embedding failure response, got %s", strings.TrimSpace(rec.Body.String()))
				}
				if !strings.Contains(fmt.Sprint(payload["error"]), "Embedding failed") {
					t.Fatalf("expected embedding failure response, got %#v", payload)
				}
				if payload["kind"] != tc.kind {
					t.Fatalf("expected error kind %q, got %#v", tc.kind, payload["kind"])
				}
			})
		}
	})
}

func TestLLMHandlerHandleEmbedBatch(t *testing.T) {
	t.Run("rejects non-post methods", func(t *testing.T) {
		handler := NewLLMHandler(&stubLLMHTTPClient{})
		req := httptest.NewRequest(http.MethodGet, "/llm/embed/batch", nil)
		rec := httptest.NewRecorder()

		handler.HandleEmbedBatch(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected status 405, got %d", rec.Code)
		}
	})

	t.Run("rejects invalid json body", func(t *testing.T) {
		handler := NewLLMHandler(&stubLLMHTTPClient{})
		req := httptest.NewRequest(http.MethodPost, "/llm/embed/batch", strings.NewReader(`{`))
		rec := httptest.NewRecorder()

		handler.HandleEmbedBatch(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("rejects missing texts", func(t *testing.T) {
		handler := NewLLMHandler(&stubLLMHTTPClient{})
		req := httptest.NewRequest(http.MethodPost, "/llm/embed/batch", strings.NewReader(`{"texts":[]}`))
		rec := httptest.NewRecorder()

		handler.HandleEmbedBatch(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("rejects blank texts", func(t *testing.T) {
		handler := NewLLMHandler(&stubLLMHTTPClient{})
		req := httptest.NewRequest(http.MethodPost, "/llm/embed/batch", strings.NewReader(`{"texts":["ok","   "]}`))
		rec := httptest.NewRecorder()

		handler.HandleEmbedBatch(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("returns embeddings from backend", func(t *testing.T) {
		client := &stubLLMHTTPClient{
			embedBatchResp: &llmv1.EmbedBatchResponse{
				Embeddings: []*llmv1.EmbedVector{
					{Values: []float32{0.1, 0.2}, TokenCount: 2},
					{Values: []float32{0.3, 0.4}, TokenCount: 3},
				},
				ModelUsed: "text-embedding-test",
				LatencyMs: 42,
			},
		}
		handler := NewLLMHandler(client)

		req := httptest.NewRequest(http.MethodPost, "/llm/embed/batch", bytes.NewBufferString(`{
			"texts": [" first ", "second"],
			"model": "standard",
			"taskType": "RETRIEVAL_DOCUMENT",
			"latencyBudgetMs": 9000
		}`))
		rec := httptest.NewRecorder()

		handler.HandleEmbedBatch(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if client.lastBatchReq == nil {
			t.Fatal("expected batch embed request to reach backend")
		}
		if got := client.lastBatchReq.GetTexts(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
			t.Fatalf("expected trimmed texts, got %#v", got)
		}
		if got := client.lastBatchReq.GetLatencyBudgetMs(); got != 9000 {
			t.Fatalf("expected latency budget 9000, got %d", got)
		}

		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("expected JSON success response, got %s", strings.TrimSpace(rec.Body.String()))
		}
		values := payload["embeddings"].([]any)
		if len(values) != 2 {
			t.Fatalf("expected two embeddings, got %#v", payload["embeddings"])
		}
		if payload["modelUsed"] != "text-embedding-test" {
			t.Fatalf("expected modelUsed, got %#v", payload["modelUsed"])
		}
	})

	t.Run("maps backend failures to typed json errors", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			err    error
			status int
			kind   string
		}{
			{
				name:   "rate limit",
				err:    status.Error(codes.ResourceExhausted, "quota exceeded"),
				status: http.StatusTooManyRequests,
				kind:   string(generateErrRateLimit),
			},
			{
				name:   "timeout",
				err:    status.Error(codes.DeadlineExceeded, "deadline exceeded"),
				status: http.StatusGatewayTimeout,
				kind:   string(generateErrTimeout),
			},
			{
				name:   "http json permanent marker stays bad request",
				err:    assertError("python llm returned 400: bad embed batch request (permanent)"),
				status: http.StatusBadRequest,
				kind:   string(generateErrPermanent),
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				client := &stubLLMHTTPClient{embedBatchErr: tc.err}
				handler := NewLLMHandler(client)

				req := httptest.NewRequest(http.MethodPost, "/llm/embed/batch", strings.NewReader(`{"texts":["a","b"]}`))
				rec := httptest.NewRecorder()

				handler.HandleEmbedBatch(rec, req)

				if rec.Code != tc.status {
					t.Fatalf("expected status %d, got %d", tc.status, rec.Code)
				}
				var payload map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
					t.Fatalf("expected JSON batch embedding failure response, got %s", strings.TrimSpace(rec.Body.String()))
				}
				if !strings.Contains(fmt.Sprint(payload["error"]), "Batch embedding failed") {
					t.Fatalf("expected batch embedding failure response, got %#v", payload)
				}
				if payload["kind"] != tc.kind {
					t.Fatalf("expected error kind %q, got %#v", tc.kind, payload["kind"])
				}
			})
		}
	})
}

func TestRouterRAGEmbedActionCompatibility(t *testing.T) {
	client := &stubLLMHTTPClient{
		embedResp: &llmv1.EmbedResponse{Embedding: []float32{0.9}},
	}
	router := NewRouter(ServerConfig{
		Version:   "test",
		LLMClient: nil,
	})
	_ = router

	llmHandler := NewLLMHandler(client)
	mux := http.NewServeMux()
	mux.HandleFunc("/rag", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "embed" {
			llmHandler.HandleEmbed(w, r)
			return
		}
		WriteError(w, http.StatusNotFound, ErrNotFound, "unsupported rag action", map[string]any{
			"allowedActions": []string{"embed"},
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/rag?action=embed", bytes.NewBufferString(`{"text":"policy"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if client.lastReq == nil || client.lastReq.GetText() != "policy" {
		t.Fatalf("expected embed request to reach LLM handler, got %#v", client.lastReq)
	}
}

func TestRouterRAGEmbedBatchActionCompatibility(t *testing.T) {
	client := &stubLLMHTTPClient{
		embedBatchResp: &llmv1.EmbedBatchResponse{
			Embeddings: []*llmv1.EmbedVector{
				{Values: []float32{0.1, 0.2}},
				{Values: []float32{0.3, 0.4}},
			},
		},
	}
	llmHandler := NewLLMHandler(client)
	mux := http.NewServeMux()
	mux.HandleFunc("/rag", func(w http.ResponseWriter, r *http.Request) {
		action := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
		if action == "embed" {
			llmHandler.HandleEmbed(w, r)
			return
		}
		if action == "embed-batch" || action == "embed_batch" {
			llmHandler.HandleEmbedBatch(w, r)
			return
		}
		WriteError(w, http.StatusNotFound, ErrNotFound, "unsupported rag action", map[string]any{
			"allowedActions": []string{"embed", "embed-batch"},
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/rag?action=embed-batch", bytes.NewBufferString(`{"texts":["alpha","beta"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if client.lastBatchReq == nil || len(client.lastBatchReq.GetTexts()) != 2 {
		t.Fatalf("expected batch embed request to reach LLM handler, got %#v", client.lastBatchReq)
	}
	if client.lastBatchReq.GetTexts()[0] != "alpha" || client.lastBatchReq.GetTexts()[1] != "beta" {
		t.Fatalf("expected batch embed texts to reach LLM handler, got %#v", client.lastBatchReq.GetTexts())
	}
}
