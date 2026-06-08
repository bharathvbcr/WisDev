package api

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

func TestSearchMappingHelpers(t *testing.T) {
	t.Run("mapWisdevProviders", func(t *testing.T) {
		got := mapWisdevProviders(wisdev.SourcesStats{
			SemanticScholar: 2,
			OpenAlex:        1,
			PubMed:          3,
			CORE:            0,
			ArXiv:           4,
			BioRxiv:         5,
			EuropePMC:       6,
			CrossRef:        7,
			DBLP:            8,
			IEEE:            9,
			NASAADS:         10,
		})

		assert.Equal(t, map[string]int{
			"semantic_scholar": 2,
			"openalex":         1,
			"pubmed":           3,
			"arxiv":            4,
			"biorxiv":          5,
			"europe_pmc":       6,
			"crossref":         7,
			"dblp":             8,
			"ieee":             9,
			"nasa_ads":         10,
		}, got)
	})

	t.Run("parseOpenSearchAuthors", func(t *testing.T) {
		assert.Equal(t, []string{"Alice", "Bob"}, parseOpenSearchAuthors([]string{"Alice", "Bob"}, nil))
		assert.Equal(t, []string{"Carol", "Dan"}, parseOpenSearchAuthors([]any{"Carol", map[string]any{"name": "Dan"}}, nil))
		assert.Equal(t, []string{"Eve", "Frank"}, parseOpenSearchAuthors("Eve; Frank", nil))
		assert.Equal(t, []string{"Grace", "Heidi"}, parseOpenSearchAuthors(nil, "Grace, Heidi"))
		assert.Empty(t, parseOpenSearchAuthors(nil, nil))
	})

	t.Run("parseOpenSearchYear", func(t *testing.T) {
		assert.Equal(t, 2024, parseOpenSearchYear(2024))
		assert.Equal(t, 2023, parseOpenSearchYear(2023.9))
		assert.Equal(t, 2022, parseOpenSearchYear("2022"))
		assert.Equal(t, 0, parseOpenSearchYear("not-a-year"))
		assert.Equal(t, 0, parseOpenSearchYear(nil))
	})

	t.Run("mapOpenSearchPaper", func(t *testing.T) {
		p := mapOpenSearchPaper(map[string]any{
			"id":             "paper-1",
			"title":          "Open Search Paper",
			"abstract":       "Abstract text",
			"doi":            "10.1/abc",
			"landingUrl":     "https://example.com/paper-1",
			"venue":          "Conference",
			"citationCount":  7.0,
			"relevanceScore": 0.81,
			"authors":        "Alice; Bob",
			"year":           "2024",
		})

		assert.Equal(t, "paper-1", p.ID)
		assert.Equal(t, "Open Search Paper", p.Title)
		assert.Equal(t, "Abstract text", p.Abstract)
		assert.Equal(t, "10.1/abc", p.DOI)
		assert.Equal(t, "https://example.com/paper-1", p.Link)
		assert.Equal(t, "Conference", p.Venue)
		assert.Equal(t, 7, p.CitationCount)
		assert.Equal(t, 0.81, p.Score)
		assert.Equal(t, []string{"Alice", "Bob"}, p.Authors)
		assert.Equal(t, 2024, p.Year)
		assert.Equal(t, "opensearch_hybrid", p.Source)
	})

	t.Run("truncateForLog", func(t *testing.T) {
		assert.Equal(t, "short", truncateForLog("short", 10))
		assert.Equal(t, "12345...", truncateForLog("123456789", 5))
	})

	t.Run("summarizeParallelAuthorCoverage", func(t *testing.T) {
		got := summarizeParallelAuthorCoverage([]search.Paper{
			{ID: "paper-1", Title: "Paper With Authors", Source: "openalex", Authors: []string{"Ada Lovelace"}},
			{ID: "paper-2", Title: "Missing Authors One", Source: "pubmed"},
			{ID: "paper-3", Title: "Missing Authors Two", Source: "pubmed", Authors: []string{"   "}},
			{ID: "paper-4", Source: "crossref"},
		})

		assert.Equal(t, 1, got.PapersWithAuthors)
		assert.Equal(t, 3, got.PapersWithoutAuthors)
		assert.False(t, got.AuthorsMissingAll)
		assert.Equal(t, []string{"crossref", "pubmed"}, got.MissingAuthorProviders)
		assert.Equal(t, "partial", got.resultLabel())
		assert.Len(t, got.MissingAuthorSamples, 3)
		assert.Contains(t, got.MissingAuthorSamples[0], "pubmed:")
		assert.Contains(t, got.MissingAuthorSamples[2], "crossref:")
	})

	t.Run("summarizeParallelAuthorCoverage_allMissing", func(t *testing.T) {
		got := summarizeParallelAuthorCoverage([]search.Paper{
			{ID: "paper-1", Title: "Authorless A", Source: "openalex"},
			{ID: "paper-2", Title: "Authorless B", Source: "pubmed"},
		})

		assert.Equal(t, 0, got.PapersWithAuthors)
		assert.Equal(t, 2, got.PapersWithoutAuthors)
		assert.True(t, got.AuthorsMissingAll)
		assert.Equal(t, "missing_all", got.resultLabel())
	})
}
