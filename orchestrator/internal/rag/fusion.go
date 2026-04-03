package rag

import (
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"sort"
)

// RRF performs Reciprocal Rank Fusion on multiple result sets.
func RRF(results [][]search.Paper, k int) []search.Paper {
	if len(results) == 0 {
		return nil
	}
	if k <= 0 {
		k = 60
	}

	scores := make(map[string]float64)
	papers := make(map[string]search.Paper)

	for _, resultSet := range results {
		for rank, paper := range resultSet {
			id := paper.ID
			if id == "" {
				id = paper.DOI
			}
			if id == "" {
				id = paper.Title
			}

			scores[id] += 1.0 / float64(k+rank+1)
			if _, ok := papers[id]; !ok {
				papers[id] = paper
			}
		}
	}

	type scoredPaper struct {
		id    string
		score float64
	}

	var sorted []scoredPaper
	for id, score := range scores {
		sorted = append(sorted, scoredPaper{id, score})
	}

	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].score > sorted[j].score
	})

	var out []search.Paper
	for _, sp := range sorted {
		p := papers[sp.id]
		p.Score = sp.score
		out = append(out, p)
	}

	return out
}
