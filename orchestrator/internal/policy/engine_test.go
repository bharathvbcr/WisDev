package policy

import "testing"

func TestEvaluateGuardrailRequiresConfirmationForMedium(t *testing.T) {
	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)
	decision := EvaluateGuardrail(cfg, budget, RiskMedium, false, 1)
	if !decision.Allowed {
		t.Fatal("expected allowed medium risk action")
	}
	if !decision.RequiresConfirmation {
		t.Fatal("expected confirmation for medium risk action")
	}
}

func TestEvaluateGuardrailRejectsBudgetExceeded(t *testing.T) {
	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)
	budget.ToolCallsUsed = budget.MaxToolCalls
	decision := EvaluateGuardrail(cfg, budget, RiskLow, false, 1)
	if decision.Allowed {
		t.Fatal("expected denied action when tool budget exceeded")
	}
}

func TestEvaluateGuardrailHardBudgetPrecedesPolicyHints(t *testing.T) {
	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)
	budget.CostCentsUsed = budget.MaxCostCents

	decision := EvaluateGuardrailWithHints(cfg, budget, RiskLow, false, 1, PolicyHints{
		ExecutionMode: "yolo",
	})
	if decision.Allowed {
		t.Fatal("expected denied action when local cost budget is exhausted")
	}
	if decision.Reason != "cost_budget_exceeded" {
		t.Fatalf("expected cost_budget_exceeded, got %q", decision.Reason)
	}
}
