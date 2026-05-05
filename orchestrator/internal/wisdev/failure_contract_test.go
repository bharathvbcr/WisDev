package wisdev

import (
	"context"
	"errors"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestAutonomousLoop_FailureModes_Contract(t *testing.T) {
	ctx := context.Background()
	reg := internalsearch.NewProviderRegistry()

	t.Run("Immediate iteration limit stop", func(t *testing.T) {
		loop := NewAutonomousLoop(reg, nil)
		req := LoopRequest{
			Query:         "test query",
			MaxIterations: 0,
		}

		result, err := loop.Run(ctx, req, nil)
		assert.NoError(t, err)
		assert.Equal(t, 0, result.Iterations)
		// It might be coverage_open if it ends before hitting the limit strictly or because of empty results
		assert.NotEmpty(t, result.StopReason)
	})

	t.Run("LLM Planning Failure (Graceful fallback)", func(t *testing.T) {
		msc := new(mockLLMServiceClient)
		llmClient := llm.NewClient()
		llmClient.SetClient(msc)

		loop := NewAutonomousLoop(reg, llmClient)

		// Mock planning failure
		msc.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, errors.New("planning failed")).Maybe()
		// Mock synthesis (fallback)
		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "fallback answer"}, nil).Maybe()

		req := LoopRequest{
			Query:         "test query",
			MaxIterations: 1,
		}

		// Should still complete or return error depending on how critical planning is
		// In autonomous.go, if initial planning fails, it might return error or fallback.
		result, err := loop.Run(ctx, req, nil)
		if err != nil {
			assert.Contains(t, err.Error(), "planning failed")
		} else {
			assert.NotEmpty(t, result.StopReason)
		}
	})

	t.Run("Missing search registry (Early return)", func(t *testing.T) {
		loop := NewAutonomousLoop(nil, nil)
		req := LoopRequest{
			Query:         "test query",
			MaxIterations: 1,
		}

		_, err := loop.Run(ctx, req, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "search registry is not initialized")
	})
}
