package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"

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
		t.Skip("requires OpenSearch infrastructure or wisdev.OpenSearchHybridSearch mock")
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

	t.Run("MapOpenSearchPaper - Extra Fields", func(t *testing.T) {
		raw := map[string]any{
			"id":            "1",
			"title":         "T1",
			"score":         10,  // int score
			"citationCount": 5.0, // float64 count
		}
		p := mapOpenSearchPaper(raw)
		assert.Equal(t, 10.0, p.Score)
		assert.Equal(t, 5, p.CitationCount)

		raw2 := map[string]any{
			"relevanceScore": 0.5,
		}
		p2 := mapOpenSearchPaper(raw2)
		assert.Equal(t, 0.5, p2.Score)
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
}
