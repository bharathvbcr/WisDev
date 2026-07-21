package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsCitationVerified_PositionalMarker(t *testing.T) {
	source := &verifyCitationSource{PaperID: "paper-1", Title: "Example"}
	assert.True(t, IsCitationVerified(source, true, 0, 0.5))
	assert.False(t, IsCitationVerified(nil, true, 0, 0.5))
}

func TestIsCitationVerified_ConfidenceThreshold(t *testing.T) {
	source := &verifyCitationSource{PaperID: "paper-1", Title: "Example"}
	assert.True(t, IsCitationVerified(source, false, 0.9, 0.5))
	assert.False(t, IsCitationVerified(source, false, 0.4, 0.5))
}

func TestVerifyStructuredCitations_NumberedMarker(t *testing.T) {
	sources := []verifyCitationSource{{PaperID: "paper-1", Title: "Gains Paper"}}
	outcomes := VerifyStructuredCitations([]structuredCitationInput{
		{CitationID: "cite_intro_1", PaperID: "1", Marker: "[1]", SectionID: "intro"},
	}, sources)
	require.Len(t, outcomes, 1)
	assert.True(t, outcomes[0].Verified)
	assert.Equal(t, "paper-1", outcomes[0].PaperID)
}

func TestVerifyStructuredCitations_UnresolvedMarker(t *testing.T) {
	sources := []verifyCitationSource{{PaperID: "paper-1", Title: "Only Source"}}
	outcomes := VerifyStructuredCitations([]structuredCitationInput{
		{CitationID: "cite_body_99", Marker: "[99]", SectionID: "body"},
	}, sources)
	require.Len(t, outcomes, 1)
	assert.False(t, outcomes[0].Verified)
	assert.NotEmpty(t, outcomes[0].Reason)
}

func TestVerifyFullPaperWorkspaceCitations(t *testing.T) {
	workspace := map[string]any{
		"sectionDraftArtifacts": []map[string]any{
			{
				"sectionId": "intro",
				"content":   "Prior work shows gains [1] and a gap [99].",
			},
		},
		"artifacts": []map[string]any{
			{
				"type": "source_bundle",
				"content": map[string]any{
					"sources": []map[string]any{
						{"paperId": "paper-1", "title": "Gains Paper"},
					},
				},
			},
		},
	}
	outcomes, _ := verifyFullPaperWorkspaceCitations(workspace)
	require.Len(t, outcomes, 2)
	verified := 0
	for _, outcome := range outcomes {
		if outcome.Verified {
			verified++
		}
	}
	assert.Equal(t, 1, verified)
}
