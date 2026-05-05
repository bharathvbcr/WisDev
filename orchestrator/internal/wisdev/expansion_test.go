package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestApplyMultiSourceScoreBoost(t *testing.T) {
	sources := []Source{
		{ID: "s1", SourceCount: 1, Score: 0.5},
		{ID: "s2", SourceCount: 2, Score: 0.5},
		{ID: "s3", SourceCount: 3, Score: 0.0},
	}

	boosted := applyMultiSourceScoreBoost(sources)
	assert.Equal(t, 0.5, boosted[0].Score)
	assert.Equal(t, 0.7, boosted[1].Score)
	assert.Equal(t, 0.2, boosted[2].Score)
}

func TestDetectIntent(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{"cancer treatment", "medical"},
		{"machine learning algorithm", "computer_science"},
		{"system design and architecture", "implementation"},
		{"systematic review of literature", "review"},
		{"random topic", "academic"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, detectIntent(tt.query))
	}
}

func TestExpandQuery(t *testing.T) {
	t.Run("Synonyms", func(t *testing.T) {
		expanded := ExpandQuery("large language model transformer")
		assert.Contains(t, expanded.Expanded, "LLM")
		assert.Equal(t, "computer_science", expanded.Intent)
		assert.Contains(t, expanded.Keywords, "transformer")
	})

	t.Run("Keywords only", func(t *testing.T) {
		expanded := ExpandQuery("abc def ghi")
		assert.Equal(t, "abc def ghi", expanded.Expanded)
		assert.Equal(t, "academic", expanded.Intent)
	})
}
