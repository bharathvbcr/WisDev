package wisdev

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestAutonomousLoop_Run_Edge(t *testing.T) {
	reg := search.NewProviderRegistry()
	mockP := &mockSearchProvider{
		name: "mock_edge",
	}
	reg.Register(mockP)
	reg.SetDefaultOrder([]string{"mock_edge"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	l := NewAutonomousLoop(reg, lc)
	ctx := context.Background()

	t.Run("evaluateSufficiency fail - converged fallback", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Evaluate if the following papers")
		})).Return(nil, errors.New("eval fail")).Twice()

		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Extract the top 2-3")
		})).Return(&llmv1.GenerateResponse{Text: `[]`}, nil).Times(5)

		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
		})).Return(&llmv1.GenerateResponse{Text: "Final"}, nil).Once()

		p1 := make([]search.Paper, 10)
		for i := range p1 {
			p1[i] = search.Paper{ID: fmt.Sprintf("pe%d", i), Title: fmt.Sprintf("Title %d", i), Source: "mock_edge"}
		}
		p2 := make([]search.Paper, 10)
		for i := range p2 {
			p2[i] = search.Paper{ID: fmt.Sprintf("pe%d", i+10), Title: fmt.Sprintf("Title %d", i+10), Source: "mock_edge"}
		}
		
		callCount := 0
		mockP.SearchFunc = func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			callCount++
			if callCount == 1 { return p1, nil }
			return p2, nil
		}

		res, err := l.Run(ctx, LoopRequest{Query: "unique_fallback_query_final_2", MaxIterations: 2})
		assert.NoError(t, err)
		assert.True(t, res.Converged) 
	})

	t.Run("assembleDossier LLM error", func(t *testing.T) {
		papers := []search.Paper{{ID: "p1", Title: "T1", Abstract: "A1"}}
		msc.On("Generate", mock.Anything, mock.Anything).Return(nil, errors.New("fail")).Once()
		res, err := l.assembleDossier(ctx, "q", papers)
		assert.NoError(t, err)
		assert.Empty(t, res)
	})

	t.Run("evaluateSufficiency unmarshal error", func(t *testing.T) {
		papers := []search.Paper{{ID: "p1", Title: "T1"}}
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "invalid json"}, nil).Once()
		_, err := l.evaluateSufficiency(ctx, "q", papers)
		assert.Error(t, err)
	})
	
	t.Run("assembleDossier unmarshal error", func(t *testing.T) {
		papers := []search.Paper{{ID: "p1", Title: "T1"}}
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "invalid json"}, nil).Once()
		res, err := l.assembleDossier(ctx, "q", papers)
		assert.NoError(t, err)
		assert.Empty(t, res)
	})

	t.Run("evaluateSufficiency empty papers", func(t *testing.T) {
		res, err := l.evaluateSufficiency(ctx, "q", nil)
		assert.NoError(t, err)
		assert.False(t, res.Sufficient)
	})

	t.Run("assembleDossier empty papers", func(t *testing.T) {
		res, err := l.assembleDossier(ctx, "q", nil)
		assert.NoError(t, err)
		assert.Nil(t, res)
	})
}
