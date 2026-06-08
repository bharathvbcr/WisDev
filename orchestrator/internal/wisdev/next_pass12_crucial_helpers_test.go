package wisdev

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextPass12CrucialWisdevHelpers(t *testing.T) {
	t.Run("arbitration helpers clone decisions and expose blackboard state", func(t *testing.T) {
		missing := buildResearchArbitrationState(nil)
		require.NotNil(t, missing)
		assert.Equal(t, "missing", missing.Verdict)
		assert.True(t, missing.ForcedRevision)
		assert.True(t, missing.Abstain)
		assert.NotEmpty(t, missing.MergeRules)

		decision := &ResearchVerifierDecision{
			Verdict:          "revise_required",
			PromotedClaimIDs: []string{"p1"},
			RejectedClaimIDs: []string{"r1"},
			RevisionReasons:  []string{"needs source"},
		}
		state := buildResearchArbitrationState(decision)
		require.NotNil(t, state)
		assert.True(t, state.ForcedRevision)
		assert.False(t, state.Abstain)
		assert.Equal(t, []string{"p1"}, state.PromotedClaimIDs)
		assert.Equal(t, []string{"r1"}, state.RejectedClaimIDs)
		assert.Equal(t, []string{"needs source"}, state.Reasons)

		decision.PromotedClaimIDs[0] = "mutated"
		assert.Equal(t, []string{"p1"}, state.PromotedClaimIDs)
		assert.Nil(t, runtimeBlackboardArbitration(nil))
		assert.Same(t, state, runtimeBlackboardArbitration(&ResearchBlackboard{Arbitration: state}))
	})

	t.Run("branch plan normalization fills defaults, clamps weights, and dedupes queries", func(t *testing.T) {
		plans := normalizeResearchBranchPlans("root query", []ResearchBranchPlan{
			{Query: " branch one ", SearchWeight: 2},
			{Query: "BRANCH ONE"},
			{ID: "p2", Query: "branch two", RetrievalPlan: []string{" extra retrieval "}, Depth: -1, SearchWeight: -0.1, Status: "custom", StopReason: "done"},
			{Query: " "},
		})

		require.Len(t, plans, 2)
		assert.Equal(t, "branch one", plans[0].Query)
		assert.Equal(t, "branch-001", plans[0].ID)
		assert.Equal(t, []string{"root query", "branch one"}, plans[0].RetrievalPlan)
		assert.Equal(t, "Investigate branch one", plans[0].Hypothesis)
		assert.Equal(t, "evidence_grounded_retrieval", plans[0].ReasoningStrategy)
		assert.Equal(t, 1.0, plans[0].SearchWeight)
		assert.Equal(t, "planned", plans[0].Status)
		assert.Equal(t, "pending_retrieval", plans[0].StopReason)

		assert.Equal(t, "p2", plans[1].ID)
		assert.Equal(t, []string{"root query", "extra retrieval"}, plans[1].RetrievalPlan)
		assert.Equal(t, 1, plans[1].Depth)
		assert.Equal(t, 0.5, plans[1].SearchWeight)
		assert.Equal(t, "custom", plans[1].Status)
		assert.Equal(t, "done", plans[1].StopReason)
	})

	t.Run("branch contradiction penalty and bounded memory integer cover edge cases", func(t *testing.T) {
		assert.Equal(t, 0.0, branchContradictionPenalty(nil))
		assert.Equal(t, 0.0, branchContradictionPenalty(NewBeliefState()))

		bs := NewBeliefState()
		bs.AddBelief(&Belief{ID: "b1", Claim: "supported", Status: BeliefStatusActive})
		bs.AddBelief(&Belief{ID: "b2", Claim: "contradicted", Status: BeliefStatusActive, ContradictingEvidence: []string{"e1"}})
		bs.AddBelief(&Belief{ID: "b3", Claim: "refuted", Status: BeliefStatusRefuted, ContradictingEvidence: []string{"e2"}})
		assert.InDelta(t, 0.1, branchContradictionPenalty(bs), 0.0001)

		assert.Equal(t, 5, boundedResearchMemoryInt(0, 5, 2, 10))
		assert.Equal(t, 2, boundedResearchMemoryInt(1, 5, 2, 10))
		assert.Equal(t, 10, boundedResearchMemoryInt(20, 5, 2, 10))
		assert.Equal(t, 7, boundedResearchMemoryInt(7, 5, 2, 10))
	})

	t.Run("quest and source helpers convert maps and source families", func(t *testing.T) {
		payload, err := questToMap(&ResearchQuest{QuestID: "quest-1", Query: "root"})
		require.NoError(t, err)
		assert.Equal(t, "quest-1", payload["questId"])
		assert.Nil(t, valueToAny(func() {}))

		assert.Equal(t, "pubmed", sourceFamilyForPaper(search.Paper{Source: " PubMed "}))
		assert.Equal(t, "openalex", sourceFamilyForPaper(search.Paper{SourceApis: []string{" OpenAlex "}}))
		assert.Equal(t, "doi", sourceFamilyForPaper(search.Paper{DOI: "10.1000/example"}))
		assert.Equal(t, "unknown", sourceFamilyForPaper(search.Paper{}))
	})

	t.Run("worker progress confidence and steering integer coercion cover supported branches", func(t *testing.T) {
		assert.Equal(t, 0.2, workerProgressConfidence(researchWorkerExecution{Err: errors.New("failed")}))
		assert.Equal(t, 0.2, workerProgressConfidence(researchWorkerExecution{Status: "cancelled"}))
		assert.Equal(t, 0.78, workerProgressConfidence(researchWorkerExecution{Evidence: []EvidenceFinding{{Claim: "claim"}}}))
		assert.Equal(t, 0.55, workerProgressConfidence(researchWorkerExecution{ExecutedQueries: []string{"q"}}))
		assert.Equal(t, 0.4, workerProgressConfidence(researchWorkerExecution{}))

		assert.Equal(t, int64(12), int64FromAny(int64(12)))
		assert.Equal(t, int64(13), int64FromAny(13))
		assert.Equal(t, int64(14), int64FromAny(float32(14.9)))
		assert.Equal(t, int64(0), int64FromAny(""))
	})

	t.Run("batch verifier scores parse arrays, maps, nested reports, and clamp values", func(t *testing.T) {
		assert.Nil(t, batchVerifierScoresFromResult(nil, 2))
		assert.Nil(t, batchVerifierScoresFromResult(map[string]any{}, 0))

		scores := batchVerifierScoresFromResult(map[string]any{
			"scores": []any{
				float64(1.4),
				map[string]any{"confidence": float64(0.4)},
				map[string]any{"confidence_report": map[string]any{"calibrated_score": float64(-0.5)}},
				map[string]any{"score": "bad"},
			},
		}, 3)
		require.Len(t, scores, 3)
		assert.Equal(t, branchVerifierScore{Index: 0, Score: 1}, scores[0])
		assert.Equal(t, branchVerifierScore{Index: 1, Score: 0.4}, scores[1])
		assert.Equal(t, branchVerifierScore{Index: 2, Score: 0}, scores[2])

		results := batchVerifierScoresFromResult(map[string]any{
			"results": []any{map[string]any{"verifier_score": float64(0.7)}},
		}, 2)
		require.Len(t, results, 1)
		assert.Equal(t, branchVerifierScore{Index: 0, Score: 0.7}, results[0])

		ranked := batchVerifierScoresFromResult(map[string]any{
			"ranked": []any{json.Number("0.8"), float64(0.6)},
		}, 2)
		require.Len(t, ranked, 1)
		assert.Equal(t, branchVerifierScore{Index: 1, Score: 0.6}, ranked[0])
	})
}
