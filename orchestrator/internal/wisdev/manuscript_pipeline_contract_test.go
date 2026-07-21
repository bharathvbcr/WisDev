package wisdev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func TestNewManuscriptPipelineOfflineSkipsSidecar(t *testing.T) {
	// The offline pipeline must short-circuit every section enrichment call
	// (empty base URL) and still produce a full grounded-scaffold manuscript
	// with no network I/O.
	pipeline := NewManuscriptPipelineOffline()
	require.Empty(t, pipeline.pythonBaseURL, "offline pipeline must not resolve a sidecar URL")

	content, err := pipeline.postSectionContent(context.Background(), "/wisdev/manuscript/section/generate", map[string]any{"section_id": "results"})
	require.Error(t, err, "offline pipeline must not attempt a sidecar call")
	assert.Empty(t, content)

	papers := []search.Paper{
		{Title: "Off-target CRISPR detection assay", Abstract: "We present an assay that detects off-target CRISPR edits with high sensitivity across cell lines.", Year: 2022, CitationCount: 9},
		{Title: "Benchmarking guide-RNA specificity", Abstract: "A benchmark comparing guide-RNA specificity prediction tools against experimental ground truth.", Year: 2021, CitationCount: 14},
	}
	result, err := pipeline.Run(context.Background(), "job_offline", "CRISPR off-target detection", papers)
	require.NoError(t, err)
	assert.Len(t, result.SectionDrafts, 7, "expected the seven-section blueprint")
	assert.NotEmpty(t, result.Blueprint.SectionOrder)
	for _, section := range result.SectionDrafts {
		assert.NotEmpty(t, section.Content, "section %s should have grounded scaffold content", section.SectionID)
	}
}

func TestBuildClaimProvenance(t *testing.T) {
	paragraphs := []SectionDraftParagraph{
		{
			ParagraphID:        "paragraph_results_1",
			ClaimPacketIDs:     []string{"pkt-1"},
			CitationIDs:        []string{"doi:10.1000/example"},
			VerificationStatus: "verified",
		},
	}
	packets := []evidence.EvidencePacket{
		{
			PacketID:       "pkt-1",
			ClaimText:      "Treatment A improves outcome B.",
			VerifierStatus: "verified",
			EvidenceSpans: []evidence.EvidenceSpan{
				{
					SourceCanonicalID: "doi:10.1000/example",
					Section:           "results",
					Locator:           "p.4",
					Snippet:           "Patients receiving treatment A improved outcome B.",
				},
			},
			ContradictionPacketIDs: []string{"pkt-2"},
		},
	}

	provenance := buildClaimProvenance(paragraphs, packets)
	if assert.Len(t, provenance, 1) {
		assert.Equal(t, "paragraph_results_1", provenance[0].ParagraphID)
		assert.Equal(t, "pkt-1", provenance[0].PacketID)
		assert.Equal(t, []string{"doi:10.1000/example"}, provenance[0].SourceCanonicalIDs)
		assert.Equal(t, []string{"p.4"}, provenance[0].EvidenceLocators)
		assert.Equal(t, []string{"pkt-2"}, provenance[0].ContradictionPacketIDs)
	}
}

func TestBuildContradictionMap(t *testing.T) {
	paragraphs := []SectionDraftParagraph{
		{
			ParagraphID:    "paragraph_discussion_1",
			ClaimPacketIDs: []string{"pkt-1"},
		},
	}
	packets := []evidence.EvidencePacket{
		{
			PacketID:               "pkt-1",
			ClaimText:              "Treatment A improves outcome B.",
			ContradictionPacketIDs: []string{"pkt-2", "pkt-3"},
		},
	}

	contradictions := buildContradictionMap(paragraphs, packets)
	if assert.Len(t, contradictions, 1) {
		assert.Equal(t, "paragraph_discussion_1", contradictions[0].ParagraphID)
		assert.Equal(t, "pkt-1", contradictions[0].PacketID)
		assert.Equal(t, []string{"pkt-2", "pkt-3"}, contradictions[0].ConflictingPacketIDs)
	}
}

func TestManuscriptPipelinePostSectionContentAddsInternalAuth(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "pipeline-secret")

	var gotPath string
	var gotContentType string
	var gotCaller string
	var gotInternalKey string
	var gotAuthorization string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotCaller = r.Header.Get("X-Caller-Service")
		gotInternalKey = r.Header.Get("X-Internal-Service-Key")
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":"generated section"}`))
	}))
	defer server.Close()

	pipeline := NewManuscriptPipeline(server.URL)
	content, err := pipeline.postSectionContent(context.Background(), "/wisdev/manuscript/section/generate", map[string]any{
		"section_id": "results",
	})

	require.NoError(t, err)
	assert.Equal(t, "generated section", content)
	assert.Equal(t, "/wisdev/manuscript/section/generate", gotPath)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "go_orchestrator", gotCaller)
	assert.Equal(t, "pipeline-secret", gotInternalKey)
	assert.Equal(t, "Bearer pipeline-secret", gotAuthorization)
}
