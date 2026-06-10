package wisdev

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
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

func allowGrammarCorrectionClient(t *testing.T, corrected string) (*llm.Client, *mockLLMServiceClient) {
	t.Helper()
	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	prepPayload := map[string]any{
		"corrected_query": corrected,
		"search_query":    corrected,
		"domain":          "medicine",
		"intent":          "medical",
		"keywords":        strings.Fields(strings.ToLower(corrected)),
		"synonyms":        []string{},
		"seed_queries":    []string{corrected, corrected + " systematic review"},
		"agenda_queries":  []string{corrected + " systematic review"},
	}
	prepJSON, err := json.Marshal(prepPayload)
	if err != nil {
		t.Fatalf("marshal prep payload: %v", err)
	}
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil && strings.Contains(req.Prompt, "Prepare this academic research query")
	})).Return(&llmv1.StructuredResponse{JsonResult: string(prepJSON)}, nil).Maybe()
	msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "mock synthesis"}, nil).Maybe()
	msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{
		JsonResult: `{"needsRevision":false,"reasoning":"mock critique","confidence":0.9,"sufficient":true}`,
	}, nil).Maybe()
	return client, msc
}

func TestMockStructuredOutputDirect(t *testing.T) {
	llmClient, _ := allowGrammarCorrectionClient(t, "test")
	_, err := llmClient.StructuredOutput(context.Background(), &llmv1.StructuredRequest{
		Prompt:     "test",
		JsonSchema: "{}",
	})
	if err != nil {
		t.Fatalf("StructuredOutput failed: %v", err)
	}
}
