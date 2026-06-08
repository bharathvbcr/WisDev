package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type mockLLMClient struct {
	mock.Mock
}

func (m *mockLLMClient) Generate(ctx context.Context, in *llmpb.GenerateRequest, opts ...grpc.CallOption) (*llmpb.GenerateResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmpb.GenerateResponse), args.Error(1)
}

func (m *mockLLMClient) GenerateStream(ctx context.Context, in *llmpb.GenerateRequest, opts ...grpc.CallOption) (llmpb.LLMService_GenerateStreamClient, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(llmpb.LLMService_GenerateStreamClient), args.Error(1)
}

func (m *mockLLMClient) StructuredOutput(ctx context.Context, in *llmpb.StructuredRequest, opts ...grpc.CallOption) (*llmpb.StructuredResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmpb.StructuredResponse), args.Error(1)
}

func (m *mockLLMClient) Embed(ctx context.Context, in *llmpb.EmbedRequest, opts ...grpc.CallOption) (*llmpb.EmbedResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmpb.EmbedResponse), args.Error(1)
}

func (m *mockLLMClient) EmbedBatch(ctx context.Context, in *llmpb.EmbedBatchRequest, opts ...grpc.CallOption) (*llmpb.EmbedBatchResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmpb.EmbedBatchResponse), args.Error(1)
}

func (m *mockLLMClient) Health(ctx context.Context, in *llmpb.HealthRequest, opts ...grpc.CallOption) (*llmpb.HealthResponse, error) {
	args := m.Called(ctx, in)
	return args.Get(0).(*llmpb.HealthResponse), args.Error(1)
}

func TestClient_InjectMetadata(t *testing.T) {
	is := assert.New(t)
	c := &Client{}

	t.Run("basic metadata", func(t *testing.T) {
		ctx := c.injectMetadata(context.Background(), map[string]string{"foo": "bar"})
		is.NotNil(ctx)
	})

	t.Run("with service key", func(t *testing.T) {
		t.Setenv("INTERNAL_SERVICE_KEY", "secret-key")
		ctx := c.injectMetadata(context.Background(), nil)
		is.NotNil(ctx)
	})
}

func TestClient_EnsureClient_Error(t *testing.T) {
	var c *Client
	err := c.ensureClient(context.Background())
	assert.Error(t, err)
	assert.Equal(t, errNilClient, err)
}

func TestClient_Methods(t *testing.T) {
	is := assert.New(t)
	m := new(mockLLMClient)
	c := &Client{}
	c.SetClient(m)

	t.Run("Generate", func(t *testing.T) {
		req := &llmpb.GenerateRequest{Model: "test"}
		m.On("Generate", mock.Anything, req).Return(&llmpb.GenerateResponse{Text: "ok"}, nil)
		resp, err := c.Generate(context.Background(), req)
		is.NoError(err)
		is.Equal("ok", resp.Text)
	})

	t.Run("StructuredOutput", func(t *testing.T) {
		req := &llmpb.StructuredRequest{Model: "test", JsonSchema: `{"type":"object"}`}
		m.On("StructuredOutput", mock.Anything, req).Return(&llmpb.StructuredResponse{JsonResult: "{}"}, nil)
		resp, err := c.StructuredOutput(context.Background(), req)
		is.NoError(err)
		is.Equal("{}", resp.JsonResult)
	})

	t.Run("Embed", func(t *testing.T) {
		req := &llmpb.EmbedRequest{Model: "test"}
		m.On("Embed", mock.Anything, req).Return(&llmpb.EmbedResponse{Embedding: []float32{0.1}}, nil)
		resp, err := c.Embed(context.Background(), req)
		is.NoError(err)
		is.Equal([]float32{0.1}, resp.Embedding)
	})

	t.Run("EmbedBatch", func(t *testing.T) {
		req := &llmpb.EmbedBatchRequest{Model: "test"}
		m.On("EmbedBatch", mock.Anything, req).Return(&llmpb.EmbedBatchResponse{Embeddings: []*llmpb.EmbedVector{{Values: []float32{0.1}}}}, nil)
		resp, err := c.EmbedBatch(context.Background(), req)
		is.NoError(err)
		is.Len(resp.Embeddings, 1)
	})

	t.Run("Health", func(t *testing.T) {
		m.On("Health", mock.Anything, mock.Anything).Return(&llmpb.HealthResponse{Ok: true}, nil)
		resp, err := c.Health(context.Background())
		is.NoError(err)
		is.True(resp.Ok)
	})
}
