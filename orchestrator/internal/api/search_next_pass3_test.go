package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func TestSearchHandlers_AdditionalCoverage(t *testing.T) {
	reg := search.NewProviderRegistry()
	h := NewSearchHandler(reg, reg, nil)

	t.Run("HandleParallelSearch - Trace ID fallback", func(t *testing.T) {
		originalParallel := wisdev.ParallelSearch
		t.Cleanup(func() {
			wisdev.ParallelSearch = originalParallel
		})

		wisdev.ParallelSearch = func(_ context.Context, _ redis.UniversalClient, _ string, _ wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
			return &wisdev.MultiSourceResult{
				Papers: []wisdev.Source{
					{ID: "paper-1", Title: "Trace fallback", Source: "openalex", Authors: []string{"A"}},
				},
				EnhancedQuery: wisdev.EnhancedQuery{
					Original: "trace fallback",
					Intent:   "papers",
				},
				Sources: wisdev.SourcesStats{
					OpenAlex: 1,
				},
				Timing: wisdev.TimingStats{
					Total: 1,
				},
				QueryUsed: "trace fallback",
				TraceID:   "",
			}, nil
		}

		req := httptest.NewRequest(http.MethodGet, "/parallel?q=trace+fallback", nil)
		req.Header.Set("X-Trace-Id", "trace-from-header")
		rec := httptest.NewRecorder()

		h.HandleParallelSearch(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var payload gatewayParallelResponse
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		assert.Equal(t, "trace-from-header", payload.TraceID)
		assert.Equal(t, 1, len(payload.Papers))
	})

	t.Run("HandleToolSearch - Success", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		reg.Register(&MockProvider{
			name: "mock1",
			papers: []search.Paper{{
				ID:    "p1",
				Title: "Tool result",
			}},
		})
		h := NewSearchHandler(reg, reg, nil)

		req := httptest.NewRequest(http.MethodPost, "/tool", strings.NewReader(`{"tool":"wisdevSearchPapers","params":{"query":"test"}}`))
		rec := httptest.NewRecorder()
		h.HandleToolSearch(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		papers, ok := payload["papers"].([]any)
		assert.True(t, ok)
		assert.Len(t, papers, 1)
	})

	t.Run("HandleQueryField - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/field", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		h.HandleQueryField(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errPayload := payload["error"].(map[string]any)
		assert.Equal(t, "BAD_REQUEST", errPayload["code"])
	})

	t.Run("HandleQueryCategories - Method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/categories", strings.NewReader(`{"query":"deep learning"}`))
		rec := httptest.NewRecorder()
		h.HandleQueryCategories(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleQueryCategories - Domain switch coverage", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			query string
		}{
			{name: "computerscience", query: "deep learning from experience"},
			{name: "medicine", query: "clinical trial therapy outcomes"},
			{name: "biology", query: "genome editing with CRISPR systems"},
			{name: "psychology", query: "cognitive behavior and memory"},
			{name: "physics", query: "quantum particle wave optics"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, "/categories", strings.NewReader(`{"query":"`+tc.query+`"}`))
				rec := httptest.NewRecorder()
				h.HandleQueryCategories(rec, req)

				assert.Equal(t, http.StatusOK, rec.Code)
				var payload map[string]any
				assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
				categories := payload["categories"].([]any)
				assert.NotEmpty(t, categories)
			})
		}
	})

	t.Run("HandleQueryIntroduction - Method not allowed", func(t *testing.T) {
		introReq := httptest.NewRequest(http.MethodGet, "/introduction", strings.NewReader(`{"query":"graph rag"}`))
		introRec := httptest.NewRecorder()
		h.HandleQueryIntroduction(introRec, introReq)
		assert.Equal(t, http.StatusMethodNotAllowed, introRec.Code)
	})

	t.Run("HandleBatchSummaries - Method not allowed", func(t *testing.T) {
		summariesReq := httptest.NewRequest(http.MethodGet, "/summaries", strings.NewReader(`{"papers":[{"title":"a"}]}`))
		summariesRec := httptest.NewRecorder()
		h.HandleBatchSummaries(summariesRec, summariesReq)
		assert.Equal(t, http.StatusMethodNotAllowed, summariesRec.Code)
	})
}

func TestSearchHelpers_MoreEdgeCases(t *testing.T) {
	t.Run("resolveSearchOptions - GET missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/parallel", nil)
		_, _, err := resolveSearchOptions(req)
		assert.EqualError(t, err, "query parameter 'q' is required")
	})

	t.Run("resolveSearchOptions - parseRequestedProviders ignores blanks", func(t *testing.T) {
		got, err := parseRequestedProviders([]string{"openalex,   , semantic scholar", " "})
		assert.NoError(t, err)
		assert.Equal(t, []string{"openalex", "semantic_scholar"}, got)
	})

	t.Run("resolveHybridSearchRequest - POST missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/hybrid", strings.NewReader(`{"limit":1}`))
		_, err := resolveHybridSearchRequest(req)
		assert.EqualError(t, err, "query field is required")
	})

	t.Run("resolveHybridSearchRequest - POST bad sources", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/hybrid", strings.NewReader(`{"query":"valid", "sources": ["openalex", "invalid-source"]}`))
		_, err := resolveHybridSearchRequest(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported provider hint")
	})

	t.Run("extractProviderWarnings - skips warning with missing provider and message", func(t *testing.T) {
		warnings := extractProviderWarnings([]map[string]any{
			{"status": "warning", "provider": "", "message": ""},
			{"status": "warning", "provider": "openalex", "message": "timeout"},
		})
		assert.Len(t, warnings, 1)
		assert.Equal(t, "openalex", warnings[0].Provider)
	})

	t.Run("mapOpenSearchPaper - fallback author string from whitespace primary", func(t *testing.T) {
		paper := mapOpenSearchPaper(map[string]any{
			"authors":      []string{"  ", ""},
			"authorString": "Alice ; Bob",
		})
		assert.Equal(t, []string{"Alice", "Bob"}, paper.Authors)
	})
}

func TestSearchMappingEdgePaths(t *testing.T) {
	t.Run("summarizeParallelAuthorCoverage - complete path", func(t *testing.T) {
		coverage := summarizeParallelAuthorCoverage([]search.Paper{{
			Title:   "Complete",
			Authors: []string{"A"},
		}})
		assert.Equal(t, "complete", coverage.resultLabel())
		assert.Equal(t, 1, coverage.PapersWithAuthors)
		assert.Zero(t, coverage.PapersWithoutAuthors)
	})

	t.Run("summarizeParallelAuthorCoverage - sample cap and unknown label", func(t *testing.T) {
		papers := make([]search.Paper, 0, 6)
		for i := 0; i < 6; i++ {
			papers = append(papers, search.Paper{})
		}
		coverage := summarizeParallelAuthorCoverage(papers)
		assert.True(t, coverage.AuthorsMissingAll)
		assert.Equal(t, 6, coverage.PapersWithoutAuthors)
		assert.Len(t, coverage.MissingAuthorSamples, 5)
		assert.Len(t, coverage.MissingAuthorProviders, 0)
	})

	t.Run("recordClick - normalized provider hint", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/search/click", strings.NewReader(`{"query":"q","paperId":"p","provider":"semantic scholar"}`))
		rec := httptest.NewRecorder()
		h := NewSearchHandler(search.NewProviderRegistry(), search.NewProviderRegistry(), nil)
		h.HandleRecordClick(rec, req)

		assert.Equal(t, http.StatusAccepted, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		assert.True(t, payload["ok"].(bool))
	})
}

func TestSearchHybridSearch_FallbackBranches(t *testing.T) {
	t.Run("HandleHybridSearch - OpenSearch fallback uses parallel search", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		h := NewSearchHandler(reg, reg, nil)

		originalParallel := wisdev.ParallelSearch
		t.Cleanup(func() {
			wisdev.ParallelSearch = originalParallel
		})

		wisdev.ParallelSearch = func(_ context.Context, _ redis.UniversalClient, _ string, _ wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
			return &wisdev.MultiSourceResult{
				Papers: []wisdev.Source{
					{
						ID:     "fallback-1",
						Title:  "Fallback fallback",
						Source: "openalex",
					},
				},
				QueryUsed: "hybrid query",
				Sources:   wisdev.SourcesStats{OpenAlex: 1},
				Timing:    wisdev.TimingStats{Total: 10},
			}, nil
		}

		h.WithOpenSearchExecutor(func(_ context.Context, _ OpenSearchRequest) (OpenSearchResponse, error) {
			return OpenSearchResponse{FallbackTriggered: true, FallbackReason: "OS unavailable", BackendUsed: "opensearch"}, nil
		})

		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=hybrid+query&retrievalBackend=opensearch", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var payload gatewayHybridResponse
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		assert.True(t, payload.FallbackTriggered)
		assert.Equal(t, "opensearch_hybrid", payload.BackendUsed)
		assert.Equal(t, "OS unavailable", payload.FallbackReason)
		assert.Len(t, payload.Papers, 1)
	})

	t.Run("HandleHybridSearch - OpenSearch fallback error from parallel search", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		h := NewSearchHandler(reg, reg, nil)

		originalParallel := wisdev.ParallelSearch
		t.Cleanup(func() {
			wisdev.ParallelSearch = originalParallel
		})

		wisdev.ParallelSearch = func(_ context.Context, _ redis.UniversalClient, _ string, _ wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
			return nil, assert.AnError
		}

		h.WithOpenSearchExecutor(func(_ context.Context, _ OpenSearchRequest) (OpenSearchResponse, error) {
			return OpenSearchResponse{FallbackTriggered: true, FallbackReason: "OS unavailable"}, nil
		})

		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=hybrid+query&retrievalBackend=opensearch", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errPayload := payload["error"].(map[string]any)
		assert.Equal(t, "SEARCH_ERROR", errPayload["code"])
	})

	t.Run("HandleHybridSearch - Default backend error", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		h := NewSearchHandler(reg, reg, nil)

		originalParallel := wisdev.ParallelSearch
		t.Cleanup(func() {
			wisdev.ParallelSearch = originalParallel
		})

		wisdev.ParallelSearch = func(_ context.Context, _ redis.UniversalClient, _ string, _ wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
			return nil, assert.AnError
		}

		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=hybrid+query", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errPayload := payload["error"].(map[string]any)
		assert.Equal(t, "SEARCH_ERROR", errPayload["code"])
	})
}
