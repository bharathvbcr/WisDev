package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	wisdevpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/wisdev"

	"github.com/stretchr/testify/assert"
)

func TestNextPass8CrucialWisdevHelpers(t *testing.T) {
	t.Run("question stop reason mapping covers all explicit proto states", func(t *testing.T) {
		assert.Equal(t, wisdevpb.QuestionStopReason_EVIDENCE_SUFFICIENT, mapQuestionStopReasonToProto(QuestionStopReasonEvidenceSufficient))
		assert.Equal(t, wisdevpb.QuestionStopReason_CLARIFICATION_BUDGET_REACHED, mapQuestionStopReasonToProto(QuestionStopReasonClarificationBudgetReached))
		assert.Equal(t, wisdevpb.QuestionStopReason_USER_PROCEED, mapQuestionStopReasonToProto(QuestionStopReasonUserProceed))
		assert.Equal(t, wisdevpb.QuestionStopReason_QUESTION_STOP_REASON_UNSPECIFIED, mapQuestionStopReasonToProto(QuestionStopReason("unknown")))
	})

	t.Run("branch evidence pointer IDs trim and dedupe", func(t *testing.T) {
		ids := evidenceIDsFromPtrs([]*EvidenceFinding{
			nil,
			{ID: " ev-2 "},
			{ID: "ev-1"},
			{ID: "ev-2"},
			{ID: " "},
		})
		assert.Equal(t, []string{"ev-2", "ev-1"}, ids)
	})

	t.Run("citation retraction status classifies title abstract and venue signals", func(t *testing.T) {
		assert.Equal(t, "flagged_retraction_or_withdrawal", citationRetractionCheckStatus(search.Paper{Title: "Retracted trial"}))
		assert.Equal(t, "flagged_retraction_or_withdrawal", citationRetractionCheckStatus(search.Paper{Abstract: "This paper was withdrawn"}))
		assert.Equal(t, "correction_signal", citationRetractionCheckStatus(search.Paper{Venue: "Erratum"}))
		assert.Equal(t, "correction_signal", citationRetractionCheckStatus(search.Paper{Title: "Correction notice"}))
		assert.Equal(t, "no_retraction_signal", citationRetractionCheckStatus(search.Paper{Title: "Stable article"}))
	})

	t.Run("research memory contradiction helpers parse payloads and summaries", func(t *testing.T) {
		contradictions := contradictionsFromDossierPayload(map[string]any{
			"contradictions": []any{
				map[string]any{
					"left":        map[string]any{"packetId": "left-1", "claimText": "Treatment improves outcome", "confidence": 0.9},
					"right":       map[string]any{"id": "right-1", "claim": "Treatment does not improve outcome", "sourceId": "source-1", "confidence": 0.1},
					"severity":    "high",
					"explanation": "Claims conflict on treatment effect",
				},
				map[string]any{
					"left":  map[string]any{"claim": ""},
					"right": map[string]any{"claim": "ignored"},
				},
			},
		})
		assert.Len(t, contradictions, 1)
		assert.Equal(t, "left-1", contradictions[0].FindingA.ID)
		assert.Equal(t, "right-1", contradictions[0].FindingB.ID)
		assert.Equal(t, ContradictionHigh, contradictions[0].Severity)
		assert.Equal(t, "Claims conflict on treatment effect", contradictions[0].Explanation)
		assert.Equal(t, []string{"Claims conflict on treatment effect"}, contradictionSummaries(contradictions))

		fallback := []ContradictionPair{{FindingA: EvidenceFinding{Claim: "Fallback claim"}}}
		assert.Equal(t, []string{"Fallback claim"}, contradictionSummaries(fallback))
		assert.Empty(t, contradictionsFromDossierPayload(map[string]any{}))
	})

	t.Run("coverage obligation inference maps aspects, rubric owners, and severities", func(t *testing.T) {
		for _, tc := range []struct {
			aspect         string
			obligationType string
			owner          string
			severity       string
		}{
			{"need counter evidence", "missing_counter_evidence", string(ResearchWorkerContradictionCritic), "critical"},
			{"replication benchmark missing", "missing_replication", string(ResearchWorkerContradictionCritic), "critical"},
			{"DOI citation metadata absent", "missing_citation_identity", string(ResearchWorkerCitationGraph), "high"},
			{"source diversity gap", "missing_source_diversity", string(ResearchWorkerSourceDiversifier), "high"},
			{"full text unavailable", "missing_full_text", string(ResearchWorkerSourceDiversifier), "high"},
			{"population subgroup missing", "missing_population", string(ResearchWorkerScout), "medium"},
			{"unclear coverage gap", "coverage_gap", string(ResearchWorkerScout), "medium"},
		} {
			gotType, gotOwner, gotSeverity := inferMissingAspectObligation(tc.aspect)
			assert.Equal(t, tc.obligationType, gotType, tc.aspect)
			assert.Equal(t, tc.owner, gotOwner, tc.aspect)
			assert.Equal(t, tc.severity, gotSeverity, tc.aspect)
		}

		assert.Equal(t, string(ResearchWorkerSourceDiversifier), inferCoverageRubricOwner("primary_evidence"))
		assert.Equal(t, string(ResearchWorkerSourceDiversifier), inferCoverageRubricOwner("review_or_meta_analysis"))
		assert.Equal(t, string(ResearchWorkerContradictionCritic), inferCoverageRubricOwner("replication_or_benchmark"))
		assert.Equal(t, string(ResearchWorkerContradictionCritic), inferCoverageRubricOwner("counter_evidence"))
		assert.Equal(t, string(ResearchWorkerCitationGraph), inferCoverageRubricOwner("citation_metadata"))
		assert.Equal(t, string(ResearchWorkerScout), inferCoverageRubricOwner("other"))

		assert.Equal(t, "critical", inferCoverageRubricSeverity("citation_metadata", 10))
		assert.Equal(t, "critical", inferCoverageRubricSeverity("counter_evidence", 90))
		assert.Equal(t, "high", inferCoverageRubricSeverity("counter_evidence", 80))
		assert.Equal(t, "medium", inferCoverageRubricSeverity("review_or_meta_analysis", 99))
		assert.Equal(t, "medium", inferCoverageRubricSeverity("other", 99))
	})
}
