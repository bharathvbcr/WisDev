package rag

import (
	"math"
	"time"
)

// CitationScorer calculates credibility scores for academic citations.
type CitationScorer struct {
	ImpactWeight  float64
	RecencyWeight float64
	ContextWeight float64
}

func NewDefaultCitationScorer() *CitationScorer {
	return &CitationScorer{
		ImpactWeight:  0.5,
		RecencyWeight: 0.3,
		ContextWeight: 0.2,
	}
}

// CalculateScore computes a 0.0-1.0 credibility score for a paper.
func (s *CitationScorer) CalculateScore(node CitationNode, queryMatch float64) float64 {
	// 1. Impact Score (logarithmic to normalize high citation counts)
	// Base: 100 citations = 0.5 score, 1000 = 0.75, 10000 = 1.0
	impact := 0.0
	if node.CitationCount > 0 {
		impact = math.Log10(float64(node.CitationCount)) / 4.0
		if impact > 1.0 {
			impact = 1.0
		}
	}

	// 2. Recency Score
	// Linear decay over 20 years. Papers > 20 years old get 0.0 recency bonus.
	currentYear := time.Now().Year()
	age := currentYear - node.Year
	if age < 0 {
		age = 0
	}
	recency := 1.0 - (float64(age) / 20.0)
	if recency < 0 {
		recency = 0
	}

	// 3. Combined Score
	score := (impact * s.ImpactWeight) + (recency * s.RecencyWeight) + (queryMatch * s.ContextWeight)
	
	// Clamp to [0, 1]
	if score > 1.0 {
		score = 1.0
	}
	if score < 0 {
		score = 0
	}
	
	return score
}

// GetCredibilityTier returns a human-readable label for the credibility score.
func GetCredibilityTier(score float64) string {
	if score >= 0.8 {
		return "High Credibility"
	}
	if score >= 0.5 {
		return "Established"
	}
	if score >= 0.3 {
		return "Emerging"
	}
	return "Preliminary"
}
