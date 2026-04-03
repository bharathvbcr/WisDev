package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestAutonomousLoop_Run(t *testing.T) {
	// Mock Search
	reg := search.NewProviderRegistry()
	mockP := &mockSearchProvider{
		name: "mock",
		papers: []search.Paper{
			{ID: "p1", Title: "Paper 1", Abstract: "Abstract 1"},
		},
	}
	reg.Register(mockP)

	// Mock LLM
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	l := NewAutonomousLoop(reg, lc)
	ctx := context.Background()

	t.Run("Success Converged", func(t *testing.T) {
		// 1. evaluateSufficiency (Balanced)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.GenerateResponse{Text: `{"sufficient": true, "reasoning": "done"}`}, nil).Once()

		// 2. assembleDossier (Balanced)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.GenerateResponse{Text: `[{"claim": "c1", "snippet": "s1", "confidence": 0.9}]`}, nil).Once()

		// 3. synthesizeWithEvidence (Heavy)
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final Synthesis"}, nil).Once()

		res, err := l.Run(ctx, LoopRequest{Query: "test", MaxIterations: 2})
		assert.NoError(t, err)
		assert.True(t, res.Converged)
		assert.Equal(t, 1, res.Iterations)
		assert.Equal(t, "Final Synthesis", res.FinalAnswer)
		assert.Len(t, res.Evidence, 1)
	})

	t.Run("Multi-iteration Success", func(t *testing.T) {
		// Iteration 1: not sufficient
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.GenerateResponse{Text: `{"sufficient": false, "nextQuery": "refined"}`}, nil).Once()

		// Iteration 2: sufficient
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.GenerateResponse{Text: `{"sufficient": true}`}, nil).Once()

		// assembleDossier
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.GenerateResponse{Text: `[]`}, nil).Once()

		// Final
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Once()

		res, err := l.Run(ctx, LoopRequest{Query: "test", MaxIterations: 5})
		assert.NoError(t, err)
		assert.Equal(t, 2, res.Iterations)
	})

	t.Run("Error synthesis", func(t *testing.T) {
		// evaluateSufficiency
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(&llmv1.GenerateResponse{Text: `{"sufficient": true}`}, nil).Once()

		// assembleDossier
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.GenerateResponse{Text: `[]`}, nil).Once()

		// synthesize fail
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(nil, errors.New("fail")).Once()

		_, err := l.Run(ctx, LoopRequest{Query: "test", MaxIterations: 1})
		assert.Error(t, err)
	})
}
