package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHypothesisRanker_CalculateScore(t *testing.T) {
	t.Run("emptyEvidence", func(t *testing.T) {
		ranker := NewHypothesisRanker()
		score := ranker.CalculateScore(&Hypothesis{})
		assert.Equal(t, 0.0, score)
	})

	t.Run("evidenceCountBoosts", func(t *testing.T) {
		ranker := NewHypothesisRanker()

		base := &Hypothesis{
			Evidence: []*EvidenceFinding{
				{Confidence: 0.8},
				{Confidence: 0.9},
			},
		}
		assert.InDelta(t, 0.85, ranker.CalculateScore(base), 0.0001)

		withFive := &Hypothesis{
			Evidence: []*EvidenceFinding{
				{Confidence: 0.8},
				{Confidence: 0.8},
				{Confidence: 0.8},
				{Confidence: 0.8},
				{Confidence: 0.8},
			},
		}
		assert.InDelta(t, 0.85, ranker.CalculateScore(withFive), 0.0001)

		withTen := &Hypothesis{
			Evidence: []*EvidenceFinding{
				{Confidence: 0.7},
				{Confidence: 0.7},
				{Confidence: 0.7},
				{Confidence: 0.7},
				{Confidence: 0.7},
				{Confidence: 0.7},
				{Confidence: 0.7},
				{Confidence: 0.7},
				{Confidence: 0.7},
				{Confidence: 0.7},
			},
		}
		assert.InDelta(t, 0.8, ranker.CalculateScore(withTen), 0.0001)

		withTwenty := &Hypothesis{
			Evidence: []*EvidenceFinding{
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
				{Confidence: 0.65},
			},
		}
		assert.InDelta(t, 0.8, ranker.CalculateScore(withTwenty), 0.0001)
	})

	t.Run("penaltyClampAndUpperBound", func(t *testing.T) {
		ranker := NewHypothesisRanker()
		oversaturated := &Hypothesis{
			Evidence: []*EvidenceFinding{
				{Confidence: 0.99},
				{Confidence: 0.99},
				{Confidence: 0.99},
				{Confidence: 0.99},
				{Confidence: 0.99},
			},
		}
		assert.Equal(t, 1.0, ranker.CalculateScore(oversaturated))

		underflow := &Hypothesis{
			ContradictionCount: 100,
			Evidence: []*EvidenceFinding{
				{Confidence: -1},
			},
		}
		assert.Equal(t, 0.0, ranker.CalculateScore(underflow))
	})
}

func TestHypothesisRanker_CalculateScoresAndRank(t *testing.T) {
	ranker := NewHypothesisRanker()
	h1 := &Hypothesis{ConfidenceScore: 0.1, Evidence: []*EvidenceFinding{{Confidence: 0.4}}}
	h2 := &Hypothesis{ConfidenceScore: 0.9, Evidence: []*EvidenceFinding{{Confidence: 0.9}, {Confidence: 0.3}, {Confidence: 0.2}, {Confidence: 0.4}, {Confidence: 0.5}}}
	h3 := &Hypothesis{ConfidenceScore: 0.5, Evidence: []*EvidenceFinding{{Confidence: 0.7}, {Confidence: 0.7}, {Confidence: 0.7}, {Confidence: 0.7}, {Confidence: 0.7}, {Confidence: 0.7}}}
	hypotheses := []*Hypothesis{h1, h2, h3}

	ranker.CalculateScores(hypotheses)
	assert.Equal(t, 3, len(hypotheses))
	assert.Greater(t, h3.ConfidenceScore, h2.ConfidenceScore)

	ranker.Rank(hypotheses)
	assert.Equal(t, h3, hypotheses[0])
	assert.Equal(t, h2, hypotheses[1])
	assert.Equal(t, h1, hypotheses[2])
}
