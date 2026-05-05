package wisdev

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-redis/redismock/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"golang.org/x/sync/semaphore"
)

func TestParallelSearch_DeduplicatePapers(t *testing.T) {
	papers := []Source{
		{
			Title:         "Paper 1",
			DOI:           "10.1/1",
			Summary:       "S1",
			CitationCount: 10,
			Source:        "openalex",
			SourceApis:    []string{"openalex"},
		},
		{
			Title:         "Paper 1 (Alt)",
			DOI:           "10.1/1",
			Summary:       "",
			Abstract:      "Abstract 1",
			CitationCount: 20,
			Publication:   "Nature",
			Authors:       []string{"Ada Lovelace", "Grace Hopper"},
			SourceApis:    []string{"semantic_scholar"},
			OpenAccessUrl: "https://example.com/oa",
		},
		{Title: "Paper 2", Summary: "S2"},
		{Title: "PAPER 2", Summary: "S2 alt"},
	}

	deduped := deduplicatePapers(papers)
	assert.Len(t, deduped, 2)
	assert.Equal(t, 20, deduped[0].CitationCount)
	assert.Equal(t, "Abstract 1", deduped[0].Abstract)
	assert.Equal(t, "Nature", deduped[0].Publication)
	assert.Equal(t, []string{"Ada Lovelace", "Grace Hopper"}, deduped[0].Authors)
	assert.Equal(t, []string{"openalex", "semantic_scholar"}, deduped[0].SourceApis)
	assert.Equal(t, "https://example.com/oa", deduped[0].OpenAccessUrl)
}

func TestParallelSearch_SortByQuality(t *testing.T) {
	papers := []Source{
		{Title: "P1", Summary: "Has summary", DOI: "10.1", SourceCount: 2},
		{Title: "P2", Summary: "", DOI: "10.2"},
	}
	sorted := sortByQuality(papers, "")
	assert.Equal(t, "P1", sorted[0].Title)
}

func TestFastParallelSearch(t *testing.T) {
	db, mockRedis := redismock.NewClientMock()
	ctx := context.Background()
	query := "quantum"

	opts := SearchOptions{
		Limit:       10,
		ExpandQuery: false,
		QualitySort: true,
	}
	key := fmt.Sprintf("%s:%v", query, opts)
	cacheKey := "search_gateway:" + key

	originalSearch := runUnifiedParallelSearch
	defer func() { runUnifiedParallelSearch = originalSearch }()

	runUnifiedParallelSearch = func(ctx context.Context, r *internalsearch.ProviderRegistry, query string, opts internalsearch.SearchOpts) internalsearch.SearchResult {
		return internalsearch.SearchResult{
			Papers: []internalsearch.Paper{
				{Title: "Quantum Paper", ID: "q1"},
			},
			LatencyMs: 100,
		}
	}

	// 1. First call: cache miss
	mockRedis.ExpectGet(cacheKey).RedisNil()
	mockRedis.ExpectSet(cacheKey, mock.Anything, 24*time.Hour).SetVal("OK")

	// Note: FastParallelSearch DOES NOT set SkipCache=true, so it SHOULD hit cache if available.
	results, err := FastParallelSearch(ctx, db, query, 10)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "Quantum Paper", results[0].Title)

	// 2. Test mock logic by bypassing cache
	results2, err := FastParallelSearch(ctx, db, query, 10)
	assert.NoError(t, err)
	assert.Len(t, results2, 1)
	assert.Equal(t, "Quantum Paper", results2[0].Title)

}

func TestExecuteWithResilience(t *testing.T) {
	cb := NewCircuitBreaker("test", WithFailureThreshold(2), WithResetTimeout(time.Second))
	sem := semaphore.NewWeighted(1)

	t.Run("Success", func(t *testing.T) {
		res, err := executeWithResilience(context.Background(), "test", cb, sem, func() (string, error) {
			return "ok", nil
		})
		assert.NoError(t, err)
		assert.Equal(t, "ok", res)
	})
}

func TestIsMedicalQuery(t *testing.T) {
	assert.True(t, isMedicalQuery("clinical trial"))
	assert.False(t, isMedicalQuery("quantum computing"))
}
