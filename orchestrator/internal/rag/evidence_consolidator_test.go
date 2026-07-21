package rag

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEvidenceConsolidator_TokenJaccard(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want float64
	}{
		{"identical", "deep learning", "deep learning", 1.0},
		{"completely different", "apple", "banana", 0.0},
		{"partial overlap", "neural networks research", "transformer networks research", 0.5}, // networks, research overlap; neural, transformer differ
		{"empty strings", "", "", 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenJaccard(tt.a, tt.b)
			assert.InDelta(t, tt.want, got, 0.01)
		})
	}
}

func TestEvidenceConsolidator_Consolidate(t *testing.T) {
	c := NewEvidenceConsolidator()

	t.Run("Deduplication", func(t *testing.T) {
		items := []RawEvidenceItem{
			{Claim: "The specific model is effective", SourceID: "S1", Confidence: 0.8},
			{Claim: "The specific model is very effective", SourceID: "S2", Confidence: 0.9},
			{Claim: "Random noise here", SourceID: "S3", Confidence: 0.7},
		}
		dossier := c.Consolidate(items)
		assert.Len(t, dossier.Unique, 2)
		assert.Equal(t, 1, dossier.DuplicatesDropped)
	})

	t.Run("Contradiction Detection High", func(t *testing.T) {
		// Use claims that are similar enough to trigger contradiction logic (>0.15 Jaccard) 
		// but NOT similar enough to deduplicate (<0.55 Jaccard)
		items := []RawEvidenceItem{
			{Claim: "The drug increases patient survival significantly", SourceID: "S1", Confidence: 0.9},
			{Claim: "The drug failed to show any beneficial survival in patients", SourceID: "S2", Confidence: 0.85},
		}
		dossier := c.Consolidate(items)
		assert.Len(t, dossier.Contradictions, 1)
		assert.Equal(t, "high", dossier.Contradictions[0].Severity)
		assert.True(t, dossier.Contradictions[0].NeedsHumanArbitration)
		assert.Len(t, dossier.HardBlockers, 1)
	})

	t.Run("Contradiction Detection Medium (Same Source)", func(t *testing.T) {
		items := []RawEvidenceItem{
			{Claim: "The drug increases patient survival significantly", SourceID: "S1", Confidence: 0.9},
			{Claim: "The drug failed to show any beneficial survival in patients", SourceID: "S1", Confidence: 0.85},
		}
		dossier := c.Consolidate(items)
		assert.Len(t, dossier.Contradictions, 1)
		assert.Equal(t, "medium", dossier.Contradictions[0].Severity)
	})

	t.Run("Knowledge Gaps", func(t *testing.T) {
		items := []RawEvidenceItem{
			{Claim: "Low confidence rare claim", SourceID: "S4", Confidence: 0.3},
		}
		dossier := c.Consolidate(items)
		assert.Len(t, dossier.Gaps, 1)
		assert.Contains(t, dossier.Gaps[0], "Low-confidence claim needs corroboration")
	})

	t.Run("Empty Input", func(t *testing.T) {
		dossier := c.Consolidate(nil)
		assert.Empty(t, dossier.Unique)
	})
}

func TestTruncateRune(t *testing.T) {
	assert.Equal(t, "abc", truncateRune("abc", 5))
	assert.Equal(t, "abc…", truncateRune("abcd", 3))
}
