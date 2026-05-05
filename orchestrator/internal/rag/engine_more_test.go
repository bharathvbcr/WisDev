package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestEngine_Extra(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{papers: []search.Paper{{ID: "p1", Title: "T1", Abstract: "Abs"}}})

	lc := llm.NewClient()
	msc := &mockLLMService{}
	lc.SetClient(msc)

	e := NewEngine(reg, lc)
	ctx := context.Background()

	t.Run("GenerateAnswer - Degraded Success", func(t *testing.T) {
		dctx := resilience.SetDegraded(ctx, true)
		resp, err := e.GenerateAnswer(dctx, AnswerRequest{Query: "q"})
		assert.NoError(t, err)
		assert.Contains(t, resp.Answer, "LLM synthesis is currently unavailable")
		assert.Len(t, resp.Papers, 1)
	})

	t.Run("MultiAgentExecute - Synthesis Error", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(nil, errors.New("synthesis fail")).Once()
		_, err := e.MultiAgentExecute(ctx, AnswerRequest{Query: "q"})
		assert.Error(t, err)
	})

	t.Run("GenerateAnswer - Custom Limit", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "ok"}, nil).Once()
		resp, err := e.GenerateAnswer(ctx, AnswerRequest{Query: "q", Limit: 5})
		assert.NoError(t, err)
		assert.Equal(t, "ok", resp.Answer)
	})

	t.Run("GenerateAnswer - Empty Synthesis Response", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "   "}, nil).Once()
		_, err := e.GenerateAnswer(ctx, AnswerRequest{Query: "q"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty text")
	})
}
