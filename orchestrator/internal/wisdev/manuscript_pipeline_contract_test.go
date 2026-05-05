package wisdev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence"
)

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
