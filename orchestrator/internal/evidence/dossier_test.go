package evidence

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

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
		assert.GreaterOrEqual(t, len(dossier.VerifiedClaims), 4)
		assert.Equal(t, "doi:10.1000/graph", dossier.CanonicalSources[0].CanonicalID)
		assert.Equal(t, 1, dossier.CoverageMetrics["sourceCount"])
		assert.GreaterOrEqual(t, dossier.CoverageMetrics["verifiedClaimCount"], 4)
		assert.Equal(t, 1, dossier.CoverageMetrics["resolvedSourceCount"])
		assert.Contains(t, dossier.VerifiedClaims[0].ClaimText, "Graph neural networks improve retrieval")
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
	assert.GreaterOrEqual(t, len(dossier.Contradictions), 1)
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
