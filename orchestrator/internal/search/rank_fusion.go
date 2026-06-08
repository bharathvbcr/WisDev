package search

import (
	"sort"
	"strings"
)

// RRFFuse merges multiple ranked lists using Reciprocal Rank Fusion.
// k is the RRF constant (default 60 as per the original paper).
func RRFFuse(lists [][]Paper, k int) []Paper {
	if k <= 0 {
		k = 60
	}

	scores := make(map[string]float64)
	papers := make(map[string]Paper)

	for _, list := range lists {
		for rank, paper := range list {
			key := paperKey(paper)
			scores[key] += 1.0 / float64(k+rank+1)
			if _, exists := papers[key]; !exists {
				if len(paper.SourceApis) == 0 && strings.TrimSpace(paper.Source) != "" {
					paper.SourceApis = []string{paper.Source}
				}
				papers[key] = paper
			} else {
				// Merge: preserve the best metadata across providers.
				existing := papers[key]
				if paper.CitationCount > existing.CitationCount {
					existing.CitationCount = paper.CitationCount
				}
				if strings.TrimSpace(existing.Abstract) == "" && strings.TrimSpace(paper.Abstract) != "" {
					existing.Abstract = paper.Abstract
				}
				if strings.TrimSpace(existing.Link) == "" && strings.TrimSpace(paper.Link) != "" {
					existing.Link = paper.Link
				}
				if existing.Year == 0 && paper.Year > 0 {
					existing.Year = paper.Year
				}
				if strings.TrimSpace(existing.Venue) == "" && strings.TrimSpace(paper.Venue) != "" {
					existing.Venue = paper.Venue
				}
				existing.SourceApis = mergeProviderList(existing.SourceApis, paper.SourceApis, existing.Source, paper.Source)
				papers[key] = existing
			}
		}
	}

	out := make([]Paper, 0, len(papers))
	for key, score := range scores {
		paper := papers[key]
		paper.Score = score
		out = append(out, paper)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// Tie-break by citation count
		return out[i].CitationCount > out[j].CitationCount
	})

	return out
}

// BoostByIntelligence adjusts paper scores based on intelligence-based provider scores.
func BoostByIntelligence(papers []Paper, providerScores map[string]float64) []Paper {
	if len(providerScores) == 0 {
		return papers
	}

	for i := range papers {
		maxProviderScore := 0.0
		for _, provider := range papers[i].SourceApis {
			if score, ok := providerScores[provider]; ok {
				if score > maxProviderScore {
					maxProviderScore = score
				}
			}
		}

		// If the paper came from a high-information-gain provider, boost it slightly.
		// Formula: newScore = oldScore * (1 + maxProviderScore * 0.1)
		if maxProviderScore > 0 {
			papers[i].Score *= (1.0 + maxProviderScore*0.1)
		}
	}

	sort.SliceStable(papers, func(i, j int) bool {
		return papers[i].Score > papers[j].Score
	})

	return papers
}

// BoostByClicks increases the score of papers that have been clicked historically.
// clicks is a map of paperID -> click count.
func BoostByClicks(papers []Paper, clicks map[string]int) []Paper {
	if len(clicks) == 0 {
		return papers
	}

	for i := range papers {
		if count, ok := clicks[papers[i].ID]; ok {
			// Boost score based on click count (logarithmic to avoid explosion)
			boost := 0.0
			if count > 0 {
				boost = 0.05
				if count > 10 {
					boost = 0.1
				}
				if count > 50 {
					boost = 0.2
				}
			}
			papers[i].Score *= (1.0 + boost)
		}
	}

	sort.SliceStable(papers, func(i, j int) bool {
		return papers[i].Score > papers[j].Score
	})

	return papers
}
