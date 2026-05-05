package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

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

	t.Run("resolveSearchOptions - preserves gateway shaping fields", func(t *testing.T) {
		body := `{"query":"graph rag","sources":["openalex","arxiv"],"yearFrom":2025,"yearTo":2021,"expandQuery":false,"pageIndex":true,"l3":true}`
		req := httptest.NewRequest(http.MethodPost, "/parallel?qualitySort=false&skipCache=true&domain=cs", strings.NewReader(body))
		req.Header.Set("X-Trace-Id", "trace-abc")
		q, opts, err := resolveSearchOptions(req)
		assert.NoError(t, err)
		assert.Equal(t, "graph rag", q)
		assert.Equal(t, []string{"arxiv", "openalex"}, opts.Sources)
		assert.Equal(t, 2021, opts.YearFrom)
		assert.Equal(t, 2025, opts.YearTo)
		assert.False(t, opts.ExpandQuery)
		assert.False(t, opts.QualitySort)
		assert.True(t, opts.SkipCache)
		assert.True(t, opts.PageIndexRerank)
		assert.True(t, opts.Stage2Rerank)
		assert.Equal(t, "cs", opts.Domain)
		assert.Equal(t, "trace-abc", opts.TraceID)
	})

	t.Run("resolveSearchOptions - accepts trace id from query params when header is absent", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/parallel?q=traceable+query&traceId=trace-query-param", nil)
		q, opts, err := resolveSearchOptions(req)
		assert.NoError(t, err)
		assert.Equal(t, "traceable query", q)
		assert.Equal(t, "trace-query-param", opts.TraceID)
	})
}
