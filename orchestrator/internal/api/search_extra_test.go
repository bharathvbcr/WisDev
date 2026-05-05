package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

func TestSearchHandler_Extra(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&MockProvider{
		name: "mock1",
		papers: []search.Paper{
			{ID: "1", Title: "P1", DOI: "10.1/1", Source: "mock1"},
		},
	})
	search.ApplyDomainRoutes(reg)

	h := NewSearchHandler(reg, reg, nil)

	t.Run("HandleLegacySearch - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/search", nil)
		rec := httptest.NewRecorder()
		h.HandleLegacySearch(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleParallelSearch - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/parallel", nil)
		rec := httptest.NewRecorder()
		h.HandleParallelSearch(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleParallelSearch - POST Missing Query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/parallel", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		h.HandleParallelSearch(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleBatchSearch - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/batch", nil)
		rec := httptest.NewRecorder()
		h.HandleBatchSearch(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleBatchSearch - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		h.HandleBatchSearch(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleBatchSearch - Empty Queries", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(`{"queries":[]}`))
		rec := httptest.NewRecorder()
		h.HandleBatchSearch(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleBatchSearch - Too Many Queries", func(t *testing.T) {
		queries := make([]string, 21)
		for i := range queries {
			queries[i] = "q"
		}
		body, _ := json.Marshal(map[string]any{"queries": queries})
		req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(string(body)))
		rec := httptest.NewRecorder()
		h.HandleBatchSearch(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleHybridSearch - GET Missing Query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/hybrid", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleHybridSearch - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/hybrid", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleHybridSearch - OpenSearch Error", func(t *testing.T) {
		h.WithOpenSearchExecutor(func(ctx context.Context, req OpenSearchRequest) (OpenSearchResponse, error) {
			return OpenSearchResponse{}, errors.New("os fail")
		})
		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=test&retrievalBackend=opensearch", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("HandleHybridSearch - OpenSearch Falls Back To Built-in Go Backend", func(t *testing.T) {
		h.WithOpenSearchExecutor(nil)
		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=test&retrievalBackend=opensearch", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp gatewayHybridResponse
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "opensearch_hybrid", resp.BackendUsed)
		assert.NotEmpty(t, resp.Papers)
	})

	t.Run("HandleToolSearch - Missing Tool", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tool", strings.NewReader(`{"params":{}}`))
		rec := httptest.NewRecorder()
		h.HandleToolSearch(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleToolSearch - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tool", nil)
		rec := httptest.NewRecorder()
		h.HandleToolSearch(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("HandleToolSearch - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tool", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		h.HandleToolSearch(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleSearchTools - Lists MCP-style retrieval tools", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/search/tools", nil)
		rec := httptest.NewRecorder()
		h.HandleSearchTools(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "wisdevSearchPapers")
		assert.Contains(t, rec.Body.String(), "paper_lookup")
	})

	t.Run("MapOpenSearchPaper - Extra Fields", func(t *testing.T) {
		raw := map[string]any{
			"id":            "1",
			"title":         "T1",
			"score":         10,  // int score
			"citationCount": 5.0, // float64 count
			"year":          "2024",
			"publication":   "Nature",
			"authors": []any{
				"Alice Example",
				map[string]any{"name": "Bob Example"},
			},
		}
		p := mapOpenSearchPaper(raw)
		assert.Equal(t, 10.0, p.Score)
		assert.Equal(t, 5, p.CitationCount)
		assert.Equal(t, 2024, p.Year)
		assert.Equal(t, "Nature", p.Venue)
		assert.Equal(t, []string{"Alice Example", "Bob Example"}, p.Authors)

		raw2 := map[string]any{
			"relevanceScore": 0.5,
			"authorString":   "Carol Example; Dan Example",
			"venue":          "Science",
			"year":           2023.0,
		}
		p2 := mapOpenSearchPaper(raw2)
		assert.Equal(t, 0.5, p2.Score)
		assert.Equal(t, "Science", p2.Venue)
		assert.Equal(t, 2023, p2.Year)
		assert.Equal(t, []string{"Carol Example", "Dan Example"}, p2.Authors)
	})

	t.Run("resolveHybridSearchRequest - POST Success complex", func(t *testing.T) {
		body := `{
			"query":"q",
			"expandQuery": true,
			"qualitySort": false,
			"skipCache": true,
			"domain": "d",
			"retrievalBackend": "b",
			"latencyBudgetMs": 100
		}`
		req := httptest.NewRequest(http.MethodPost, "/hybrid", strings.NewReader(body))
		r, err := resolveHybridSearchRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "q", r.Query)
		assert.True(t, *r.ExpandQuery)
		assert.False(t, *r.QualitySort)
		assert.True(t, *r.SkipCache)
		assert.Equal(t, "d", r.Domain)
		assert.Equal(t, "b", r.RetrievalBackend)
		assert.Equal(t, 100, r.LatencyBudgetMs)
	})

	t.Run("HandleParallelSearch - Propagates query shaping into WisDev search core", func(t *testing.T) {
		originalParallelSearch := wisdev.ParallelSearch
		t.Cleanup(func() {
			wisdev.ParallelSearch = originalParallelSearch
		})

		wisdev.ParallelSearch = func(ctx context.Context, rdb redis.UniversalClient, query string, opts wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
			assert.Equal(t, "test query", query)
			assert.Equal(t, 5, opts.Limit)
			assert.True(t, opts.ExpandQuery)
			assert.False(t, opts.QualitySort)
			assert.True(t, opts.SkipCache)
			assert.Equal(t, []string{"arxiv", "openalex"}, opts.Sources)
			assert.Equal(t, 2020, opts.YearFrom)
			assert.Equal(t, 2024, opts.YearTo)
			assert.True(t, opts.PageIndexRerank)
			assert.True(t, opts.Stage2Rerank)
			assert.Equal(t, "trace-test", opts.TraceID)
			assert.NotNil(t, opts.Registry)
			return &wisdev.MultiSourceResult{
				Papers: []wisdev.Source{
					{ID: "1", Title: "P1", DOI: "10.1/1", Source: "openalex"},
				},
				EnhancedQuery: wisdev.EnhancedQuery{
					Original: "test query",
					Expanded: "test query expanded",
					Intent:   "papers",
					Keywords: []string{"test"},
					Synonyms: []string{"query"},
				},
				Sources: wisdev.SourcesStats{
					OpenAlex: 1,
				},
				Timing: wisdev.TimingStats{
					Total:     33,
					Expansion: 7,
					Search:    26,
				},
				QueryUsed: "test query expanded",
				TraceID:   "trace-test",
				RetrievalTrace: []map[string]any{
					{"provider": "openalex", "status": "warning", "message": "timeout"},
					{"strategy": "query_expansion", "status": "fallback_to_original_query_succeeded"},
				},
			}, nil
		}

		req := httptest.NewRequest(http.MethodPost, "/parallel", strings.NewReader(`{
			"query":"test query",
			"limit":5,
			"expandQuery":true,
			"qualitySort":false,
			"skipCache":true,
			"sources":["openalex","arxiv"],
			"yearFrom":2024,
			"yearTo":2020,
			"pageIndex":true,
			"l3":true
		}`))
		req.Header.Set("X-Trace-Id", "trace-test")
		rec := httptest.NewRecorder()

		h.HandleParallelSearch(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "trace-test", rec.Header().Get("X-Trace-Id"))

		var resp gatewayParallelResponse
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NoError(t, err)
		assert.Equal(t, "test query expanded", resp.EnhancedQuery.Expanded)
		assert.Equal(t, "test query expanded", resp.QueryUsed)
		assert.Equal(t, int64(7), resp.Timing.Expansion)
		assert.Equal(t, "trace-test", resp.TraceID)
		assert.Len(t, resp.Warnings, 1)
		assert.True(t, resp.Metadata.FallbackTriggered)
		assert.Equal(t, "query_expansion:fallback_to_original_query_succeeded", resp.Metadata.FallbackReason)
		assert.Equal(t, []string{"openalex"}, resp.ProvidersUsed)
	})
}
