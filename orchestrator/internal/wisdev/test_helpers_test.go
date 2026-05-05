package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
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

func assertWisdevStructuredPromptHygiene(t *testing.T, prompt string) {
	t.Helper()
	assert.NotEmpty(t, prompt)
	assert.Contains(t, prompt, wisdevStructuredOutputSchemaInstruction)

	legacyPhrases := []string{
		"Return JSON",
		"Return ONLY JSON",
		"Return a JSON",
		"return a JSON object",
		"Output ONLY a JSON",
		"Respond with JSON",
		"Respond with JSON consensus",
	}
	for _, phrase := range legacyPhrases {
		assert.NotContains(t, prompt, phrase)
	}
}

type mockSearchProvider struct {
	search.BaseProvider
	name             string
	papers           []search.Paper
	SearchFunc       func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error)
	GetCitationsFunc func(ctx context.Context, paperID string, limit int) ([]search.Paper, error)
}

func (m *mockSearchProvider) Name() string      { return m.name }
func (m *mockSearchProvider) Domains() []string { return []string{"general"} }
func (m *mockSearchProvider) Search(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
	if m.SearchFunc != nil {
		return m.SearchFunc(ctx, query, opts)
	}
	return m.papers, nil
}
func (m *mockSearchProvider) GetCitations(ctx context.Context, paperID string, limit int) ([]search.Paper, error) {
	if m.GetCitationsFunc != nil {
		return m.GetCitationsFunc(ctx, paperID, limit)
	}
	return nil, nil
}
func (m *mockSearchProvider) Healthy() bool { return true }
