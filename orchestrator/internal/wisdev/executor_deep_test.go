package wisdev

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestExecutor_Execute_Full(t *testing.T) {
	os.Setenv("LLM_HEAVY_MODEL", "pro")
	os.Setenv("LLM_BALANCED_MODEL", "balanced")
	os.Setenv("LLM_LIGHT_MODEL", "flash")
	os.Setenv(allowGoCitationFallbackEnv, "true")
	defer func() {
		os.Unsetenv("LLM_HEAVY_MODEL")
		os.Unsetenv("LLM_BALANCED_MODEL")
		os.Unsetenv("LLM_LIGHT_MODEL")
		os.Unsetenv(allowGoCitationFallbackEnv)
	}()

	// Mock LLM
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	// Policy config that allows everything
	pc := policy.PolicyConfig{
		AllowLowRiskAutoRun:    true,
		MaxCostPerSessionCents: 1000,
		MaxToolCallsPerSession: 100,
	}

	e := NewPlanExecutor(nil, pc, lc, nil, nil, nil, nil)
	e.maxParallelLanes = 1

	t.Run("Linear Success Path", func(t *testing.T) {
		e.pythonExecute = func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{"success": true, "confidence": 0.95}, nil
		}

		session := &AgentSession{
			SessionID:     "linear_success",
			OriginalQuery: "sleep and memory",
			Status:        SessionGeneratingTree,
			Plan: &PlanState{
				PlanID: "p1",
				Steps: []PlanStep{
					{
						ID:              "s1",
						Action:          "search",
						ExecutionTarget: ExecutionTargetPythonCapability,
						Risk:            RiskLevelLow,
					},
					{
						ID:               "s2",
						Action:           "retrieve",
						ExecutionTarget:  ExecutionTargetPythonCapability,
						DependsOnStepIDs: []string{"s1"},
						Risk:             RiskLevelLow,
					},
				},
				CompletedStepIDs: make(map[string]bool),
				FailedStepIDs:    make(map[string]string),
				ApprovedStepIDs:  map[string]bool{"s1": true, "s2": true},
			},
			Budget: policy.NewBudgetState(pc),
		}

		msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "CONTINUE"}, nil)

		out := make(chan PlanExecutionEvent, 100)
		e.Execute(context.Background(), session, out)

		for range out {
		} // consume all

		assert.True(t, session.Plan.CompletedStepIDs["s1"])
		assert.True(t, session.Plan.CompletedStepIDs["s2"])
		assert.Equal(t, SessionComplete, session.Status)
	})

	t.Run("Deadlock and Replan", func(t *testing.T) {
		session := &AgentSession{
			SessionID:     "deadlock_replan",
			OriginalQuery: "sleep and memory",
			Status:        SessionGeneratingTree,
			Plan: &PlanState{
				PlanID: "p_deadlock",
				Steps: []PlanStep{
					{ID: "s1", Action: "search", DependsOnStepIDs: []string{"unknown"}},
				},
				CompletedStepIDs: make(map[string]bool),
				FailedStepIDs:    make(map[string]string),
			},
			Budget: policy.NewBudgetState(pc),
		}

		// Initial Execute will find no ready steps -> trigger replan
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Execute capability research.coordinateReplan")
		})).Return(&llmv1.GenerateResponse{Text: "OK"}, nil).Once()

		// Replan step will also have no ready steps if we don't fix the plan.
		// But let's just see it trigger.

		// Wait, Execute loop for "no ready steps" will add a step then CONTINUE the loop.
		// We need to limit this or it will loop forever in test.
		e.maxReplans = 1

		out := make(chan PlanExecutionEvent, 100)
		// Use a context with timeout to be safe
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		e.Execute(ctx, session, out)
		for range out {
		}

		assert.Equal(t, 1, session.Plan.ReplanCount)
		assert.True(t, len(session.Plan.Steps) > 1)
	})
}
