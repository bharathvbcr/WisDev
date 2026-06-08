package wisdev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOpenSearchHybridSearch_NoConfig(t *testing.T) {
	os.Unsetenv("OPENSEARCH_URL")
	resp, err := OpenSearchHybridSearch(context.Background(), OpenSearchHybridRequest{Query: "test"})
	assert.NoError(t, err)
	assert.True(t, resp.FallbackTriggered)
	assert.Contains(t, resp.FallbackReason, "not configured")
}

func TestOpenSearchHybridSearch_HealthCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "_count")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"count": 100}`))
	}))
	defer server.Close()

	os.Setenv("OPENSEARCH_URL", server.URL)
	defer os.Unsetenv("OPENSEARCH_URL")

	resp, err := OpenSearchHybridSearch(context.Background(), OpenSearchHybridRequest{Query: "health-check"})
	assert.NoError(t, err)
	assert.False(t, resp.FallbackTriggered)
	assert.Equal(t, 1.0, resp.QualitySignals["health"])
}

func TestOpenSearchHybridSearch_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "_search")
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		query, ok := body["query"].(map[string]any)
		assert.True(t, ok)
		boolQuery, ok := query["bool"].(map[string]any)
		assert.True(t, ok)
		filters, ok := boolQuery["filter"].([]any)
		assert.True(t, ok)
		assert.Len(t, filters, 3)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"hits": {
				"total": {"value": 1},
				"hits": [
					{
						"_id": "1",
						"_score": 1.5,
						"_source": {"title": "Test Paper"}
					}
				]
			}
		}`))
	}))
	defer server.Close()

	os.Setenv("OPENSEARCH_URL", server.URL)
	defer os.Unsetenv("OPENSEARCH_URL")

	resp, err := OpenSearchHybridSearch(context.Background(), OpenSearchHybridRequest{
		Query: "test query",
		Filters: map[string]any{
			"domain": "medicine",
			"year":   map[string]any{"gte": 2020, "lte": 2023},
			"source": []string{"semantic_scholar", "openalex"},
		},
	})
	assert.NoError(t, err)
	assert.Len(t, resp.Papers, 1)
	assert.Equal(t, "Test Paper", resp.Papers[0]["title"])
	assert.Equal(t, 1.5, resp.Papers[0]["_score"])
}

func TestBuildOSFilterClauses(t *testing.T) {
	clauses := buildOSFilterClauses(map[string]any{
		"domain": "biology",
		"year":   map[string]any{"gte": 2021, "lte": 2024},
		"source": []string{"arxiv"},
	})
	assert.Len(t, clauses, 3)
}

func TestOpenSearchHybridSearch_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer server.Close()

	os.Setenv("OPENSEARCH_URL", server.URL)
	defer os.Unsetenv("OPENSEARCH_URL")

	resp, err := OpenSearchHybridSearch(context.Background(), OpenSearchHybridRequest{Query: "test"})
	assert.NoError(t, err)
	assert.True(t, resp.FallbackTriggered)
	assert.Contains(t, resp.FallbackReason, "opensearch_status_500")
}

func TestApplyOSAuth(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	applyOSAuth(req, "user", "pass")
	user, pass, ok := req.BasicAuth()
	assert.True(t, ok)
	assert.Equal(t, "user", user)
	assert.Equal(t, "pass", pass)
}

func TestParseOSResponse_InvalidJSON(t *testing.T) {
	_, err := parseOSResponse(strings.NewReader("invalid"), time.Now(), 10)
	assert.NoError(t, err) // Returns osFallback, no error
}

func TestResolveOpenSearchConfig(t *testing.T) {
	os.Setenv("OPENSEARCH_URL", "http://os:9200/")
	os.Setenv("OPENSEARCH_INDEX", "my-index")
	os.Setenv("OPENSEARCH_USER", "u")
	os.Setenv("OPENSEARCH_PASSWORD", "p")
	defer func() {
		os.Unsetenv("OPENSEARCH_URL")
		os.Unsetenv("OPENSEARCH_INDEX")
		os.Unsetenv("OPENSEARCH_USER")
		os.Unsetenv("OPENSEARCH_PASSWORD")
	}()

	baseURL, index, user, password, ok := resolveOpenSearchConfig()
	assert.True(t, ok)
	assert.Equal(t, "http://os:9200", baseURL)
	assert.Equal(t, "my-index", index)
	assert.Equal(t, "u", user)
	assert.Equal(t, "p", password)
}
