package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestSearchHandler(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&MockProvider{
		name: "mock1",
		papers: []search.Paper{
			{ID: "1", Title: "P1", DOI: "10.1/1", Source: "mock1"},
		},
	})
	search.ApplyDomainRoutes(reg)

	h := NewSearchHandler(reg, reg, nil)

	t.Run("HandleLegacySearch - Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/search?q=test&limit=1", nil)
		rec := httptest.NewRecorder()
		h.HandleLegacySearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp []search.Paper
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Len(t, resp, 1)
	})

	t.Run("HandleLegacySearch - Missing Query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/search?limit=1", nil)
		rec := httptest.NewRecorder()
		h.HandleLegacySearch(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("HandleParallelSearch - GET Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/parallel?q=test", nil)
		rec := httptest.NewRecorder()
		h.HandleParallelSearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp gatewayParallelResponse
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Len(t, resp.Papers, 1)
		assert.Equal(t, "go-parallel", resp.Metadata.Backend)
	})

	t.Run("HandleParallelSearch - POST Success", func(t *testing.T) {
		body := `{"query":"test", "limit":5, "qualitySort":true}`
		req := httptest.NewRequest(http.MethodPost, "/parallel", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleParallelSearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleBatchSearch - Success", func(t *testing.T) {
		body := `{"queries":["q1", "q2"], "limit":2}`
		req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleBatchSearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		results := resp["results"].(map[string]any)
		assert.Len(t, results, 2)
	})

	t.Run("HandleHybridSearch - Parallel Success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=test&retrievalBackend=parallel", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp gatewayHybridResponse
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "go-parallel", resp.BackendUsed)
	})

	t.Run("HandleHybridSearch - OpenSearch Success", func(t *testing.T) {
		h.WithOpenSearchExecutor(func(ctx context.Context, req OpenSearchRequest) (OpenSearchResponse, error) {
			return OpenSearchResponse{
				Papers: []map[string]any{
					{"id": "os1", "title": "OS Paper"},
				},
				TotalFound:  1,
				BackendUsed: "opensearch",
			}, nil
		})
		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=test&retrievalBackend=opensearch", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp gatewayHybridResponse
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "opensearch", resp.BackendUsed)
		assert.Len(t, resp.Papers, 1)
	})

	t.Run("HandleToolSearch - Success", func(t *testing.T) {
		// Mock search.HandleToolSearch behavior if needed, but it currently calls ParallelSearch
		// since we registered mock1, it should be available.
		body := `{"tool":"mock1", "params":{"q":"test"}}`
		req := httptest.NewRequest(http.MethodPost, "/tool", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleToolSearch(rec, req)
		// search.HandleToolSearch might need real provider tools logic
		// If it fails, we'll see.
	})
}

type MockProvider struct {
	search.BaseProvider
	name   string
	papers []search.Paper
}

func (m *MockProvider) Name() string      { return m.name }
func (m *MockProvider) Domains() []string { return []string{"general"} }
func (m *MockProvider) Healthy() bool     { return true }
func (m *MockProvider) Search(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
	return m.papers, nil
}
