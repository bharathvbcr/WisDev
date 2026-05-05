package llm

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
)

type mockLLMServiceClient struct {
	mock.Mock
}

func (m *mockLLMServiceClient) Generate(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (*llmv1.GenerateResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.GenerateResponse), args.Error(1)
}

func (m *mockLLMServiceClient) GenerateStream(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (llmv1.LLMService_GenerateStreamClient, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(llmv1.LLMService_GenerateStreamClient), args.Error(1)
}

func (m *mockLLMServiceClient) StructuredOutput(ctx context.Context, in *llmv1.StructuredRequest, opts ...grpc.CallOption) (*llmv1.StructuredResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.StructuredResponse), args.Error(1)
}

func (m *mockLLMServiceClient) Embed(ctx context.Context, in *llmv1.EmbedRequest, opts ...grpc.CallOption) (*llmv1.EmbedResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.EmbedResponse), args.Error(1)
}

func (m *mockLLMServiceClient) EmbedBatch(ctx context.Context, in *llmv1.EmbedBatchRequest, opts ...grpc.CallOption) (*llmv1.EmbedBatchResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.EmbedBatchResponse), args.Error(1)
}

func (m *mockLLMServiceClient) Health(ctx context.Context, in *llmv1.HealthRequest, opts ...grpc.CallOption) (*llmv1.HealthResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.HealthResponse), args.Error(1)
}

func TestClient(t *testing.T) {
	msc := &mockLLMServiceClient{}
	c := NewClient()
	c.SetClient(msc)

	ctx := context.Background()

	t.Run("Generate Success", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "hi"}, nil).Once()
		resp, err := c.Generate(ctx, &llmv1.GenerateRequest{Prompt: "hello"})
		assert.NoError(t, err)
		assert.Equal(t, "hi", resp.Text)
	})

	t.Run("Generate Error", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		_, err := c.Generate(ctx, &llmv1.GenerateRequest{Prompt: "hello"})
		assert.Error(t, err)
	})

	t.Run("GenerateStream Success", func(t *testing.T) {
		msc.On("GenerateStream", mock.Anything, mock.Anything).Return(new(mockStreamClient), nil).Once()
		_, err := c.GenerateStream(ctx, &llmv1.GenerateRequest{Prompt: "hello"})
		assert.NoError(t, err)
	})

	t.Run("GenerateStream Error", func(t *testing.T) {
		msc.On("GenerateStream", mock.Anything, mock.Anything).Return(nil, errors.New("stream fail")).Once()
		_, err := c.GenerateStream(ctx, &llmv1.GenerateRequest{Prompt: "hello"})
		assert.Error(t, err)
	})

	t.Run("StructuredOutput Success", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: "{}"}, nil).Once()
		resp, err := c.StructuredOutput(ctx, &llmv1.StructuredRequest{Prompt: "hello", JsonSchema: `{"type":"object"}`})
		assert.NoError(t, err)
		assert.Equal(t, "{}", resp.JsonResult)
	})

	t.Run("StructuredOutput Error", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		_, err := c.StructuredOutput(ctx, &llmv1.StructuredRequest{Prompt: "hello", JsonSchema: `{"type":"object"}`})
		assert.Error(t, err)
	})

	t.Run("StructuredOutput Missing Schema", func(t *testing.T) {
		_, err := c.StructuredOutput(ctx, &llmv1.StructuredRequest{Prompt: "hello"})
		assert.ErrorIs(t, err, errStructuredOutputSchemaRequired)
	})

	t.Run("Embed Success", func(t *testing.T) {
		msc.On("Embed", mock.Anything, mock.Anything).Return(&llmv1.EmbedResponse{Embedding: []float32{0.1}}, nil).Once()
		resp, err := c.Embed(ctx, &llmv1.EmbedRequest{Text: "hello"})
		assert.NoError(t, err)
		assert.Len(t, resp.Embedding, 1)
	})

	t.Run("Embed Error", func(t *testing.T) {
		msc.On("Embed", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		_, err := c.Embed(ctx, &llmv1.EmbedRequest{Text: "hello"})
		assert.Error(t, err)
	})

	t.Run("EmbedBatch Success", func(t *testing.T) {
		msc.On("EmbedBatch", mock.Anything, mock.Anything).Return(&llmv1.EmbedBatchResponse{
			Embeddings: []*llmv1.EmbedVector{{Values: []float32{0.1}}},
		}, nil).Once()
		resp, err := c.EmbedBatch(ctx, &llmv1.EmbedBatchRequest{Texts: []string{"hello"}})
		assert.NoError(t, err)
		assert.Len(t, resp.Embeddings, 1)
	})

	t.Run("EmbedBatch Error", func(t *testing.T) {
		msc.On("EmbedBatch", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		_, err := c.EmbedBatch(ctx, &llmv1.EmbedBatchRequest{Texts: []string{"hello"}})
		assert.Error(t, err)
	})

	t.Run("Health Success", func(t *testing.T) {
		msc.On("Health", mock.Anything, mock.Anything).Return(&llmv1.HealthResponse{Ok: true}, nil).Once()
		resp, err := c.Health(ctx)
		assert.NoError(t, err)
		assert.True(t, resp.Ok)
	})

	t.Run("Health Error", func(t *testing.T) {
		msc.On("Health", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		_, err := c.Health(ctx)
		assert.Error(t, err)
	})

	t.Run("Close", func(t *testing.T) {
		err := c.Close()
		assert.NoError(t, err)
	})
}

func TestWarmUpWithRetryLogsTransientProbeFailureAsInfo(t *testing.T) {
	var logBuffer bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() {
		slog.SetDefault(originalLogger)
	})

	msc := &mockLLMServiceClient{}
	c := NewClient()
	c.SetClient(msc)

	msc.On("Health", mock.Anything, mock.Anything).Return(nil, errors.New("sidecar booting")).Once()
	msc.On("Health", mock.Anything, mock.Anything).Return(&llmv1.HealthResponse{Ok: true, Version: "test"}, nil).Once()

	err := c.WarmUpWithRetry(context.Background(), 2)

	assert.NoError(t, err)
	logs := logBuffer.String()
	assert.Contains(t, logs, "warm-up probe failed; retry pending")
	assert.NotContains(t, strings.ToUpper(logs), `"LEVEL":"WARN"`)
}

type mockStreamClient struct {
	llmv1.LLMService_GenerateStreamClient
}

func TestStructuredOutputDirect(t *testing.T) {
	// When VertexDirect is set, StructuredOutput routes to structuredOutputDirect
	// → generateStructuredWithTokens (native Gemini SDK), NOT the sidecar gRPC client.
	ctx := context.Background()
	jsonResp := `{"suggestedDomains":["cs"],"complexity":"moderate","intent":"broad_topic","methodologyHints":[],"reasoning":"RLHF is a CS topic."}`

	t.Run("routes to VertexDirect when set", func(t *testing.T) {
		mockModels := new(mockGenAIModels)
		vc := &VertexClient{client: mockModels}
		c := NewClient()
		c.VertexDirect = vc

		mockModels.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(&genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{
					{Content: &genai.Content{Parts: []*genai.Part{{Text: jsonResp}}}},
				},
			}, nil).Once()

		resp, err := c.StructuredOutput(ctx, &llmv1.StructuredRequest{
			Prompt:     "select domains for RLHF query",
			JsonSchema: `{"type":"object","properties":{"suggestedDomains":{"type":"array","items":{"type":"string"}}}}`,
			Model:      "gemini-2.5-flash-lite",
		})
		assert.NoError(t, err)
		assert.Equal(t, jsonResp, resp.JsonResult)
		assert.True(t, resp.SchemaValid)
		mockModels.AssertExpectations(t)
	})

	t.Run("falls back to sidecar when vertex direct rejects unsupported parameter", func(t *testing.T) {
		mockModels := new(mockGenAIModels)
		sidecar := &mockLLMServiceClient{}
		vc := &VertexClient{client: mockModels}
		c := NewClient()
		c.VertexDirect = vc
		c.SetClient(sidecar)

		req := &llmv1.StructuredRequest{
			Prompt:     "select domains for RLHF query",
			JsonSchema: `{"type":"object","properties":{"suggestedDomains":{"type":"array","items":{"type":"string"}}}}`,
			Model:      "gemini-2.5-flash-lite",
		}

		mockModels.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(nil, errors.New("serviceTier parameter is not supported in Vertex AI")).Once()
		sidecar.On("StructuredOutput", mock.Anything, req).
			Return(&llmv1.StructuredResponse{JsonResult: jsonResp, SchemaValid: true}, nil).Once()

		resp, err := c.StructuredOutput(ctx, req)
		assert.NoError(t, err)
		assert.Equal(t, jsonResp, resp.JsonResult)
		assert.True(t, resp.SchemaValid)
		mockModels.AssertExpectations(t)
		sidecar.AssertExpectations(t)
	})

	t.Run("VertexDirect nil falls back to sidecar and fails without connection", func(t *testing.T) {
		c := &Client{transport: transportGRPC}
		c.VertexDirect = nil
		_, err := c.StructuredOutput(ctx, &llmv1.StructuredRequest{
			Prompt:     "test",
			Model:      "gemini-2.5-flash-lite",
			JsonSchema: `{"type":"object"}`,
		})
		assert.Error(t, err, "should fail without a sidecar connection")
	})
}

func TestShouldFallbackStructuredOutputToSidecar(t *testing.T) {
	t.Run("matches vertex unsupported parameter errors", func(t *testing.T) {
		assert.True(t, shouldFallbackStructuredOutputToSidecar(errors.New("serviceTier parameter is not supported in Vertex AI")))
		assert.True(t, shouldFallbackStructuredOutputToSidecar(errors.New("requestClass parameter is not supported in Vertex AI")))
	})

	t.Run("matches sdk constructor compatibility errors", func(t *testing.T) {
		assert.True(t, shouldFallbackStructuredOutputToSidecar(errors.New("service_tier\n  Extra inputs are not permitted")))
		assert.True(t, shouldFallbackStructuredOutputToSidecar(errors.New("unexpected keyword argument 'service_tier'")))
	})

	t.Run("ignores unrelated errors", func(t *testing.T) {
		assert.False(t, shouldFallbackStructuredOutputToSidecar(nil))
		assert.False(t, shouldFallbackStructuredOutputToSidecar(errors.New("deadline exceeded")))
	})
}

func TestRecoverableStructuredRequestClassification(t *testing.T) {
	assert.True(t, isRecoverableStructuredRequest(&llmv1.StructuredRequest{
		RequestClass: "standard",
		ServiceTier:  "standard",
	}))
	assert.True(t, isRecoverableStructuredRequest(&llmv1.StructuredRequest{
		RequestClass: "standard",
	}))
	assert.False(t, isRecoverableStructuredRequest(&llmv1.StructuredRequest{
		RequestClass: "structured_high_value",
		ServiceTier:  "priority",
	}))
	assert.False(t, isRecoverableStructuredRequest(&llmv1.StructuredRequest{}))
}

func TestStructuredOutputSkipsRecoverableDirectCallDuringCooldown(t *testing.T) {
	vertexProviderRateLimitMu.Lock()
	previousCooldown := vertexProviderRateLimitUntil
	vertexProviderRateLimitUntil = time.Now().Add(time.Minute)
	vertexProviderRateLimitMu.Unlock()
	recoverableStructuredPaceMu.Lock()
	previousLastStart := recoverableStructuredLastStart
	recoverableStructuredLastStart = time.Time{}
	recoverableStructuredPaceMu.Unlock()
	defer func() {
		vertexProviderRateLimitMu.Lock()
		vertexProviderRateLimitUntil = previousCooldown
		vertexProviderRateLimitMu.Unlock()
		recoverableStructuredPaceMu.Lock()
		recoverableStructuredLastStart = previousLastStart
		recoverableStructuredPaceMu.Unlock()
	}()

	c := &Client{
		transport:    transportGRPC,
		VertexDirect: &VertexClient{},
	}
	resp, err := c.StructuredOutput(context.Background(), &llmv1.StructuredRequest{
		Prompt:       "test",
		Model:        "gemini-2.5-flash",
		JsonSchema:   `{"type":"object"}`,
		RequestClass: "standard",
		ServiceTier:  "standard",
	})

	assert.Nil(t, resp)
	assert.ErrorIs(t, err, errStructuredProviderCoolingDown)
}
