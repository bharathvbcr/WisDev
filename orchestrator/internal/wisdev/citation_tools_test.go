package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCitationToolsLocalFormatCitation(t *testing.T) {
	item := citationFormatItem{
		ID:    "paper1",
		Title: "Test Paper",
		Author: []citationFormatItemAuthor{
			{Family: "Doe", Given: "John"},
			{Family: "Smith", Given: "Jane"},
		},
		Issued: &citationFormatItemIssued{
			DateParts: [][]int{{2023}},
		},
		ContainerTitle: "Nature",
		DOI:            "10.1234/test",
	}

	t.Run("APA", func(t *testing.T) {
		formatted, err := localFormatCitation("apa", item)
		assert.NoError(t, err)
		assert.Contains(t, formatted, "Doe, J. & Smith, J. (2023). Test Paper. Nature.")
		assert.Contains(t, formatted, "https://doi.org/10.1234/test")
	})

	t.Run("MLA", func(t *testing.T) {
		formatted, err := localFormatCitation("mla", item)
		assert.NoError(t, err)
		assert.Contains(t, formatted, "Doe, John & Smith, Jane. \"Test Paper.\" Nature")
	})

	t.Run("Chicago", func(t *testing.T) {
		formatted, err := localFormatCitation("chicago", item)
		assert.NoError(t, err)
		assert.Contains(t, formatted, "Doe, J. & Smith, J.. \"Test Paper.\" Nature")
	})
}

func TestCitationToolsFormatAuthorsForStyle(t *testing.T) {
	authors := []citationFormatItemAuthor{
		{Family: "A", Given: "B"},
		{Family: "C", Given: "D"},
		{Family: "E", Given: "F"},
	}
	formatted := formatAuthorsForStyle("apa", authors)
	assert.Equal(t, "A, B., et al.", formatted)
}

func TestCitationToolsSplitTerms(t *testing.T) {
	terms := splitTerms("Machine Learning (AI)!")
	assert.Contains(t, terms, "machine")
	assert.Contains(t, terms, "learning")
	assert.Contains(t, terms, "ai")
	assert.NotContains(t, terms, "a") // too short
}

func TestCitationToolsComputeTraceIntegrityHash(t *testing.T) {
	h1 := ComputeTraceIntegrityHash("payload")
	h2 := ComputeTraceIntegrityHash("payload")
	assert.Equal(t, h1, h2)
	assert.NotEmpty(t, h1)
}

func TestCitationToolsRankTools(t *testing.T) {
	tools := []ToolDefinition{
		{Name: "search", Description: "Search for papers", Risk: RiskLevelLow},
		{Name: "unrelated", Description: "Bake a cake", Risk: RiskLevelLow},
	}
	ranked := RankTools("find machine learning papers", tools, 5)
	assert.NotEmpty(t, ranked)
	assert.Equal(t, "search", ranked[0].Name)
}
