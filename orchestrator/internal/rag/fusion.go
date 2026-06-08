package rag

import (
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

// RRF performs Reciprocal Rank Fusion on multiple result sets.
// It delegates to search.RRFFuse which includes full metadata merging:
// best citation count, abstract, link, DOI, year, venue, and source-API deduplication.
func RRF(results [][]search.Paper, k int) []search.Paper {
	return search.RRFFuse(results, k)
}

// RRFWithIntelligence fuses result lists and then boosts scores with provider intelligence
// scores learned from historical search interactions.
func RRFWithIntelligence(results [][]search.Paper, k int, providerScores map[string]float64) []search.Paper {
	fused := search.RRFFuse(results, k)
	if len(providerScores) == 0 {
		return fused
	}
	return search.BoostByIntelligence(fused, providerScores)
}

// RRFWithClicks fuses result lists and then re-ranks using user click history.
// clicks maps paper ID → click count.
func RRFWithClicks(results [][]search.Paper, k int, clicks map[string]int) []search.Paper {
	fused := search.RRFFuse(results, k)
	if len(clicks) == 0 {
		return fused
	}
	return search.BoostByClicks(fused, clicks)
}
