package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestSearchHandler_Deep(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&MockProvider{
		name: "mock1",
		papers: []search.Paper{
			{ID: "1", Title: "P1", DOI: "10.1/1", Source: "mock1"},
		},
	})
	h := NewSearchHandler(nil, nil, nil) // Test resolution of defaults

	t.Run("resolveRegistry Defaults", func(t *testing.T) {
		r := h.resolveRegistry()
		assert.NotNil(t, r)
		fr := h.resolveFastRegistry()
		assert.NotNil(t, fr)
	})

	t.Run("HandleBatchSearch - Empty query strings", func(t *testing.T) {
		hReal := NewSearchHandler(reg, reg, nil)
		body := `{"queries":["", "  "]}`
		req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(body))
		rec := httptest.NewRecorder()
		hReal.HandleBatchSearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("resolveSearchOptions - POST from Query Params", func(t *testing.T) {
		hReal := NewSearchHandler(reg, reg, nil)
		req := httptest.NewRequest(http.MethodPost, "/parallel?query=test&limit=5&qualitySort=false&domain=cs", nil)
		rec := httptest.NewRecorder()
		hReal.HandleParallelSearch(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("resolveHybridSearchRequest - POST Fallbacks", func(t *testing.T) {
		body := `{"expandQuery":true}`
		req := httptest.NewRequest(http.MethodPost, "/hybrid?query=q", strings.NewReader(body))
		r, err := resolveHybridSearchRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "q", r.Query)
	})

	t.Run("resolveHybridSearchRequest - POST q fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/hybrid?q=q", strings.NewReader(`{}`))
		r, err := resolveHybridSearchRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "q", r.Query)
	})

	t.Run("readHybridSearchRequestBody - EOF", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/hybrid", strings.NewReader(""))
		_, err := readHybridSearchRequestBody(req)
		assert.NoError(t, err)
	})

	t.Run("HandleToolSearch - Error", func(t *testing.T) {
		hReal := NewSearchHandler(reg, reg, nil)
		body := `{"tool":"unknown", "params":{}}`
		req := httptest.NewRequest(http.MethodPost, "/tool", strings.NewReader(body))
		rec := httptest.NewRecorder()
		hReal.HandleToolSearch(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("HandleParallelSearch - POST body overrides query params", func(t *testing.T) {
		body := `{"query":"bodyQuery", "limit":20}`
		req := httptest.NewRequest(http.MethodPost, "/parallel?query=paramQuery&limit=5", strings.NewReader(body))
		q, opts, err := resolveSearchOptions(req)
		assert.NoError(t, err)
		assert.Equal(t, "bodyQuery", q)
		assert.Equal(t, 20, opts.Limit)
	})
}
