package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
)

type ragLLMStub struct {
	generateReq    *llmv1.GenerateRequest
	generateResp   *llmv1.GenerateResponse
	generateErr    error
	structuredReq  *llmv1.StructuredRequest
	structuredResp *llmv1.StructuredResponse
	structuredErr  error
}

func (m *ragLLMStub) Generate(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (*llmv1.GenerateResponse, error) {
	m.generateReq = in
	return m.generateResp, m.generateErr
}

func (m *ragLLMStub) GenerateStream(ctx context.Context, in *llmv1.GenerateRequest, opts ...grpc.CallOption) (llmv1.LLMService_GenerateStreamClient, error) {
	return nil, errors.New("unexpected GenerateStream call")
}

func (m *ragLLMStub) StructuredOutput(ctx context.Context, in *llmv1.StructuredRequest, opts ...grpc.CallOption) (*llmv1.StructuredResponse, error) {
	m.structuredReq = in
	return m.structuredResp, m.structuredErr
}

func (m *ragLLMStub) Embed(ctx context.Context, in *llmv1.EmbedRequest, opts ...grpc.CallOption) (*llmv1.EmbedResponse, error) {
	return nil, errors.New("unexpected Embed call")
}

func (m *ragLLMStub) EmbedBatch(ctx context.Context, in *llmv1.EmbedBatchRequest, opts ...grpc.CallOption) (*llmv1.EmbedBatchResponse, error) {
	return nil, errors.New("unexpected EmbedBatch call")
}

func (m *ragLLMStub) Health(ctx context.Context, in *llmv1.HealthRequest, opts ...grpc.CallOption) (*llmv1.HealthResponse, error) {
	return nil, errors.New("unexpected Health call")
}

func newRagLLMClient(stub *ragLLMStub) *llm.Client {
	client := &llm.Client{}
	client.SetClient(stub)
	return client
}

func TestHindsightRefinementAgent(t *testing.T) {
	t.Run("refines the summary and trims the result", func(t *testing.T) {
		stub := &ragLLMStub{
			generateResp: &llmv1.GenerateResponse{Text: " refined answer \n"},
		}
		agent := NewHindsightRefinementAgent(newRagLLMClient(stub))

		got, err := agent.Refine(
			context.Background(),
			"what changed?",
			"current answer",
			[]search.Paper{
				{Title: "Paper A", Abstract: "Abstract A"},
				{Title: "Paper B"},
			},
		)

		assert.NoError(t, err)
		assert.Equal(t, "refined answer", got)
		if assert.NotNil(t, stub.generateReq) {
			assert.Equal(t, llm.ResolveHeavyModel(), stub.generateReq.GetModel())
			assert.InDelta(t, 0.1, stub.generateReq.GetTemperature(), 0.0001)
			assert.Equal(t, "heavy", stub.generateReq.GetRequestClass())
			assert.Equal(t, "standard", stub.generateReq.GetRetryProfile())
			assert.Equal(t, "priority", stub.generateReq.GetServiceTier())
			assert.EqualValues(t, 8192, stub.generateReq.GetThinkingBudget())
			assert.Greater(t, stub.generateReq.GetLatencyBudgetMs(), int32(0))
			assert.Contains(t, stub.generateReq.GetPrompt(), "what changed?")
			assert.Contains(t, stub.generateReq.GetPrompt(), "current answer")
			assert.Contains(t, stub.generateReq.GetPrompt(), "PAPER [1]: Paper A")
			assert.Contains(t, stub.generateReq.GetPrompt(), "ABSTRACT: Abstract A")
			assert.Contains(t, stub.generateReq.GetPrompt(), "PAPER [2]: Paper B")
		}
	})

	t.Run("returns generate errors", func(t *testing.T) {
		stub := &ragLLMStub{generateErr: errors.New("boom")}
		agent := NewHindsightRefinementAgent(newRagLLMClient(stub))

		got, err := agent.Refine(context.Background(), "query", "answer", nil)
		assert.Error(t, err)
		assert.Empty(t, got)
	})

	t.Run("rejects empty generate output", func(t *testing.T) {
		stub := &ragLLMStub{generateResp: &llmv1.GenerateResponse{Text: "   "}}
		agent := NewHindsightRefinementAgent(newRagLLMClient(stub))

		got, err := agent.Refine(context.Background(), "query", "answer", nil)
		assert.Error(t, err)
		assert.Empty(t, got)
		assert.Contains(t, err.Error(), "empty text")
	})
}
