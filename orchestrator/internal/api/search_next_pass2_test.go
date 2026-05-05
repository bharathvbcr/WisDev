package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func TestSearchParallelHandler_EdgePaths(t *testing.T) {
	reg := search.NewProviderRegistry()
	h := NewSearchHandler(reg, reg, nil)

	t.Run("HandleParallelSearch - Search Error", func(t *testing.T) {
		originalSearch := wisdev.ParallelSearch
		t.Cleanup(func() {
			wisdev.ParallelSearch = originalSearch
		})

		wisdev.ParallelSearch = func(_ context.Context, _ redis.UniversalClient, query string, _ wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
			assert.Equal(t, "search fail", strings.TrimSpace(query))
			return nil, errors.New("search backend failed")
		}

		req := httptest.NewRequest(http.MethodGet, "/parallel?q=search+fail", nil)
		rec := httptest.NewRecorder()
		h.HandleParallelSearch(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errObj, ok := payload["error"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, "SEARCH_ERROR", errObj["code"])
		assert.Contains(t, errObj["message"].(string), "search backend failed")
	})

	t.Run("HandleParallelSearch - Zero Results", func(t *testing.T) {
		originalSearch := wisdev.ParallelSearch
		t.Cleanup(func() {
			wisdev.ParallelSearch = originalSearch
		})

		wisdev.ParallelSearch = func(_ context.Context, _ redis.UniversalClient, _ string, _ wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
			return &wisdev.MultiSourceResult{
				Papers: []wisdev.Source{},
				EnhancedQuery: wisdev.EnhancedQuery{
					Original: "zero",
					Intent:   "papers",
				},
				Sources: wisdev.SourcesStats{},
				Timing: wisdev.TimingStats{
					Total:     10,
					Search:    8,
					Expansion: 2,
				},
				QueryUsed: "zero",
				RetrievalTrace: []map[string]any{
					{"provider": "openalex", "status": "warning", "message": "no matches"},
				},
				TraceID: "trace-zero",
			}, nil
		}

		req := httptest.NewRequest(http.MethodGet, "/parallel?q=zero+results", nil)
		req.Header.Set("X-Trace-Id", "trace-override")
		rec := httptest.NewRecorder()

		h.HandleParallelSearch(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var payload gatewayParallelResponse
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		assert.Equal(t, "trace-zero", payload.TraceID)
		assert.Len(t, payload.Papers, 0)
		assert.Len(t, payload.Warnings, 1)
		assert.Equal(t, 1, payload.Metadata.WarningCount)
	})

	t.Run("HandleParallelSearch - No Authors Triggers Coverage Warning", func(t *testing.T) {
		originalSearch := wisdev.ParallelSearch
		t.Cleanup(func() {
			wisdev.ParallelSearch = originalSearch
		})

		wisdev.ParallelSearch = func(_ context.Context, _ redis.UniversalClient, _ string, _ wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
			return &wisdev.MultiSourceResult{
				Papers: []wisdev.Source{
					{ID: "no-authors", Title: "No Author Paper", Source: "openalex"},
				},
				EnhancedQuery: wisdev.EnhancedQuery{
					Original: "authors",
					Intent:   "papers",
				},
				Sources: wisdev.SourcesStats{
					OpenAlex: 1,
				},
				Timing: wisdev.TimingStats{
					Total:  6,
					Search: 6,
				},
				QueryUsed: "authors",
				TraceID:   "trace-authors",
			}, nil
		}

		req := httptest.NewRequest(http.MethodGet, "/parallel?q=authors", nil)
		rec := httptest.NewRecorder()

		h.HandleParallelSearch(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var payload gatewayParallelResponse
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		assert.Equal(t, "trace-authors", payload.TraceID)
		assert.Len(t, payload.Papers, 1)
		assert.True(t, len(payload.Papers[0].Authors) == 0)
	})
}

func TestSearchToolsAndHelpers_EdgePaths(t *testing.T) {
	t.Run("HandleSearchTools - Method Not Allowed", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		h := NewSearchHandler(reg, reg, nil)

		req := httptest.NewRequest(http.MethodPost, "/search/tools", nil)
		rec := httptest.NewRecorder()
		h.HandleSearchTools(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errPayload := payload["error"].(map[string]any)
		assert.Equal(t, "METHOD_NOT_ALLOWED", errPayload["code"])
	})

	t.Run("HandleToolSearch - Unsupported Tool", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		h := NewSearchHandler(reg, reg, nil)

		req := httptest.NewRequest(http.MethodPost, "/tool", strings.NewReader(`{"tool":"does_not_exist","params":{}}`))
		rec := httptest.NewRecorder()
		h.HandleToolSearch(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errPayload := payload["error"].(map[string]any)
		assert.Equal(t, "TOOL_SEARCH_FAILED", errPayload["code"])
		assert.Contains(t, errPayload["message"].(string), "unsupported tool")
	})

	t.Run("resolveSearchClickUserID - Internal Caller Resolution", func(t *testing.T) {
		assert.Equal(t, "requested-user", resolveSearchClickUserID(httptest.NewRequest(http.MethodPost, "/search/click", nil).WithContext(
			context.WithValue(context.Background(), contextKey("user_id"), "admin"),
		), "requested-user"))
		assert.Equal(t, "internal-service", resolveSearchClickUserID(httptest.NewRequest(http.MethodPost, "/search/click", nil).WithContext(
			context.WithValue(context.Background(), contextKey("user_id"), "internal-service"),
		), ""))
		assert.Equal(t, "", resolveSearchClickUserID(httptest.NewRequest(http.MethodPost, "/search/click", nil), "requested-user"))
	})

	t.Run("HandleRelatedArticles - Search Error", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		reg.SetDB(nil)
		h := NewSearchHandler(reg, reg, nil)

		originalFast := wisdev.FastParallelSearch
		t.Cleanup(func() {
			wisdev.FastParallelSearch = originalFast
		})

		wisdev.FastParallelSearch = func(context.Context, redis.UniversalClient, string, int) ([]wisdev.Source, error) {
			return nil, errors.New("related lookup failed")
		}

		req := httptest.NewRequest(http.MethodPost, "/related", strings.NewReader(`{"query":"q"}`))
		rec := httptest.NewRecorder()
		h.HandleRelatedArticles(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errPayload := payload["error"].(map[string]any)
		assert.Equal(t, "WISDEV_FAILED", errPayload["code"])
	})

	t.Run("readSearchRequestBody - Empty Body and Nil Body", func(t *testing.T) {
		reqEmpty, _ := http.NewRequest(http.MethodPost, "/parallel", strings.NewReader(""))
		_, err := readSearchRequestBody(reqEmpty)
		assert.NoError(t, err)

		reqNil := httptest.NewRequest(http.MethodPost, "/parallel", strings.NewReader(""))
		reqNil.Body = nil
		_, err = readSearchRequestBody(reqNil)
		assert.NoError(t, err)
	})

	t.Run("readHybridSearchRequestBody - Empty Body and Nil Body", func(t *testing.T) {
		reqEmpty, _ := http.NewRequest(http.MethodPost, "/hybrid", strings.NewReader(""))
		_, err := readHybridSearchRequestBody(reqEmpty)
		assert.NoError(t, err)

		reqNil := httptest.NewRequest(http.MethodPost, "/hybrid", strings.NewReader(""))
		reqNil.Body = nil
		_, err = readHybridSearchRequestBody(reqNil)
		assert.NoError(t, err)
	})
}

func TestSearchAuxHelpers_Coverage(t *testing.T) {
	t.Run("mapWisdevProviders - Includes CORE When Positive", func(t *testing.T) {
		got := mapWisdevProviders(wisdev.SourcesStats{
			CORE: 9,
		})
		assert.Equal(t, map[string]int{
			"core": 9,
		}, got)
	})

	t.Run("mapOpenSearchPaper - Integer score and int relevance fallback", func(t *testing.T) {
		paper := mapOpenSearchPaper(map[string]any{
			"citationCount":  3,
			"score":          0,
			"relevanceScore": 9,
			"year":           2021.0,
			"journal":        "Journal of Tests",
			"authorString":   "Ada ; Turing",
		})
		assert.Equal(t, 3, paper.CitationCount)
		assert.Equal(t, 9.0, paper.Score)
		assert.Equal(t, 2021, paper.Year)
		assert.Equal(t, "Journal of Tests", paper.Venue)
		assert.Equal(t, []string{"Ada", "Turing"}, paper.Authors)
	})
}
