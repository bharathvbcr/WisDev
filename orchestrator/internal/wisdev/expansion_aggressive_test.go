package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestS2SynonymVariations(t *testing.T) {
	query := "machine learning models"
	vars := s2SynonymVariations(query)
	assert.NotEmpty(t, vars)
	assert.Equal(t, "synonym", vars[0].Strategy)
	assert.Contains(t, vars[0].Query, "ml")
}

func TestS3MeSHVariations(t *testing.T) {
	query := "heart attack symptoms"
	vars := s3MeSHVariations(query)
	assert.NotEmpty(t, vars)
	assert.Equal(t, "mesh", vars[0].Strategy)
	assert.Contains(t, vars[0].Query, "myocardial infarction")
}

func TestS4AbbreviationVariations(t *testing.T) {
	query := "ml research"
	vars := s4AbbreviationVariations(query)
	assert.NotEmpty(t, vars)
	assert.Contains(t, vars[0].Query, "machine learning")

	query2 := "machine learning research"
	vars2 := s4AbbreviationVariations(query2)
	assert.NotEmpty(t, vars2)
	assert.Contains(t, vars2[0].Query, "ML")
}

func TestS12PermutationVariations(t *testing.T) {
	words := []string{"quantum", "computing"}
	vars := s12PermutationVariations(words)
	assert.Len(t, vars, 1)
	assert.Equal(t, "computing quantum", vars[0].Query)

	words3 := []string{"deep", "learning", "models"}
	vars3 := s12PermutationVariations(words3)
	assert.Len(t, vars3, 1)
	assert.Equal(t, "learning models deep", vars3[0].Query)
}

func TestGenerateAggressiveExpansion(t *testing.T) {
	query := "transformer models"
	resp := GenerateAggressiveExpansion(nil, query, 5, true, true, true, []string{"arxiv"})

	assert.Equal(t, query, resp.Original)
	assert.NotEmpty(t, resp.Variations)
	assert.LessOrEqual(t, len(resp.Variations), 5)

	strategies := resp.Metadata.Strategies
	assert.Contains(t, strategies, "original")
}

func TestCalculateCoverageEstimate(t *testing.T) {
	cov := calculateCoverageEstimate(15, 10)
	assert.Equal(t, 1.0, cov)

	cov2 := calculateCoverageEstimate(0, 0)
	assert.Equal(t, 0.0, cov2)
}

func TestDeduplicateQueryVariations(t *testing.T) {
	vs := []QueryVariation{
		{Query: "Test", Strategy: "s1"},
		{Query: " test ", Strategy: "s2"},
		{Query: "unique", Strategy: "s3"},
	}
	deduped := deduplicateQueryVariations(vs)
	assert.Len(t, deduped, 2)
}
