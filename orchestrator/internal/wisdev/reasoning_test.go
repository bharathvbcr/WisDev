package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
)

func TestReasoning_ClampFloat(t *testing.T) {
	assert.Equal(t, 0.5, ClampFloat(0.5, 0, 1))
	assert.Equal(t, 0.0, ClampFloat(-0.1, 0, 1))
	assert.Equal(t, 1.0, ClampFloat(1.1, 0, 1))
}

func TestReasoning_ActionImpact(t *testing.T) {
	assert.Equal(t, 0.95, ActionImpact("claim extraction"))
	assert.Equal(t, 0.88, ActionImpact("verify citation"))
	assert.Equal(t, 0.82, ActionImpact("search"))
	assert.Equal(t, 0.74, ActionImpact("plan replan"))
	assert.Equal(t, 0.65, ActionImpact("draft synthesis"))
	assert.Equal(t, 0.6, ActionImpact("unknown"))
}

func TestReasoning_BuildDecisionCandidates(t *testing.T) {

	plan := &PlanState{
		Steps: []PlanStep{
			{ID: "s1", Action: "search", Parallelizable: true, Risk: RiskLevelLow},
			{ID: "s2", Action: "verify", DependsOnStepIDs: []string{"s1"}, Risk: RiskLevelMedium},
			{ID: "s3", Action: "completed", Risk: RiskLevelLow},
		},
		CompletedStepIDs: map[string]bool{"s3": true},
	}
	budget := policy.BudgetState{MaxCostCents: 1000, MaxToolCalls: 10}
	cfg := policy.PolicyConfig{MaxCostPerSessionCents: 1000, AllowLowRiskAutoRun: true}

	candidates := BuildDecisionCandidates(plan, budget, cfg)

	// Only s1 should be ready
	assert.Len(t, candidates, 1)
	assert.Equal(t, "s1", candidates[0].StepID)
	assert.Greater(t, candidates[0].Score, 0.0)
}

func TestReasoning_BuildDecisionCandidatesRejectsOverBudgetBeforePolicyBridge(t *testing.T) {
	plan := &PlanState{
		Steps: []PlanStep{
			{ID: "cheap", Action: "retrieve evidence", Risk: RiskLevelLow, EstimatedCostCents: 5},
			{ID: "expensive", Action: "draft expensive report", Risk: RiskLevelLow, EstimatedCostCents: 500},
		},
	}
	candidates := BuildDecisionCandidates(
		plan,
		policy.BudgetState{MaxCostCents: 100, MaxToolCalls: 10},
		policy.DefaultPolicyConfig(),
	)
	assert.Len(t, candidates, 1)
	assert.Equal(t, "cheap", candidates[0].StepID)
}

func TestReasoning_SelectParallelCandidates(t *testing.T) {
	plan := &PlanState{
		Steps: []PlanStep{
			{ID: "s1", Parallelizable: true},
			{ID: "s2", Parallelizable: true},
			{ID: "s3", Parallelizable: false},
		},
	}
	candidates := []DecisionCandidate{
		{StepID: "s1", RequiresApproval: false},
		{StepID: "s2", RequiresApproval: false},
		{StepID: "s3", RequiresApproval: false},
	}

	selected := SelectParallelCandidates(plan, candidates, 5)
	assert.Len(t, selected, 2)
	assert.Contains(t, selected, "s1")
	assert.Contains(t, selected, "s2")
	assert.NotContains(t, selected, "s3")
}
