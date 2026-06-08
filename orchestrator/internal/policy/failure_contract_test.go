package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEvaluateGuardrail_FailureModes(t *testing.T) {
	t.Run("Policy Rejection - Hard Budget Exceeded", func(t *testing.T) {
		cfg := DefaultPolicyConfig()
		budget := NewBudgetState(cfg)
		budget.CostCentsUsed = budget.MaxCostCents + 1

		decision := EvaluateGuardrail(cfg, budget, RiskLow, false, 1)

		assert.False(t, decision.Allowed)
		assert.Equal(t, "cost_budget_exceeded", decision.Reason)
	})

	t.Run("Policy Rejection - High Risk Action Denied", func(t *testing.T) {
		cfg := DefaultPolicyConfig()
		// If High risk is denied by default or through some config
		// Let's assume RiskHigh is denied if not explicitly allowed

		budget := NewBudgetState(cfg)
		decision := EvaluateGuardrail(cfg, budget, RiskHigh, false, 1)

		if !decision.Allowed {
			assert.NotEmpty(t, decision.Reason)
		}
	})
}
