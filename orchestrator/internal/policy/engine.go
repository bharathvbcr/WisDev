package policy

import (
	"strings"
	"time"
)

type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

type PolicyConfig struct {
	PolicyVersion                   string   `json:"policyVersion"`
	AllowLowRiskAutoRun             bool     `json:"allowLowRiskAutoRun"`
	RequireConfirmationForMedium    bool     `json:"requireConfirmationForMedium"`
	AlwaysConfirmHighRisk           bool     `json:"alwaysConfirmHighRisk"`
	MaxToolCallsPerSession          int      `json:"maxToolCallsPerSession"`
	MaxScriptRunsPerSession         int      `json:"maxScriptRunsPerSession"`
	MaxCostPerSessionCents          int      `json:"maxCostPerSessionCents"`
	DefaultProviderPriority         []string `json:"defaultProviderPriority,omitempty"`
	MedicalProviderPriorityOverride []string `json:"medicalProviderPriorityOverride,omitempty"`
	CSProviderPriorityOverride      []string `json:"csProviderPriorityOverride,omitempty"`
}

type BudgetState struct {
	ToolCallsUsed   int   `json:"toolCallsUsed"`
	ScriptRunsUsed  int   `json:"scriptRunsUsed"`
	CostCentsUsed   int   `json:"costCentsUsed"`
	MaxToolCalls    int   `json:"maxToolCalls"`
	MaxScriptRuns   int   `json:"maxScriptRuns"`
	MaxCostCents    int   `json:"maxCostCents"`
	LastUpdatedUnix int64 `json:"lastUpdatedUnix"`
}

type GuardrailDecision struct {
	Allowed              bool
	RequiresConfirmation bool
	Reason               string
}

func DefaultPolicyConfig() PolicyConfig {
	return PolicyConfig{
		PolicyVersion:                   "go-policy-v1",
		AllowLowRiskAutoRun:             true,
		RequireConfirmationForMedium:    true,
		AlwaysConfirmHighRisk:           true,
		MaxToolCallsPerSession:          24,
		MaxScriptRunsPerSession:         1,
		MaxCostPerSessionCents:          50,
		DefaultProviderPriority:         []string{"semantic-scholar", "openalex", "crossref", "pubmed", "arxiv", "clinicaltrials"},
		MedicalProviderPriorityOverride: []string{"pubmed", "clinicaltrials", "semantic-scholar", "openalex", "crossref", "arxiv"},
		CSProviderPriorityOverride:      []string{"semantic-scholar", "arxiv", "openalex", "crossref", "pubmed", "clinicaltrials"},
	}
}

func NewBudgetState(cfg PolicyConfig) BudgetState {
	return BudgetState{
		MaxToolCalls:    cfg.MaxToolCallsPerSession,
		MaxScriptRuns:   cfg.MaxScriptRunsPerSession,
		MaxCostCents:    cfg.MaxCostPerSessionCents,
		LastUpdatedUnix: time.Now().UnixMilli(),
	}
}

func EvaluateGuardrail(cfg PolicyConfig, budget BudgetState, risk RiskLevel, isScript bool, estimatedCostCents int) GuardrailDecision {
	return EvaluateGuardrailWithHints(cfg, budget, risk, isScript, estimatedCostCents, PolicyHints{})
}

func EvaluateGuardrailWithHints(cfg PolicyConfig, budget BudgetState, risk RiskLevel, isScript bool, estimatedCostCents int, hints PolicyHints) GuardrailDecision {
	if decision, blocked := evaluateHardBudgetGuardrail(budget, isScript, estimatedCostCents); blocked {
		return decision
	}
	_ = hints
	return evaluateGuardrailHeuristic(cfg, budget, risk, isScript, estimatedCostCents)
}

func evaluateGuardrailHeuristic(cfg PolicyConfig, budget BudgetState, risk RiskLevel, isScript bool, estimatedCostCents int) GuardrailDecision {
	if decision, blocked := evaluateHardBudgetGuardrail(budget, isScript, estimatedCostCents); blocked {
		return decision
	}

	switch risk {
	case RiskHigh:
		if cfg.AlwaysConfirmHighRisk {
			return GuardrailDecision{Allowed: true, RequiresConfirmation: true, Reason: "high_risk_confirmation_required"}
		}
		return GuardrailDecision{Allowed: true, Reason: "high_risk_allowed"}
	case RiskMedium:
		if cfg.RequireConfirmationForMedium {
			return GuardrailDecision{Allowed: true, RequiresConfirmation: true, Reason: "medium_risk_confirmation_required"}
		}
		return GuardrailDecision{Allowed: true, Reason: "medium_risk_allowed"}
	default:
		if cfg.AllowLowRiskAutoRun {
			return GuardrailDecision{Allowed: true, Reason: "low_risk_auto_allowed"}
		}
		return GuardrailDecision{Allowed: true, RequiresConfirmation: true, Reason: "low_risk_manual_only"}
	}
}

func evaluateHardBudgetGuardrail(budget BudgetState, isScript bool, estimatedCostCents int) (GuardrailDecision, bool) {
	if budget.ToolCallsUsed >= budget.MaxToolCalls {
		return GuardrailDecision{Allowed: false, Reason: "tool_budget_exceeded"}, true
	}
	if isScript && budget.ScriptRunsUsed >= budget.MaxScriptRuns {
		return GuardrailDecision{Allowed: false, Reason: "script_budget_exceeded"}, true
	}
	if estimatedCostCents > 0 && (budget.CostCentsUsed+estimatedCostCents) > budget.MaxCostCents {
		return GuardrailDecision{Allowed: false, Reason: "cost_budget_exceeded"}, true
	}
	return GuardrailDecision{}, false
}

func ApplyBudgetUsage(budget *BudgetState, isScript bool, actualCostCents int) {
	budget.ToolCallsUsed++
	if isScript {
		budget.ScriptRunsUsed++
	}
	if actualCostCents > 0 {
		budget.CostCentsUsed += actualCostCents
	}
	budget.LastUpdatedUnix = time.Now().UnixMilli()
}

func ResolveProviderOrder(cfg PolicyConfig, query string, domainHint string) []string {
	q := strings.ToLower(query)
	d := strings.ToLower(domainHint)
	isMedical := strings.Contains(d, "med") ||
		strings.Contains(q, "clinical") ||
		strings.Contains(q, "patient") ||
		strings.Contains(q, "trial") ||
		strings.Contains(q, "pubmed")
	isCS := strings.Contains(d, "cs") ||
		strings.Contains(d, "ml") ||
		strings.Contains(q, "transformer") ||
		strings.Contains(q, "llm") ||
		strings.Contains(q, "arxiv")
	if isMedical {
		return append([]string{}, cfg.MedicalProviderPriorityOverride...)
	}
	if isCS {
		return append([]string{}, cfg.CSProviderPriorityOverride...)
	}
	return append([]string{}, cfg.DefaultProviderPriority...)
}
