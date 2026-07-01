package wisdev

import (
	"context"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDedupeCrossSectionParagraphs(t *testing.T) {
	dupText := "retrieval augmented generation grounds language models in external verified knowledge sources"
	sections := []SectionDraftArtifact{
		// The abstract summarizes the body, so it echoes it — it must be left fully
		// intact and must NOT shadow/delete the body paragraphs it summarizes.
		{SectionID: "abstract", Title: "Abstract", Paragraphs: []SectionDraftParagraph{{Text: dupText}}},
		{SectionID: "introduction", Title: "Introduction", Paragraphs: []SectionDraftParagraph{
			{Text: dupText}, {Text: "unique introduction framing about clinical adoption challenges"},
		}},
		{SectionID: "discussion", Title: "Discussion", Paragraphs: []SectionDraftParagraph{
			{Text: dupText}, // near-duplicate of the introduction paragraph -> dropped
			{Text: "distinct discussion point about deployment latency and privacy tradeoffs"},
		}},
	}
	out := dedupeCrossSectionParagraphs(sections)
	assert.Len(t, out[0].Paragraphs, 1, "the abstract is never deduped")
	assert.Len(t, out[1].Paragraphs, 2, "the abstract must NOT shadow/delete the introduction's body paragraph")
	require.Len(t, out[2].Paragraphs, 1, "the recycled paragraph is dropped from the later body section")
	assert.Contains(t, out[2].Paragraphs[0].Text, "deployment latency")
	assert.NotContains(t, out[2].Content, dupText)
}

func TestContainsPrimaryResearchVoice(t *testing.T) {
	assert.True(t, containsPrimaryResearchVoice("In this work, we conducted a survey of methods."))
	assert.True(t, containsPrimaryResearchVoice("This study proposes a novel framework."))
	assert.True(t, containsPrimaryResearchVoice("Our results show a 30% improvement."))
	// Attributed third-person review voice is fine.
	assert.False(t, containsPrimaryResearchVoice("Prior work conducted a survey; Busch et al. reviewed 89 studies."))
	assert.False(t, containsPrimaryResearchVoice("This review synthesizes the literature on RAG."))
}

func TestApplyAdversarialReviewOfflineNoOp(t *testing.T) {
	p := &ManuscriptPipeline{} // empty pythonBaseURL -> no sidecar call
	crit := map[string]any{"overallScore": 0.8, "weaknesses": []string{"x"}}
	out := p.applyAdversarialReview(context.Background(), "q", ManuscriptBlueprint{},
		evidence.ManuscriptRawMaterialSet{}, []SectionDraftArtifact{{Title: "Intro", Content: "body"}}, crit)
	assert.Equal(t, 0.8, out["overallScore"], "offline review must not change the score")
	assert.NotContains(t, out, "contentReviewScore")
}

func TestAppendCritiqueList(t *testing.T) {
	crit := map[string]any{"weaknesses": []string{"a"}}
	appendCritiqueList(crit, "weaknesses", []string{"b", "a"})
	assert.ElementsMatch(t, []string{"a", "b"}, crit["weaknesses"].([]string))
	appendCritiqueList(crit, "recommendations", nil)
	assert.NotContains(t, crit, "recommendations")
}

// These tests cover the manuscript-accuracy fixes: positional-citation grounding,
// the content-grounded concept diagram (and its label rendering), and the
// grounding-ratio peer-review score.

func TestResolvePositionalPacketIDs(t *testing.T) {
	packets := []evidence.EvidencePacket{{PacketID: "p1"}, {PacketID: "p2"}, {PacketID: "p3"}}
	assert.Equal(t, []string{"p1", "p3"}, resolvePositionalPacketIDs("A claim [1]. Another [3].", packets))
	assert.Equal(t, []string{"p2", "p3"}, resolvePositionalPacketIDs("see [2, 3]", packets))
	assert.Empty(t, resolvePositionalPacketIDs("[0] and [4]", packets)) // out of range
	assert.Empty(t, resolvePositionalPacketIDs("[p1]", packets))        // non-numeric (literal)
	assert.Empty(t, resolvePositionalPacketIDs("no markers", packets))
}

func TestBuildContentParagraphsHonorsPositionalCitations(t *testing.T) {
	packets := []evidence.EvidencePacket{
		{PacketID: "p1", ClaimText: "first grounded claim", VerifierStatus: "verified", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s1"}}},
		{PacketID: "p2", ClaimText: "second grounded claim", VerifierStatus: "verified", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s2"}}},
	}
	content := "Prose grounded in the first source [1].\n\nProse grounded in the second source [2]."
	paragraphs := buildContentParagraphs("results", content, packets)
	require.Len(t, paragraphs, 2)

	assert.Equal(t, []string{"p1"}, paragraphs[0].ClaimPacketIDs)
	assert.Equal(t, []string{"s1"}, paragraphs[0].CitationIDs)
	assert.Equal(t, "verified", paragraphs[0].VerificationStatus)
	assert.Empty(t, paragraphs[0].VerifierNotes, "positional [n] citation must not be flagged as missing")

	assert.Equal(t, []string{"p2"}, paragraphs[1].ClaimPacketIDs)
	assert.Equal(t, "verified", paragraphs[1].VerificationStatus)
}

func TestVerifySectionsBlindStopsFalseFlagging(t *testing.T) {
	p := &ManuscriptPipeline{}

	verifiedPackets := []evidence.EvidencePacket{
		{PacketID: "p1", ClaimText: "grounded claim one", VerifierStatus: "verified", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s1"}}},
	}
	grounded := SectionDraftArtifact{SectionID: "results", Title: "Results",
		Paragraphs: buildContentParagraphs("results", "Grounded prose [1].", verifiedPackets)}
	out := p.verifySectionsBlind([]SectionDraftArtifact{grounded})
	assert.Equal(t, "ready_for_review", out[0].ReviewStatus)
	assert.Equal(t, "blind_verified", out[0].LastReviewDecision)
	assert.NotContains(t, out[0].UnresolvedIssues, "Blind verifier found missing or weak paragraph grounding.")

	// A cited paragraph backed by a real-but-unverified packet is flagged, not blocked.
	nrPackets := []evidence.EvidencePacket{
		{PacketID: "p1", ClaimText: "grounded claim one", VerifierStatus: "needs_review", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s1"}}},
	}
	needsReview := SectionDraftArtifact{SectionID: "results", Title: "Results",
		Paragraphs: buildContentParagraphs("results", "Grounded prose [1].", nrPackets)}
	out = p.verifySectionsBlind([]SectionDraftArtifact{needsReview})
	assert.Equal(t, "ready_for_review", out[0].ReviewStatus)

	// Genuinely uncited scaffold prose still blocks the section.
	uncited := SectionDraftArtifact{SectionID: "results", Title: "Results",
		Paragraphs: buildContentParagraphs("results", "Unsupported prose without any citation.", verifiedPackets)}
	out = p.verifySectionsBlind([]SectionDraftArtifact{uncited})
	assert.Equal(t, "needs_revision", out[0].ReviewStatus)
}

func TestBuildVisualSpecEmitsLabeledNodes(t *testing.T) {
	packets := map[string]evidence.EvidencePacket{
		"p1": {PacketID: "p1", ClaimText: "Alpha claim text"},
		"p2": {PacketID: "p2", ClaimText: "Beta claim text"},
	}
	visual := evidence.VisualEvidence{VisualID: "v1", Title: "My Visual", SourcePacketIDs: []string{"p1", "p2"}}
	specType, specAny := buildVisualSpec(visual, packets)
	require.Equal(t, "mermaid", specType)
	spec := specAny.(string)

	assert.Contains(t, spec, "p1[", "child node label declaration must be emitted")
	assert.Contains(t, spec, "p2[")
	assert.Contains(t, spec, "Alpha claim text")
	assert.Contains(t, spec, "--> p1")
	// Every edge endpoint must have a matching declaration line (no bare ids).
	for _, line := range strings.Split(spec, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "-->") {
			target := strings.TrimSpace(line[strings.Index(line, "-->")+3:])
			assert.Contains(t, spec, target+"[", "edge target %q must be declared with a label", target)
		}
	}
}

func TestMermaidEscapeLabel(t *testing.T) {
	assert.Equal(t, "He said #quot;hi#quot;", mermaidEscapeLabel(`He said "hi"`))
	assert.Equal(t, "a (b) c", mermaidEscapeLabel("a [b] c"))
	assert.Equal(t, "one two", mermaidEscapeLabel("one\ntwo"))
}

func TestComposeVisualsBuildsGroundedConceptDiagram(t *testing.T) {
	p := &ManuscriptPipeline{}
	raw := evidence.ManuscriptRawMaterialSet{
		ClaimPackets: []evidence.EvidencePacket{
			{PacketID: "p1", ClaimText: "Retrieval grounding reduces hallucination", Confidence: 0.8},
		},
	}
	blueprint := ManuscriptBlueprint{Sections: []SectionBrief{
		{SectionID: "results", Title: "Results", RequiredClaimPacketIDs: []string{"p1"}},
	}}
	visuals := p.composeVisuals("job", "clinical RAG", raw, blueprint)
	require.Len(t, visuals, 1)
	assert.Equal(t, "ready_for_review", visuals[0].ReviewStatus)
	assert.Empty(t, visuals[0].UnresolvedIssues)
	assert.NotEmpty(t, visuals[0].SourcePacketIDs)
	spec, ok := visuals[0].Spec.(string)
	require.True(t, ok)
	assert.Contains(t, spec, "Results")
	assert.Contains(t, spec, "Retrieval grounding")

	// With no real packets and an open gap, the fallback stays needs_revision.
	rawEmpty := evidence.ManuscriptRawMaterialSet{Gaps: []string{"acquire more sources"}}
	bp := ManuscriptBlueprint{Sections: []SectionBrief{{SectionID: "results", Title: "Results"}}}
	visuals = p.composeVisuals("job", "q", rawEmpty, bp)
	require.Len(t, visuals, 1)
	assert.Equal(t, "needs_revision", visuals[0].ReviewStatus)
}

func TestPeerReviewScoreReflectsGrounding(t *testing.T) {
	p := &ManuscriptPipeline{}

	flagged := SectionDraftArtifact{SectionID: "results", Title: "Results", ReviewStatus: "needs_revision",
		BlindVerifier: BlindVerifierReport{RejectedParagraphs: 3}}
	crit := p.peerReview("job", "q", evidence.ManuscriptRawMaterialSet{}, ManuscriptBlueprint{}, []SectionDraftArtifact{flagged}, nil)
	score, _ := crit["overallScore"].(float64)
	assert.Less(t, score, 0.5, "an all-flagged draft must not score high")

	grounded := SectionDraftArtifact{SectionID: "results", Title: "Results", ReviewStatus: "ready_for_review",
		BlindVerifier: BlindVerifierReport{VerifiedParagraphs: 5}}
	crit = p.peerReview("job", "q", evidence.ManuscriptRawMaterialSet{}, ManuscriptBlueprint{}, []SectionDraftArtifact{grounded}, nil)
	score, _ = crit["overallScore"].(float64)
	assert.Greater(t, score, 0.7, "a fully grounded draft should score well")

	// A draft cited only to weakly-resolved (flagged) sources scores in the middle:
	// above an uncited draft, below a DOI-grade verified one.
	weak := SectionDraftArtifact{SectionID: "results", Title: "Results", ReviewStatus: "ready_for_review",
		BlindVerifier: BlindVerifierReport{FlaggedParagraphs: 4}}
	crit = p.peerReview("job", "q", evidence.ManuscriptRawMaterialSet{}, ManuscriptBlueprint{}, []SectionDraftArtifact{weak}, nil)
	score, _ = crit["overallScore"].(float64)
	assert.Greater(t, score, 0.45)
	assert.Less(t, score, 0.7)
}

func TestConceptDiagramDedupsSharedClaims(t *testing.T) {
	raw := evidence.ManuscriptRawMaterialSet{
		ClaimPackets: []evidence.EvidencePacket{
			{PacketID: "p1", ClaimText: "Shared finding about grounding", Confidence: 0.9},
		},
	}
	blueprint := ManuscriptBlueprint{Sections: []SectionBrief{
		{SectionID: "results", Title: "Results", RequiredClaimPacketIDs: []string{"p1"}},
		{SectionID: "discussion", Title: "Discussion", RequiredClaimPacketIDs: []string{"p1"}},
	}}
	spec, drawn := buildConceptDiagramSpec("q", raw, blueprint)
	// One claim node, shared by both sections — the label appears exactly once.
	assert.Equal(t, 1, strings.Count(spec, "Shared finding about grounding"))
	assert.Len(t, drawn, 1)
	assert.Contains(t, spec, "Results")
	assert.Contains(t, spec, "Discussion")
}

func TestComposeVisualsEmitsEvidenceTable(t *testing.T) {
	p := &ManuscriptPipeline{}
	raw := evidence.ManuscriptRawMaterialSet{
		CanonicalSources: []evidence.CanonicalCitationRecord{{CanonicalID: "s1", Title: "Source One"}, {CanonicalID: "s2", Title: "Source Two"}},
		ClaimPackets: []evidence.EvidencePacket{
			{PacketID: "p1", ClaimText: "Finding one", Confidence: 0.8, EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s1"}}},
			{PacketID: "p2", ClaimText: "Finding two", Confidence: 0.7, EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "s2"}}},
		},
		SourceClusters: []evidence.ManuscriptSourceCluster{
			{ClusterID: "c1", Label: "Theme A", PacketIDs: []string{"p1"}},
			{ClusterID: "c2", Label: "Theme B", PacketIDs: []string{"p2"}},
		},
	}
	blueprint := ManuscriptBlueprint{Sections: []SectionBrief{{SectionID: "results", Title: "Results", RequiredClaimPacketIDs: []string{"p1", "p2"}}}}
	visuals := p.composeVisuals("job", "q", raw, blueprint)
	var table *VisualArtifact
	for i := range visuals {
		if visuals[i].SpecType == "table" {
			table = &visuals[i]
		}
	}
	require.NotNil(t, table, "an evidence-summary table should be synthesized")
	spec, ok := table.Spec.(ManuscriptTableSpec)
	require.True(t, ok)
	assert.Len(t, spec.Rows, 2)
	assert.Equal(t, "results", table.SectionID)
}

func TestSelectRelevantPacketsBackfillGating(t *testing.T) {
	packets := []evidence.EvidencePacket{
		{PacketID: "m1", SectionRelevance: []string{"methods"}},
		{PacketID: "r1", SectionRelevance: []string{"results"}}, // belongs to results
		{PacketID: "g1"}, // section-agnostic
	}
	ids := func(ps []evidence.EvidencePacket) []string {
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			out = append(out, p.PacketID)
		}
		return out
	}

	// Methods (specific) gets its own + the general packet, never the results one.
	methods := ids(selectRelevantPackets(packets, "methods", 8, nil))
	assert.Contains(t, methods, "m1")
	assert.Contains(t, methods, "g1")
	assert.NotContains(t, methods, "r1")

	// Abstract (synthesis) may draw on any packet, including the results one.
	abstract := ids(selectRelevantPackets(packets, "abstract", 8, nil))
	assert.Contains(t, abstract, "r1")
}

func TestSelectRelevantPacketsPrefersUnassigned(t *testing.T) {
	// Two general packets; with g1 already assigned, a limit-1 selection prefers the
	// unassigned g2 so sections contribute distinct evidence.
	packets := []evidence.EvidencePacket{{PacketID: "g1"}, {PacketID: "g2"}}
	got := selectRelevantPackets(packets, "results", 1, map[string]int{"g1": 1})
	require.Len(t, got, 1)
	assert.Equal(t, "g2", got[0].PacketID)
	// But if everything is assigned, it still fills rather than starving.
	got = selectRelevantPackets(packets, "results", 1, map[string]int{"g1": 1, "g2": 1})
	require.Len(t, got, 1)
}
