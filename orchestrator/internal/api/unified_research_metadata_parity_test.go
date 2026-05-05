package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestUnifiedResearchRuntimeMetadataParityAcrossSurfaces(t *testing.T) {
	loopResult := unifiedResearchMetadataParityLoopResult()
	surfaces := map[string]map[string]any{
		"deep_route":       buildDeepResearchLoopPayload("sleep memory consolidation", []string{"neuroscience"}, "neuroscience", loopResult),
		"autonomous_route": buildAutonomousResearchLoopPayload("sleep memory consolidation", "neuroscience", loopResult, nil, true),
		"job_yolo":         buildUnifiedLoopPayload(loopResult),
		"quest_gateway": wisdev.BuildUnifiedResearchRuntimePayload(&wisdev.UnifiedResearchResult{
			State:      loopResult.RuntimeState,
			LoopResult: loopResult,
		}, "sleep memory consolidation", nil),
	}

	requiredKeys := []string{
		"branchEvaluations",
		"coverageLedger",
		"workerReports",
		"verifierDecision",
		"finalizationGate",
		"followUpQueries",
		"stopReason",
	}
	for surface, payload := range surfaces {
		require.NotNil(t, payload, surface)
		for _, key := range requiredKeys {
			value, ok := payload[key]
			require.Truef(t, ok, "%s omitted required runtime metadata key %q; payload=%#v", surface, key, payload)
			require.NotNilf(t, value, "%s returned nil runtime metadata key %q", surface, key)
		}
	}
}

func unifiedResearchMetadataParityLoopResult() *wisdev.LoopResult {
	query := "sleep memory consolidation"
	followUp := "sleep memory consolidation independent replication"
	ledger := []wisdev.CoverageLedgerEntry{
		{
			ID:                "gap-source-diversity",
			Category:          "source_diversity",
			Status:            "open",
			Title:             "Need independent replication evidence",
			SupportingQueries: []string{followUp},
			Required:          true,
			Priority:          90,
		},
	}
	branchEvaluations := []wisdev.ResearchBranchEvaluation{
		{
			ID:              "branch-replication",
			Query:           followUp,
			Hypothesis:      "Independent replication should support or falsify the synthesis.",
			Status:          "revise_required",
			OverallScore:    0.72,
			BranchScore:     0.72,
			VerifierVerdict: "revise_required",
			OpenGaps:        []string{"Need independent replication evidence"},
			StopReason:      "coverage_open",
		},
	}
	workerReports := []wisdev.ResearchWorkerState{
		{
			Role:           wisdev.ResearchWorkerScout,
			Status:         "completed",
			PlannedQueries: []string{followUp},
			CoverageLedger: ledger,
		},
	}
	verifierDecision := &wisdev.ResearchVerifierDecision{
		Role:            wisdev.ResearchWorkerIndependentVerifier,
		Verdict:         "revise_required",
		StopReason:      "coverage_open",
		RevisionReasons: []string{"Need independent replication evidence"},
		Confidence:      0.74,
		EvidenceOnly:    true,
	}
	gate := &wisdev.ResearchFinalizationGate{
		Status:          "revise_required",
		Ready:           false,
		Provisional:     true,
		StopReason:      "coverage_open",
		VerifierVerdict: "revise_required",
		OpenLedgerCount: 1,
		FollowUpQueries: []string{followUp},
		RevisionReasons: []string{"Need independent replication evidence"},
	}
	state := &wisdev.ResearchSessionState{
		SessionID:         "metadata-parity",
		Query:             query,
		Plane:             wisdev.ResearchExecutionPlaneDeep,
		PlannedQueries:    []string{query, followUp},
		BranchPlans:       []wisdev.ResearchBranchPlan{{ID: "branch-replication", Query: followUp, Hypothesis: "Independent replication should support or falsify the synthesis.", Status: "selected"}},
		ExecutedQueries:   []string{query},
		CoverageLedger:    ledger,
		BranchEvaluations: branchEvaluations,
		VerifierDecision:  verifierDecision,
		Workers:           workerReports,
		Blackboard: &wisdev.ResearchBlackboard{
			PlannedQueries:    []string{query, followUp},
			ExecutedQueries:   []string{query},
			CoverageLedger:    ledger,
			BranchEvaluations: branchEvaluations,
			ReadyForSynthesis: false,
			OpenLedgerCount:   1,
			SynthesisGate:     "blocked: 1 coverage ledger item remains open",
		},
		StopReason: "coverage_open",
	}
	return &wisdev.LoopResult{
		FinalAnswer: "A provisional synthesis requires independent replication before promotion.",
		Papers: []search.Paper{
			{ID: "paper-1", Title: "Sleep and memory consolidation", Source: "crossref"},
		},
		Evidence: []wisdev.EvidenceFinding{
			{ID: "finding-1", Claim: "Sleep supports memory consolidation.", SourceID: "paper-1", Status: "accepted", Confidence: 0.82},
		},
		Iterations:       1,
		Converged:        false,
		BranchPlans:      state.BranchPlans,
		ExecutedQueries:  state.ExecutedQueries,
		GapAnalysis:      &wisdev.LoopGapState{NextQueries: []string{followUp}, Ledger: ledger, MissingAspects: []string{"independent replication"}},
		FinalizationGate: gate,
		StopReason:       "coverage_open",
		WorkerReports:    workerReports,
		RuntimeState:     state,
		Mode:             wisdev.WisDevModeGuided,
		ServiceTier:      wisdev.ServiceTierStandard,
	}
}
