package search

import (
	"context"
	"math/rand"
)

// RankingStrategy defines the version of ranking logic to use.
type RankingStrategy string

const (
	StrategyBaseline RankingStrategy = "baseline" // RRF only
	StrategyAdaptive RankingStrategy = "adaptive" // RRF + Intelligence + Clicks
)

// ABTestManager handles assignment of users/requests to ranking strategies.
type ABTestManager struct {
	canaryPercentage float64 // 0.0 to 1.0 (e.g. 0.05 for 5%)
}

func NewABTestManager(canaryPercentage float64) *ABTestManager {
	return &ABTestManager{
		canaryPercentage: canaryPercentage,
	}
}

// GetStrategy assigns a strategy for the given request.
func (ab *ABTestManager) GetStrategy(ctx context.Context, userID string) RankingStrategy {
	// Simple deterministic assignment based on userID if provided, else random.
	if userID != "" {
		// Use hash of userID for consistency across requests for same user.
		// For simplicity in this prototype, we use a simple check.
		hash := 0
		for _, c := range userID {
			hash += int(c)
		}
		if float64(hash%100)/100.0 < ab.canaryPercentage {
			return StrategyAdaptive
		}
		return StrategyBaseline
	}

	if rand.Float64() < ab.canaryPercentage {
		return StrategyAdaptive
	}
	return StrategyBaseline
}

// ApplyStrategy applies the selected strategy to the search results.
func (ab *ABTestManager) ApplyStrategy(ctx context.Context, strategy RankingStrategy, papers []Paper, intelligence *SearchIntelligence, clicks map[string]int) []Paper {
	switch strategy {
	case StrategyAdaptive:
		// Enhanced ranking
		scores, _ := intelligence.GetProviderScores(ctx)
		papers = BoostByIntelligence(papers, scores)
		papers = BoostByClicks(papers, clicks)
		return papers
	case StrategyBaseline:
		fallthrough
	default:
		// Default RRF ranking (already done by RRFFuse)
		return papers
	}
}
