package cli

import (
	"flag"
	"io"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseInterspersedDocGenArgs(t *testing.T) {
	newFS := func() (*flag.FlagSet, *bool, *string) {
		fs := flag.NewFlagSet("docgen", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		offline := fs.Bool("offline", false, "")
		out := fs.String("o", "", "")
		return fs, offline, out
	}

	// Flags BEFORE the query.
	fs, offline, out := newFS()
	q, err := parseInterspersedDocGenArgs(fs, []string{"--offline", "-o", "p.md", "clinical", "RAG"})
	require.NoError(t, err)
	assert.Equal(t, "clinical RAG", q)
	assert.True(t, *offline)
	assert.Equal(t, "p.md", *out)

	// Flags AFTER the query are still honored (the bug this fixes).
	fs, offline, out = newFS()
	q, err = parseInterspersedDocGenArgs(fs, []string{"clinical", "RAG", "--offline", "-o", "p.md"})
	require.NoError(t, err)
	assert.Equal(t, "clinical RAG", q)
	assert.True(t, *offline)
	assert.Equal(t, "p.md", *out)

	// A `--` terminator makes everything after it literal, even multiple
	// dash-leading tokens.
	fs, offline, _ = newFS()
	q, err = parseInterspersedDocGenArgs(fs, []string{"--offline", "--", "deep", "-learning", "-methods"})
	require.NoError(t, err)
	assert.Equal(t, "deep -learning -methods", q)
	assert.True(t, *offline)
}

// The sidecar numbers each section's claim packets locally (1..N), so a prose
// [n] means "this section's n-th packet", NOT bibliography entry n. These tests
// lock in the render-time remap that rewrites [n] to the global reference number.
func TestBuildCitationRefMapMapsPacketsToBibliographyOrder(t *testing.T) {
	research := &agent.YOLOResult{Papers: []agent.Paper{
		{Title: "Paper A"}, {Title: "Paper B"}, {Title: "Paper C"},
	}}
	result := internalwisdev.ManuscriptPipelineResult{
		RawMaterials: evidence.ManuscriptRawMaterialSet{
			CanonicalSources: []evidence.CanonicalCitationRecord{
				{CanonicalID: "cA", Title: "Paper A"},
				{CanonicalID: "cC", Title: "Paper C"},
			},
			ClaimPackets: []evidence.EvidencePacket{
				{PacketID: "p1", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "cC"}}},
				{PacketID: "p2", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "cA"}}},
			},
		},
	}
	packetRef := buildCitationRefMap(research, result)
	require.NotNil(t, packetRef)
	assert.Equal(t, 3, packetRef["p1"]) // p1 -> Paper C -> ref 3
	assert.Equal(t, 1, packetRef["p2"]) // p2 -> Paper A -> ref 1
}

func TestRemapSectionContentCitations(t *testing.T) {
	packetRef := map[string]int{"p1": 3, "p2": 1}
	sectionPackets := []string{"p1", "p2"} // local [1]->p1->3, [2]->p2->1

	assert.Equal(t, "First claim [3]. Second claim [1].",
		remapSectionContentCitations("First claim [1]. Second claim [2].", sectionPackets, packetRef))
	// Combined marker resolves and de-duplicates while preserving global numbers.
	assert.Equal(t, "Both [3, 1].",
		remapSectionContentCitations("Both [1, 2].", sectionPackets, packetRef))
	// Out-of-range local index is left untouched (never silently mis-mapped).
	assert.Equal(t, "Bad [5].",
		remapSectionContentCitations("Bad [5].", sectionPackets, packetRef))
	// No mapping available -> content unchanged.
	assert.Equal(t, "Keep [1].",
		remapSectionContentCitations("Keep [1].", sectionPackets, nil))
}

func TestBuildReferenceModelPrunesAndOrdersByCitation(t *testing.T) {
	research := &agent.YOLOResult{Papers: []agent.Paper{
		{Title: "Alpha"}, {Title: "Beta"}, {Title: "Gamma"}, {Title: "Delta"},
	}}
	result := internalwisdev.ManuscriptPipelineResult{
		Blueprint: internalwisdev.ManuscriptBlueprint{SectionOrder: []string{"results"}},
		SectionDrafts: []internalwisdev.SectionDraftArtifact{
			{SectionID: "results", ClaimPacketIDs: []string{"p1", "p2"}, Content: "First claim [1]. Second claim [2]."},
		},
		RawMaterials: evidence.ManuscriptRawMaterialSet{
			CanonicalSources: []evidence.CanonicalCitationRecord{
				{CanonicalID: "cA", Title: "Alpha"}, {CanonicalID: "cB", Title: "Beta"},
				{CanonicalID: "cG", Title: "Gamma"}, {CanonicalID: "cD", Title: "Delta"},
			},
			ClaimPackets: []evidence.EvidencePacket{
				{PacketID: "p1", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "cG"}}},
				{PacketID: "p2", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "cA"}}},
			},
		},
	}
	refs, packetRef := buildReferenceModel(research, result, false)

	// Only the two cited sources survive, ordered by first citation: Gamma then Alpha.
	require.Len(t, refs, 2)
	assert.Equal(t, "Gamma", refs[0].title)
	assert.Equal(t, "Alpha", refs[1].title)
	// Uncited Beta and Delta are pruned.
	for _, r := range refs {
		assert.NotContains(t, []string{"Beta", "Delta"}, r.title)
	}
	// packetRef points at the FINAL (citation-ordered) numbers.
	assert.Equal(t, 1, packetRef["p1"]) // Gamma
	assert.Equal(t, 2, packetRef["p2"]) // Alpha
}

func TestBuildReferenceModelIncludeUncitedAppendsFullCorpus(t *testing.T) {
	research := &agent.YOLOResult{Papers: []agent.Paper{
		{Title: "Alpha"}, {Title: "Beta"}, {Title: "Gamma"}, {Title: "Delta"},
	}}
	result := internalwisdev.ManuscriptPipelineResult{
		Blueprint: internalwisdev.ManuscriptBlueprint{SectionOrder: []string{"results"}},
		SectionDrafts: []internalwisdev.SectionDraftArtifact{
			{SectionID: "results", ClaimPacketIDs: []string{"p1", "p2"}, Content: "First claim [1]. Second claim [2]."},
		},
		RawMaterials: evidence.ManuscriptRawMaterialSet{
			CanonicalSources: []evidence.CanonicalCitationRecord{
				{CanonicalID: "cA", Title: "Alpha"}, {CanonicalID: "cB", Title: "Beta"},
				{CanonicalID: "cG", Title: "Gamma"}, {CanonicalID: "cD", Title: "Delta"},
			},
			ClaimPackets: []evidence.EvidencePacket{
				{PacketID: "p1", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "cG"}}},
				{PacketID: "p2", EvidenceSpans: []evidence.EvidenceSpan{{SourceCanonicalID: "cA"}}},
			},
		},
	}
	refs, packetRef := buildReferenceModel(research, result, true)

	// Cited sources stay first in citation order (Gamma, Alpha); the uncited retrieved
	// sources (Beta, Delta) are appended so the whole corpus is represented.
	require.Len(t, refs, 4)
	assert.Equal(t, "Gamma", refs[0].title)
	assert.Equal(t, "Alpha", refs[1].title)
	titles := []string{refs[2].title, refs[3].title}
	assert.Contains(t, titles, "Beta")
	assert.Contains(t, titles, "Delta")
	// Cited packets still map to their citation-ordered numbers; appended refs add no packets.
	assert.Equal(t, 1, packetRef["p1"]) // Gamma
	assert.Equal(t, 2, packetRef["p2"]) // Alpha
}

func TestRefineCitedReferencesDedupsPreprintAndPublished(t *testing.T) {
	refs := []docGenReference{
		{titleKey: "betareview", title: "Beta Review", venue: "Preprints.org", link: "https://preprints.org/x", preprint: true},
		{titleKey: "betareview", title: "Beta Review", venue: "Journal", link: "https://doi.org/10.1/x"},
	}
	// Both versions cited (base 1 then base 2).
	final, baseToFinal := refineCitedReferences(refs, []int{1, 2})
	require.Len(t, final, 1, "preprint + published of the same work collapse to one entry")
	assert.False(t, final[0].preprint, "the published version is preferred")
	assert.Equal(t, baseToFinal[1], baseToFinal[2], "both base indices map to the same final number")
}
