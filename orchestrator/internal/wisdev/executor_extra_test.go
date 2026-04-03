package wisdev

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/stretchr/testify/assert"
)

func TestExecutor_Helpers(t *testing.T) {
	os.Setenv("LLM_HEAVY_MODEL", "pro")
	os.Setenv("LLM_BALANCED_MODEL", "balanced")
	os.Setenv("LLM_LIGHT_MODEL", "flash")
	defer func() {
		os.Unsetenv("LLM_HEAVY_MODEL")
		os.Unsetenv("LLM_BALANCED_MODEL")
		os.Unsetenv("LLM_LIGHT_MODEL")
	}()

	t.Run("dependenciesSatisfied", func(t *testing.T) {
		completed := map[string]bool{"s1": true}
		step := PlanStep{DependsOnStepIDs: []string{"s1"}}
		assert.True(t, dependenciesSatisfied(step, completed))
		
		step2 := PlanStep{DependsOnStepIDs: []string{"s2"}}
		assert.False(t, dependenciesSatisfied(step2, completed))
	})

	t.Run("classifyErrorCode", func(t *testing.T) {
		assert.Equal(t, "TOOL_TIMEOUT", classifyErrorCode(errors.New("timeout reached")))
		assert.Equal(t, "TOOL_RATE_LIMIT", classifyErrorCode(errors.New("PY_RATE_LIMIT: busy")))
		assert.Equal(t, "GUARDRAIL_BLOCKED", classifyErrorCode(errors.New("GUARDRAIL_BLOCKED: forbidden")))
		assert.Equal(t, "TOOL_EXEC_FAILED", classifyErrorCode(errors.New("random")))
	})

	t.Run("resolveModelForTier", func(t *testing.T) {
		budget := policy.BudgetState{MaxCostCents: 100, CostCentsUsed: 0}
		assert.Equal(t, llm.ResolveHeavyModel(), resolveModelForTier(ModelTierHeavy, budget))
		
		budget.CostCentsUsed = 90
		assert.Equal(t, llm.ResolveBalancedModel(), resolveModelForTier(ModelTierHeavy, budget))
		
		assert.Equal(t, llm.ResolveLightModel(), resolveModelForTier(ModelTierLight, budget))
	})
	
	t.Run("selectRunnableSteps", func(t *testing.T) {
		ready := []PlanStep{
			{ID: "s1", Parallelizable: true},
			{ID: "s2", Parallelizable: true},
			{ID: "s3", Parallelizable: false},
		}
		res := selectRunnableSteps(ready, 2)
		assert.Len(t, res, 2)
		assert.Equal(t, "s1", res[0].ID)
		
		res2 := selectRunnableSteps(ready[2:], 1)
		assert.Len(t, res2, 1)
		assert.Equal(t, "s3", res2[0].ID)
	})

	t.Run("degradedQuery", func(t *testing.T) {
		assert.Equal(t, "a b c d e f", degradedQuery("a b c d e f g h"))
		assert.Equal(t, "short", degradedQuery("short"))
		assert.Equal(t, "", degradedQuery(""))
	})
}

func TestExecutor_RunStepWithRecovery_Guardrail(t *testing.T) {
	os.Setenv("WISDEV_RUST_REQUIRED", "false")
	defer os.Unsetenv("WISDEV_RUST_REQUIRED")

	pc := policy.PolicyConfig{MaxToolCallsPerSession: 0} // force block
	e := NewPlanExecutor(nil, pc, nil, nil, nil, nil, nil)
	session := &AgentSession{
		Plan: &PlanState{
			StepFailureCount: make(map[string]int),
			ApprovedStepIDs:  make(map[string]bool),
		},
		Budget: policy.NewBudgetState(pc),
	}
	step := PlanStep{ID: "s1", Action: "search", Risk: RiskLevelLow}

	t.Run("Guardrail Blocked", func(t *testing.T) {
		res := e.RunStepWithRecovery(context.Background(), session, step, 0)
		assert.Error(t, res.Err)
		assert.Contains(t, res.Err.Error(), "GUARDRAIL_BLOCKED")
	})
}
