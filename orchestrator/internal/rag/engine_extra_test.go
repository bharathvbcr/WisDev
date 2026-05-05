package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
)

type mockLLMService struct {
	mock.Mock
}

func (m *mockLLMService) Generate(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (*llmv1.GenerateResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.GenerateResponse), args.Error(1)
}
func (m *mockLLMService) GenerateStream(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (llmv1.LLMService_GenerateStreamClient, error) {
	return nil, nil
}
func (m *mockLLMService) StructuredOutput(ctx context.Context, in *llmv1.StructuredRequest, opts ...grpc.CallOption) (*llmv1.StructuredResponse, error) {
	return nil, nil
}
func (m *mockLLMService) Embed(ctx context.Context, in *llmv1.EmbedRequest, opts ...grpc.CallOption) (*llmv1.EmbedResponse, error) {
	return nil, nil
}
func (m *mockLLMService) EmbedBatch(ctx context.Context, in *llmv1.EmbedBatchRequest, opts ...grpc.CallOption) (*llmv1.EmbedBatchResponse, error) {
	return nil, nil
}
func (m *mockLLMService) Health(ctx context.Context, in *llmv1.HealthRequest, opts ...grpc.CallOption) (*llmv1.HealthResponse, error) {
	return nil, nil
}

type mockSearchProvider struct {
	search.BaseProvider
	papers []search.Paper
}

func (m *mockSearchProvider) Name() string      { return "mock" }
func (m *mockSearchProvider) Domains() []string { return []string{"general"} }
func (m *mockSearchProvider) Search(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
	return m.papers, nil
}

func TestEngine_Deep(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{papers: []search.Paper{{ID: "p1", Title: "T1", Abstract: "Abs"}}})

	lc := llm.NewClient()
	msc := &mockLLMService{}
	lc.SetClient(msc)

	e := NewEngine(reg, lc)
	ctx := context.Background()

	t.Run("GenerateAnswer - Success", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "Answer [1]"}, nil).Once()
		resp, err := e.GenerateAnswer(ctx, AnswerRequest{Query: "q"})
		assert.NoError(t, err)
		assert.NotEmpty(t, resp.Answer)
		assert.Len(t, resp.Citations, 1)
	})

	t.Run("GenerateAnswer - No Papers", func(t *testing.T) {
		regEmpty := search.NewProviderRegistry()
		eEmpty := NewEngine(regEmpty, lc)
		resp, err := eEmpty.GenerateAnswer(ctx, AnswerRequest{Query: "q"})
		assert.NoError(t, err)
		assert.Contains(t, resp.Answer, "couldn't find")
	})

	t.Run("GenerateAnswer - Synthesis Error", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		_, err := e.GenerateAnswer(ctx, AnswerRequest{Query: "q"})
		assert.Error(t, err)
	})

	t.Run("SelectSectionContext - Success", func(t *testing.T) {
		req := SectionContextRequest{
			SectionName: "Intro",
			SectionGoal: "climate",
			Papers: []search.Paper{
				{ID: "1", Title: "T1", Abstract: "climate change is real"},
				{ID: "2", Title: "T2", Abstract: "other stuff"},
			},
		}
		resp, err := e.SelectSectionContext(ctx, req)
		assert.NoError(t, err)
		assert.Equal(t, "Intro", resp.SectionName)
		assert.NotEmpty(t, resp.SelectedChunks)
	})

	t.Run("SelectSectionContext - No Abstracts", func(t *testing.T) {
		req := SectionContextRequest{Papers: []search.Paper{{ID: "1"}}}
		resp, err := e.SelectSectionContext(ctx, req)
		assert.NoError(t, err)
		assert.Empty(t, resp.SelectedChunks)
	})

	t.Run("MultiAgentExecute - Success", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "MultiAgent Answer"}, nil).Once()
		resp, err := e.MultiAgentExecute(ctx, AnswerRequest{Query: "q"})
		assert.NoError(t, err)
		assert.Equal(t, "MultiAgent Answer", resp.Answer)
	})
}
