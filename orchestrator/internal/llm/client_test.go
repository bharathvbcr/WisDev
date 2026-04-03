package llm

import (
	"context"
	"errors"
	"testing"

	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"

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
func (m *mockLLMServiceClient) GenerateImages(ctx context.Context, in *llmv1.GenerateImagesRequest, opts ...grpc.CallOption) (*llmv1.GenerateImagesResponse, error) {
	return &llmv1.GenerateImagesResponse{}, nil
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
		resp, err := c.StructuredOutput(ctx, &llmv1.StructuredRequest{Prompt: "hello"})
		assert.NoError(t, err)
		assert.Equal(t, "{}", resp.JsonResult)
	})

	t.Run("StructuredOutput Error", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		_, err := c.StructuredOutput(ctx, &llmv1.StructuredRequest{Prompt: "hello"})
		assert.Error(t, err)
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

type mockStreamClient struct {
	llmv1.LLMService_GenerateStreamClient
}
