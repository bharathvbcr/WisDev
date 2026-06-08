package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

func TestSearchHelpers_PriorityEdges(t *testing.T) {
	t.Run("parseRequestedProviders - normalizes aliases, dedupes, sorts", func(t *testing.T) {
		got, err := parseRequestedProviders(
			[]string{"openalex, arXiv", "  semanticscholar ", "openalex"},
			[]string{"ieee", "googlescholar"},
		)

		assert.NoError(t, err)
		assert.Equal(t, []string{"arxiv", "google_scholar", "ieee", "openalex", "semantic_scholar"}, got)
	})

	t.Run("parseRequestedProviders - rejects unsupported provider", func(t *testing.T) {
		_, err := parseRequestedProviders([]string{"openalex, invalid-provider"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported provider hint")
	})

	t.Run("parseRequestedProviders - empty inputs", func(t *testing.T) {
		got, err := parseRequestedProviders(nil, []string{})
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestResolveHybridSearchRequest_PriorityEdges(t *testing.T) {
	t.Run("resolveHybridSearchRequest - method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/hybrid?q=test", nil)
		_, err := resolveHybridSearchRequest(req)
		assert.EqualError(t, err, "method not allowed")
	})

	t.Run("resolveHybridSearchRequest - GET requires query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/hybrid", nil)
		_, err := resolveHybridSearchRequest(req)
		assert.EqualError(t, err, "query parameter 'q' is required")
	})

	t.Run("resolveHybridSearchRequest - GET supports provider list parsing with aliases", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=deep+learning&sources=ARXIV%2Copenalex&source=core", nil)
		got, err := resolveHybridSearchRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "deep learning", got.Query)
		assert.ElementsMatch(t, []string{"arxiv", "core", "openalex"}, got.Sources)
		assert.Equal(t, 20, got.Limit)
	})

	t.Run("resolveHybridSearchRequest - GET unsupported provider in query string", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/hybrid?q=test&source=invalid", nil)
		_, err := resolveHybridSearchRequest(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported provider hint")
	})

	t.Run("resolveHybridSearchRequest - POST falls back query to q when body misses it", func(t *testing.T) {
		body := `{"limit":10,"sources":["pubmed"]}`
		req := httptest.NewRequest(http.MethodPost, "/hybrid?query=from-query&q=from-q", strings.NewReader(body))
		got, err := resolveHybridSearchRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "from-query", got.Query)
		assert.Equal(t, 10, got.Limit)
		assert.Equal(t, []string{"pubmed"}, got.Sources)
		assert.NotNil(t, got.PageIndex)
		assert.NotNil(t, got.L3)
		assert.False(t, *got.PageIndex)
		assert.False(t, *got.L3)
	})

	t.Run("resolveHybridSearchRequest - POST invalid json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/hybrid", strings.NewReader(`{`))
		_, err := resolveHybridSearchRequest(req)
		assert.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "failed to parse request body"))
	})

	t.Run("resolveHybridSearchRequest - POST body source and query overrides query params", func(t *testing.T) {
		body := `{
			"query":"body query",
			"sources":["openalex","openalex"],
			"limit":25,
			"yearFrom":2026,
			"yearTo":2020,
			"expandQuery":false,
			"qualitySort":true,
			"skipCache":true,
			"pageIndex":true,
			"l3":true,
			"retrievalBackend":"opensearch",
			"retrievalMode":"dense",
			"fusionMode":"rerank",
			"latencyBudgetMs":500
		}`
		req := httptest.NewRequest(http.MethodPost, "/hybrid?query=ignored&sources=ieee&limit=7&yearFrom=1990&yearTo=1991", strings.NewReader(body))
		got, err := resolveHybridSearchRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "body query", got.Query)
		assert.Equal(t, 25, got.Limit)
		assert.Equal(t, false, *got.ExpandQuery)
		assert.True(t, *got.QualitySort)
		assert.True(t, *got.SkipCache)
		assert.Equal(t, []string{"openalex"}, got.Sources)
		assert.Equal(t, 2020, got.YearFrom)
		assert.Equal(t, 2026, got.YearTo)
		assert.True(t, *got.PageIndex)
		assert.True(t, *got.L3)
		assert.Equal(t, "opensearch", got.RetrievalBackend)
		assert.Equal(t, "dense", got.RetrievalMode)
		assert.Equal(t, "rerank", got.FusionMode)
		assert.Equal(t, 500, got.LatencyBudgetMs)
		assert.NotEqual(t, req.URL.Query().Get("sources"), strings.Join(got.Sources, ","))
	})

	t.Run("resolveHybridSearchRequest - POST uses query param source when body source is absent", func(t *testing.T) {
		body := `{"query":"query","yearFrom":1999}`
		req := httptest.NewRequest(http.MethodPost, "/hybrid?source=openalex&sources=ieee%2Ccrossref&yearTo=2020", strings.NewReader(body))
		got, err := resolveHybridSearchRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, []string{"crossref", "ieee", "openalex"}, got.Sources)
		assert.Equal(t, 2020, got.YearTo)
		assert.Equal(t, 1999, got.YearFrom)
	})
}

func TestSearchHelpers_MapHybridOpenSearchResponse(t *testing.T) {
	t.Run("mapHybridOpenSearchResponse - defaults missing backend to opensearch_hybrid", func(t *testing.T) {
		resp := mapHybridOpenSearchResponse(" query ", " medicine ", OpenSearchResponse{
			Papers: []map[string]any{
				{
					"id":          "p1",
					"title":       "Query paper",
					"url":         "https://example.com",
					"authors":     []string{"A", "B"},
					"score":       3.5,
					"year":        "2024",
					"journal":     "JMLR",
					"venue":       "venue fallback",
					"publication": "pub fallback",
				},
			},
			TotalFound:        7,
			LatencyMs:         111,
			FallbackTriggered: true,
			FallbackReason:    "provider_timeout",
			BackendUsed:       "",
		}, 333)
		assert.Equal(t, "", resp.BackendUsed)
		assert.Equal(t, "", resp.Metadata.Backend)
		assert.Equal(t, "medicine", resp.Metadata.RequestedDomain)
		assert.Equal(t, []string{"opensearch_hybrid"}, resp.Metadata.SelectedProviders)
		assert.Equal(t, "opensearch_hybrid", resp.EnhancedQuery.Intent)
		assert.True(t, resp.FallbackTriggered)
		assert.Equal(t, "provider_timeout", resp.FallbackReason)
		assert.Equal(t, "query", resp.QueryUsed)
		assert.Len(t, resp.Papers, 1)
		assert.Equal(t, int64(333), resp.LatencyMs)
		assert.Equal(t, int64(111), resp.Timing.Search)
		assert.Equal(t, int64(111), resp.Timing.Total)
		assert.Equal(t, 7, resp.TotalFound)
		assert.Len(t, resp.Warnings, 0)
	})

	t.Run("mapHybridOpenSearchResponse - preserves provided backend", func(t *testing.T) {
		resp := mapHybridOpenSearchResponse("query", "", OpenSearchResponse{
			Papers:            []map[string]any{},
			TotalFound:        0,
			LatencyMs:         0,
			FallbackTriggered: false,
			FallbackReason:    "",
			BackendUsed:       "opensearch_v2",
		}, 50)
		assert.Equal(t, "opensearch_v2", resp.BackendUsed)
		assert.Equal(t, "opensearch_v2", resp.Metadata.Backend)
		assert.Equal(t, int64(0), resp.Timing.Total)
		assert.False(t, resp.FallbackTriggered)
		assert.Equal(t, "", resp.FallbackReason)
	})
}

func TestResolveSearchOptions_PriorityEdges(t *testing.T) {
	t.Run("resolveSearchOptions - unsupported provider in body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/parallel?q=test&source=not-a-provider", nil)
		_, _, err := resolveSearchOptions(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported provider hint")
	})

	t.Run("resolveSearchOptions - unsupported provider in body list", func(t *testing.T) {
		body := `{
			"query":"openai benchmark",
			"sources":["openalex", "invalid-provider"]
		}`
		req := httptest.NewRequest(http.MethodPost, "/parallel", strings.NewReader(body))
		_, _, err := resolveSearchOptions(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported provider hint")
	})

	t.Run("resolveSearchOptions - method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/parallel?q=test", nil)
		_, _, err := resolveSearchOptions(req)
		assert.Error(t, err)
		assert.Equal(t, "method not allowed", err.Error())
	})

	t.Run("resolveSearchOptions - trace id from header has precedence", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/parallel?q=graph+rag&traceId=from-query", nil)
		req.Header.Set("X-Trace-Id", "from-header")
		_, opts, err := resolveSearchOptions(req)

		assert.NoError(t, err)
		assert.Equal(t, "from-header", opts.TraceID)
	})

	t.Run("resolveSearchOptions - trace id from legacy query key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/parallel?q=graph+rag&trace_id=legacy-trace", nil)
		_, opts, err := resolveSearchOptions(req)

		assert.NoError(t, err)
		assert.Equal(t, "legacy-trace", opts.TraceID)
	})

	t.Run("resolveSearchOptions - POST body with year normalization and provider merge", func(t *testing.T) {
		body := `{
			"query":"  deep learning  ",
			"yearFrom":2025,
			"yearTo":2021,
			"sources":["openalex", "ARXIV", "openalex"]
		}`
		req := httptest.NewRequest(http.MethodPost, "/parallel?yearFrom=2000&yearTo=1999&source=invalid", strings.NewReader(body))
		q, opts, err := resolveSearchOptions(req)

		assert.NoError(t, err)
		assert.Equal(t, "deep learning", q)
		assert.Equal(t, 15, opts.Limit)
		assert.Equal(t, 2021, opts.YearFrom)
		assert.Equal(t, 2025, opts.YearTo)
		assert.Equal(t, []string{"arxiv", "openalex"}, opts.Sources)
	})

	t.Run("resolveSearchOptions - POST body controls flags and ignores query overrides", func(t *testing.T) {
		body := `{
			"query":"  deep learning  ",
			"limit":5,
			"qualitySort": false,
			"skipCache": true,
			"expandQuery": false,
			"sources":["ieee"],
			"pageIndex": true,
			"l3": true,
			"yearFrom":2021,
			"yearTo":2020,
			"domain":" medicine "
		}`
		req := httptest.NewRequest(
			http.MethodPost,
			"/parallel?limit=20&qualitySort=true&skipCache=false&expandQuery=true&pageIndex=false&l3=false&yearFrom=1999&yearTo=1998&source=openalex&domain=cs",
			strings.NewReader(body),
		)
		q, opts, err := resolveSearchOptions(req)

		assert.NoError(t, err)
		assert.Equal(t, "deep learning", q)
		assert.Equal(t, 5, opts.Limit)
		assert.False(t, opts.QualitySort)
		assert.True(t, opts.SkipCache)
		assert.False(t, opts.ExpandQuery)
		assert.Equal(t, "medicine", opts.Domain)
		assert.Equal(t, 2020, opts.YearFrom)
		assert.Equal(t, 2021, opts.YearTo)
		assert.Equal(t, []string{"ieee"}, opts.Sources)
		assert.True(t, opts.PageIndexRerank)
		assert.True(t, opts.Stage2Rerank)
	})

	t.Run("resolveSearchOptions - post request invalid json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/parallel", strings.NewReader(`{invalid`))
		_, _, err := resolveSearchOptions(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse request body")
	})
}

func TestMapParallelResponse_PriorityCoverage(t *testing.T) {
	result := &wisdev.MultiSourceResult{
		Papers: []wisdev.Source{
			{
				ID:     "p-1",
				Title:  "Paper with warnings",
				Source: "openalex",
				DOI:    "10.1/1",
			},
		},
		EnhancedQuery: wisdev.EnhancedQuery{
			Original: "query",
			Expanded: "expanded query",
			Intent:   "papers",
			Keywords: []string{"query"},
			Synonyms: []string{"term"},
		},
		Sources: wisdev.SourcesStats{
			OpenAlex: 1,
		},
		Timing: wisdev.TimingStats{
			Total:     111,
			Expansion: 11,
			Search:    100,
		},
		QueryUsed: "expanded query",
		TraceID:   "  trace-id  ",
		RetrievalTrace: []map[string]any{
			{"provider": "openalex", "status": "warning", "message": "upstream timeout"},
			{"strategy": "query_expansion", "status": "fallback_to_original_query_succeeded"},
		},
	}

	resp := mapParallelResponse("query", "  cs  ", result)
	assert.Equal(t, "go-parallel", resp.Metadata.Backend)
	assert.Equal(t, "expanded query", resp.QueryUsed)
	assert.Equal(t, "trace-id", resp.TraceID)
	assert.True(t, resp.Metadata.FallbackTriggered)
	assert.Equal(t, "query_expansion:fallback_to_original_query_succeeded", resp.Metadata.FallbackReason)
	assert.Len(t, resp.Papers, 1)
	assert.Equal(t, []string{"openalex"}, resp.ProvidersUsed)
	assert.Equal(t, 1, resp.Metadata.WarningCount)
	assert.Len(t, resp.Warnings, 1)
	assert.Equal(t, "upstream timeout", resp.Warnings[0].Message)
	assert.Equal(t, int64(111), resp.Timing.Total)
	assert.True(t, resp.Cached == result.Cached)
}

func TestSearchQueryFingerprint_Deterministic(t *testing.T) {
	var payload struct {
		left  string
		right string
	}
	payload.left = searchQueryFingerprint("  query  ")
	payload.right = searchQueryFingerprint("query")

	assert.Equal(t, payload.left, payload.right)
}

func TestWriteSearchError_Contract(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSearchError(rec, 400, "TEST_CODE", "test message")

	assert.Equal(t, 400, rec.Code)

	var payload map[string]any
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errObj := payload["error"].(map[string]any)
	assert.Equal(t, "TEST_CODE", errObj["code"])
	assert.Equal(t, "test message", errObj["message"])
	assert.Equal(t, 400.0, errObj["status"])
}
