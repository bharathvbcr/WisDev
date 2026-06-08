package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextPass5CrucialWisdevHelpers(t *testing.T) {
	t.Run("worker search warnings append trimmed provider failures", func(t *testing.T) {
		existing := []map[string]string{{"query": "old", "provider": "openalex", "message": "kept"}}
		out := appendWorkerSearchWarnings(existing, " neural search ", []search.ProviderWarning{
			{Provider: " semantic_scholar ", Message: " timeout "},
			{Provider: " pubmed ", Message: " rate limited "},
		})

		assert.Equal(t, []map[string]string{
			{"query": "old", "provider": "openalex", "message": "kept"},
			{"query": "neural search", "provider": "semantic_scholar", "message": "timeout"},
			{"query": "neural search", "provider": "pubmed", "message": "rate limited"},
		}, out)
		assert.Equal(t, []map[string]string{{"query": "q", "provider": "p", "message": "m"}}, appendWorkerSearchWarnings("bad-existing-shape", "q", []search.ProviderWarning{{Provider: "p", Message: "m"}}))
	})

	t.Run("content paragraph builder classifies grounded, inferred, and rejected paragraphs", func(t *testing.T) {
		packets := []evidence.EvidencePacket{
			{
				PacketID:       "p1",
				ClaimText:      "neural reranking improves retrieval precision",
				VerifierStatus: "verified",
				EvidenceSpans:  []evidence.EvidenceSpan{{SourceCanonicalID: "s1"}},
			},
			{
				PacketID:       "p2",
				ClaimText:      "citation integrity checks reduce hallucinated references",
				VerifierStatus: "tentative",
			},
		}
		content := "Neural reranking improves retrieval precision [p1].\n\nCitation integrity checks reduce hallucinated references.\n\nUnsupported paragraph without evidence."

		paragraphs := buildContentParagraphs("results", content, packets)
		require.Len(t, paragraphs, 3)

		assert.Equal(t, "paragraph_results_1", paragraphs[0].ParagraphID)
		assert.Equal(t, []string{"p1"}, paragraphs[0].ClaimPacketIDs)
		assert.Equal(t, []string{"s1"}, paragraphs[0].CitationIDs)
		assert.Equal(t, "verified", paragraphs[0].VerificationStatus)
		assert.Empty(t, paragraphs[0].VerifierNotes)

		assert.Equal(t, []string{"p2"}, paragraphs[1].ClaimPacketIDs)
		assert.Equal(t, "needs_review", paragraphs[1].VerificationStatus)
		assert.Contains(t, paragraphs[1].VerifierNotes, "paragraph is missing explicit packet citations")
		assert.Contains(t, paragraphs[1].VerifierNotes, "paragraph does not map to grounded source citations")
		assert.Contains(t, paragraphs[1].VerifierNotes, "packet p2 is not blind-verified")

		assert.Empty(t, paragraphs[2].ClaimPacketIDs)
		assert.Equal(t, "rejected", paragraphs[2].VerificationStatus)
		assert.Contains(t, paragraphs[2].VerifierNotes, "paragraph could not be aligned to grounded claim packets")

		assert.Nil(t, buildContentParagraphs("results", "  ", packets))
	})

	t.Run("visual and section helpers map packets, blueprints, and revision states", func(t *testing.T) {
		visual := evidence.VisualEvidence{VisualID: "v1", Kind: "plot", SourcePacketIDs: []string{"p1", "missing", "p2", "p1"}}
		packets := map[string]evidence.EvidencePacket{
			"p1": {PacketID: "p1", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s1"}, {SourceCanonicalID: "s1"}}},
			"p2": {PacketID: "p2", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s2"}}},
		}
		blueprint := ManuscriptBlueprint{Sections: []SectionBrief{
			{SectionID: "methods", PlannedVisualIDs: []string{"other"}},
			{SectionID: "results", PlannedVisualIDs: []string{"v1"}},
		}}

		assert.Equal(t, "results", inferVisualSection(visual, blueprint))
		assert.Equal(t, "results", inferVisualSection(evidence.VisualEvidence{Kind: "table"}, ManuscriptBlueprint{}))
		assert.Equal(t, "discussion", inferVisualSection(evidence.VisualEvidence{Kind: "diagram"}, ManuscriptBlueprint{}))
		assert.Equal(t, "chart", visualKind(visual))
		assert.Equal(t, "table_summary", visualKind(evidence.VisualEvidence{Kind: "table"}))
		assert.Equal(t, "concept_diagram", visualKind(evidence.VisualEvidence{Kind: "network"}))
		assert.Equal(t, []string{"s1", "s2"}, sourceCanonicalIDsForVisual(visual, packets))
		assert.Equal(t, []string{"v1"}, plannedVisualIDs([]evidence.VisualEvidence{visual, {VisualID: "v2", SourcePacketIDs: []string{"missing"}}}, []string{"p2"}))

		assert.Equal(t, "pending", stageStatusForRevisionCount(1))
		assert.Equal(t, "completed", stageStatusForRevisionCount(0))
		assert.Equal(t, "needs_revision", sectionStatusFromClaims(nil))
		assert.Equal(t, "needs_revision", sectionStatusFromClaims([]evidence.EvidencePacket{{VerifierStatus: "tentative"}}))
		assert.Equal(t, "needs_revision", sectionStatusFromClaims([]evidence.EvidencePacket{{VerifierStatus: "verified", ContradictionPacketIDs: []string{"p2"}}}))
		assert.Equal(t, "ready_for_review", sectionStatusFromClaims([]evidence.EvidencePacket{{VerifierStatus: "verified"}}))
		assert.Equal(t, "high", revisionPriority([]string{"contradiction found"}))
		assert.Equal(t, "high", revisionPriority([]string{"no grounded source"}))
		assert.Equal(t, "medium", revisionPriority([]string{"minor issue"}))
		assert.Equal(t, "low", revisionPriority(nil))
	})

	t.Run("citation graph query helpers prefer stable identifiers and titles", func(t *testing.T) {
		paper := search.Paper{DOI: "10.1/test", ArxivID: "arxiv-1", ID: "paper-1", Link: "https://example.test", Title: "Graph Paper"}
		assert.Equal(t, "node-1 cites", citationPaperForwardQuery(" node-1 ", paper))
		assert.Equal(t, "10.1/test cites", citationPaperForwardQuery("", paper))
		assert.Equal(t, "Graph Paper citing papers", citationPaperForwardQuery("", search.Paper{Title: "Graph Paper"}))
		assert.Equal(t, "", citationPaperForwardQuery("", search.Paper{}))

		assert.Equal(t, "node-1 references", citationPaperBackwardQuery(" node-1 ", paper))
		assert.Equal(t, "10.1/test references", citationPaperBackwardQuery("", paper))
		assert.Equal(t, "Graph Paper references", citationPaperBackwardQuery("", search.Paper{Title: "Graph Paper"}))
		assert.Equal(t, "", citationPaperBackwardQuery("", search.Paper{}))
	})
}
