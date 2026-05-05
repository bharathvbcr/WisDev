package rag

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
)

type synthesisLLMStub struct {
	generateReq    *llmv1.GenerateRequest
	generateResp   *llmv1.GenerateResponse
	generateErr    error
	structuredReq  *llmv1.StructuredRequest
	structuredResp *llmv1.StructuredResponse
	structuredErr  error
}

func (s *synthesisLLMStub) Generate(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (*llmv1.GenerateResponse, error) {
	s.generateReq = in
	return s.generateResp, s.generateErr
}

func (s *synthesisLLMStub) GenerateStream(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (llmv1.LLMService_GenerateStreamClient, error) {
	return nil, nil
}

func (s *synthesisLLMStub) StructuredOutput(ctx context.Context, in *llmv1.StructuredRequest, opts ...grpc.CallOption) (*llmv1.StructuredResponse, error) {
	s.structuredReq = in
	return s.structuredResp, s.structuredErr
}

func (s *synthesisLLMStub) Embed(ctx context.Context, in *llmv1.EmbedRequest, opts ...grpc.CallOption) (*llmv1.EmbedResponse, error) {
	return nil, nil
}

func (s *synthesisLLMStub) EmbedBatch(ctx context.Context, in *llmv1.EmbedBatchRequest, opts ...grpc.CallOption) (*llmv1.EmbedBatchResponse, error) {
	return nil, nil
}

func (s *synthesisLLMStub) Health(ctx context.Context, in *llmv1.HealthRequest, opts ...grpc.CallOption) (*llmv1.HealthResponse, error) {
	return nil, nil
}

func TestEngine_SynthesizeBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("falls back to citation markers when claim extraction fails", func(t *testing.T) {
		stub := &synthesisLLMStub{
			generateResp:  &llmv1.GenerateResponse{Text: "Answer [1] and [2]."},
			structuredErr: errors.New("structured extraction failed"),
		}
		client := llm.NewClient()
		client.SetClient(stub)

		engine := NewEngine(search.NewProviderRegistry(), client)
		papers := []search.Paper{
			{ID: "p1", Title: "Paper One", Abstract: "Background text"},
			{ID: "p2", Title: "Paper Two", Abstract: "Additional background"},
		}

		answer, citations, err := engine.synthesize(ctx, "query", papers, "standard")
		assert.NoError(t, err)
		assert.Equal(t, "Answer [1] and [2].", answer)
		assert.Len(t, citations, 2)
		assert.Equal(t, "p1", citations[0].SourceID)
		assert.Equal(t, "p2", citations[1].SourceID)
		if assert.NotNil(t, stub.generateReq) {
			assert.Equal(t, "standard", stub.generateReq.GetRequestClass())
			assert.Equal(t, "standard", stub.generateReq.GetRetryProfile())
			assert.Equal(t, "standard", stub.generateReq.GetServiceTier())
			assert.EqualValues(t, 1024, stub.generateReq.GetThinkingBudget())
			assert.Greater(t, stub.generateReq.GetLatencyBudgetMs(), int32(0))
		}
	})

	t.Run("builds citations and co-citation edges on structured claims", func(t *testing.T) {
		stub := &synthesisLLMStub{
			generateResp: &llmv1.GenerateResponse{Text: "Synthesized answer."},
			structuredResp: &llmv1.StructuredResponse{
				JsonResult: `{"claims":[{"text":"Graph neural networks improve protein folding.","category":"empirical","reasoning":"matches the first paper"},{"text":"Graph neural networks improve protein docking.","category":"empirical","reasoning":"matches the second paper"}]}`,
			},
		}
		client := llm.NewClient()
		client.SetClient(stub)

		engine := NewEngine(search.NewProviderRegistry(), client)
		papers := []search.Paper{
			{
				ID:       "p1",
				Title:    "Graph neural networks improve protein folding",
				Abstract: "Graph neural networks improve protein folding.",
				Year:     2024,
			},
			{
				ID:       "p2",
				Title:    "Graph neural networks improve protein docking",
				Abstract: "Graph neural networks improve protein docking.",
				Year:     2024,
			},
		}

		answer, citations, err := engine.synthesize(ctx, "query", papers, "standard")
		assert.NoError(t, err)
		assert.Equal(t, "Synthesized answer.", answer)
		assert.Len(t, citations, 2)
		assert.Contains(t, []string{"p1", "p2"}, citations[0].SourceID)
		assert.Contains(t, []string{"p1", "p2"}, citations[1].SourceID)
		assert.Len(t, engine.citationGraph.Edges, 1)
		assert.Equal(t, "p1", engine.citationGraph.Edges[0].SourceID)
		assert.Equal(t, "p2", engine.citationGraph.Edges[0].TargetID)
		if assert.NotNil(t, stub.generateReq) {
			assert.Equal(t, "standard", stub.generateReq.GetRequestClass())
			assert.EqualValues(t, 1024, stub.generateReq.GetThinkingBudget())
		}
	})
}

func TestEngine_GenerateAnswer_GlobalIntent(t *testing.T) {
	stub := &synthesisLLMStub{
		generateResp:  &llmv1.GenerateResponse{Text: "Global answer [1]."},
		structuredErr: errors.New("structured extraction failed"),
	}
	client := llm.NewClient()
	client.SetClient(stub)

	reg := search.NewProviderRegistry()
	for i := 0; i < 6; i++ {
		reg.Register(&mockSearchProvider{
			papers: []search.Paper{
				{
					ID:       fmt.Sprintf("p%d", i+1),
					Title:    fmt.Sprintf("Paper %d", i+1),
					Abstract: "Global query support paper with enough context to retrieve.",
				},
			},
		})
	}

	engine := NewEngine(reg, client)
	resp, err := engine.GenerateAnswer(context.Background(), AnswerRequest{
		Query: "summarize overview landscape",
		Limit: 10,
	})
	assert.NoError(t, err)
	assert.NotNil(t, resp.Metadata)
	assert.True(t, resp.Metadata.GlobalIntent)
	assert.Contains(t, resp.Answer, "Global answer")
}
