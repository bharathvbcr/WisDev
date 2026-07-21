package wisdev

import (
	"sort"
)

// HypothesisRanker scores and ranks hypotheses based on evidence quality.
type HypothesisRanker struct{}

// NewHypothesisRanker creates a new hypothesis ranker.
func NewHypothesisRanker() *HypothesisRanker {
	return &HypothesisRanker{}
}

// Rank sorts a slice of hypotheses by their confidence scores in descending order.
func (r *HypothesisRanker) Rank(hypotheses []*Hypothesis) {
	sort.Slice(hypotheses, func(i, j int) bool {
		return hypotheses[i].ConfidenceScore > hypotheses[j].ConfidenceScore
	})
}

// CalculateScores computes confidence scores for a slice of hypotheses.
func (r *HypothesisRanker) CalculateScores(hypotheses []*Hypothesis) {
	for _, h := range hypotheses {
		h.ConfidenceScore = r.CalculateScore(h)
	}
}

// CalculateScore computes a confidence score for a single hypothesis.
// Factors:
// 1. Evidence Count (Logarithmic boost)
// 2. Mean Evidence Confidence
// 3. Contradiction Penalty (-10% per contradiction)
// 4. Recency Bonus (papers from last 3 years)
func (r *HypothesisRanker) CalculateScore(h *Hypothesis) float64 {
	if len(h.Evidence) == 0 {
		return 0.0
	}

	// 1. Base score from mean confidence
	sumConf := 0.0
	for _, ev := range h.Evidence {
		sumConf += ev.Confidence
	}
	baseScore := sumConf / float64(len(h.Evidence))

	// 2. Contradiction penalty
	penalty := float64(h.ContradictionCount) * 0.1
	if penalty > 0.5 {
		penalty = 0.5
	}

	score := baseScore - penalty

	// 3. Evidence count boost (capped)
	// 5 papers = 0.05 boost, 10 papers = 0.1 boost, 20+ papers = 0.15 boost
	countBoost := 0.0
	switch {
	case len(h.Evidence) >= 20:
		countBoost = 0.15
	case len(h.Evidence) >= 10:
		countBoost = 0.1
	case len(h.Evidence) >= 5:
		countBoost = 0.05
	}
	score += countBoost

	// Bounds checking
	if score < 0 {
		score = 0
	}
	if score > 1.0 {
		score = 1.0
	}

	return score
}
