package wisdev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// ---- Feature A: coordination ----

func TestFetchCoordinationPlanOfflineNoop(t *testing.T) {
	p := NewManuscriptPipelineOffline()
	got := p.fetchCoordinationPlan(context.Background(), ManuscriptBlueprint{Query: "q"}, evidence.ManuscriptRawMaterialSet{})
	assert.Nil(t, got, "offline coordination must no-op")
}

func TestDedupeOwnershipConcepts(t *testing.T) {
	order := []string{"results", "introduction", "methods"}
	in := []OwnershipConcept{
		{ConceptLabel: "89-study survey", OwningSectionID: "results"},
		{ConceptLabel: "89-Study Survey", OwningSectionID: "introduction"}, // dup label (case-insensitive) -> dropped
		{ConceptLabel: "PRISMA map", OwningSectionID: "ghost"},             // unknown section -> dropped
		{ConceptLabel: "", OwningSectionID: "methods"},                     // empty label -> dropped
		{ConceptLabel: "modular RAG taxonomy", OwningSectionID: "methods"},
	}
	out := dedupeOwnershipConcepts(in, order)
	require.Len(t, out, 2)
	assert.Equal(t, "89-study survey", out[0].ConceptLabel)
	assert.Equal(t, "results", out[0].OwningSectionID)
	assert.Equal(t, "modular RAG taxonomy", out[1].ConceptLabel)
}

func TestRenderOwnershipForSection(t *testing.T) {
	concepts := []OwnershipConcept{
		{ConceptLabel: "89-study survey", OwningSectionID: "results"},
		{ConceptLabel: "modular RAG taxonomy", OwningSectionID: "literature_review"},
	}
	owner := renderOwnershipForSection("results", concepts)
	assert.Contains(t, owner, "OWNS")
	assert.Contains(t, owner, "89-study survey")
	assert.Contains(t, owner, "back-reference", "non-owned concept must be a back-reference, not an omit directive")
	assert.Contains(t, owner, "modular RAG taxonomy")

	// No concepts -> empty directive.
	assert.Empty(t, renderOwnershipForSection("results", nil))
}

func TestMergeReviseFindingsFoldsOwnershipAndEntailment(t *testing.T) {
	concepts := []OwnershipConcept{{ConceptLabel: "89-study survey", OwningSectionID: "results"}}
	section := SectionDraftArtifact{
		SectionID:        "introduction",
		UnresolvedIssues: []string{entailmentIssuePrefix + " — fix or cut: \"x\" (no snippet)"},
	}
	out := mergeReviseFindings([]string{"redundant phrasing"}, section, concepts)
	joined := strings.Join(out, "\n")
	assert.Contains(t, joined, "redundant phrasing")
	assert.Contains(t, joined, "EXCLUSIVE OWNERSHIP")
	assert.Contains(t, joined, entailmentIssuePrefix)
}

func TestOwnershipThreadedIntoGeneratePayload(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_ = json.NewEncoder(w).Encode(map[string]string{"content": "Generated section [1]."})
	}))
	defer srv.Close()

	p := &ManuscriptPipeline{pythonBaseURL: srv.URL, httpClient: srv.Client()}
	blueprint := ManuscriptBlueprint{
		Query:        "q",
		SectionOrder: []string{"results"},
		OwnershipConcepts: []OwnershipConcept{
			{ConceptLabel: "89-study survey", OwningSectionID: "results"},
		},
	}
	brief := SectionBrief{SectionID: "results", Title: "Results"}
	content, err := p.generateSectionContent(context.Background(), brief, nil, blueprint)
	require.NoError(t, err)
	assert.Equal(t, "Generated section [1].", content)
	directive, _ := captured["ownership_directive"].(string)
	assert.Contains(t, directive, "89-study survey")
}

// ---- Feature B: entailment ----

func TestFactCheckSectionsOfflineNoop(t *testing.T) {
	p := NewManuscriptPipelineOffline()
	in := []SectionDraftArtifact{
		{SectionID: "results", Content: "Some prose.", Paragraphs: []SectionDraftParagraph{{Text: "Some prose."}}},
	}
	out := p.factCheckSections(context.Background(), in, evidence.ManuscriptRawMaterialSet{}, true)
	assert.False(t, out[0].Paragraphs[0].EntailmentChecked, "offline must not mark paragraphs checked")
	assert.Empty(t, out[0].UnresolvedIssues)
}

func TestApplyEntailmentFlagsScoreBearingDowngrades(t *testing.T) {
	section := SectionDraftArtifact{
		SectionID: "results",
		Paragraphs: []SectionDraftParagraph{
			{ParagraphID: "p1", Text: "Outcomes improved by 70% across all cohorts [3]."},
			{ParagraphID: "p2", Text: "A separate well-supported sentence."},
		},
	}
	result := &factCheckResult{FlaggedSentences: []flaggedSentence{
		// Echoed sentence DROPS the [3] marker — normalized match must still attach.
		{SentenceText: "Outcomes improved by 70% across all cohorts.", Issue: "unsupported statistic", EntailedPacketIDs: nil, UnentailedReason: "no snippet states 70%"},
	}}
	out := applyEntailmentFlags(section, result, true)

	assert.Equal(t, "needs_revision", out.ReviewStatus)
	require.NotEmpty(t, out.UnresolvedIssues)
	assert.Contains(t, out.UnresolvedIssues[0], entailmentIssuePrefix)
	// Matching paragraph downgraded to a blocking score; cited-to-nothing -> 0.0.
	assert.True(t, out.Paragraphs[0].EntailmentChecked)
	assert.Equal(t, 0.0, out.Paragraphs[0].EntailmentScore)
	// Unflagged paragraph left untouched (NOT checked -> never trips the verifier).
	assert.False(t, out.Paragraphs[1].EntailmentChecked)
}

func TestApplyEntailmentFlagsFeedPassRecordsIssuesOnly(t *testing.T) {
	section := SectionDraftArtifact{
		SectionID:  "results",
		Paragraphs: []SectionDraftParagraph{{ParagraphID: "p1", Text: "Outcomes improved by 70%."}},
	}
	result := &factCheckResult{FlaggedSentences: []flaggedSentence{
		{SentenceText: "Outcomes improved by 70%.", Issue: "unsupported", EntailedPacketIDs: nil},
	}}
	out := applyEntailmentFlags(section, result, false)
	assert.NotEmpty(t, out.UnresolvedIssues)
	assert.False(t, out.Paragraphs[0].EntailmentChecked, "feed pass must not set entailment scores")
}

func TestApplyEntailmentFlagsPartialEntailmentDoesNotBlock(t *testing.T) {
	section := SectionDraftArtifact{
		SectionID:  "results",
		Paragraphs: []SectionDraftParagraph{{ParagraphID: "p1", Text: "This is a partially supported claim about clinical outcomes."}},
	}
	result := &factCheckResult{FlaggedSentences: []flaggedSentence{
		{SentenceText: "This is a partially supported claim about clinical outcomes.", Issue: "loose", EntailedPacketIDs: []string{"p1"}},
	}}
	out := applyEntailmentFlags(section, result, true)
	assert.True(t, out.Paragraphs[0].EntailmentChecked)
	assert.Equal(t, 0.5, out.Paragraphs[0].EntailmentScore, "partial entailment scores 0.5 (above the block threshold)")
	assert.Greater(t, out.Paragraphs[0].EntailmentScore, entailmentBlockThreshold)
}

func TestApplyEntailmentFlagsShortSentenceNotMatched(t *testing.T) {
	// A short/generic flagged sentence must NOT attach a blocking score (it could
	// substring-match unrelated paragraphs).
	section := SectionDraftArtifact{
		SectionID:  "results",
		Paragraphs: []SectionDraftParagraph{{ParagraphID: "p1", Text: "It improved outcomes substantially in the trial cohort."}},
	}
	result := &factCheckResult{FlaggedSentences: []flaggedSentence{
		{SentenceText: "It improved.", Issue: "vague", EntailedPacketIDs: nil},
	}}
	out := applyEntailmentFlags(section, result, true)
	assert.False(t, out.Paragraphs[0].EntailmentChecked, "a sub-6-word flag must not score a paragraph")
	assert.NotEmpty(t, out.UnresolvedIssues, "the issue is still recorded for the rewrite")
}

func TestVerifySectionsBlindEntailmentDowngrade(t *testing.T) {
	p := &ManuscriptPipeline{}
	packets := []evidence.EvidencePacket{
		{PacketID: "p1", ClaimText: "grounded claim", VerifierStatus: "verified", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s1"}}},
	}
	// Full lineage (verified) BUT entailment-checked at a blocking score -> must end
	// needs_revision, and must NOT be rejected (lineage exists).
	section := SectionDraftArtifact{SectionID: "results", Title: "Results",
		Paragraphs: buildContentParagraphs("results", "Overreaching prose [1].", packets)}
	require.NotEmpty(t, section.Paragraphs)
	section.Paragraphs[0].EntailmentChecked = true
	section.Paragraphs[0].EntailmentScore = 0.0

	out := p.verifySectionsBlind([]SectionDraftArtifact{section})
	assert.Equal(t, "needs_revision", out[0].ReviewStatus)
	assert.NotEqual(t, "rejected", out[0].Paragraphs[0].VerificationStatus)
	assert.Equal(t, "needs_review", out[0].Paragraphs[0].VerificationStatus)
}

func TestVerifySectionsBlindUncheckedZeroScoreNotDowngraded(t *testing.T) {
	p := &ManuscriptPipeline{}
	packets := []evidence.EvidencePacket{
		{PacketID: "p1", ClaimText: "grounded claim", VerifierStatus: "verified", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s1"}}},
	}
	// EntailmentScore defaults to 0.0 but EntailmentChecked is false -> the verifier
	// MUST treat it as a normal grounded paragraph (no cascade regression).
	section := SectionDraftArtifact{SectionID: "results", Title: "Results",
		Paragraphs: buildContentParagraphs("results", "Grounded prose [1].", packets)}
	out := p.verifySectionsBlind([]SectionDraftArtifact{section})
	assert.Equal(t, "ready_for_review", out[0].ReviewStatus)
}

func TestAnySectionHasEntailmentFlags(t *testing.T) {
	assert.False(t, anySectionHasEntailmentFlags([]SectionDraftArtifact{{UnresolvedIssues: []string{"some other issue"}}}))
	assert.True(t, anySectionHasEntailmentFlags([]SectionDraftArtifact{{UnresolvedIssues: []string{entailmentIssuePrefix + " — fix"}}}))
}

func TestNormalizeForMatchStripsCitations(t *testing.T) {
	assert.Equal(t, "outcomes improved by 70", normalizeForMatch("Outcomes  improved   by 70% [3, 4]."))
}

func TestRunOfflineUnaffectedByNewStages(t *testing.T) {
	pipeline := NewManuscriptPipelineOffline()
	result, err := pipeline.Run(context.Background(), "job_off", "RAG clinical decision support",
		[]search.Paper{
			{Title: "RAG for clinical QA", Abstract: "Retrieval-augmented generation improves clinical question answering accuracy across benchmarks.", Year: 2024, CitationCount: 11},
			{Title: "Grounding clinical LLMs", Abstract: "A study grounding clinical LLM outputs in retrieved evidence to reduce hallucination.", Year: 2023, CitationCount: 7},
		})
	require.NoError(t, err)
	assert.Len(t, result.SectionDrafts, 7)
	assert.Empty(t, result.Blueprint.OwnershipConcepts, "offline run must carry no ownership plan")
	for _, s := range result.SectionDrafts {
		for _, para := range s.Paragraphs {
			assert.False(t, para.EntailmentChecked, "offline run must not entailment-check paragraphs")
		}
	}
}
