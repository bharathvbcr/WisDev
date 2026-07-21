package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeriveSubtopics(t *testing.T) {
	query := "climate change impacts on arctic sea ice"
	domain := "environmental science"

	subtopics, keywords, variations := deriveSubtopics(query, domain, 5)

	assert.NotEmpty(t, subtopics)
	assert.NotEmpty(t, keywords)
	assert.NotEmpty(t, variations)
	assert.Contains(t, keywords, "arctic")
	assert.Equal(t, 5, len(subtopics))
}

func TestDeriveStudyTypes(t *testing.T) {
	query := "systematic review of transformer models"
	domain := "ai"
	subtopics := []string{"Performance", "Architecture"}

	studyTypes, signals := deriveStudyTypes(query, domain, subtopics, 5)

	assert.Contains(t, studyTypes, "systematic review")
	assert.Contains(t, signals, "review_intent")
}

func TestEstimatePathScore(t *testing.T) {
	score := estimatePathScore("query", []string{"s1", "s2"}, []string{"t1"})
	assert.Greater(t, score, 0.35)
	assert.LessOrEqual(t, score, 1.0)
}

func TestEvaluateCoverage(t *testing.T) {
	query := "machine learning"
	queries := []string{"machine learning basics"}
	results := []map[string]any{
		{"title": "Intro to Machine Learning", "summary": "ML basics", "abstract": "abc"},
	}

	coverage, missingTerms, recommended := evaluateCoverage(query, queries, results, nil)

	assert.Greater(t, coverage, 0.0)
	assert.NotNil(t, missingTerms)
	assert.NotNil(t, recommended)
}

func TestToTitlePhrase(t *testing.T) {
	assert.Equal(t, "Hello World", toTitlePhrase("hello_world"))
	assert.Equal(t, "Machine Learning", toTitlePhrase("machine learning"))
}

func TestInferExpertiseLevel(t *testing.T) {
	assert.Equal(t, "beginner", inferExpertiseLevel(0.3, "cs"))
	assert.Equal(t, "expert", inferExpertiseLevel(0.9, "cs"))
	assert.Equal(t, "intermediate", inferExpertiseLevel(0.6, "cs"))
}

func TestTokenizeResearchText(t *testing.T) {
	tokens := tokenizeResearchText("Machine learning with neural networks.")
	assert.Contains(t, tokens, "machine")
	assert.Contains(t, tokens, "learning")
	assert.Contains(t, tokens, "neural")
	assert.Contains(t, tokens, "networks")
	assert.NotContains(t, tokens, "with") // stopword
}
