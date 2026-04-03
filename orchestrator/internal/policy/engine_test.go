package policy

import "testing"

func TestEvaluateGuardrailRequiresConfirmationForMedium(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "false")
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
	t.Setenv("WISDEV_RUST_REQUIRED", "false")
	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)
	budget.ToolCallsUsed = budget.MaxToolCalls
	decision := EvaluateGuardrail(cfg, budget, RiskLow, false, 1)
	if decision.Allowed {
		t.Fatal("expected denied action when tool budget exceeded")
	}
}
