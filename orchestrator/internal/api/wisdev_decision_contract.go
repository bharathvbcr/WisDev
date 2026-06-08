package api

import (
	"sort"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

const maxDecisionParallelCandidates = 3

type decisionRequest struct {
	SessionID        string           `json:"sessionId"`
	UserID           string           `json:"userId"`
	Query            string           `json:"query"`
	DomainHint       string           `json:"domainHint"`
	ExecutionMode    string           `json:"executionMode"`
	OperationMode    string           `json:"operationMode"`
	CandidateStepIDs []string         `json:"candidateStepIds"`
	MemoryHints      []string         `json:"memoryHints"`
	Plan             decisionPlanBody `json:"plan"`
}

type decisionPlanBody struct {
	PlanID    string                `json:"planId"`
	Steps     []decisionPlanStep    `json:"steps"`
	LiveState decisionPlanLiveState `json:"liveState"`
}

type decisionPlanLiveState struct {
	CompletedStepIDs []string `json:"completedStepIds"`
	PendingStepIDs   []string `json:"pendingStepIds"`
}

type decisionPlanStep struct {
	ID                   string         `json:"id"`
	Action               string         `json:"action"`
	Label                string         `json:"label"`
	Reason               string         `json:"reason"`
	Rationale            string         `json:"rationale"`
	Risk                 any            `json:"risk"`
	Impact               float64        `json:"impact"`
	ExpectedImpact       float64        `json:"expectedImpact"`
	DependsOnStepIDs     []string       `json:"dependsOnStepIds"`
	Metadata             map[string]any `json:"metadata"`
	ParallelGroup        string         `json:"parallelGroup"`
	VerificationRequired bool           `json:"verificationRequired"`
	Status               string         `json:"status"`
	StepStatus           string         `json:"stepStatus"`
}

func buildDecisionPayload(req decisionRequest, cfg policy.PolicyConfig) map[string]any {
	executionMode := normalizeDecisionExecutionMode(req.ExecutionMode, req.OperationMode)
	readySteps := collectReadyDecisionSteps(req.Plan)
	selectedCandidates := resolveDecisionCandidates(req, readySteps, executionMode)
	selectedStep := chooseDecisionStep(selectedCandidates)
	verificationGateStep := findVerificationGateStep(readySteps)
	verificationRedirected := false
	if verificationGateStep != nil && (selectedStep == nil || selectedStep.ID != verificationGateStep.ID) {
		selectedStep = verificationGateStep
		selectedCandidates = []decisionPlanStep{*verificationGateStep}
		verificationRedirected = true
	}

	riskLevel := decisionStepRisk(selectedStep)
	requiresConfirmation := false
	guardrailReason := ""
	nextActions := []string{}
	if selectedStep != nil && executionMode == "guided" {
		guardrail := policy.EvaluateGuardrail(cfg, policy.NewBudgetState(cfg), riskLevel, false, 0)
		requiresConfirmation = guardrail.RequiresConfirmation
		guardrailReason = guardrail.Reason
		if (selectedStep.VerificationRequired || verificationRedirected) && !requiresConfirmation {
			requiresConfirmation = true
			guardrailReason = "medium_risk_confirmation_required"
		}
		if requiresConfirmation {
			nextActions = wisdev.ConfirmationActions()
		}
	}

	selectedStepID := ""
	selectedTool := ""
	rationale := "Go native decision logic did not find a ready step."
	confidence := 0.42
	if selectedStep != nil {
		selectedStepID = selectedStep.ID
		selectedTool = strings.TrimSpace(selectedStep.Action)
		rationale = decisionRationale(*selectedStep, executionMode, verificationRedirected)
		confidence = 0.88
		if len(selectedCandidates) > 1 {
			confidence = 0.83
		}
	}
	parallelIDs := decisionParallelStepIDs(selectedCandidates, executionMode)
	if verificationRedirected {
		parallelIDs = nil
	}

	explainability := map[string]any{
		"executionMode":          executionMode,
		"policyVersion":          cfg.PolicyVersion,
		"decisionSource":         "go_decide_route",
		"readyStepIds":           decisionStepIDs(readySteps),
		"candidateStepIds":       decisionStepIDs(selectedCandidates),
		"memoryHintCount":        len(req.MemoryHints),
		"domainHint":             strings.TrimSpace(req.DomainHint),
		"verificationRedirected": verificationRedirected,
	}

	payload := map[string]any{
		"sessionId":            strings.TrimSpace(req.SessionID),
		"planId":               strings.TrimSpace(req.Plan.PlanID),
		"selectedStepId":       selectedStepID,
		"selectedTool":         selectedTool,
		"rationale":            rationale,
		"confidence":           confidence,
		"risk":                 string(riskLevel),
		"executionMode":        executionMode,
		"operationMode":        resolveOperationMode(req.OperationMode),
		"requiresConfirmation": requiresConfirmation,
		"pendingUserApproval":  requiresConfirmation,
		"explainability":       explainability,
	}
	if len(parallelIDs) > 0 {
		payload["selectedParallelStepIds"] = parallelIDs
	}
	if requiresConfirmation {
		payload["guardrailReason"] = guardrailReason
		payload["nextActions"] = nextActions
	}
	return payload
}

func normalizeDecisionExecutionMode(rawExecutionMode string, rawOperationMode string) string {
	if strings.Contains(strings.ToLower(strings.TrimSpace(rawExecutionMode)), "yolo") {
		return "yolo"
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(rawOperationMode)), "yolo") {
		return "yolo"
	}
	return "guided"
}

func collectReadyDecisionSteps(plan decisionPlanBody) []decisionPlanStep {
	completed := make(map[string]struct{}, len(plan.LiveState.CompletedStepIDs))
	for _, stepID := range plan.LiveState.CompletedStepIDs {
		trimmed := strings.TrimSpace(stepID)
		if trimmed != "" {
			completed[trimmed] = struct{}{}
		}
	}

	ready := make([]decisionPlanStep, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		if stepIsCompleted(step, completed) {
			continue
		}
		deps := decisionStepDependencies(step)
		blocked := false
		for _, dep := range deps {
			if _, ok := completed[dep]; !ok {
				blocked = true
				break
			}
		}
		if !blocked {
			ready = append(ready, step)
		}
	}
	return ready
}

func resolveDecisionCandidates(req decisionRequest, ready []decisionPlanStep, executionMode string) []decisionPlanStep {
	if len(ready) == 0 {
		return nil
	}

	readyIndex := make(map[string]decisionPlanStep, len(ready))
	for _, step := range ready {
		readyIndex[strings.TrimSpace(step.ID)] = step
	}

	candidates := make([]decisionPlanStep, 0, len(ready))
	for _, stepID := range req.CandidateStepIDs {
		if step, ok := readyIndex[strings.TrimSpace(stepID)]; ok {
			candidates = append(candidates, step)
		}
	}
	if len(candidates) == 0 {
		candidates = append(candidates, ready...)
	}

	if executionMode == "guided" {
		return topDecisionCandidates(candidates, 1)
	}
	return topDecisionCandidates(candidates, maxDecisionParallelCandidates)
}

func topDecisionCandidates(steps []decisionPlanStep, limit int) []decisionPlanStep {
	if len(steps) == 0 || limit <= 0 || len(steps) <= limit {
		return steps
	}
	cloned := append([]decisionPlanStep(nil), steps...)
	sort.SliceStable(cloned, func(i, j int) bool {
		return decisionStepImpact(cloned[i]) > decisionStepImpact(cloned[j])
	})
	return cloned[:limit]
}

func chooseDecisionStep(steps []decisionPlanStep) *decisionPlanStep {
	if len(steps) == 0 {
		return nil
	}
	selected := steps[0]
	for _, step := range steps[1:] {
		if decisionStepImpact(step) > decisionStepImpact(selected) {
			selected = step
		}
	}
	return &selected
}

func findVerificationGateStep(steps []decisionPlanStep) *decisionPlanStep {
	for _, step := range steps {
		if step.VerificationRequired {
			copy := step
			return &copy
		}
	}
	return nil
}

func decisionParallelStepIDs(steps []decisionPlanStep, executionMode string) []string {
	if executionMode != "yolo" || len(steps) <= 1 {
		return nil
	}
	return decisionStepIDs(steps)
}

func decisionStepIDs(steps []decisionPlanStep) []string {
	out := make([]string, 0, len(steps))
	for _, step := range steps {
		if trimmed := strings.TrimSpace(step.ID); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func decisionStepImpact(step decisionPlanStep) float64 {
	if step.ExpectedImpact > 0 {
		return step.ExpectedImpact
	}
	if step.Impact > 0 {
		return step.Impact
	}
	return 0
}

func decisionStepRisk(step *decisionPlanStep) policy.RiskLevel {
	if step == nil {
		return policy.RiskLow
	}
	switch raw := step.Risk.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "high":
			return policy.RiskHigh
		case "medium":
			return policy.RiskMedium
		default:
			return policy.RiskLow
		}
	case float64:
		switch {
		case raw >= 0.7:
			return policy.RiskHigh
		case raw >= 0.4:
			return policy.RiskMedium
		default:
			return policy.RiskLow
		}
	default:
		return policy.RiskLow
	}
}

func decisionStepDependencies(step decisionPlanStep) []string {
	if len(step.DependsOnStepIDs) > 0 {
		return uniqueStrings(step.DependsOnStepIDs)
	}
	if step.Metadata == nil {
		return nil
	}
	rawDeps, ok := step.Metadata["dependsOnStepIds"]
	if !ok {
		return nil
	}
	values, ok := rawDeps.([]any)
	if !ok {
		return nil
	}
	deps := make([]string, 0, len(values))
	for _, value := range values {
		if dep := strings.TrimSpace(wisdev.AsOptionalString(value)); dep != "" {
			deps = append(deps, dep)
		}
	}
	return uniqueStrings(deps)
}

func stepIsCompleted(step decisionPlanStep, completed map[string]struct{}) bool {
	if _, ok := completed[strings.TrimSpace(step.ID)]; ok {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(step.Status))
	stepStatus := strings.ToLower(strings.TrimSpace(step.StepStatus))
	return status == "completed" || stepStatus == "completed"
}

func decisionRationale(step decisionPlanStep, executionMode string, verificationRedirected bool) string {
	base := strings.TrimSpace(step.Rationale)
	if base == "" {
		base = strings.TrimSpace(step.Reason)
	}
	if base == "" {
		base = "Go native decision logic selected the next ready step."
	}
	if verificationRedirected {
		return "Verification gate redirected execution to " + strings.TrimSpace(step.ID) + ". " + base
	}
	if executionMode == "yolo" {
		return "Go native decision logic selected the next parallel-capable step. " + base
	}
	return "Go native decision logic selected the next guided step. " + base
}
