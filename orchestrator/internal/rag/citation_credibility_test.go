package rag

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewDefaultCitationScorer(t *testing.T) {
	scorer := NewDefaultCitationScorer()

	assert.InDelta(t, 0.5, scorer.ImpactWeight, 0.0001)
	assert.InDelta(t, 0.3, scorer.RecencyWeight, 0.0001)
	assert.InDelta(t, 0.2, scorer.ContextWeight, 0.0001)
}

func TestCalculateScore(t *testing.T) {
	t.Run("high impact clamped by one", func(t *testing.T) {
		scorer := NewDefaultCitationScorer()
		node := CitationNode{
			CitationCount: 20000,
			Year:          time.Now().Year(),
		}

		score := scorer.CalculateScore(node, 1.0)

		assert.InDelta(t, 1.0, score, 0.0001)
	})

	t.Run("scores above one are clamped", func(t *testing.T) {
		scorer := NewDefaultCitationScorer()
		node := CitationNode{
			CitationCount: 20000,
			Year:          time.Now().Year(),
		}

		score := scorer.CalculateScore(node, 2.0)

		assert.InDelta(t, 1.0, score, 0.0001)
	})

	t.Run("zero citations and old paper produce zero", func(t *testing.T) {
		scorer := NewDefaultCitationScorer()
		node := CitationNode{
			CitationCount: 0,
			Year:          time.Now().Year() - 30,
		}

		score := scorer.CalculateScore(node, 0.0)

		assert.InDelta(t, 0.0, score, 0.0001)
	})

	t.Run("future publication uses zero age", func(t *testing.T) {
		scorer := NewDefaultCitationScorer()
		node := CitationNode{
			CitationCount: 10,
			Year:          time.Now().Year() + 1,
		}

		score := scorer.CalculateScore(node, 0.25)

		assert.GreaterOrEqual(t, score, 0.0)
		assert.LessOrEqual(t, score, 1.0)
	})

	t.Run("negative score is clamped to zero", func(t *testing.T) {
		scorer := &CitationScorer{
			ImpactWeight:  0.5,
			RecencyWeight: 0.3,
			ContextWeight: 0.2,
		}
		node := CitationNode{
			CitationCount: 0,
			Year:          time.Now().Year() - 30,
		}

		score := scorer.CalculateScore(node, -1.0)

		assert.Equal(t, 0.0, score)
	})
}

func TestGetCredibilityTier(t *testing.T) {
	t.Run("high credibility", func(t *testing.T) {
		assert.Equal(t, "High Credibility", GetCredibilityTier(0.95))
		assert.Equal(t, "High Credibility", GetCredibilityTier(0.8))
	})

	t.Run("established", func(t *testing.T) {
		assert.Equal(t, "Established", GetCredibilityTier(0.69))
		assert.Equal(t, "Established", GetCredibilityTier(0.5))
	})

	t.Run("emerging", func(t *testing.T) {
		assert.Equal(t, "Emerging", GetCredibilityTier(0.49))
		assert.Equal(t, "Emerging", GetCredibilityTier(0.3))
	})

	t.Run("preliminary", func(t *testing.T) {
		assert.Equal(t, "Preliminary", GetCredibilityTier(0.29))
		assert.Equal(t, "Preliminary", GetCredibilityTier(0.0))
	})
}
