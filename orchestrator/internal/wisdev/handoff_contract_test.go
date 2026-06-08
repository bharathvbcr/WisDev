package wisdev

import (
	"context"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestHandoff_OrchestrationPlanQueries(t *testing.T) {
	session := &Session{
		Query: "original query",
		Answers: map[string]Answer{
			"q4_subtopics": {Values: []string{"subtopic A", "subtopic B"}},
			"q2_scope":     {Values: []string{"focused"}},
		},
	}

	gq := GenerateSearchQueries(session)
	assert.GreaterOrEqual(t, gq.QueryCount, 1)
	assert.Contains(t, gq.Queries, "original query")

	// Verify subtopic enrichment
	foundSubtopic := false
	for _, q := range gq.Queries {
		if (q == "original query subtopic A") || (q == "original query subtopic B") {
			foundSubtopic = true
			break
		}
	}
	assert.True(t, foundSubtopic, "expected subtopic enrichment in generated queries")
}

func TestHandoff_EvidenceRanking(t *testing.T) {
	source := Source{
		ID:            "p1",
		Title:         "Title 1",
		Summary:       "This is a unique summary sentence.",
		Abstract:      "This is a unique abstract sentence. Another unique abstract sentence.",
		DOI:           "10.1234/p1",
		CitationCount: 100,
	}

	findings := buildEvidenceFindingsFromSource(source, 2)
	assert.Len(t, findings, 2)
	for _, f := range findings {
		// buildEvidenceFindingsFromSource prioritizes DOI as SourceID
		assert.Equal(t, "10.1234/p1", f.SourceID)
		assert.NotEmpty(t, f.Claim)
		assert.Greater(t, f.Confidence, 0.0)
	}
}

func TestHandoff_TreeConfirmedQueryPreference(t *testing.T) {
	// This test simulates the logic in RunLoop where seedQueries (tree-confirmed)
	// are used if provided, bypassing regeneration.

	req := LoopRequest{
		Query:       "base query",
		SeedQueries: []string{"tree confirmed 1", "tree confirmed 2"},
	}

	// Logic from RunLoop:
	plannedQueries := normalizeLoopQueries(req.Query, req.SeedQueries)

	assert.Equal(t, "base query", plannedQueries[0])
	assert.Contains(t, plannedQueries, "tree confirmed 1")
	assert.Contains(t, plannedQueries, "tree confirmed 2")
	assert.Len(t, plannedQueries, 3)
}

func TestHandoff_EmptyUpstreamResultNotSuccess(t *testing.T) {
	// Verify that ParallelSearch correctly indicates zero results as a warning state
	// even if the call itself doesn't return an error.

	oldRunner := runUnifiedParallelSearch
	defer func() { runUnifiedParallelSearch = oldRunner }()

	runUnifiedParallelSearch = func(ctx context.Context, reg *search.ProviderRegistry, query string, opts search.SearchOpts) search.SearchResult {
		return search.SearchResult{
			Papers: []search.Paper{},
			Warnings: []search.ProviderWarning{
				{Provider: "all", Message: "no results found"},
			},
		}
	}

	res, err := ParallelSearch(context.Background(), nil, "test", SearchOptions{})
	assert.NoError(t, err)
	assert.Empty(t, res.Papers)

	// Check that the zero-result warning is traceable
	foundWarning := false
	for _, t := range res.RetrievalTrace {
		if t["status"] == "warning" {
			foundWarning = true
			break
		}
	}
	assert.True(t, foundWarning, "expected traceable warning for zero results")
}
