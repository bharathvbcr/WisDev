package evidence

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestEvidenceGrounding_FailureModes_Contract(t *testing.T) {
	t.Run("Grounding - Low Confidence Citations", func(t *testing.T) {
		// Internal helper confidenceFromRecord is used in Dossier
		// confidenceFromRecord(IDs, title) 
		// If IDs are empty and title is empty -> 0.5
		assert.Equal(t, 0.5, confidenceFromRecord(CanonicalIDs{}, ""))
	})

	t.Run("Grounding - Invalid Paper Behavior", func(t *testing.T) {
		// Build dossier with paper that fails validation (empty title)
		dossier, err := BuildDossier("job-1", "test", []search.Paper{
			{ID: "p1", Title: ""}, // Fails validation
		})
		
		assert.NoError(t, err)
		assert.Empty(t, dossier.CanonicalSources)
		assert.Empty(t, dossier.VerifiedClaims)
		assert.Equal(t, 0, dossier.CoverageMetrics["verifiedClaimCount"])
	})
	
	t.Run("Grounding - Malformed DOI/Arxiv Preservation", func(t *testing.T) {
		p := search.Paper{
			ID:      "p1",
			DOI:     "not-a-doi",
			ArxivID: "not-arxiv",
			Title:   "Valid Title",
		}
		
		// Verify buildCanonicalRecord handles malformed IDs
		record := buildCanonicalRecord(p)
		// The system prioritizes DOI and prepends scheme
		assert.Equal(t, "doi:not-a-doi", record.CanonicalID)
		assert.Equal(t, "not-a-doi", record.SourceIDs.DOI)
		assert.Equal(t, "not-arxiv", record.SourceIDs.Arxiv)
	})
}
