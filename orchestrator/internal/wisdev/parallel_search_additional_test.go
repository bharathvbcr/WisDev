package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-redis/redismock/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"golang.org/x/sync/semaphore"
)

func TestParallelSearchHelpers(t *testing.T) {
	t.Run("mapping helpers", func(t *testing.T) {
		opts := mapSearchOpts(SearchOptions{
			Limit:       5,
			Domain:      " medicine ",
			YearFrom:    2020,
			YearTo:      2024,
			SkipCache:   true,
			QualitySort: true,
		})
		assert.Equal(t, "medicine", opts.Domain)
		assert.True(t, opts.SkipCache)

		source := mapPaperToSource(internalsearch.Paper{
			ID:                       "paper-1",
			Title:                    "Paper Title",
			Abstract:                 "Abstract",
			Link:                     "https://example.com",
			DOI:                      "10.1/abc",
			Source:                   "arxiv",
			SourceApis:               []string{"arxiv", "openalex"},
			Authors:                  []string{"A", "B"},
			Year:                     2024,
			Month:                    5,
			Venue:                    "Nature",
			Keywords:                 []string{"ai", "biology"},
			Score:                    0.9,
			CitationCount:            11,
			ReferenceCount:           7,
			InfluentialCitationCount: 3,
			OpenAccessUrl:            "https://example.com/oa",
			PdfUrl:                   "https://example.com/paper.pdf",
			FullText:                 "full text",
			StructureMap:             []any{"section-1"},
		})
		assert.Equal(t, "paper-1", source.ID)
		assert.Equal(t, "Abstract", source.Abstract)
		assert.Equal(t, "Abstract", source.Summary)
		assert.Equal(t, []string{"arxiv", "openalex"}, source.SourceApis)
		assert.Equal(t, "arxiv", source.SiteName)
		assert.Equal(t, "Nature", source.Publication)
		assert.Equal(t, []string{"A", "B"}, source.Authors)
		assert.Equal(t, []string{"ai", "biology"}, source.Keywords)
		assert.Equal(t, 5, source.Month)
		assert.Equal(t, 7, source.ReferenceCount)
		assert.Equal(t, 3, source.InfluentialCitationCount)
		assert.Equal(t, "https://example.com/oa", source.OpenAccessUrl)
		assert.Equal(t, "https://example.com/paper.pdf", source.PdfUrl)
		assert.Equal(t, "full text", source.FullText)
		assert.Equal(t, []any{"section-1"}, source.StructureMap)

		multi := mapSearchResultToMultiSource(internalsearch.SearchResult{
			Papers: []internalsearch.Paper{
				{ID: "1", Title: "S1", Source: "Semantic_Scholar"},
				{ID: "2", Title: "S2", Source: "OpenAlex"},
				{ID: "3", Title: "S3", Source: "PubMed"},
			},
			LatencyMs: 42,
			Cached:    true,
		}, EnhancedQuery{Original: "q"})
		assert.Equal(t, int64(42), multi.Timing.Total)
		assert.True(t, multi.Cached)
		assert.Equal(t, 1, multi.Sources.SemanticScholar)
		assert.Equal(t, 1, multi.Sources.OpenAlex)
		assert.Equal(t, 1, multi.Sources.PubMed)
	})

	t.Run("parallel search propagates retrieval metadata", func(t *testing.T) {
		originalSearch := runUnifiedParallelSearch
		originalExpand := expandQueryAnalysis
		t.Cleanup(func() {
			runUnifiedParallelSearch = originalSearch
			expandQueryAnalysis = originalExpand
		})

		expandQueryAnalysis = func(_ context.Context, query string) (EnhancedQuery, error) {
			return EnhancedQuery{
				Original: query,
				Expanded: query + " expanded",
				Keywords: []string{"alpha", "beta"},
			}, nil
		}
		runUnifiedParallelSearch = func(ctx context.Context, r *internalsearch.ProviderRegistry, query string, opts internalsearch.SearchOpts) internalsearch.SearchResult {
			assert.Equal(t, "base query expanded", query)
			return internalsearch.SearchResult{
				Papers: []internalsearch.Paper{
					{ID: "1", Title: "Paper One", Source: "Semantic_Scholar"},
				},
				Providers: map[string]int{"semantic_scholar": 1},
				LatencyMs: 27,
				Warnings: []internalsearch.ProviderWarning{
					{Provider: "openalex", Message: "timeout"},
				},
			}
		}

		result, err := ParallelSearch(context.Background(), nil, "base query", SearchOptions{
			Limit:               5,
			ExpandQuery:         true,
			QualitySort:         true,
			Domain:              "medicine",
			YearFrom:            2020,
			YearTo:              2024,
			TraceID:             "trace-123",
			PageIndexRerank:     false,
			RetrievalStrategies: []string{RetrievalStrategyLexicalBroad},
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "trace-123", result.TraceID)
		assert.Equal(t, "base query expanded", result.QueryUsed)
		assert.Equal(t, []string{RetrievalStrategyLexicalBroad}, result.RetrievalStrategies)
		if assert.GreaterOrEqual(t, len(result.RetrievalTrace), 1) {
			// Find the provider_registry trace entry
			var found bool
			for _, trace := range result.RetrievalTrace {
				if trace["strategy"] == "provider_registry" {
					assert.Equal(t, "base query expanded", trace["queryUsed"])
					assert.Equal(t, "medicine", trace["domain"])
					assert.Equal(t, false, trace["pageIndexRerank"])
					assert.EqualValues(t, 27, trace["latencyMs"])
					found = true
				}
			}
			assert.True(t, found, "expected provider_registry entry in trace")
		}
	})

	t.Run("cache key is stable across equivalent option ordering", func(t *testing.T) {
		keyA := buildSearchCacheKey("graph rag", SearchOptions{
			Limit:               10,
			Domain:              "ai",
			Sources:             []string{"openalex", "semantic_scholar"},
			RetrievalStrategies: []string{RetrievalStrategySemanticFocus, RetrievalStrategyLexicalBroad},
		})
		keyB := buildSearchCacheKey("graph rag", SearchOptions{
			Limit:               10,
			Domain:              "ai",
			Sources:             []string{"semantic_scholar", "openalex"},
			RetrievalStrategies: []string{RetrievalStrategyLexicalBroad, RetrievalStrategySemanticFocus},
		})
		assert.Equal(t, keyA, keyB)
		assert.Len(t, keyA, 64)
		assert.NotContains(t, keyA, "graph rag")
	})

	t.Run("search options normalize year ranges and source casing", func(t *testing.T) {
		normalized := normalizedSearchOptions(SearchOptions{
			YearFrom: 2025,
			YearTo:   2020,
			Sources:  []string{" OpenAlex ", "SEMANTIC_SCHOLAR", "openalex"},
		})
		assert.Equal(t, 2020, normalized.YearFrom)
		assert.Equal(t, 2025, normalized.YearTo)
		assert.Equal(t, []string{"openalex", "semantic_scholar"}, normalized.Sources)
	})

	t.Run("query normalization collapses whitespace", func(t *testing.T) {
		assert.Equal(t, "graph rag retrieval", normalizeSearchQuery("  graph   rag\nretrieval  "))
	})

	t.Run("legacy direct provider functions fail explicitly", func(t *testing.T) {
		for name, fn := range map[string]func(context.Context, string, int) ([]Source, error){
			"Semantic Scholar": searchSemanticScholar,
			"OpenAlex":         searchOpenAlex,
			"PubMed":           searchPubMed,
			"CORE":             searchCORE,
			"arXiv":            searchArXiv,
		} {
			t.Run(name, func(t *testing.T) {
				papers, err := fn(context.Background(), "graph rag", 5)
				assert.Nil(t, papers)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unified provider registry")
			})
		}

		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		papers, err := searchOpenAlex(cancelled, "graph rag", 5)
		assert.Nil(t, papers)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("parallel search degrades when expansion fails", func(t *testing.T) {
		originalSearch := runUnifiedParallelSearch
		originalExpand := expandQueryAnalysis
		t.Cleanup(func() {
			runUnifiedParallelSearch = originalSearch
			expandQueryAnalysis = originalExpand
		})

		expandQueryAnalysis = func(_ context.Context, query string) (EnhancedQuery, error) {
			return EnhancedQuery{}, errors.New("expansion unavailable")
		}
		runUnifiedParallelSearch = func(ctx context.Context, r *internalsearch.ProviderRegistry, query string, opts internalsearch.SearchOpts) internalsearch.SearchResult {
			assert.Equal(t, "base query", query)
			return internalsearch.SearchResult{
				Papers:    []internalsearch.Paper{{ID: "1", Title: "Paper One", Source: "Semantic_Scholar"}},
				LatencyMs: 19,
			}
		}

		result, err := ParallelSearch(context.Background(), nil, "base query", SearchOptions{
			Limit:       5,
			ExpandQuery: true,
			QualitySort: true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "base query", result.QueryUsed)
		require.Len(t, result.RetrievalTrace, 2)
		assert.Equal(t, "degraded_to_original_query", result.RetrievalTrace[1]["status"])
	})

	t.Run("parallel search falls back to original query when expanded search is empty", func(t *testing.T) {
		originalSearch := runUnifiedParallelSearch
		originalExpand := expandQueryAnalysis
		t.Cleanup(func() {
			runUnifiedParallelSearch = originalSearch
			expandQueryAnalysis = originalExpand
		})

		calls := make([]string, 0, 2)
		expandQueryAnalysis = func(_ context.Context, query string) (EnhancedQuery, error) {
			return EnhancedQuery{
				Original: query,
				Expanded: query + " expanded",
			}, nil
		}
		runUnifiedParallelSearch = func(ctx context.Context, r *internalsearch.ProviderRegistry, query string, opts internalsearch.SearchOpts) internalsearch.SearchResult {
			calls = append(calls, query)
			if query == "base query expanded" {
				return internalsearch.SearchResult{LatencyMs: 11}
			}
			return internalsearch.SearchResult{
				Papers:    []internalsearch.Paper{{ID: "paper-1", Title: "Recovered Paper", Source: "openalex"}},
				LatencyMs: 13,
			}
		}

		result, err := ParallelSearch(context.Background(), nil, "base query", SearchOptions{
			Limit:       5,
			ExpandQuery: true,
			QualitySort: true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, []string{"base query expanded", "base query"}, calls)
		assert.Equal(t, "base query", result.QueryUsed)
		require.NotEmpty(t, result.Papers)
		assert.Equal(t, "Recovered Paper", result.Papers[0].Title)
		foundFallback := false
		for _, trace := range result.RetrievalTrace {
			if trace["status"] == "fallback_to_original_query_succeeded" {
				foundFallback = true
				break
			}
		}
		assert.True(t, foundFallback)
	})

	t.Run("parallel search degrades when registry init panics", func(t *testing.T) {
		originalBuilder := buildUnifiedSearchRegistry
		t.Cleanup(func() {
			buildUnifiedSearchRegistry = originalBuilder
		})

		buildUnifiedSearchRegistry = func(requestedProviders ...string) *internalsearch.ProviderRegistry {
			_ = requestedProviders
			panic("registry unavailable")
		}

		result, err := ParallelSearch(context.Background(), nil, "base query", SearchOptions{
			Limit:       5,
			QualitySort: true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Empty(t, result.Papers)
		foundInitError := false
		foundNoRegistry := false
		for _, trace := range result.RetrievalTrace {
			if trace["status"] == "registry_init_error" {
				foundInitError = true
			}
			if trace["status"] == "skipped_no_registry" {
				foundNoRegistry = true
			}
		}
		assert.True(t, foundInitError)
		assert.True(t, foundNoRegistry)
	})

	t.Run("cache hit is rehydrated for the current trace", func(t *testing.T) {
		db, mockRedis := redismock.NewClientMock()
		cachedResult := &MultiSourceResult{
			Papers:    []Source{{ID: "paper-1", Title: "Cached Paper"}},
			TraceID:   "stale-trace",
			QueryUsed: "cached query",
			RetrievalTrace: []map[string]any{
				{"strategy": RetrievalStrategyLexicalBroad, "backend": "provider_registry"},
			},
		}
		cacheKey := buildSearchCacheKey("graph rag", SearchOptions{
			Limit:       10,
			QualitySort: true,
		})
		serialized, err := json.Marshal(cachedResult)
		require.NoError(t, err)
		mockRedis.ExpectGet(searchGatewayCachePrefix + cacheKey).SetVal(string(serialized))

		result, err := ParallelSearch(context.Background(), db, "graph rag", SearchOptions{
			Limit:       10,
			QualitySort: true,
			TraceID:     "fresh-trace",
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.Cached)
		assert.Equal(t, "fresh-trace", result.TraceID)
		require.Len(t, result.RetrievalTrace, 2)
		assert.Equal(t, "cache_hit", result.RetrievalTrace[1]["strategy"])
	})

	t.Run("stage2 rerank panic preserves existing order", func(t *testing.T) {
		originalSearch := runUnifiedParallelSearch
		originalShouldStage2 := shouldApplyStage2Rerank
		originalStage2 := applyStage2Rerank
		originalShouldPageIndex := shouldRunPageIndexRerank
		t.Cleanup(func() {
			runUnifiedParallelSearch = originalSearch
			shouldApplyStage2Rerank = originalShouldStage2
			applyStage2Rerank = originalStage2
			shouldRunPageIndexRerank = originalShouldPageIndex
		})

		runUnifiedParallelSearch = func(ctx context.Context, r *internalsearch.ProviderRegistry, query string, opts internalsearch.SearchOpts) internalsearch.SearchResult {
			return internalsearch.SearchResult{
				Papers: []internalsearch.Paper{
					{ID: "paper-1", Title: "Paper 1", Source: "semantic_scholar"},
					{ID: "paper-2", Title: "Paper 2", Source: "openalex"},
				},
			}
		}
		shouldApplyStage2Rerank = func(requested bool) bool { return requested }
		applyStage2Rerank = func(ctx context.Context, query string, papers []Source, domain string, topK int) []Source {
			panic("stage2 unavailable")
		}
		shouldRunPageIndexRerank = func(requested bool) bool { return false }

		result, err := ParallelSearch(context.Background(), nil, "graph rag", SearchOptions{
			Limit:        5,
			QualitySort:  true,
			Stage2Rerank: true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Papers, 2)
		assert.Equal(t, "Paper 1", result.Papers[0].Title)
		foundPreserved := false
		for _, trace := range result.RetrievalTrace {
			if trace["strategy"] == "stage2_rerank" && trace["status"] == "preserved_existing_order" {
				foundPreserved = true
				break
			}
		}
		assert.True(t, foundPreserved)
	})

	t.Run("rerank gates emit skipped traces when disabled", func(t *testing.T) {
		originalSearch := runUnifiedParallelSearch
		originalShouldStage2 := shouldApplyStage2Rerank
		originalShouldPageIndex := shouldRunPageIndexRerank
		t.Cleanup(func() {
			runUnifiedParallelSearch = originalSearch
			shouldApplyStage2Rerank = originalShouldStage2
			shouldRunPageIndexRerank = originalShouldPageIndex
		})

		runUnifiedParallelSearch = func(ctx context.Context, r *internalsearch.ProviderRegistry, query string, opts internalsearch.SearchOpts) internalsearch.SearchResult {
			return internalsearch.SearchResult{
				Papers: []internalsearch.Paper{{ID: "paper-1", Title: "Paper 1", Source: "semantic_scholar"}},
			}
		}
		shouldApplyStage2Rerank = func(requested bool) bool { return false }
		shouldRunPageIndexRerank = func(requested bool) bool { return false }

		result, err := ParallelSearch(context.Background(), nil, "graph rag", SearchOptions{
			Limit:               5,
			QualitySort:         true,
			Stage2Rerank:        true,
			PageIndexRerank:     true,
			RetrievalStrategies: []string{RetrievalStrategyLexicalBroad},
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		foundStage2 := false
		foundPageIndex := false
		for _, trace := range result.RetrievalTrace {
			if trace["strategy"] == "stage2_rerank" && trace["status"] == "skipped_gate_disabled" {
				foundStage2 = true
			}
			if trace["strategy"] == "pageindex_rerank" && trace["status"] == "skipped_gate_disabled" {
				foundPageIndex = true
			}
		}
		assert.True(t, foundStage2)
		assert.True(t, foundPageIndex)
	})

	t.Run("result set is capped to the requested limit without rerank", func(t *testing.T) {
		originalSearch := runUnifiedParallelSearch
		originalShouldStage2 := shouldApplyStage2Rerank
		originalShouldPageIndex := shouldRunPageIndexRerank
		t.Cleanup(func() {
			runUnifiedParallelSearch = originalSearch
			shouldApplyStage2Rerank = originalShouldStage2
			shouldRunPageIndexRerank = originalShouldPageIndex
		})

		runUnifiedParallelSearch = func(ctx context.Context, r *internalsearch.ProviderRegistry, query string, opts internalsearch.SearchOpts) internalsearch.SearchResult {
			return internalsearch.SearchResult{
				Papers: []internalsearch.Paper{
					{ID: "paper-1", Title: "Paper 1", Source: "semantic_scholar"},
					{ID: "paper-2", Title: "Paper 2", Source: "semantic_scholar"},
					{ID: "paper-3", Title: "Paper 3", Source: "semantic_scholar"},
				},
			}
		}
		shouldApplyStage2Rerank = func(requested bool) bool { return false }
		shouldRunPageIndexRerank = func(requested bool) bool { return false }

		result, err := ParallelSearch(context.Background(), nil, "graph rag", SearchOptions{
			Limit:       2,
			QualitySort: false,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Len(t, result.Papers, 2)
	})

	t.Run("type helpers and query utilities", func(t *testing.T) {
		assert.True(t, isMedicalQuery("clinical treatment for disease"))
		assert.False(t, isMedicalQuery("computer vision"))
		assert.Equal(t, "alpha", firstNonEmptyString([]string{"", " alpha ", "beta"}))
		assert.Equal(t, []string{"a", "b"}, limitStrings([]string{"a", "b"}, 5))
		assert.Equal(t, []string{"a"}, limitStrings([]string{"a", "b"}, 1))

		assert.Equal(t, 12, intFromAny("12"))
		assert.Equal(t, 12, intFromAny(json.Number("12")))
		assert.Equal(t, 1, intFromAny(float64(1.9)))
		assert.Equal(t, 2.5, floatFromAny("2.5"))
		assert.Equal(t, 2.5, floatFromAny(json.Number("2.5")))
		assert.True(t, boolFromAny("true"))
		assert.False(t, boolFromAny("not-bool"))
		assert.Equal(t, 0.5, clampFloat64(0.5, 0, 1))
		assert.Equal(t, 0.0, clampFloat64(-1, 0, 1))
		assert.Equal(t, 1.0, clampFloat64(2, 0, 1))
	})

	t.Run("follow up queries and PRM", func(t *testing.T) {
		originalExpand := expandQueryAnalysis
		expandQueryAnalysis = func(_ context.Context, query string) (EnhancedQuery, error) {
			return EnhancedQuery{
				Original: query,
				Expanded: query + " neural",
				Keywords: []string{"alpha", "beta"},
			}, nil
		}
		t.Cleanup(func() {
			expandQueryAnalysis = originalExpand
		})

		queries := buildDeterministicFollowUpQueries(context.Background(), []string{"machine learning"}, []Source{
			{Title: "Deep Neural Networks", Summary: "Transformers and CNNs"},
		})
		assert.Contains(t, queries, "machine learning")
		assert.NotEmpty(t, queries)

		reward, err := callPRM(context.Background(), "session", map[string]any{
			"paperCount":            10,
			"searchSuccess":         0.8,
			"citationVerifiedRatio": 0.9,
			"coverageScore":         0.7,
			"success":               true,
		})
		require.NoError(t, err)
		assert.Greater(t, reward, 0.8)
	})
}

func TestExecuteWithResilienceBranches(t *testing.T) {
	t.Run("success path", func(t *testing.T) {
		sem := semaphore.NewWeighted(1)
		cb := NewCircuitBreaker("test-success")
		out, err := executeWithResilience(context.Background(), "test", cb, sem, func() (int, error) {
			return 7, nil
		})
		require.NoError(t, err)
		assert.Equal(t, 7, out)
	})

	t.Run("semaphore full", func(t *testing.T) {
		sem := semaphore.NewWeighted(1)
		require.NoError(t, sem.Acquire(context.Background(), 1))
		defer sem.Release(1)

		out, err := executeWithResilience(context.Background(), "test", nil, sem, func() (int, error) {
			return 0, nil
		})
		assert.Error(t, err)
		assert.Zero(t, out)
	})

	t.Run("breaker open", func(t *testing.T) {
		sem := semaphore.NewWeighted(1)
		cb := NewCircuitBreaker("test-open", WithFailureThreshold(1))
		cb.RecordFailure()

		out, err := executeWithResilience(context.Background(), "test", cb, sem, func() (int, error) {
			return 0, errors.New("should not run")
		})
		assert.Error(t, err)
		assert.Zero(t, out)
	})
}
