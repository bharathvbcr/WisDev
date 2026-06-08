package wisdev

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextPass4CrucialWisdevHelpers(t *testing.T) {
	t.Run("source family resolution maps known providers", func(t *testing.T) {
		for _, tc := range []struct {
			source string
			want   string
		}{
			{" arXiv preprint ", "arxiv"},
			{"PubMed Central", "medicine"},
			{"Semantic Scholar", "academic_graph"},
			{"OpenAlex", "academic_graph"},
			{"Google Scholar", "general_scholar"},
			{"IEEE Xplore", "computer_science"},
			{"ACM Digital Library", "computer_science"},
			{"Custom Registry", "custom registry"},
		} {
			assert.Equal(t, tc.want, resolveSourceFamily(search.Paper{Source: tc.source}), tc.source)
		}
	})

	t.Run("branch manager selects highest scoring active branch only", func(t *testing.T) {
		manager := NewBranchManager(5, 0)
		low := manager.Fork("", &Hypothesis{ID: "low", Claim: "low confidence", ConfidenceScore: 0.2})
		high := manager.Fork("", &Hypothesis{ID: "high", Claim: "high confidence", ConfidenceScore: 0.9})
		pruned := manager.Fork("", &Hypothesis{ID: "pruned", Claim: "pruned confidence", ConfidenceScore: 1.0})
		pruned.Status = "pruned"

		winner := manager.SelectWinner()
		require.NotNil(t, winner)
		assert.Equal(t, high.ID, winner.ID)
		assert.NotEqual(t, pruned.ID, winner.ID)

		low.Status = "pruned"
		high.Status = "pruned"
		assert.Nil(t, manager.SelectWinner())
	})

	t.Run("durable job failure helper records cancelled and failed states", func(t *testing.T) {
		failed := &ResearchDurableJobState{Status: researchDurableJobStatusRunning}
		failResearchDurableJob(failed, errors.New("provider unavailable"), false)
		assert.Equal(t, researchDurableJobStatusFailed, failed.Status)
		assert.Equal(t, "failed", failed.StopReason)
		assert.Equal(t, "provider unavailable", failed.FailureReason)
		assert.Greater(t, failed.UpdatedAt, int64(0))

		cancelled := &ResearchDurableJobState{Status: researchDurableJobStatusRunning}
		failResearchDurableJob(cancelled, nil, true)
		assert.Equal(t, researchDurableJobStatusCancelled, cancelled.Status)
		assert.Equal(t, "cancelled", cancelled.StopReason)
		assert.Empty(t, cancelled.FailureReason)

		assert.NotPanics(t, func() { failResearchDurableJob(nil, errors.New("ignored"), false) })
		assert.Equal(t, "1970-01-01T00:00:01Z", nowString(time.Second.Milliseconds()))
	})

	t.Run("verifier follow-up derivation maps ledger categories", func(t *testing.T) {
		base := "immune checkpoint response"
		for _, tc := range []struct {
			category string
			wantPart string
		}{
			{"citation_integrity", "DOI arXiv OpenAlex Semantic Scholar citation metadata"},
			{"source_diversity", "independent replication systematic review"},
			{"claim_source_diversity", "independent replication systematic review"},
			{"source_acquisition", "open access PDF full text source acquisition"},
			{"full_text_fetch", "open access PDF full text source acquisition"},
			{"source_fetch", "DOI arXiv PubMed PMC source identifiers full text"},
			{"source_identity", "DOI arXiv PubMed PMC source identifiers full text"},
			{"contradiction", "contradictory evidence replication"},
			{"hypothesis_branch", "branch evidence independent support"},
			{"citation_gate", "DOI citation metadata"},
		} {
			got := derivedVerifierLedgerFollowUpQueries(base, CoverageLedgerEntry{Category: tc.category})
			require.Len(t, got, 1, tc.category)
			assert.Contains(t, got[0], base)
			assert.Contains(t, got[0], tc.wantPart)
		}

		generic := derivedVerifierLedgerFollowUpQueries("", CoverageLedgerEntry{Title: "Validate with independent corroborating source"})
		require.Len(t, generic, 1)
		assert.True(t, strings.HasPrefix(generic[0], "research question "))

		assert.Nil(t, derivedVerifierLedgerFollowUpQueries(base, CoverageLedgerEntry{}))
	})

	t.Run("quest branch outcomes dedupe accepted and rejected branches", func(t *testing.T) {
		fallback := []EvidenceFinding{
			{ID: "fallback", Claim: "retrieval precision improves with neural reranking", Snippet: "fallback evidence", Confidence: 0.8, Status: "accepted"},
		}
		accepted, rejected := questOutcomeFromReasoningBranches([]ReasoningBranch{
			{ID: "b1", Claim: "retrieval precision improves", Status: "verified", SupportScore: 0.7},
			{ID: "b1-dup", Claim: "retrieval precision improves", Status: "supported", SupportScore: 0.9},
			{ID: "b2", Thought: "citation integrity needs manual review", Status: "rejected"},
			{ID: "b3", Status: "rejected"},
			{ID: "b4", Claim: "fresh accepted claim", Status: "accepted", SupportScore: 0.6, Findings: []EvidenceFinding{{ID: "provided"}}},
		}, fallback)

		require.Len(t, accepted, 2)
		assert.Equal(t, "fallback", accepted[0].ID)
		assert.Equal(t, "accepted", accepted[1].Status)
		assert.Equal(t, "fresh accepted claim", accepted[1].Claim)
		assert.Equal(t, "fresh accepted claim", accepted[1].Snippet)
		assert.InDelta(t, 0.6, accepted[1].Confidence, 0.001)

		require.Len(t, rejected, 1)
		assert.Equal(t, "b2", rejected[0].ID)
		assert.Equal(t, "citation integrity needs manual review", rejected[0].Content)
	})

	t.Run("quest confidence averaging ignores nil entries and clamps", func(t *testing.T) {
		assert.Equal(t, 0.62, averageQuestEvidenceConfidence(nil, 0.62))
		assert.Equal(t, 0.45, averageQuestEvidenceConfidence([]*EvidenceFinding{nil}, 0.9))

		low := &EvidenceFinding{Confidence: 0.1}
		high := &EvidenceFinding{Confidence: 1.2}
		assert.Equal(t, 0.65, averageQuestEvidenceConfidence([]*EvidenceFinding{low, high}, 0.5))
	})
}
