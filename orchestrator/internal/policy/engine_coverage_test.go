package policy

import (
	"testing"
)

func TestEvaluateGuardrailHeuristic(t *testing.T) {
	tests := []struct {
		name               string
		risk               RiskLevel
		isScript           bool
		estimatedCostCents int
		setup              func(cfg *PolicyConfig, b *BudgetState)
		wantAllowed        bool
		wantConfirm        bool
	}{
		{
			name:        "low risk",
			risk:        RiskLow,
			wantAllowed: true,
		},
		{
			name: "high risk confirmation",
			risk: RiskHigh,
			setup: func(cfg *PolicyConfig, b *BudgetState) {
				cfg.AlwaysConfirmHighRisk = true
			},
			wantAllowed: true,
			wantConfirm: true,
		},
		{
			name: "tool budget exceeded",
			risk: RiskLow,
			setup: func(cfg *PolicyConfig, b *BudgetState) {
				b.ToolCallsUsed = b.MaxToolCalls
			},
			wantAllowed: false,
		},
		{
			name:     "script budget exceeded",
			risk:     RiskLow,
			isScript: true,
			setup: func(cfg *PolicyConfig, b *BudgetState) {
				b.ScriptRunsUsed = b.MaxScriptRuns
			},
			wantAllowed: false,
		},
		{
			name:               "cost budget exceeded",
			risk:               RiskLow,
			estimatedCostCents: 100,
			setup: func(cfg *PolicyConfig, b *BudgetState) {
				b.CostCentsUsed = b.MaxCostCents
			},
			wantAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultPolicyConfig()
			budget := NewBudgetState(cfg)
			if tt.setup != nil {
				tt.setup(&cfg, &budget)
			}

			got := evaluateGuardrailHeuristic(cfg, budget, tt.risk, tt.isScript, tt.estimatedCostCents)
			if got.Allowed != tt.wantAllowed {
				t.Errorf("%s: got Allowed=%v, want %v. Reason: %s", tt.name, got.Allowed, tt.wantAllowed, got.Reason)
			}
			if got.RequiresConfirmation != tt.wantConfirm {
				t.Errorf("%s: got RequiresConfirmation=%v, want %v", tt.name, got.RequiresConfirmation, tt.wantConfirm)
			}
		})
	}
}

// TestEvaluateGuardrailDispatch covers the EvaluateGuardrail dispatcher.
func TestEvaluateGuardrailDispatch(t *testing.T) {
	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)

	got := EvaluateGuardrail(cfg, budget, RiskLow, false, 0)
	if !got.Allowed {
		t.Errorf("expected heuristic fallback to allow low-risk; got Allowed=false, Reason=%s", got.Reason)
	}
}

func TestApplyBudgetUsageCoverage(t *testing.T) {
	budget := BudgetState{}
	ApplyBudgetUsage(&budget, false, 10)
	if budget.ToolCallsUsed != 1 || budget.CostCentsUsed != 10 {
		t.Errorf("Unexpected budget: %+v", budget)
	}

	ApplyBudgetUsage(&budget, true, 5)
	if budget.ScriptRunsUsed != 1 || budget.CostCentsUsed != 15 {
		t.Errorf("Unexpected budget: %+v", budget)
	}
}

func TestResolveProviderOrderCoverage(t *testing.T) {
	cfg := DefaultPolicyConfig()

	p := ResolveProviderOrder(cfg, "query", "medicine")
	if p[0] != "pubmed" {
		t.Errorf("Expected pubmed for medicine, got %s", p[0])
	}

	p = ResolveProviderOrder(cfg, "transformer", "general")
	if p[0] != "semantic-scholar" {
		t.Errorf("Expected semantic-scholar for transformer, got %s", p[0])
	}

	p = ResolveProviderOrder(cfg, "general", "general")
	if len(p) == 0 {
		t.Error("Expected providers for general")
	}
}
