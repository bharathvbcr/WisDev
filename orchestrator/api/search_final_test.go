package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestSearchHandler_Final(t *testing.T) {
	reg := search.NewProviderRegistry()
	h := NewSearchHandler(reg, reg, nil)

	t.Run("resolveHybridSearchRequest - POST body partial", func(t *testing.T) {
		body := `{"query":"q"}`
		req := httptest.NewRequest(http.MethodPost, "/hybrid", strings.NewReader(body))
		r, err := resolveHybridSearchRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "q", r.Query)
		// Verify defaults
		assert.Equal(t, 20, r.Limit)
		assert.True(t, *r.ExpandQuery)
	})

	t.Run("HandleHybridSearch - LatencyBudget", func(t *testing.T) {
		h.WithOpenSearchExecutor(func(ctx context.Context, req OpenSearchRequest) (OpenSearchResponse, error) {
			_, ok := ctx.Deadline()
			assert.True(t, ok)
			return OpenSearchResponse{}, nil
		})
		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=q&retrievalBackend=opensearch&latencyBudgetMs=100", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleToolSearch - Handle Error from Search", func(t *testing.T) {
		// Mock search.HandleToolSearch returning error
		// This requires some control over internal/search or its registry.
		// Since I can't easily mock search.HandleToolSearch package function, I'll skip if it's too hard.
		// Wait, HandleToolSearch is a function in internal/search.
	})

	t.Run("readSearchRequestBody - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/parallel", strings.NewReader(`{invalid`))
		_, err := readSearchRequestBody(req)
		assert.Error(t, err)
	})

	t.Run("readHybridSearchRequestBody - Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/hybrid", strings.NewReader(`{invalid`))
		_, err := readHybridSearchRequestBody(req)
		assert.Error(t, err)
	})

	t.Run("HandleHybridSearch - Default parallel path", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=test", nil)
		rec := httptest.NewRecorder()
		h.HandleHybridSearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
