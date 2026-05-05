package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestNextPass11CrucialWisdevHelpers(t *testing.T) {
	t.Run("loop result material detection covers all material sources", func(t *testing.T) {
		assert.False(t, loopResultHasResearchMaterial(nil))
		assert.False(t, loopResultHasResearchMaterial(&LoopResult{}))
		assert.True(t, loopResultHasResearchMaterial(&LoopResult{FinalAnswer: " answer "}))
		assert.True(t, loopResultHasResearchMaterial(&LoopResult{Papers: []search.Paper{{Title: "paper"}}}))
		assert.True(t, loopResultHasResearchMaterial(&LoopResult{Evidence: []EvidenceFinding{{Claim: "claim"}}}))
		assert.True(t, loopResultHasResearchMaterial(&LoopResult{ExecutedQueries: []string{"q"}}))
	})

	t.Run("claim citation health and confidence scoring cover citation states", func(t *testing.T) {
		assert.Equal(t, "missing_evidence", claimCitationHealth(ClaimVerificationRecord{}))
		assert.Equal(t, "missing_source", claimCitationHealth(ClaimVerificationRecord{SupportCount: 1}))
		assert.Equal(t, "single_source", claimCitationHealth(ClaimVerificationRecord{SupportCount: 1, SourceIDs: []string{"s1"}, SourceFamilies: []string{"pubmed"}}))
		assert.Equal(t, "weak_family_coverage", claimCitationHealth(ClaimVerificationRecord{SupportCount: 2, SourceIDs: []string{"s1", "s2"}, SourceFamilies: []string{"pubmed"}}))
		assert.Equal(t, "healthy", claimCitationHealth(ClaimVerificationRecord{SupportCount: 2, SourceIDs: []string{"s1", "s2"}, SourceFamilies: []string{"pubmed", "openalex"}}))

		assert.Equal(t, 1.0, citationHealthScore("healthy"))
		assert.Equal(t, 0.7, citationHealthScore("weak_family_coverage"))
		assert.Equal(t, 0.55, citationHealthScore("single_source"))
		assert.Equal(t, 0.35, citationHealthScore("missing_source"))
		assert.Equal(t, 0.15, citationHealthScore("missing_evidence"))
	})

	t.Run("claim verification status and follow-up queries map failure modes", func(t *testing.T) {
		status, stop := claimVerificationStatus(ClaimVerificationRecord{ContradictionStatus: "open", CitationHealth: "healthy"})
		assert.Equal(t, "contradicted", status)
		assert.Equal(t, "contradiction_open", stop)

		status, stop = claimVerificationStatus(ClaimVerificationRecord{CitationHealth: "healthy"})
		assert.Equal(t, "supported", status)
		assert.Equal(t, "", stop)

		status, stop = claimVerificationStatus(ClaimVerificationRecord{CitationHealth: "single_source"})
		assert.Equal(t, "needs_triangulation", status)
		assert.Equal(t, "source_diversity_open", stop)

		status, stop = claimVerificationStatus(ClaimVerificationRecord{CitationHealth: "missing_source"})
		assert.Equal(t, "unsupported", status)
		assert.Equal(t, "citation_missing", stop)

		assert.Equal(t, []string{"root contradiction resolution claim"}, claimVerificationFollowUpQueries("root", ClaimVerificationRecord{Claim: "claim", StopReason: "contradiction_open"}))
		assert.Equal(t, []string{"root independent replication claim"}, claimVerificationFollowUpQueries("root", ClaimVerificationRecord{Claim: "claim", StopReason: "source_diversity_open"}))
		assert.Equal(t, []string{"root primary source citation claim"}, claimVerificationFollowUpQueries("root", ClaimVerificationRecord{Claim: "claim", StopReason: "citation_missing"}))
		assert.Nil(t, claimVerificationFollowUpQueries("root", ClaimVerificationRecord{Claim: "claim", StopReason: "done"}))
	})

	t.Run("verifier revision reasons and decision helpers normalize defaults", func(t *testing.T) {
		assert.Equal(t, "contradiction unresolved for claim: c1", verifierRevisionReason(ClaimVerificationRecord{Claim: "c1", ContradictionStatus: "open"}))
		assert.Equal(t, "contradiction unresolved for claim: c2", verifierRevisionReason(ClaimVerificationRecord{Claim: "c2", Status: "contradicted"}))
		assert.Equal(t, "citation health is single_source for claim: c3", verifierRevisionReason(ClaimVerificationRecord{Claim: "c3", CitationHealth: "single_source", Status: "supported"}))
		assert.Equal(t, "claim remains unverified: id-4", verifierRevisionReason(ClaimVerificationRecord{ID: "id-4", CitationHealth: "healthy"}))
		assert.Equal(t, "claim requires verifier review: c5", verifierRevisionReason(ClaimVerificationRecord{Claim: "c5", CitationHealth: "healthy", Status: "supported"}))

		assert.Equal(t, "", decisionStopReason(nil))
		assert.Equal(t, "needs_revision", decisionStopReason(&ResearchVerifierDecision{StopReason: " needs_revision "}))
		assert.Equal(t, 0.0, decisionConfidence(nil))
		assert.Equal(t, 0.0, decisionConfidence(&ResearchVerifierDecision{Confidence: -0.2}))
		assert.Equal(t, 0.6, decisionConfidence(&ResearchVerifierDecision{Confidence: 0.6}))
		assert.Equal(t, 1.0, decisionConfidence(&ResearchVerifierDecision{Confidence: 2}))
	})

	t.Run("reasoning node and runtime citation helpers cover fallback branches", func(t *testing.T) {
		assert.False(t, reasoningNodeExists(nil, "n1"))
		assert.False(t, reasoningNodeExists(&ReasoningGraph{Nodes: []ReasoningNode{{ID: "n1"}}}, " "))
		assert.True(t, reasoningNodeExists(&ReasoningGraph{Nodes: []ReasoningNode{{ID: "n1"}}}, "n1"))
		assert.False(t, reasoningNodeExists(&ReasoningGraph{Nodes: []ReasoningNode{{ID: "n1"}}}, "n2"))

		assert.Equal(t, "methodology", runtimeCitationCategory(EvidenceFinding{Keywords: []string{" methodology "}}))
		assert.Equal(t, "evidence", runtimeCitationCategory(EvidenceFinding{Keywords: []string{" "}}))
		assert.Equal(t, "evidence", runtimeCitationCategory(EvidenceFinding{}))

		assert.Equal(t, "High Credibility", runtimeCredibilityTier(0.95))
		assert.Equal(t, "Established", runtimeCredibilityTier(0.8))
		assert.Equal(t, "Moderate", runtimeCredibilityTier(0.65))
		assert.Equal(t, "Provisional", runtimeCredibilityTier(0.3))
	})
}
