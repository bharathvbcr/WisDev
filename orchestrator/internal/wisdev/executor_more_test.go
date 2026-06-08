package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
)

func TestAllStepsTerminal(t *testing.T) {
	plan := &PlanState{
		Steps: []PlanStep{
			{ID: "s1"},
			{ID: "s2"},
		},
		CompletedStepIDs: map[string]bool{"s1": true},
		FailedStepIDs:    map[string]string{"s2": "error"},
	}
	assert.True(t, allStepsTerminal(plan))

	plan.FailedStepIDs = make(map[string]string)
	assert.False(t, allStepsTerminal(plan))
}

func TestResolveModelForTier(t *testing.T) {
	budget := policy.BudgetState{
		MaxCostCents:  100,
		CostCentsUsed: 0,
	}

	t.Run("Heavy", func(t *testing.T) {
		model := resolveModelForTier(ModelTierHeavy, budget)
		assert.NotEmpty(t, model)
	})

	t.Run("Heavy Downgrade", func(t *testing.T) {
		lowBudget := policy.BudgetState{
			MaxCostCents:  100,
			CostCentsUsed: 60, // 40 remaining, < 50
		}
		model := resolveModelForTier(ModelTierHeavy, lowBudget)
		// Should return standard model
		assert.NotEmpty(t, model)
	})

	t.Run("Default", func(t *testing.T) {
		model := resolveModelForTier("", budget)
		assert.NotEmpty(t, model)
	})
}

func TestApplyAutomaticReplan(t *testing.T) {
	e := &PlanExecutor{}
	session := &AgentSession{
		Plan: &PlanState{
			Steps: []PlanStep{},
		},
	}

	replanStep := e.applyAutomaticReplan(session, "s1", "reason")
	assert.Equal(t, 1, session.Plan.ReplanCount)
	assert.Len(t, session.Plan.Steps, 1)
	assert.Equal(t, replanStep.ID, session.Plan.Steps[0].ID)
	assert.Equal(t, "research.coordinateReplan", replanStep.Action)
}

func TestPlanExecutor_Execute_DeadlockReplan(t *testing.T) {

	e := &PlanExecutor{
		maxReplans: 1,
	}
	session := &AgentSession{
		SessionID: "s1",
		Status:    SessionGeneratingTree,
		Plan: &PlanState{
			PlanID: "p1",
			Steps: []PlanStep{
				{ID: "s1", DependsOnStepIDs: []string{"nonexistent"}},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		},
	}

	out := make(chan PlanExecutionEvent, 10)
	// This will trigger applyAutomaticReplan because s1 is not ready and no other steps exist
	// We need to limit the loop because it might loop forever if we don't handle it

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Stop after we see a plan revision
		for ev := range out {
			if ev.Type == EventPlanRevised {
				cancel()
			}
		}
	}()

	e.Execute(ctx, session, out)
	assert.Equal(t, 1, session.Plan.ReplanCount)
}

func TestCoordinateAgentFeedback_NoClient(t *testing.T) {
	e := &PlanExecutor{}
	decision, err := e.CoordinateAgentFeedback(context.Background(), &AgentSession{}, nil)
	assert.NoError(t, err)
	assert.Equal(t, "CONTINUE", decision)
}

func TestRequiresAgentFeedbackMediationIgnoresDegradedPrepSuccess(t *testing.T) {
	assert.False(t, requiresAgentFeedbackMediation([]PlanOutcome{{
		StepID:   "step-01",
		Action:   "research.queryDecompose",
		Success:  true,
		Degraded: true,
	}}))
}

func TestRequiresAgentFeedbackMediationKeepsGroundedEmptyResultCheck(t *testing.T) {
	assert.True(t, requiresAgentFeedbackMediation([]PlanOutcome{{
		StepID:      "step-03",
		Action:      "research.retrievePapers",
		Success:     true,
		ResultCount: 0,
	}}))
}
