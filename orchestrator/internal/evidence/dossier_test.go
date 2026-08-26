package evidence

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDossier(t *testing.T) {
	t.Run("valid dossier with skipped invalid paper", func(t *testing.T) {
		dossier, err := BuildDossier("job-123", "graph neural networks", []search.Paper{
			{},
			{
				ID:       "openalex:W123",
				Title:    "Graph Neural Networks for Science",
				DOI:      "10.1000/graph",
				Link:     "https://example.com/paper",
				Authors:  []string{"Ada Lovelace", "Alan Turing"},
				Abstract: "Graph neural networks improve retrieval. They are useful for literature synthesis.",
				FullText: "The method improves retrieval precision by 12 percent. However, the approach remains data intensive.",
				StructureMap: []any{
					map[string]any{"type": "table", "title": "Table 1", "caption": "Precision improved by 12 percent."},
				},
				Venue: "WisDev",
				Year:  2024,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "job-123", dossier.JobID)
		assert.Equal(t, "graph neural networks", dossier.Query)
		assert.Len(t, dossier.CanonicalSources, 1)
		// DOI presence alone is identity_present / needs_review, not content-verified.
		assert.Empty(t, dossier.VerifiedClaims)
		assert.GreaterOrEqual(t, len(dossier.TentativeClaims), 4)
		assert.Equal(t, "doi:10.1000/graph", dossier.CanonicalSources[0].CanonicalID)
		assert.Equal(t, 1, dossier.CoverageMetrics["sourceCount"])
		assert.Equal(t, 0, dossier.CoverageMetrics["verifiedClaimCount"])
		assert.GreaterOrEqual(t, dossier.CoverageMetrics["identityStrongCount"], 4)
		assert.Equal(t, 1, dossier.CoverageMetrics["resolvedSourceCount"])
		assert.Contains(t, dossier.TentativeClaims[0].ClaimText, "Graph neural networks improve retrieval")
	})

	t.Run("validation errors", func(t *testing.T) {
		_, err := BuildDossier("", "query", nil)
		require.Error(t, err)

		_, err = BuildDossier("job", "", nil)
		require.Error(t, err)

		_, err = BuildDossier("job", "query", make([]search.Paper, 10001))
		require.Error(t, err)
	})
}

func TestBuildRawMaterialSet(t *testing.T) {
	rawMaterials, dossier, err := BuildRawMaterialSet("job-raw", "graph neural networks", []search.Paper{
		{
			ID:       "arxiv:2501.00001",
			Title:    "Grounded Retrieval for Science",
			Abstract: "The system improves recall. However, benchmark coverage remains narrow.",
			FullText: "A chart reports 18 percent improvement over the baseline.",
			StructureMap: []any{
				map[string]any{"type": "figure", "title": "Figure 1", "caption": "Recall improves by 18 percent."},
				map[string]any{"type": "table", "title": "Table 1", "caption": "Coverage remains narrow for long-tail queries."},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "job-raw", rawMaterials.JobID)
	assert.Equal(t, dossier.Query, rawMaterials.Query)
	assert.GreaterOrEqual(t, len(rawMaterials.ClaimPackets), 3)
	assert.Len(t, rawMaterials.CanonicalSources, 1)
	assert.Len(t, rawMaterials.SourceClusters, 1)
	assert.GreaterOrEqual(t, len(rawMaterials.VisualEvidence), 2)
	assert.NotEmpty(t, rawMaterials.CoverageMetrics["sectionCoverage"])
	assert.NotEmpty(t, rawMaterials.ClaimPackets[0].SectionRelevance)
	assert.NotEmpty(t, rawMaterials.ClaimPackets[0].SourceClusterID)
	// This single paper only pairs an improvement ("improves recall") with a
	// self-disclosed caveat ("coverage remains narrow") — that is NOT a
	// contradiction, so no spurious link should be manufactured. (The old
	// heuristic linked every hedge-word claim to a baseline and reported one.)
	assert.Empty(t, dossier.Contradictions)
}

func TestBuildRawMaterialSetExtractsTableHeadersAndRows(t *testing.T) {
	rawMaterials, _, err := BuildRawMaterialSet("job-table", "metrics", []search.Paper{
		{
			ID:       "doi:10.1000/table",
			Title:    "Table Paper",
			Abstract: "The study reports benchmark metrics.",
			StructureMap: []any{
				map[string]any{
					"type":    "table",
					"title":   "Benchmark Results",
					"caption": "Accuracy improved across tasks.",
					"headers": []any{"Task", "Score"},
					"rows": []any{
						[]any{"QA", "0.91"},
						[]any{"Summarization", "0.87"},
					},
				},
				map[string]any{
					"type":  "table",
					"title": "Cells Alias Table",
					"cells": []any{
						[]any{"A", "B"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, rawMaterials.VisualEvidence)

	var benchmark, cellsAlias *VisualEvidence
	for i := range rawMaterials.VisualEvidence {
		switch rawMaterials.VisualEvidence[i].Title {
		case "Benchmark Results":
			benchmark = &rawMaterials.VisualEvidence[i]
		case "Cells Alias Table":
			cellsAlias = &rawMaterials.VisualEvidence[i]
		}
	}
	require.NotNil(t, benchmark)
	assert.Equal(t, []string{"Task", "Score"}, benchmark.Headers)
	assert.Equal(t, [][]string{{"QA", "0.91"}, {"Summarization", "0.87"}}, benchmark.Rows)
	require.NotNil(t, cellsAlias)
	assert.Equal(t, [][]string{{"A", "B"}}, cellsAlias.Rows)

	for _, visual := range rawMaterials.VisualEvidence {
		if visual.Kind != "table" {
			continue
		}
		require.NotEmpty(t, visual.SourcePacketIDs, "table visuals should ground to linked claim packets")
		for _, packetID := range visual.SourcePacketIDs {
			found := false
			for _, packet := range rawMaterials.ClaimPackets {
				if packet.PacketID != packetID {
					continue
				}
				found = true
				assert.Contains(t, packet.VisualEvidenceIDs, visual.VisualID)
			}
			assert.True(t, found, "source packet %s should exist", packetID)
		}
	}
}

func TestExtractQuantitativeClaims(t *testing.T) {
	got := extractQuantitativeClaims("RAG reduced hallucinations by 35% across 250 patients in 12 studies")
	require.NotEmpty(t, got)
	pairs := map[string]string{}
	for _, q := range got {
		pairs[q.Value] = q.Unit
	}
	assert.Equal(t, "%", pairs["35"])
	assert.Equal(t, "patients", pairs["250"])
	assert.Equal(t, "studies", pairs["12"])
	// Bare numbers / years without a unit are not captured.
	assert.Nil(t, extractQuantitativeClaims("published in 2024 by the lab"))
}

func TestClaimSnippetConsistent(t *testing.T) {
	// No distinct snippet -> cannot judge -> consistent.
	assert.True(t, claimSnippetConsistent("RAG improves accuracy", ""))
	assert.True(t, claimSnippetConsistent("RAG improves accuracy", "RAG improves accuracy"))
	// Different subject -> cannot judge -> consistent.
	assert.True(t, claimSnippetConsistent("RAG improves accuracy", "Tokenizers are fast"))
	// Same subject, polarity flip -> inconsistent.
	assert.False(t, claimSnippetConsistent(
		"Retrieval augmentation improved diagnostic accuracy in the cohort",
		"Retrieval augmentation did not improve diagnostic accuracy in the cohort"))
}

func TestAnnotateCorroboration(t *testing.T) {
	packets := []EvidencePacket{
		// Same finding from two distinct sources -> corroboration count 2.
		{PacketID: "a", ClaimText: "Retrieval augmentation reduces clinical hallucinations substantially", Confidence: 0.8, EvidenceSpans: []EvidenceSpan{{SourceCanonicalID: "s1"}}},
		{PacketID: "b", ClaimText: "Retrieval augmentation reduces clinical hallucinations substantially", Confidence: 0.8, EvidenceSpans: []EvidenceSpan{{SourceCanonicalID: "s2"}}},
		// A distinct, single-source claim -> count 1.
		{PacketID: "c", ClaimText: "Knowledge graphs improve longitudinal patient modelling", Confidence: 0.7, EvidenceSpans: []EvidenceSpan{{SourceCanonicalID: "s3"}}},
	}
	annotateCorroboration(packets)
	assert.Equal(t, 2, packets[0].CorroboratingSourceCount)
	assert.Equal(t, 2, packets[1].CorroboratingSourceCount)
	assert.Greater(t, packets[0].Confidence, 0.8, "corroborated packets get a confidence nudge")
	assert.Equal(t, 1, packets[2].CorroboratingSourceCount)
	assert.Equal(t, 0.7, packets[2].Confidence)
}

func TestBuildRawMaterialSetCorroboratesAcrossSources(t *testing.T) {
	// Two distinct sources (different DOIs) assert the SAME finding -> the merged
	// claim is corroborated by 2 independent sources end-to-end.
	finding := "Retrieval augmented generation substantially reduces clinical hallucinations in large language models."
	rawMaterials, _, err := BuildRawMaterialSet("job-corr", "clinical RAG", []search.Paper{
		{ID: "doi:10.1/aaa", Title: "Paper One on Clinical RAG Decision Support", DOI: "10.1/aaa", Abstract: finding},
		{ID: "doi:10.2/bbb", Title: "Paper Two on Clinical RAG Decision Support", DOI: "10.2/bbb", Abstract: finding},
	})
	require.NoError(t, err)
	maxCorroboration := 0
	for _, packet := range rawMaterials.ClaimPackets {
		if packet.CorroboratingSourceCount > maxCorroboration {
			maxCorroboration = packet.CorroboratingSourceCount
		}
	}
	assert.GreaterOrEqual(t, maxCorroboration, 2, "the shared finding should be corroborated by both sources")
}

func TestDerivePacketVerifierStatus(t *testing.T) {
	// Strong bibliographic IDs are identity_present → needs_review, never "verified".
	assert.Equal(t, "needs_review", derivePacketVerifierStatus(CanonicalCitationRecord{Resolved: true, ResolutionConfidence: 0.95, SourceIDs: CanonicalIDs{DOI: "10.1/2"}}))
	assert.Equal(t, "needs_review", derivePacketVerifierStatus(CanonicalCitationRecord{Resolved: true, ResolutionConfidence: 0.82, SourceIDs: CanonicalIDs{SemanticScholar: "S1"}}))
	assert.True(t, hasStrongBibliographicIdentity(CanonicalCitationRecord{Resolved: true, ResolutionConfidence: 0.95, SourceIDs: CanonicalIDs{DOI: "10.1/2"}}))
	// Resolved only by a fuzzy title match -> needs_review, not verified.
	assert.Equal(t, "needs_review", derivePacketVerifierStatus(CanonicalCitationRecord{Resolved: true, ResolutionConfidence: 0.7, Title: "A Title"}))
	assert.False(t, hasStrongBibliographicIdentity(CanonicalCitationRecord{Resolved: true, ResolutionConfidence: 0.95, Title: "A Title"}))
	// Unresolved but titled -> needs_review; untitled -> provisional.
	assert.Equal(t, "needs_review", derivePacketVerifierStatus(CanonicalCitationRecord{Resolved: false, Title: "A Title"}))
	assert.Equal(t, "provisional", derivePacketVerifierStatus(CanonicalCitationRecord{}))
}

func TestBuildRawMaterialSetDedupesByCanonicalID(t *testing.T) {
	raw, _, err := BuildRawMaterialSet("job-dedupe", "duplicate doi", []search.Paper{
		{ID: "doi:10.1/dup", DOI: "10.1/dup", Title: "First Copy of Duplicate Paper", Abstract: "Duplicate sources share one CanonicalID. Claims must not double-count."},
		{ID: "openalex:W999", DOI: "10.1/dup", Title: "Second Copy of Duplicate Paper", Abstract: "Duplicate sources share one CanonicalID. Claims must not double-count."},
	})
	require.NoError(t, err)
	assert.Len(t, raw.CanonicalSources, 1)
	assert.Equal(t, "doi:10.1/dup", raw.CanonicalSources[0].CanonicalID)
	assert.Equal(t, 1, raw.CoverageMetrics["sourceCount"])
}

func TestBuildContradictionPayloadsSkipsSelfLinks(t *testing.T) {
	packets := []EvidencePacket{
		{
			PacketID:               "same",
			ClaimText:              "Retrieval augmentation improved diagnostic accuracy in the cohort",
			ContradictionPacketIDs: []string{"same", "other"},
		},
		{
			PacketID:  "other",
			ClaimText: "Retrieval augmentation did not improve diagnostic accuracy in the cohort",
		},
	}
	payloads := buildContradictionPayloads(packets)
	require.Len(t, payloads, 1)
	ids, ok := payloads[0]["packetIds"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"other", "same"}, ids)
}

func TestSanitizeStringRuneSafe(t *testing.T) {
	in := "日本語テスト"
	got := sanitizeString(in, 5)
	assert.Equal(t, 5, utf8.RuneCountInString(got))
	assert.True(t, utf8.ValidString(got))
	assert.False(t, strings.ContainsRune(got, utf8.RuneError))
}

func TestPolarityUngatedForDistinctSupport(t *testing.T) {
	raw, _, err := BuildRawMaterialSet("job-polarity", "polarity check", []search.Paper{{
		ID:    "doi:10.9/polarity",
		DOI:   "10.9/polarity",
		Title: "Polarity Flip Paper On Clinical Retrieval Augmentation",
		Abstract: "Retrieval augmentation improved diagnostic accuracy in the cohort. " +
			"Retrieval augmentation did not improve diagnostic accuracy in the cohort.",
	}})
	require.NoError(t, err)
	foundNote := false
	for _, packet := range raw.ClaimPackets {
		assert.NotEqual(t, "verified", packet.VerifierStatus)
		for _, note := range packet.VerifierNotes {
			if strings.Contains(note, "polarity") {
				foundNote = true
			}
		}
	}
	assert.True(t, foundNote, "distinct opposed support should emit a polarity verifier note")
	assert.Equal(t, 0, raw.CoverageMetrics["verifiedClaimCount"])
	assert.Greater(t, raw.CoverageMetrics["identityStrongCount"], 0)
}

func TestAssignContradictions(t *testing.T) {
	t.Run("links only same-section, same-subject, opposed claims", func(t *testing.T) {
		packets := []EvidencePacket{
			{PacketID: "a", ClaimText: "Retrieval augmentation improved diagnostic accuracy in the cohort", SectionRelevance: []string{"results"}},
			{PacketID: "b", ClaimText: "Retrieval augmentation did not improve diagnostic accuracy in the external cohort", SectionRelevance: []string{"results"}},
		}
		assignContradictions(packets)
		assert.Equal(t, []string{"b"}, packets[0].ContradictionPacketIDs)
		assert.Equal(t, []string{"a"}, packets[1].ContradictionPacketIDs)
	})

	t.Run("does not link unrelated subjects or non-opposed hedges", func(t *testing.T) {
		packets := []EvidencePacket{
			// Opposition word but different subjects -> no shared subject tokens.
			{PacketID: "a", ClaimText: "Tokenizer throughput did not regress", SectionRelevance: []string{"methods"}},
			{PacketID: "b", ClaimText: "Knowledge graphs improve grounding", SectionRelevance: []string{"methods"}},
			// Same subject but neither is an opposition (a plain limitation hedge).
			{PacketID: "c", ClaimText: "Knowledge graphs improve grounding but require curation", SectionRelevance: []string{"methods"}},
		}
		assignContradictions(packets)
		assert.Empty(t, packets[0].ContradictionPacketIDs)
		assert.Empty(t, packets[1].ContradictionPacketIDs)
		assert.Empty(t, packets[2].ContradictionPacketIDs)
	})

	t.Run("does not link across different sections", func(t *testing.T) {
		packets := []EvidencePacket{
			{PacketID: "a", ClaimText: "Retrieval augmentation improved diagnostic accuracy", SectionRelevance: []string{"results"}},
			{PacketID: "b", ClaimText: "Retrieval augmentation did not improve diagnostic accuracy", SectionRelevance: []string{"discussion"}},
		}
		assignContradictions(packets)
		assert.Empty(t, packets[0].ContradictionPacketIDs)
		assert.Empty(t, packets[1].ContradictionPacketIDs)
	})

	t.Run("does not self-link identical packet IDs", func(t *testing.T) {
		packets := []EvidencePacket{
			{PacketID: "dup", ClaimText: "Retrieval augmentation improved diagnostic accuracy in the cohort", SectionRelevance: []string{"results"}},
			{PacketID: "dup", ClaimText: "Retrieval augmentation did not improve diagnostic accuracy in the cohort", SectionRelevance: []string{"results"}},
		}
		assignContradictions(packets)
		assert.Empty(t, packets[0].ContradictionPacketIDs)
		assert.Empty(t, packets[1].ContradictionPacketIDs)
	})
}

func TestEvidenceHelpers(t *testing.T) {
	assert.Equal(t, 1, countResolved([]CanonicalCitationRecord{{Resolved: true}, {Resolved: false}}))
	assert.Equal(t, 0.95, confidenceFromRecord(CanonicalIDs{DOI: "10.1/2"}, "title"))
	assert.Equal(t, 0.9, confidenceFromRecord(CanonicalIDs{Arxiv: "2301.00001"}, "title"))
	assert.Equal(t, 0.85, confidenceFromRecord(CanonicalIDs{OpenAlex: "W1"}, "title"))
	assert.Equal(t, 0.82, confidenceFromRecord(CanonicalIDs{SemanticScholar: "S1"}, "title"))
	assert.Equal(t, 0.7, confidenceFromRecord(CanonicalIDs{}, "Title"))
	assert.Equal(t, 0.5, confidenceFromRecord(CanonicalIDs{}, ""))
	assert.Equal(t, "title", normalizeTitle(" Title "))
	assert.Equal(t, "doi:10.1/2", formatID("doi", "10.1/2"))
	assert.Equal(t, "alpha", firstNonEmpty("", " alpha ", "beta"))
	assert.Equal(t, "Alpha beta gamma delta epsilon.", firstSentence("Alpha beta gamma delta epsilon. Zeta eta theta."))
	assert.Equal(t, "", sanitizeURL("ftp://example.com"))
	assert.Equal(t, "https://example.com", sanitizeURL("https://example.com"))
	assert.Equal(t, []string{"a", "b"}, sanitizeAuthors([]string{"a", "b", "c"}, 2, 10))
	assert.Equal(t, 2024, validateYear(2024))
	assert.Equal(t, 0, validateYear(1800))
	assert.Equal(t, "job", hashID("job"))
	assert.Len(t, hashID("longidentifiervalue"), 16)
	assert.Equal(t, "This sentence is definitely long enough to trigger the sentence extractor.", firstSentence("This sentence is definitely long enough to trigger the sentence extractor. Second sentence follows."))
}
