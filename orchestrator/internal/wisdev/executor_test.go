package wisdev

import (
	"context"
	"errors"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"

	"github.com/stretchr/testify/assert"
)

func TestDependenciesSatisfied(t *testing.T) {
	step := PlanStep{
		DependsOnStepIDs: []string{"s1", "s2"},
	}

	completed := map[string]bool{"s1": true}
	assert.False(t, dependenciesSatisfied(step, completed))

	completed["s2"] = true
	assert.True(t, dependenciesSatisfied(step, completed))
}

func TestCollectReadySteps(t *testing.T) {
	plan := &PlanState{
		Steps: []PlanStep{
			{ID: "s1"},
			{ID: "s2", DependsOnStepIDs: []string{"s1"}},
			{ID: "s3"},
		},
		CompletedStepIDs: map[string]bool{"s1": true},
		FailedStepIDs:    map[string]string{"s3": "fail"},
	}

	ready := collectReadySteps(plan)
	assert.Len(t, ready, 1)
	assert.Equal(t, "s2", ready[0].ID)
}

func TestSelectRunnableSteps(t *testing.T) {
	ready := []PlanStep{
		{ID: "p1", Parallelizable: true},
		{ID: "p2", Parallelizable: true},
		{ID: "s1", Parallelizable: false},
	}

	runnable := selectRunnableSteps(ready, 2)
	assert.Len(t, runnable, 2)
	assert.Equal(t, "p1", runnable[0].ID)
	assert.Equal(t, "p2", runnable[1].ID)

	runnable2 := selectRunnableSteps(ready[2:], 2)
	assert.Len(t, runnable2, 1)
	assert.Equal(t, "s1", runnable2[0].ID)
}

func TestSelectRunnableStepsUsesLaneBudget(t *testing.T) {
	ready := []PlanStep{
		{ID: "p1", Parallelizable: true},
		{ID: "p2", Parallelizable: true},
		{ID: "p3", Parallelizable: true},
	}
	assert.Len(t, selectRunnableSteps(ready, 1), 1)
	assert.Len(t, selectRunnableSteps(ready, 3), 3)
}

func TestClassifyErrorCode(t *testing.T) {
	assert.Equal(t, "TOOL_TIMEOUT", classifyErrorCode(errors.New("request timeout")))
	assert.Equal(t, "TOOL_RATE_LIMIT", classifyErrorCode(errors.New("429 too many requests")))
	assert.Equal(t, "GUARDRAIL_BLOCKED", classifyErrorCode(errors.New("policy blocked")))
	assert.Equal(t, "TOOL_EXEC_FAILED", classifyErrorCode(errors.New("random error")))
}

func TestDegradedQuery(t *testing.T) {
	assert.Equal(t, "a b c d e f", degradedQuery("a b c d e f g h i"))
	assert.Equal(t, "short query", degradedQuery("short query"))
}

func TestPlanExecutor_Execute_Basic(t *testing.T) {

	e := &PlanExecutor{
		maxParallelLanes: 1,
		maxReplans:       1,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:    true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{"success": true}, nil
		},
	}
	session := &AgentSession{
		SessionID: "s1",
		Status:    SessionGeneratingTree,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID: "p1",
			Steps: []PlanStep{
				{ID: "step1", Action: "search", Reason: "initial search", ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
			ApprovedStepIDs:  make(map[string]bool),
		},
	}

	out := make(chan PlanExecutionEvent, 10)
	e.Execute(context.Background(), session, out)

	var events []PlanExecutionEvent
	for ev := range out {
		events = append(events, ev)
	}

	assert.NotEmpty(t, events)
	assert.Equal(t, SessionComplete, session.Status)
}

func TestPlanExecutor_Execute_StepCompletedPayloadIncludesDegraded(t *testing.T) {

	e := &PlanExecutor{
		maxParallelLanes: 1,
		maxReplans:       0,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:    true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{"success": true}, nil
		},
	}

	session := &AgentSession{
		SessionID: "s2",
		Status:    SessionGeneratingTree,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID: "p2",
			Steps: []PlanStep{
				{ID: "step1", Action: "search", Reason: "initial search", ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow},
			},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
			ApprovedStepIDs:  map[string]bool{},
		},
	}

	out := make(chan PlanExecutionEvent, 10)
	e.Execute(context.Background(), session, out)

	var completed *PlanExecutionEvent
	for ev := range out {
		if ev.Type == EventStepCompleted {
			tmp := ev
			completed = &tmp
		}
	}

	if assert.NotNil(t, completed) {
		_, ok := completed.Payload["degraded"]
		assert.True(t, ok)
		assert.Equal(t, false, completed.Payload["degraded"])
	}
}

func TestPlanExecutor_Execute_FailedSteps(t *testing.T) {

	e := &PlanExecutor{
		maxParallelLanes: 1,
		maxReplans:       0,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun: true,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return nil, errors.New("execution failed")
		},
	}
	session := &AgentSession{
		SessionID: "s1",
		Status:    SessionGeneratingTree,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 100,
		},
		Plan: &PlanState{
			PlanID: "p1",
			Steps: []PlanStep{
				{ID: "step1", Action: "fail_action", MaxAttempts: 1, ExecutionTarget: ExecutionTargetPythonCapability},
			},
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
			ApprovedStepIDs:  make(map[string]bool),
		},
	}

	out := make(chan PlanExecutionEvent, 10)
	e.Execute(context.Background(), session, out)

	for range out {
	} // drain

	assert.Equal(t, SessionFailed, session.Status)
	assert.NotEmpty(t, session.Plan.FailedStepIDs["step1"])
}

func TestPlanExecutor_Execute_ProgressCarriesArchitectureState(t *testing.T) {

	e := &PlanExecutor{
		maxParallelLanes: 1,
		maxReplans:       0,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:    true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{
				"hypotheses": []map[string]any{
					{
						"claim":                   "Primary synthesis path",
						"falsifiabilityCondition": "Counterexample exists",
						"supportScore":            0.82,
					},
				},
			}, nil
		},
	}

	session := &AgentSession{
		SessionID:     "s-arch",
		Status:        SessionGeneratingTree,
		OriginalQuery: "LLM evaluation",
		Mode:          WisDevModeYOLO,
		ServiceTier:   ServiceTierFlex,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID:           "p-arch",
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
			ApprovedStepIDs:  map[string]bool{"step-1": true},
			Steps: []PlanStep{
				{ID: "step-1", Action: "research.generateHypotheses", ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow},
			},
		},
	}

	out := make(chan PlanExecutionEvent, 16)
	e.Execute(context.Background(), session, out)

	var progress *PlanExecutionEvent
	for event := range out {
		if event.Type == EventProgress && event.Payload["reasoningGraph"] != nil {
			tmp := event
			progress = &tmp
		}
	}

	if assert.NotNil(t, progress) {
		assert.Equal(t, WisDevModeYOLO, progress.Payload["mode"])
		assert.Equal(t, ServiceTierFlex, progress.Payload["serviceTier"])
		graph, ok := progress.Payload["reasoningGraph"].(*ReasoningGraph)
		if assert.True(t, ok) {
			assert.NotEmpty(t, graph.Nodes)
		}
		memory, ok := progress.Payload["memoryTiers"].(*MemoryTierState)
		if !ok {
			memoryMap, mapOK := progress.Payload["memoryTiers"].(map[string]any)
			if assert.True(t, mapOK) {
				assert.NotEmpty(t, memoryMap)
			}
		} else {
			assert.NotEmpty(t, memory.ArtifactMemory)
		}
	}
}

func TestPlanExecutor_Guardrail_Blocked(t *testing.T) {

	e := &PlanExecutor{
		policyConfig: policy.PolicyConfig{
			MaxCostPerSessionCents: 0,
		},
	}

	session := &AgentSession{
		Budget: policy.BudgetState{
			MaxCostCents: 0,
		},
		Plan: &PlanState{
			Steps: []PlanStep{{ID: "s1", Action: "expensive", EstimatedCostCents: 100, ExecutionTarget: ExecutionTargetPythonCapability}},
		},
	}

	result, sources, conf, err := e.executeStepOnce(context.Background(), session, session.Plan.Steps[0], false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GUARDRAIL_BLOCKED")
	assert.Empty(t, result)
	assert.Empty(t, sources)
	assert.Equal(t, 0.0, conf)
}

func TestPlanExecutor_Guardrail_Confirmation(t *testing.T) {

	e := &PlanExecutor{
		policyConfig: policy.PolicyConfig{
			AlwaysConfirmHighRisk:  true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
	}

	session := &AgentSession{
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			Steps:           []PlanStep{{ID: "s1", Action: "risky", Risk: RiskLevelHigh, EstimatedCostCents: 100, ExecutionTarget: ExecutionTargetPythonCapability}},
			ApprovedStepIDs: make(map[string]bool),
		},
	}

	_, _, _, err := e.executeStepOnce(context.Background(), session, session.Plan.Steps[0], false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "CONFIRMATION_REQUIRED")

	// Approve and try again
	session.Plan.ApprovedStepIDs["s1"] = true
	e.pythonExecute = func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
		return map[string]any{"success": true}, nil
	}
	_, _, _, err = e.executeStepOnce(context.Background(), session, session.Plan.Steps[0], false)
	assert.NoError(t, err)
}

func TestPlanExecutor_GuidedCheckpoint(t *testing.T) {

	e := &PlanExecutor{
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:    true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{"success": true}, nil
		},
	}

	session := &AgentSession{
		Mode: WisDevModeGuided,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			ApprovedStepIDs: make(map[string]bool),
			Steps: []PlanStep{{
				ID:                      "s1",
				Action:                  "research.verifyCitations",
				Risk:                    RiskLevelLow,
				RequiresHumanCheckpoint: true,
				ExecutionTarget:         ExecutionTargetPythonCapability,
				EstimatedCostCents:      1,
			}},
		},
	}

	_, _, _, err := e.executeStepOnce(context.Background(), session, session.Plan.Steps[0], false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "CONFIRMATION_REQUIRED")

	session.Plan.ApprovedStepIDs["s1"] = true
	_, _, _, err = e.executeStepOnce(context.Background(), session, session.Plan.Steps[0], false)
	assert.NoError(t, err)
}

func TestPlanExecutor_ExecuteStepOnce_ForwardsWisDevPolicyMetadata(t *testing.T) {

	var captured map[string]any
	e := &PlanExecutor{
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:    true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			captured = payload
			return map[string]any{"success": true}, nil
		},
	}

	session := &AgentSession{
		SessionID:   "s-policy",
		Mode:        WisDevModeYOLO,
		ServiceTier: ServiceTierPriority,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID:          "p-policy",
			ApprovedStepIDs: map[string]bool{"s1": true},
			Steps: []PlanStep{{
				ID:                      "s1",
				Action:                  "research.generateHypotheses",
				Risk:                    RiskLevelLow,
				RequiresHumanCheckpoint: true,
				ExecutionTarget:         ExecutionTargetPythonCapability,
				Params: map[string]any{
					"recursiveDepth": 2,
					"multiPath":      true,
				},
			}},
		},
	}

	_, _, _, err := e.executeStepOnce(context.Background(), session, session.Plan.Steps[0], false)
	assert.NoError(t, err)
	if assert.NotNil(t, captured) {
		assert.Equal(t, "yolo", captured["executionMode"])
		assert.Equal(t, "yolo", captured["operationMode"])
		assert.Equal(t, "priority", captured["serviceTier"])
		assert.Equal(t, true, captured["verificationRequired"])
		assert.Equal(t, true, captured["requiresHumanCheckpoint"])
		assert.Equal(t, 2, captured["recursiveDepth"])
		assert.Equal(t, true, captured["multiPath"])
		integrity, ok := captured["academicIntegrity"].(map[string]any)
		if assert.True(t, ok) {
			assert.Equal(t, true, integrity["requireCanonicalBibliography"])
		}
	}
}

func TestPlanExecutor_StoresNormalizedArtifactsOnCompletion(t *testing.T) {

	e := &PlanExecutor{
		maxParallelLanes: 1,
		maxReplans:       0,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:    true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{
				"canonicalSources": []map[string]any{
					{"id": "p1", "title": "Paper 1", "doi": "10.1000/p1"},
				},
				"resolvedCount": 1,
			}, nil
		},
	}

	session := &AgentSession{
		SessionID: "s-artifacts",
		Status:    SessionGeneratingTree,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID:           "p-artifacts",
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
			ApprovedStepIDs:  map[string]bool{},
			StepAttempts:     map[string]int{},
			StepFailureCount: map[string]int{},
			StepConfidences:  map[string]float64{},
			StepArtifacts:    map[string]StepArtifactSet{},
			Steps: []PlanStep{{
				ID:              "step-04",
				Action:          "research.resolveCanonicalCitations",
				Risk:            RiskLevelLow,
				ExecutionTarget: ExecutionTargetPythonCapability,
			}},
		},
	}

	out := make(chan PlanExecutionEvent, 8)
	e.Execute(context.Background(), session, out)
	for range out {
	}

	artifactSet, ok := session.Plan.StepArtifacts["step-04"]
	if assert.True(t, ok) {
		assert.Equal(t, "research.resolveCanonicalCitations", artifactSet.Action)
		if assert.NotNil(t, artifactSet.CitationBundle) {
			assert.Len(t, artifactSet.CitationBundle.CanonicalSources, 1)
			assert.Equal(t, "10.1000/p1", artifactSet.CitationBundle.CanonicalSources[0].DOI)
		}
		assert.NotNil(t, artifactSet.Artifacts["canonicalSources"])
		assert.NotNil(t, artifactSet.Artifacts["citations"])
	}
	if assert.NotNil(t, session.MemoryTiers) {
		assert.NotEmpty(t, session.MemoryTiers.ArtifactMemory)
	}
}

func TestPlanExecutor_StoresReasoningVerificationArtifactsOnCompletion(t *testing.T) {

	e := &PlanExecutor{
		maxParallelLanes: 1,
		maxReplans:       0,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:    true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{
				"branches": []map[string]any{
					{"claim": "Claim A", "supportScore": 0.8},
				},
				"totalBranches":     1,
				"verifiedBranches":  1,
				"rejectedBranches":  0,
				"readyForSynthesis": true,
			}, nil
		},
	}

	session := &AgentSession{
		SessionID: "s-reasoning",
		Status:    SessionGeneratingTree,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID:           "p-reasoning",
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
			ApprovedStepIDs:  map[string]bool{},
			StepAttempts:     map[string]int{},
			StepFailureCount: map[string]int{},
			StepConfidences:  map[string]float64{},
			StepArtifacts:    map[string]StepArtifactSet{},
			Steps: []PlanStep{{
				ID:              "step-08",
				Action:          "research.verifyReasoningPaths",
				Risk:            RiskLevelLow,
				ExecutionTarget: ExecutionTargetPythonCapability,
			}},
		},
	}

	out := make(chan PlanExecutionEvent, 8)
	e.Execute(context.Background(), session, out)
	for range out {
	}

	artifactSet, ok := session.Plan.StepArtifacts["step-08"]
	if assert.True(t, ok) {
		assert.Equal(t, "research.verifyReasoningPaths", artifactSet.Action)
		if assert.NotNil(t, artifactSet.ReasoningBundle) {
			if assert.NotNil(t, artifactSet.ReasoningBundle.Verification) {
				assert.Equal(t, 1, artifactSet.ReasoningBundle.Verification.TotalBranches)
				assert.True(t, artifactSet.ReasoningBundle.Verification.ReadyForSynthesis)
			}
		}
		assert.NotNil(t, artifactSet.Artifacts["reasoningVerification"])
	}
	if assert.NotNil(t, session.MemoryTiers) {
		assert.NotEmpty(t, session.MemoryTiers.ArtifactMemory)
	}
}

func TestPlanExecutor_StoresHypothesisBranchArtifactsOnCompletion(t *testing.T) {

	e := &PlanExecutor{
		maxParallelLanes: 1,
		maxReplans:       0,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:    true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{
				"hypotheses": []any{
					map[string]any{
						"claim":                   "Claim A",
						"falsifiabilityCondition": "Counterexample exists",
						"supportScore":            0.8,
					},
				},
			}, nil
		},
	}

	session := &AgentSession{
		SessionID: "s-hypothesis",
		Status:    SessionGeneratingTree,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID:           "p-hypothesis",
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
			ApprovedStepIDs:  map[string]bool{},
			StepAttempts:     map[string]int{},
			StepFailureCount: map[string]int{},
			StepConfidences:  map[string]float64{},
			StepArtifacts:    map[string]StepArtifactSet{},
			Steps: []PlanStep{{
				ID:              "step-02",
				Action:          "research.generateHypotheses",
				Risk:            RiskLevelLow,
				ExecutionTarget: ExecutionTargetPythonCapability,
			}},
		},
	}

	out := make(chan PlanExecutionEvent, 8)
	e.Execute(context.Background(), session, out)
	for range out {
	}

	artifactSet, ok := session.Plan.StepArtifacts["step-02"]
	if assert.True(t, ok) {
		assert.Equal(t, "research.generateHypotheses", artifactSet.Action)
		if assert.NotNil(t, artifactSet.ReasoningBundle) {
			assert.Len(t, artifactSet.ReasoningBundle.Branches, 1)
			assert.Equal(t, "Claim A", artifactSet.ReasoningBundle.Branches[0].Claim)
		}
		assert.NotNil(t, artifactSet.Artifacts["branches"])
	}
	if assert.NotNil(t, session.MemoryTiers) {
		assert.NotEmpty(t, session.MemoryTiers.ArtifactMemory)
	}
}

func TestPlanExecutor_StoresClaimEvidenceArtifactsOnCompletion(t *testing.T) {

	e := &PlanExecutor{
		maxParallelLanes: 1,
		maxReplans:       0,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:    true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{
				"table":    "| Claim | Evidence |\n|---|---|\n| A | 1 source |",
				"rowCount": 1,
			}, nil
		},
	}

	session := &AgentSession{
		SessionID: "s-claim-table",
		Status:    SessionGeneratingTree,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID:           "p-claim-table",
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
			ApprovedStepIDs:  map[string]bool{},
			StepAttempts:     map[string]int{},
			StepFailureCount: map[string]int{},
			StepConfidences:  map[string]float64{},
			StepArtifacts:    map[string]StepArtifactSet{},
			Steps: []PlanStep{{
				ID:              "step-07",
				Action:          "research.buildClaimEvidenceTable",
				Risk:            RiskLevelLow,
				ExecutionTarget: ExecutionTargetPythonCapability,
			}},
		},
	}

	out := make(chan PlanExecutionEvent, 8)
	e.Execute(context.Background(), session, out)
	for range out {
	}

	artifactSet, ok := session.Plan.StepArtifacts["step-07"]
	if assert.True(t, ok) {
		assert.Equal(t, "research.buildClaimEvidenceTable", artifactSet.Action)
		if assert.NotNil(t, artifactSet.ClaimEvidenceArtifact) {
			assert.Equal(t, 1, artifactSet.ClaimEvidenceArtifact.RowCount)
			assert.Contains(t, artifactSet.ClaimEvidenceArtifact.Table, "Claim")
		}
		claimTable, ok := artifactSet.Artifacts["claimEvidenceTable"].(map[string]any)
		if assert.True(t, ok) {
			assert.Equal(t, 1, claimTable["rowCount"])
		}
	}
	if assert.NotNil(t, session.MemoryTiers) {
		assert.NotEmpty(t, session.MemoryTiers.ArtifactMemory)
	}
}

func TestPlanExecutor_InjectsDependencyArtifactsIntoPayload(t *testing.T) {

	var captured map[string]any
	e := &PlanExecutor{
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:    true,
			MaxToolCallsPerSession: 10,
			MaxCostPerSessionCents: 1000,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			captured = payload
			return map[string]any{"readyForSynthesis": true}, nil
		},
	}

	session := &AgentSession{
		SessionID: "s-forward",
		Mode:      WisDevModeYOLO,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID: "p-forward",
			CompletedStepIDs: map[string]bool{
				"step-02": true,
				"step-04": true,
				"step-07": true,
			},
			FailedStepIDs:   map[string]string{},
			ApprovedStepIDs: map[string]bool{"step-08": true},
			StepArtifacts: map[string]StepArtifactSet{
				"step-02": {
					StepID: "step-02",
					Action: "research.generateHypotheses",
					Artifacts: map[string]any{
						"branches": []any{
							map[string]any{"claim": "A", "supportScore": 0.8},
						},
					},
				},
				"step-04": {
					StepID: "step-04",
					Action: "research.resolveCanonicalCitations",
					Artifacts: map[string]any{
						"canonicalSources": []any{
							map[string]any{"id": "p1", "title": "Paper 1", "doi": "10.1000/p1"},
						},
						"citations": []any{
							map[string]any{"id": "p1", "title": "Paper 1", "doi": "10.1000/p1"},
						},
					},
				},
				"step-07": {
					StepID: "step-07",
					Action: "research.buildClaimEvidenceTable",
					Artifacts: map[string]any{
						"claimEvidenceTable": map[string]any{"table": "| Claim | Evidence |", "rowCount": 1},
					},
				},
			},
		},
	}

	step := PlanStep{
		ID:                      "step-08",
		Action:                  "research.verifyReasoningPaths",
		Risk:                    RiskLevelLow,
		ExecutionTarget:         ExecutionTargetPythonCapability,
		DependsOnStepIDs:        []string{"step-02", "step-04", "step-07"},
		RequiresHumanCheckpoint: true,
	}

	_, _, _, err := e.executeStepOnce(context.Background(), session, step, false)
	assert.NoError(t, err)
	if assert.NotNil(t, captured) {
		assert.NotNil(t, captured["branches"])
		assert.NotNil(t, captured["citations"])
		assert.NotNil(t, captured["claimEvidenceTable"])
		assert.NotNil(t, captured["dependencyArtifacts"])
		deps, ok := captured["dependencyArtifacts"].(map[string]map[string]any)
		assert.True(t, ok)
		assert.NotNil(t, deps["step-02"]["branches"])
	}
}

// ---------------------------------------------------------------------------
// Phase 3: artifact contract tests
// ---------------------------------------------------------------------------

// TestNormalizeStepArtifacts_CitationBundle asserts that a resolveCanonicalCitations
// result produces a typed CitationBundle AND exposes both "canonicalSources" and
// "citations" in the legacy Artifacts map.
func TestNormalizeStepArtifacts_CitationBundle(t *testing.T) {
	step := PlanStep{ID: "cite-01", Action: "research.resolveCanonicalCitations"}
	result := map[string]any{
		"canonicalSources": []map[string]any{
			{"id": "p1", "title": "Paper One", "doi": "10.1000/x", "resolved": true},
		},
		"citations":      []map[string]any{{"id": "p1", "title": "Paper One", "doi": "10.1000/x"}},
		"resolvedCount":  1,
		"duplicateCount": 0,
	}
	set, err := normalizeStepArtifacts(step, result, nil)
	assert.NoError(t, err)

	if assert.NotNil(t, set.CitationBundle, "CitationBundle must not be nil") {
		assert.Len(t, set.CitationBundle.CanonicalSources, 1)
		assert.Equal(t, "10.1000/x", set.CitationBundle.CanonicalSources[0].DOI)
		assert.Equal(t, 1, set.CitationBundle.ResolvedCount)
	}
	assert.NotNil(t, set.Artifacts["canonicalSources"])
	assert.NotNil(t, set.Artifacts["citations"])
}

// TestNormalizeStepArtifacts_VerifyCitations asserts verifiedRecords is in both
// the typed bundle and the legacy map.
func TestNormalizeStepArtifacts_VerifyCitations(t *testing.T) {
	step := PlanStep{ID: "cite-02", Action: "research.verifyCitations"}
	result := map[string]any{
		"verifiedRecords": []map[string]any{
			{"id": "p1", "title": "P1", "doi": "10.1/p1", "verified": true},
			{"id": "p2", "title": "P2", "arxivId": "2301.0001", "verified": false},
		},
		"validCount":     1,
		"invalidCount":   1,
		"duplicateCount": 0,
	}
	set, err := normalizeStepArtifacts(step, result, nil)
	assert.NoError(t, err)

	if assert.NotNil(t, set.CitationBundle) {
		assert.Len(t, set.CitationBundle.VerifiedRecords, 2)
		assert.Equal(t, 1, set.CitationBundle.ValidCount)
		assert.Equal(t, 1, set.CitationBundle.InvalidCount)
	}
	assert.NotNil(t, set.Artifacts["verifiedRecords"])
	assert.NotNil(t, set.Artifacts["citations"])
}

// TestNormalizeStepArtifacts_ReasoningBundle asserts proposeHypotheses produces
// a typed ReasoningBundle with branches AND the legacy "branches" key.
func TestNormalizeStepArtifacts_ReasoningBundle(t *testing.T) {
	step := PlanStep{ID: "reason-01", Action: "research.generateHypotheses"}
	result := map[string]any{
		"hypotheses": []any{
			map[string]any{
				"claim":                   "Claim A",
				"falsifiabilityCondition": "if X then Y",
				"supportScore":            0.8, // key normalizeStepArtifacts reads
				"isTerminated":            false,
			},
		},
	}
	set, err := normalizeStepArtifacts(step, result, nil)
	assert.NoError(t, err)

	if assert.NotNil(t, set.ReasoningBundle) {
		assert.Len(t, set.ReasoningBundle.Branches, 1)
		assert.Equal(t, "Claim A", set.ReasoningBundle.Branches[0].Claim)
		assert.InDelta(t, 0.8, set.ReasoningBundle.Branches[0].SupportScore, 0.001)
	}
	assert.NotNil(t, set.Artifacts["branches"])
}

// TestNormalizeStepArtifacts_VerifyReasoningPaths asserts verification summary
// and branches are in both typed bundle and legacy map.
func TestNormalizeStepArtifacts_VerifyReasoningPaths(t *testing.T) {
	step := PlanStep{ID: "reason-02", Action: "research.verifyReasoningPaths"}
	result := map[string]any{
		"totalBranches":     2,
		"verifiedBranches":  1,
		"rejectedBranches":  1,
		"readyForSynthesis": true,
		"branches": []any{
			map[string]any{"claim": "H1", "supportScore": 0.9},
			map[string]any{"claim": "H2", "supportScore": 0.4},
		},
	}
	set, err := normalizeStepArtifacts(step, result, nil)
	assert.NoError(t, err)

	if assert.NotNil(t, set.ReasoningBundle) {
		if assert.NotNil(t, set.ReasoningBundle.Verification) {
			assert.Equal(t, 2, set.ReasoningBundle.Verification.TotalBranches)
			assert.True(t, set.ReasoningBundle.Verification.ReadyForSynthesis)
		}
	}
	assert.NotNil(t, set.Artifacts["reasoningVerification"])
}

// TestNormalizeStepArtifacts_ClaimEvidence asserts buildClaimEvidenceTable
// produces the typed artifact and "claimEvidenceTable" legacy key.
func TestNormalizeStepArtifacts_ClaimEvidence(t *testing.T) {
	step := PlanStep{ID: "claim-01", Action: "research.buildClaimEvidenceTable"}
	result := map[string]any{
		"table":    "| Claim | Evidence |\n|---|---|\n| A | B |",
		"rowCount": 1,
	}
	set, err := normalizeStepArtifacts(step, result, nil)
	assert.NoError(t, err)

	if assert.NotNil(t, set.ClaimEvidenceArtifact) {
		assert.Equal(t, 1, set.ClaimEvidenceArtifact.RowCount)
		assert.Contains(t, set.ClaimEvidenceArtifact.Table, "Claim")
	}
	claimTable, ok := set.Artifacts["claimEvidenceTable"].(map[string]any)
	if assert.True(t, ok) {
		assert.Equal(t, 1, claimTable["rowCount"])
	}
}

// TestArtifactKeys_TypedPrecedence checks artifactKeys returns typed key names
// alongside legacy aliases without duplicates.
func TestArtifactKeys_TypedPrecedence(t *testing.T) {
	set := StepArtifactSet{
		StepID: "k1",
		Action: "research.resolveCanonicalCitations",
		CitationBundle: &CitationArtifactBundle{
			Citations:        []CanonicalCitation{{Title: "P1", DOI: "10.0/1"}},
			CanonicalSources: []CanonicalCitation{{Title: "P1", DOI: "10.0/1"}},
		},
		Artifacts: map[string]any{
			"citations":        []any{},
			"canonicalSources": []any{},
		},
	}
	keys := artifactKeys(set)
	assert.Contains(t, keys, "citationBundle")
	assert.Contains(t, keys, "citations")
	assert.Contains(t, keys, "canonicalSources")
	seen := map[string]bool{}
	for _, k := range keys {
		assert.False(t, seen[k], "duplicate key: %s", k)
		seen[k] = true
	}
}

func TestValidateEmitterIngressKeys_AcceptsCanonicalKeys(t *testing.T) {
	err := validateEmitterIngressKeys("research.resolveCanonicalCitations", map[string]any{
		"canonicalSources": []any{map[string]any{"id": "p1"}},
		"citations":        []any{map[string]any{"id": "p1"}},
	})
	assert.NoError(t, err)

	err = validateEmitterIngressKeys("research.verifyCitations", map[string]any{
		"verifiedRecords": []any{map[string]any{"id": "p1"}},
	})
	assert.NoError(t, err)

	err = validateEmitterIngressKeys("research.verifyReasoningPaths", map[string]any{
		"branches": []any{map[string]any{"claim": "H1"}},
		"reasoningVerification": map[string]any{
			"totalBranches":     1,
			"verifiedBranches":  1,
			"rejectedBranches":  0,
			"readyForSynthesis": true,
		},
	})
	assert.NoError(t, err)

	err = validateEmitterIngressKeys("research.buildClaimEvidenceTable", map[string]any{
		"claimEvidenceTable": map[string]any{"table": "T", "rowCount": 1},
	})
	assert.NoError(t, err)
}

func TestValidateEmitterIngressKeys_RejectsKeyDrift(t *testing.T) {
	err := validateEmitterIngressKeys("research.resolveCanonicalCitations", map[string]any{
		"canonical_sources": []any{map[string]any{"id": "p1"}},
	})
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "canonicalSources")
	}

	err = validateEmitterIngressKeys("research.verifyCitations", map[string]any{
		"verified_records": []any{map[string]any{"id": "p1"}},
	})
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "verifiedRecords")
	}

	err = validateEmitterIngressKeys("research.buildClaimEvidenceTable", map[string]any{
		"claim_evidence_table": map[string]any{"table": "T", "rowCount": 1},
	})
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "claimEvidenceTable")
	}
}

func TestValidateStepArtifactSetAgainstCanonicalSchema_RejectsMissingCitationTitle(t *testing.T) {
	err := validateStepArtifactSetAgainstCanonicalSchema(StepArtifactSet{
		StepID: "cite-bad",
		Action: "research.resolveCanonicalCitations",
		CitationBundle: &CitationArtifactBundle{
			CanonicalSources: []CanonicalCitation{{DOI: "10.1000/missing-title"}},
		},
		CitationTrustBundle: &CitationTrustBundle{},
	})
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "citationBundle.canonicalSources[0].title")
	}
}

func TestValidateStepArtifactSetAgainstCanonicalSchema_RejectsMissingClaimTable(t *testing.T) {
	err := validateStepArtifactSetAgainstCanonicalSchema(StepArtifactSet{
		StepID: "claim-bad",
		Action: "research.buildClaimEvidenceTable",
		ClaimEvidenceArtifact: &ClaimEvidenceArtifact{
			Table: "",
		},
	})
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "claimEvidenceArtifact.table")
	}
}
