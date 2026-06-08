package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/metadata"
)

type mockVertexClient struct {
	mock.Mock
}

func (m *mockVertexClient) GenerateText(ctx context.Context, modelID, prompt, systemPrompt string, temperature float32, maxTokens int32) (string, error) {
	args := m.Called(ctx, modelID, prompt, systemPrompt, temperature, maxTokens)
	return args.String(0), args.Error(1)
}

func (m *mockVertexClient) GenerateStructured(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32) (string, error) {
	args := m.Called(ctx, modelID, prompt, systemPrompt, jsonSchemaStr, temperature, maxTokens)
	return args.String(0), args.Error(1)
}

func (m *mockVertexClient) GenerateStructuredWithPolicy(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32, serviceTier string, thinkingBudget *int32, requestClass string, retryProfile string) (string, error) {
	args := m.Called(ctx, modelID, prompt, systemPrompt, jsonSchemaStr, temperature, maxTokens, serviceTier, thinkingBudget, requestClass, retryProfile)
	return args.String(0), args.Error(1)
}

func (m *mockVertexClient) EmbedText(ctx context.Context, modelID, text, taskType string) ([]float32, error) {
	args := m.Called(ctx, modelID, text, taskType)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]float32), args.Error(1)
}

func (m *mockVertexClient) EmbedBatch(ctx context.Context, modelID string, texts []string, taskType string) ([][]float32, error) {
	args := m.Called(ctx, modelID, texts, taskType)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([][]float32), args.Error(1)
}

type mockGenerateStreamServer struct {
	ctx       context.Context
	chunks    []*llmv1.GenerateChunk
	sendErr   error
	sendCount int
}

func (s *mockGenerateStreamServer) Context() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}
func (s *mockGenerateStreamServer) SetHeader(md metadata.MD) error  { return nil }
func (s *mockGenerateStreamServer) SendHeader(md metadata.MD) error { return nil }
func (s *mockGenerateStreamServer) SetTrailer(md metadata.MD)       {}
func (s *mockGenerateStreamServer) SendMsg(m any) error             { return nil }
func (s *mockGenerateStreamServer) RecvMsg(m any) error             { return nil }

func (s *mockGenerateStreamServer) Send(chunk *llmv1.GenerateChunk) error {
	s.sendCount++
	s.chunks = append(s.chunks, chunk)
	return s.sendErr
}

func TestServer(t *testing.T) {
	is := assert.New(t)
	mvc := new(mockVertexClient)
	srv := NewServer(mvc)

	ctx := context.Background()

	t.Run("Generate Success", func(t *testing.T) {
		mvc.On("GenerateText", mock.Anything, "m1", "p1", "s1", float32(0.7), int32(100)).
			Return("resp", nil).Once()

		resp, err := srv.Generate(ctx, &llmv1.GenerateRequest{
			Model: "m1", Prompt: "p1", SystemPrompt: "s1", Temperature: 0.7, MaxTokens: 100,
		})
		is.NoError(err)
		is.Equal("resp", resp.Text)
		is.Equal("m1", resp.ModelUsed)
	})

	t.Run("Generate Error", func(t *testing.T) {
		mvc.On("GenerateText", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return("", errors.New("fail")).Once()

		_, err := srv.Generate(ctx, &llmv1.GenerateRequest{})
		is.Error(err)
	})

	t.Run("Embed Success", func(t *testing.T) {
		mvc.On("EmbedText", mock.Anything, "m1", "t1", "query").
			Return([]float32{0.1}, nil).Once()

		resp, err := srv.Embed(ctx, &llmv1.EmbedRequest{
			Model: "m1", Text: "t1", TaskType: "query",
		})
		is.NoError(err)
		is.Equal([]float32{0.1}, resp.Embedding)
	})

	t.Run("EmbedBatch Success", func(t *testing.T) {
		mvc.On("EmbedBatch", mock.Anything, "m1", []string{"t1"}, "query").
			Return([][]float32{{0.1}}, nil).Once()

		resp, err := srv.EmbedBatch(ctx, &llmv1.EmbedBatchRequest{
			Model: "m1", Texts: []string{"t1"}, TaskType: "query",
		})
		is.NoError(err)
		is.Len(resp.Embeddings, 1)
		is.Equal([]float32{0.1}, resp.Embeddings[0].Values)
	})

	t.Run("Health", func(t *testing.T) {
		resp, err := srv.Health(ctx, &llmv1.HealthRequest{})
		is.NoError(err)
		is.True(resp.Ok)
	})

	t.Run("StructuredOutput Uses Native Structured Path", func(t *testing.T) {
		schema := `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`
		thinkingBudget := int32(1024)
		mvc.On("GenerateStructuredWithPolicy", mock.Anything, "m1", "p1", "s1", schema, float32(0.2), int32(256), "priority", &thinkingBudget, "structured_high_value", "standard").
			Return(`{"answer":"ok"}`, nil).Once()

		resp, err := srv.StructuredOutput(ctx, &llmv1.StructuredRequest{
			Model:          "m1",
			Prompt:         "p1",
			SystemPrompt:   "s1",
			JsonSchema:     schema,
			Temperature:    0.2,
			MaxTokens:      256,
			ServiceTier:    "priority",
			ThinkingBudget: &thinkingBudget,
			RequestClass:   "structured_high_value",
			RetryProfile:   "standard",
		})
		is.NoError(err)
		is.Equal(`{"answer":"ok"}`, resp.JsonResult)
		is.True(resp.SchemaValid)
		is.Empty(resp.Error)
	})

	t.Run("StructuredOutput Flags Invalid JSON", func(t *testing.T) {
		mvc.On("GenerateStructuredWithPolicy", mock.Anything, "m2", "p2", "", `{"type":"object"}`, float32(0.3), int32(128), "", (*int32)(nil), "", "").
			Return(`not-json`, nil).Once()

		resp, err := srv.StructuredOutput(ctx, &llmv1.StructuredRequest{
			Model:       "m2",
			Prompt:      "p2",
			JsonSchema:  `{"type":"object"}`,
			Temperature: 0.3,
			MaxTokens:   128,
		})
		is.NoError(err)
		is.Equal(`not-json`, resp.JsonResult)
		is.False(resp.SchemaValid)
		is.Equal("vertex structured output returned invalid JSON", resp.Error)
	})

	t.Run("StructuredOutput Rejects Missing Schema", func(t *testing.T) {
		_, err := srv.StructuredOutput(ctx, &llmv1.StructuredRequest{
			Model:  "m3",
			Prompt: "p3",
		})
		is.ErrorIs(err, errStructuredSchemaRequired)
	})

	t.Run("StructuredOutput Rejects Nil Request", func(t *testing.T) {
		_, err := srv.StructuredOutput(ctx, nil)
		is.ErrorIs(err, errStructuredSchemaRequired)
	})

	t.Run("GenerateStream emits chunked deltas", func(t *testing.T) {
		longPayload := strings.Repeat("x", 300)
		mvc.On("GenerateText", mock.Anything, "mstream", "hello", "", float32(0.5), int32(256)).
			Return(longPayload, nil).Once()

		stream := &mockGenerateStreamServer{ctx: ctx}
		err := srv.GenerateStream(&llmv1.GenerateRequest{
			Model:        "mstream",
			Prompt:       "hello",
			Temperature:  0.5,
			MaxTokens:    256,
			SystemPrompt: "",
		}, stream)
		is.NoError(err)
		is.GreaterOrEqual(stream.sendCount, 3)
		is.Equal(300, len(stream.chunks[0].Delta)+len(stream.chunks[1].Delta)+len(stream.chunks[2].Delta))
		is.Equal("x", stream.chunks[0].Delta[:1])
		is.Equal("", stream.chunks[0].FinishReason)
		is.False(stream.chunks[0].Done)
		is.True(stream.chunks[len(stream.chunks)-1].Done)
		is.Equal("stop", stream.chunks[len(stream.chunks)-1].FinishReason)
		is.Equal(longPayload, stream.chunks[0].Delta+stream.chunks[1].Delta+stream.chunks[2].Delta)
	})

	t.Run("GenerateStream emits final chunk marker for empty output", func(t *testing.T) {
		mvc.On("GenerateText", mock.Anything, "mempty", "empty", "", float32(0.5), int32(128)).
			Return("", nil).Once()

		stream := &mockGenerateStreamServer{ctx: ctx}
		err := srv.GenerateStream(&llmv1.GenerateRequest{
			Model:       "mempty",
			Prompt:      "empty",
			Temperature: 0.5,
			MaxTokens:   128,
		}, stream)
		is.NoError(err)
		is.Equal(1, stream.sendCount)
		is.Equal("", stream.chunks[0].Delta)
		is.True(stream.chunks[0].Done)
		is.Equal("stop", stream.chunks[0].FinishReason)
	})

	t.Run("GenerateStream Propagates send error", func(t *testing.T) {
		sendErr := errors.New("send failed")
		mvc.On("GenerateText", mock.Anything, "msendfail", "x", "", float32(0.1), int32(32)).
			Return("payload", nil).Once()

		stream := &mockGenerateStreamServer{ctx: ctx, sendErr: sendErr}
		err := srv.GenerateStream(&llmv1.GenerateRequest{
			Model:       "msendfail",
			Prompt:      "x",
			Temperature: 0.1,
			MaxTokens:   32,
		}, stream)
		is.Equal(sendErr, err)
		is.Equal(1, stream.sendCount)
	})

	t.Run("Embed Error", func(t *testing.T) {
		mvc.On("EmbedText", mock.Anything, "membederr", "broken", "query").
			Return(nil, errors.New("embed fail")).Once()

		_, err := srv.Embed(ctx, &llmv1.EmbedRequest{
			Model:    "membederr",
			Text:     "broken",
			TaskType: "query",
		})
		is.Error(err)
	})

	t.Run("EmbedBatch Error", func(t *testing.T) {
		mvc.On("EmbedBatch", mock.Anything, "mbb", []string{"x", "y"}, "query").
			Return(nil, errors.New("batch fail")).Once()

		_, err := srv.EmbedBatch(ctx, &llmv1.EmbedBatchRequest{
			Model:    "mbb",
			Texts:    []string{"x", "y"},
			TaskType: "query",
		})
		is.Error(err)
	})

	t.Run("Generate Error uses server start latency path", func(t *testing.T) {
		mvc.On("GenerateText", mock.Anything, "mfail", "bad", "", float32(0.1), int32(0)).
			Return("", errors.New("generation fail")).Once()

		_, err := srv.Generate(ctx, &llmv1.GenerateRequest{
			Model:       "mfail",
			Prompt:      "bad",
			MaxTokens:   0,
			Temperature: 0.1,
		})
		is.Error(err)
	})
}

func TestServer_ErrorBranches(t *testing.T) {
	mvc := new(mockVertexClient)
	srv := NewServer(mvc)
	ctx := context.Background()

	mvc.On("GenerateText", mock.Anything, "mstreamfail", "bad", "", float32(0.2), int32(64)).
		Return("", errors.New("stream generation fail")).Once()
	err := srv.GenerateStream(&llmv1.GenerateRequest{
		Model:       "mstreamfail",
		Prompt:      "bad",
		Temperature: 0.2,
		MaxTokens:   64,
	}, &mockGenerateStreamServer{ctx: ctx})
	assert.Error(t, err)

	mvc.On("GenerateStructuredWithPolicy", mock.Anything, "mstructfail", "bad", "", `{"type":"object"}`, float32(0.2), int32(64), "", (*int32)(nil), "", "").
		Return("", errors.New("structured generation fail")).Once()
	_, err = srv.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Model:       "mstructfail",
		Prompt:      "bad",
		JsonSchema:  `{"type":"object"}`,
		Temperature: 0.2,
		MaxTokens:   64,
	})
	assert.Error(t, err)
}
