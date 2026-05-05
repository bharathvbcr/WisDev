package wisdev

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
)

type DecisionCandidate struct {
	StepID           string    `json:"stepId"`
	Action           string    `json:"action"`
	Risk             RiskLevel `json:"risk"`
	Score            float64   `json:"score"`
	ExpectedImpact   float64   `json:"expectedImpact"`
	RequiresApproval bool      `json:"requiresApproval"`
	GuardrailReason  string    `json:"guardrailReason"`
	Rationale        string    `json:"rationale"`
}

func ClampFloat(v float64, min float64, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func riskPenalty(risk RiskLevel) float64 {
	switch risk {
	case RiskLevelHigh:
		return 0.35
	case RiskLevelMedium:
		return 0.15
	default:
		return 0.0
	}
}

func ActionImpact(action string) float64 {
	a := strings.ToLower(strings.TrimSpace(action))
	switch {
	case strings.Contains(a, "claim"), strings.Contains(a, "evidence"):
		return 0.95
	case strings.Contains(a, "verify"), strings.Contains(a, "citation"):
		return 0.88
	case strings.Contains(a, "retrieve"), strings.Contains(a, "search"):
		return 0.82
	case strings.Contains(a, "decompose"), strings.Contains(a, "plan"):
		return 0.74
	case strings.Contains(a, "draft"), strings.Contains(a, "synth"):
		return 0.65
	default:
		return 0.6
	}
}

func countDependents(Plan *PlanState, stepID string) int {
	total := 0
	for _, s := range Plan.Steps {
		for _, dep := range s.DependsOnStepIDs {
			if dep == stepID {
				total++
				break
			}
		}
	}
	return total
}

func BuildDecisionCandidates(Plan *PlanState, budget policy.BudgetState, cfg policy.PolicyConfig) []DecisionCandidate {
	if Plan == nil {
		return nil
	}
	out := make([]DecisionCandidate, 0)
	for _, step := range Plan.Steps {
		if Plan.CompletedStepIDs[step.ID] || Plan.FailedStepIDs[step.ID] != "" {
			continue
		}
		if !dependenciesSatisfied(step, Plan.CompletedStepIDs) {
			continue
		}
		if decisionCandidateExceedsBudget(step, budget) {
			continue
		}

		impact := ActionImpact(step.Action)
		depBonus := math.Min(0.2, float64(countDependents(Plan, step.ID))*0.05)
		parallelBonus := 0.0
		if step.Parallelizable {
			parallelBonus = 0.05
		}
		costPenalty := 0.0
		if step.EstimatedCostCents > 0 && budget.MaxCostCents > 0 {
			costPenalty = ClampFloat(float64(step.EstimatedCostCents)/float64(budget.MaxCostCents), 0, 0.15)
		}
		score := ClampFloat(impact+depBonus+parallelBonus-riskPenalty(step.Risk)-costPenalty, 0, 1)

		guard := policy.EvaluateGuardrail(
			cfg,
			budget,
			ToPolicyRisk(step.Risk),
			step.ExecutionTarget == ExecutionTargetPythonSandbox,
			step.EstimatedCostCents,
		)
		if !guard.Allowed {
			continue
		}
		out = append(out, DecisionCandidate{
			StepID:           step.ID,
			Action:           step.Action,
			Risk:             step.Risk,
			Score:            score,
			ExpectedImpact:   impact,
			RequiresApproval: guard.RequiresConfirmation,
			GuardrailReason:  guard.Reason,
			Rationale: fmt.Sprintf(
				"impact=%.2f dep_bonus=%.2f parallel_bonus=%.2f risk_penalty=%.2f cost_penalty=%.2f",
				impact, depBonus, parallelBonus, riskPenalty(step.Risk), costPenalty,
			),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].StepID < out[j].StepID
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func decisionCandidateExceedsBudget(step PlanStep, budget policy.BudgetState) bool {
	if budget.MaxToolCalls > 0 && budget.ToolCallsUsed >= budget.MaxToolCalls {
		return true
	}
	if step.ExecutionTarget == ExecutionTargetPythonSandbox && budget.MaxScriptRuns > 0 && budget.ScriptRunsUsed >= budget.MaxScriptRuns {
		return true
	}
	if step.EstimatedCostCents > 0 && budget.MaxCostCents > 0 && budget.CostCentsUsed+step.EstimatedCostCents > budget.MaxCostCents {
		return true
	}
	return false
}

func SelectParallelCandidates(Plan *PlanState, candidates []DecisionCandidate, limit int) []string {
	if Plan == nil || len(candidates) == 0 || limit <= 0 {
		return nil
	}
	stepByID := make(map[string]PlanStep, len(Plan.Steps))
	for _, step := range Plan.Steps {
		stepByID[step.ID] = step
	}

	out := make([]string, 0, limit)
	for _, c := range candidates {
		if len(out) >= limit {
			break
		}
		step, ok := stepByID[c.StepID]
		if !ok {
			continue
		}
		if !step.Parallelizable {
			continue
		}
		if c.RequiresApproval {
			continue
		}
		out = append(out, c.StepID)
	}
	return out
}
