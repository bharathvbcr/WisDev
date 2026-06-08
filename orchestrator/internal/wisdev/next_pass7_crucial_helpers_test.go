package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextPass7CrucialWisdevHelpers(t *testing.T) {
	t.Run("compat tier and confidence helpers normalize canonical values", func(t *testing.T) {
		assert.Equal(t, "heavy", (&googleGenAIModel{tier: ModelTierHeavy}).resolvedTier())
		assert.Equal(t, "light", (&googleGenAIModel{tier: ModelTierLight}).resolvedTier())
		assert.Equal(t, "standard", (&googleGenAIModel{tier: ModelTierStandard}).resolvedTier())
		assert.Equal(t, "standard", (&googleGenAIModel{}).resolvedTier())

		assert.Equal(t, 0.0, clampUnitConfidence(-0.2))
		assert.Equal(t, 0.4, clampUnitConfidence(0.4))
		assert.Equal(t, 1.0, clampUnitConfidence(1.4))
	})

	t.Run("ADK role inference maps research actions to canonical workers", func(t *testing.T) {
		for _, tc := range []struct {
			action string
			want   ResearchWorkerRole
		}{
			{"contradict the claim", ResearchWorkerContradictionCritic},
			{"improve source diversity", ResearchWorkerSourceDiversifier},
			{"verify DOI citation metadata", ResearchWorkerCitationVerifier},
			{"build citation graph network", ResearchWorkerCitationVerifier},
			{"map evidence network", ResearchWorkerCitationGraph},
			{"verify entailment", ResearchWorkerIndependentVerifier},
			{"synthesize draft", ResearchWorkerSynthesizer},
			{"scout next plan", ResearchWorkerScout},
			{"unrelated", ""},
		} {
			assert.Equal(t, tc.want, inferResearchRoleForAction(tc.action), tc.action)
		}
	})

	t.Run("adaptive expansion API targeting selects domain-appropriate providers", func(t *testing.T) {
		assert.Equal(t, []string{"pubmed", "europe_pmc", "semantic_scholar", "openalex"}, adaptiveExpansionTargetAPIs("cancer immunotherapy clinical trial"))
		assert.Equal(t, []string{"arxiv", "nasa_ads", "semantic_scholar", "openalex"}, adaptiveExpansionTargetAPIs("quantum gravity galaxy survey"))
		assert.Equal(t, []string{"dblp", "arxiv", "semantic_scholar", "openalex"}, adaptiveExpansionTargetAPIs("neural network database security algorithm"))
		assert.Equal(t, []string{"semantic_scholar", "openalex", "arxiv", "crossref"}, adaptiveExpansionTargetAPIs("education policy outcomes"))
	})

	t.Run("quest model tier selection respects decision then execution profile", func(t *testing.T) {
		assert.Equal(t, ModelTierStandard, firstNonEmptyQuestModelTier(nil))
		assert.Equal(t, ModelTierHeavy, firstNonEmptyQuestModelTier(&ResearchQuest{DecisionModelTier: ModelTierHeavy, ExecutionProfile: ResearchExecutionProfile{PrimaryModelTier: ModelTierLight}}))
		assert.Equal(t, ModelTierLight, firstNonEmptyQuestModelTier(&ResearchQuest{ExecutionProfile: ResearchExecutionProfile{PrimaryModelTier: ModelTierLight}}))
		assert.Equal(t, ModelTierStandard, firstNonEmptyQuestModelTier(&ResearchQuest{}))
	})

	t.Run("finalization stop reason helpers separate blocking from nonblocking states", func(t *testing.T) {
		for _, reason := range []string{"", "verifier_promoted"} {
			assert.False(t, isBlockingFinalizationStopReason(reason), reason)
			assert.True(t, finalizationStopReasonAllowsOpenLedgerOverride(reason), reason)
		}
		for _, reason := range []string{
			"verifier_rejected",
			"claim_verification_open",
			"missing_sources",
			"unverified_claim",
			"claim_coverage_open",
			"budget_exhausted",
			"no_grounded_sources",
		} {
			assert.True(t, isBlockingFinalizationStopReason(reason), reason)
			assert.False(t, finalizationStopReasonAllowsOpenLedgerOverride(reason), reason)
		}
		assert.False(t, isBlockingFinalizationStopReason("operator_paused"))
		assert.True(t, finalizationStopReasonAllowsOpenLedgerOverride("operator_paused"))
	})

	t.Run("gap extraction and post verifier followups merge ledger claim and branch obligations", func(t *testing.T) {
		gap := &LoopGapState{Ledger: []CoverageLedgerEntry{
			{
				ID:          "ledger-1",
				Category:    "contradiction",
				Status:      coverageLedgerStatusOpen,
				Title:       "Need contradiction evidence",
				Description: "Need contradiction evidence",
			},
		}}
		result := &LoopResult{GapAnalysis: gap}
		assert.Same(t, gap, gapFromLoopResult(result))
		assert.Nil(t, gapFromLoopResult(nil))

		state := &ResearchSessionState{
			ClaimVerification: &ClaimVerificationLedger{
				RequiredFollowUpQueries: []string{"claim ledger follow up"},
				Records: []ClaimVerificationRecord{
					{FollowUpQueries: []string{"record follow up"}},
				},
			},
			BranchEvaluations: []ResearchBranchEvaluation{
				{Query: "branch gap query", OpenGaps: []string{"missing source"}},
				{Query: "branch unexecuted query", StopReason: "branch_unexecuted"},
				{Query: "branch complete query", StopReason: "complete"},
			},
		}

		followUps := buildPostVerifierFollowUpQueries("root research", gap, state, 6)
		require.NotEmpty(t, followUps)
		assert.Contains(t, followUps, "claim ledger follow up")
		assert.Contains(t, followUps, "record follow up")
		assert.Contains(t, followUps, "branch gap query")
		assert.Contains(t, followUps, "branch unexecuted query")
		assert.NotContains(t, followUps, "branch complete query")
		assert.Len(t, buildPostVerifierFollowUpQueries("root research", gap, state, 2), 2)
		assert.Nil(t, buildPostVerifierFollowUpQueries("root research", gap, state, 0))
	})
}
