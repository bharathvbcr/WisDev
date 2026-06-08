package wisdev

import (
	"context"
	"testing"
)

func TestProgrammaticBranchPlansPreserveTreeMetadata(t *testing.T) {
	tree := treeLoopResult{
		BestConfidence: 0.82,
		Final: map[string]any{
			"tasks": []any{
				map[string]any{
					"query":                    "sleep spindle memory consolidation replication",
					"hypothesis":               "Sleep spindle density predicts memory consolidation.",
					"retrieval_plan":           []any{"sleep spindle memory trial", "sleep spindle replication"},
					"reasoning_strategy":       "replication_first",
					"falsifiability_condition": "failed replication invalidates the branch",
					"closure_condition":        "replication and contradiction checks complete",
					"search_weight":            0.9,
				},
			},
		},
		Iterations: []treeLoopIteration{
			{
				Iteration:  1,
				BranchID:   7,
				Success:    true,
				Score:      0.71,
				Confidence: 0.78,
				Status:     "completed",
				Reason:     "mcts_rollout_complete",
				Output: map[string]any{
					"tasks": []any{
						map[string]any{
							"query":              "sleep spindle contradiction evidence",
							"hypothesis":         "Negative findings constrain the spindle-memory claim.",
							"reasoning_strategy": "contradiction_first",
						},
					},
				},
			},
		},
	}

	plans := extractProgrammaticBranchPlansFromTreeResult("sleep and memory", tree)
	if len(plans) < 2 {
		t.Fatalf("expected final and rollout branch plans, got %d", len(plans))
	}
	first := plans[0]
	if first.Query != "sleep spindle memory consolidation replication" {
		t.Fatalf("unexpected first branch query: %q", first.Query)
	}
	if first.Hypothesis != "Sleep spindle density predicts memory consolidation." {
		t.Fatalf("hypothesis was not preserved: %q", first.Hypothesis)
	}
	if first.ReasoningStrategy != "replication_first" {
		t.Fatalf("reasoning strategy was not preserved: %q", first.ReasoningStrategy)
	}
	if first.FalsifiabilityCondition != "failed replication invalidates the branch" {
		t.Fatalf("falsifiability condition was not preserved: %q", first.FalsifiabilityCondition)
	}
	if !hardeningTestContainsString(first.RetrievalPlan, "sleep spindle memory trial") || !hardeningTestContainsString(first.RetrievalPlan, "sleep spindle replication") {
		t.Fatalf("expected retrieval plan to be preserved, got %#v", first.RetrievalPlan)
	}
}

func TestBranchPlansAreVisibleFromWorkerBlackboardArtifacts(t *testing.T) {
	plans := []ResearchBranchPlan{
		defaultResearchBranchPlan("memory consolidation", "memory consolidation source diversity", "branch-scout-1"),
	}
	workers := []ResearchWorkerState{
		{
			Role:      ResearchWorkerScout,
			Status:    "completed",
			Artifacts: map[string]any{"branchPlans": plans},
		},
	}

	blackboard := buildResearchBlackboard(workers)
	blackboard.BranchPlans = researchBranchPlansFromWorkerReports("memory consolidation", workers)

	if len(blackboard.BranchPlans) != 1 {
		t.Fatalf("expected scout branch plan to be exposed on blackboard, got %d", len(blackboard.BranchPlans))
	}
	if blackboard.BranchPlans[0].ID != "branch-scout-1" {
		t.Fatalf("unexpected branch id: %q", blackboard.BranchPlans[0].ID)
	}
	if len(researchBranchPlanQueries(blackboard.BranchPlans)) != 1 {
		t.Fatalf("expected branch queries to remain available as compatibility projection")
	}
}

func TestVerifyReasoningPathsRejectsUngroundedSingleSourceBranch(t *testing.T) {
	caps := &BrainCapabilities{}
	result, err := caps.VerifyReasoningPaths(context.Background(), []map[string]any{
		{
			"id":            "branch-1",
			"supportScore":  0.92,
			"evidenceCount": 1,
			"findings": []any{
				map[string]any{"claim": "ungrounded claim"},
			},
		},
	}, "")
	if err == nil {
		t.Fatalf("expected verifier to reject ungrounded single-source branch")
	}
	if ready, _ := result["readyForSynthesis"].(bool); ready {
		t.Fatalf("ungrounded branch must not be ready for synthesis")
	}
	branches := hardeningTestMaps(result["branches"])
	if len(branches) == 0 {
		t.Fatalf("expected audited branch details")
	}
	reasons := hardeningTestStringSlice(branches[0]["verificationReasons"])
	if !hardeningTestContainsString(reasons, "evidence_provenance_unverified") {
		t.Fatalf("expected provenance rejection reason, got %#v", reasons)
	}
}

func TestFinalizationGateUsesCanonicalStatuses(t *testing.T) {
	promoteState := &ResearchSessionState{
		Query:            "memory consolidation",
		Blackboard:       &ResearchBlackboard{ReadyForSynthesis: true},
		VerifierDecision: &ResearchVerifierDecision{Verdict: "promote", StopReason: "verifier_promoted"},
	}
	promoteGate := buildResearchFinalizationGate(promoteState, &LoopResult{})
	if promoteGate.Status != "promote" || promoteGate.Provisional {
		t.Fatalf("expected promote gate, got status=%q provisional=%v", promoteGate.Status, promoteGate.Provisional)
	}

	reviseState := &ResearchSessionState{
		Query: "memory consolidation",
		Blackboard: &ResearchBlackboard{
			ReadyForSynthesis: false,
			CoverageLedger: []CoverageLedgerEntry{{
				ID:       "gap-1",
				Category: "source_diversity",
				Status:   coverageLedgerStatusOpen,
				Title:    "Need independent source family",
			}},
		},
		VerifierDecision: &ResearchVerifierDecision{Verdict: "revise_required", StopReason: "coverage_open"},
	}
	reviseGate := buildResearchFinalizationGate(reviseState, &LoopResult{})
	if reviseGate.Status != "revise_required" {
		t.Fatalf("expected revise_required gate, got %q", reviseGate.Status)
	}
	if len(reviseGate.FollowUpQueries) == 0 {
		t.Fatalf("expected open gap to produce follow-up queries")
	}

	abstainState := &ResearchSessionState{
		Query:            "memory consolidation",
		Blackboard:       &ResearchBlackboard{ReadyForSynthesis: false},
		VerifierDecision: &ResearchVerifierDecision{Verdict: "abstain", StopReason: "insufficient_evidence"},
	}
	abstainGate := buildResearchFinalizationGate(abstainState, &LoopResult{})
	if abstainGate.Status != "abstain" {
		t.Fatalf("expected abstain gate, got %q", abstainGate.Status)
	}
}

func TestFinalizationGateClassifiesUnavailableEvidence(t *testing.T) {
	entry := CoverageLedgerEntry{
		ID:             "full-text-gap",
		Category:       "source_acquisition",
		ObligationType: "missing_full_text",
		OwnerWorker:    string(ResearchWorkerSourceDiversifier),
		Status:         coverageLedgerStatusOpen,
		Title:          "Full text source acquisition pending",
		Required:       true,
		Priority:       94,
	}
	state := &ResearchSessionState{
		Query: "memory consolidation",
		Blackboard: &ResearchBlackboard{
			ReadyForSynthesis: false,
			CoverageLedger:    []CoverageLedgerEntry{entry},
		},
		VerifierDecision: &ResearchVerifierDecision{Verdict: "revise_required", StopReason: "missing_full_text"},
	}

	gate := buildResearchFinalizationGate(state, &LoopResult{GapAnalysis: &LoopGapState{Ledger: []CoverageLedgerEntry{entry}}})

	if gate.Status != "blocked_unavailable_evidence" {
		t.Fatalf("expected unavailable-evidence gate, got %q", gate.Status)
	}
	if !gate.Provisional || gate.OpenLedgerCount == 0 {
		t.Fatalf("expected provisional gate with open ledger pressure, got %#v", gate)
	}
}

func TestVerifierCoverageLedgerRoutesTypedObligation(t *testing.T) {
	decision := &ResearchVerifierDecision{
		Verdict:         "revise_required",
		StopReason:      "verifier_requires_revision",
		RevisionReasons: []string{"citation health is missing_source for claim: Sleep improves memory"},
		Confidence:      0.61,
	}

	entry := verifierCoverageLedgerEntry("sleep memory", decision)

	if entry.Status != coverageLedgerStatusOpen {
		t.Fatalf("expected open verifier ledger entry, got %#v", entry)
	}
	if entry.ObligationType != "missing_citation_identity" || entry.OwnerWorker != string(ResearchWorkerCitationGraph) {
		t.Fatalf("expected citation obligation routed to citation graph worker, got %#v", entry)
	}
	if len(entry.SupportingQueries) == 0 || len(entry.ClosureEvidence) == 0 {
		t.Fatalf("expected follow-up and closure metadata, got %#v", entry)
	}
	if !hardeningTestContainsString(entry.ClosureEvidence, "decision:verifier_requires_revision") {
		t.Fatalf("expected verifier decision closure evidence, got %#v", entry.ClosureEvidence)
	}
}

func hardeningTestStringSlice(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value := AsOptionalString(item); value != "" {
				out = append(out, value)
			}
		}
		return out
	}
	return nil
}

func hardeningTestMaps(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped := asMap(item); len(mapped) > 0 {
				out = append(out, mapped)
			}
		}
		return out
	}
	return nil
}

func hardeningTestContainsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
