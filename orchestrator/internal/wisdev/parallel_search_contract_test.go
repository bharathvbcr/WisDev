package wisdev

import (
	"context"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestParallelSearch_Contract(t *testing.T) {
	// Backup and restore the global search runner
	oldRunner := runUnifiedParallelSearch
	defer func() { runUnifiedParallelSearch = oldRunner }()

	t.Run("Empty upstream result returns traceable zero results", func(t *testing.T) {
		runUnifiedParallelSearch = func(ctx context.Context, reg *search.ProviderRegistry, query string, opts search.SearchOpts) search.SearchResult {
			return search.SearchResult{
				Papers:    []search.Paper{},
				Providers: map[string]int{"mock": 0},
				LatencyMs: 10,
			}
		}

		res, err := ParallelSearch(context.Background(), nil, "test query", SearchOptions{})
		assert.NoError(t, err)
		assert.NotNil(t, res)
		assert.Empty(t, res.Papers)
		assert.Equal(t, "test query", res.QueryUsed)
	})

	t.Run("Partial failure returns degraded output with warnings", func(t *testing.T) {
		runUnifiedParallelSearch = func(ctx context.Context, reg *search.ProviderRegistry, query string, opts search.SearchOpts) search.SearchResult {
			return search.SearchResult{
				Papers: []search.Paper{
					{ID: "p1", Title: "Success Paper", Source: "provider1"},
				},
				Providers: map[string]int{"provider1": 1},
				Warnings: []search.ProviderWarning{
					{Provider: "provider2", Message: "rate limit exceeded"},
				},
				LatencyMs: 20,
			}
		}

		res, err := ParallelSearch(context.Background(), nil, "test query", SearchOptions{})
		assert.NoError(t, err)
		assert.NotNil(t, res)
		assert.Len(t, res.Papers, 1)

		// Verify warning propagation in RetrievalTrace
		foundWarning := false
		for _, trace := range res.RetrievalTrace {
			if trace["provider"] == "provider2" && trace["status"] == "warning" {
				foundWarning = true
				assert.Equal(t, "rate limit exceeded", trace["message"])
			}
		}
		assert.True(t, foundWarning, "expected to find provider warning in retrieval trace")
	})

	t.Run("Query normalization survival", func(t *testing.T) {
		var capturedQuery string
		runUnifiedParallelSearch = func(ctx context.Context, reg *search.ProviderRegistry, query string, opts search.SearchOpts) search.SearchResult {
			capturedQuery = query
			return search.SearchResult{}
		}

		_, _ = ParallelSearch(context.Background(), nil, "  Spaces   Around  ", SearchOptions{})
		assert.Equal(t, "Spaces Around", capturedQuery)
	})

	t.Run("Strict empty query rejection", func(t *testing.T) {
		res, err := ParallelSearch(context.Background(), nil, "   ", SearchOptions{})
		assert.Error(t, err)
		assert.Nil(t, res)
		assert.Contains(t, err.Error(), "query is required")
	})
}

func TestMapPaperToSource_MetadataContract(t *testing.T) {
	paper := search.Paper{
		ID:            "id123",
		Title:         "Test Title",
		Abstract:      "Test Abstract",
		Link:          "https://example.com/p1",
		DOI:           "10.1234/test",
		ArxivID:       "2401.12345",
		Source:        "semantic_scholar",
		Authors:       []string{"Author A", "Author B"},
		Year:          2024,
		CitationCount: 42,
		PdfUrl:        "https://example.com/p1.pdf",
	}

	source := mapPaperToSource(paper)

	assert.Equal(t, paper.ID, source.ID)
	assert.Equal(t, paper.Title, source.Title)
	assert.Equal(t, paper.Abstract, source.Summary)
	assert.Equal(t, paper.Link, source.Link)
	assert.Equal(t, paper.DOI, source.DOI)
	assert.Equal(t, paper.ArxivID, source.ArxivID)
	assert.Equal(t, paper.Source, source.Source)
	assert.Equal(t, paper.Authors, source.Authors)
	assert.Equal(t, paper.Year, source.Year)
	assert.Equal(t, paper.CitationCount, source.CitationCount)
	assert.Equal(t, paper.PdfUrl, source.PdfUrl)
}
