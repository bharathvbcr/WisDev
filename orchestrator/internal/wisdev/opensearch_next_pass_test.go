package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type stubOSClient struct {
	response *http.Response
	err      error
}

func (c stubOSClient) Do(_ *http.Request) (*http.Response, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.response, nil
}

func TestBuildOSFilterClauses_ArrayAny(t *testing.T) {
	clauses := buildOSFilterClauses(map[string]any{
		"source": []any{"arxiv", "semantic_scholar"},
	})
	assert.Len(t, clauses, 1)
	assert.NotNil(t, clauses[0]["terms"])
}

func TestBuildOSFilterClauses_Empty(t *testing.T) {
	assert.Nil(t, buildOSFilterClauses(map[string]any{}))
	assert.Nil(t, buildOSFilterClauses(nil))
}

func TestBuildOSFilterClauses_TermFilter(t *testing.T) {
	clauses := buildOSFilterClauses(map[string]any{
		"domain": "biology",
	})
	assert.Len(t, clauses, 1)
	assert.Equal(t, map[string]any{"term": map[string]any{"domain": "biology"}}, clauses[0])
}

func TestOpenSearchHealthCheck_RequestBuildFailure(t *testing.T) {
	resp, err := osHealthCheck(context.Background(), "http://[::1", "idx", "", "", time.Now())
	assert.NoError(t, err)
	assert.True(t, resp.FallbackTriggered)
	assert.Contains(t, resp.FallbackReason, "health_check_request_failed")
}

func TestOpenSearchHealthCheck_Unreachable(t *testing.T) {
	oldClient := osClient
	defer func() {
		osClient = oldClient
	}()

	expectedErr := errors.New("unreachable")
	osClient = stubOSClient{err: expectedErr}
	t.Setenv("OPENSEARCH_URL", "http://localhost")
	t.Setenv("OPENSEARCH_INDEX", "papers")

	resp, err := OpenSearchHybridSearch(context.Background(), OpenSearchHybridRequest{Query: "health-check"})
	assert.NoError(t, err)
	assert.True(t, resp.FallbackTriggered)
	assert.Contains(t, resp.FallbackReason, "opensearch_unreachable")
	assert.Contains(t, resp.FallbackReason, expectedErr.Error())
}

func TestOpenSearchHealthCheck_StatusFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	t.Setenv("OPENSEARCH_URL", server.URL)
	t.Setenv("OPENSEARCH_INDEX", "idx")

	resp, err := OpenSearchHybridSearch(context.Background(), OpenSearchHybridRequest{Query: "health-check"})
	assert.NoError(t, err)
	assert.True(t, resp.FallbackTriggered)
	assert.Contains(t, resp.FallbackReason, "opensearch_status_429")
}

func TestOpenSearchHybridSearch_RequestBuildFailure(t *testing.T) {
	resp, err := osHybridSearch(
		context.Background(),
		"http://[::1",
		"idx",
		"",
		"",
		OpenSearchHybridRequest{Query: "query"},
		10,
		time.Now(),
	)
	assert.NoError(t, err)
	assert.True(t, resp.FallbackTriggered)
	assert.Contains(t, resp.FallbackReason, "request_build_failed")
}

func TestApplyOSAuth_NoUser(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	assert.NoError(t, err)

	applyOSAuth(req, "", "")
	_, _, hasAuth := req.BasicAuth()
	assert.False(t, hasAuth)
}

func TestOpenSearchHybridSearch_QueryMarshalFailure(t *testing.T) {
	resp, err := osHybridSearch(
		context.Background(),
		"http://localhost",
		"idx",
		"",
		"",
		OpenSearchHybridRequest{
			Query: "query",
			Filters: map[string]any{
				"bad": make(chan int),
			},
		},
		10,
		time.Now(),
	)
	assert.NoError(t, err)
	assert.True(t, resp.FallbackTriggered)
	assert.Contains(t, resp.FallbackReason, "query_marshal_failed")
}

func TestParseOSResponse_TrimByLimitAndNilDoc(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"hits": map[string]any{
			"total": map[string]any{"value": 3},
			"hits": []map[string]any{
				{"_id": "a", "_score": 2.0, "_source": nil},
				{"_id": "b", "_score": 1.2, "_source": map[string]any{"id": "pre-set", "title": "Second"}},
			},
		},
	})
	assert.NoError(t, err)

	resp, err := parseOSResponse(strings.NewReader(string(body)), time.Now(), 1)
	assert.NoError(t, err)
	assert.False(t, resp.FallbackTriggered)
	assert.Len(t, resp.Papers, 1)
	assert.Equal(t, "a", resp.Papers[0]["id"])
	assert.Equal(t, 1.0, resp.QualitySignals["result_count"])
}

func TestParseOSResponse_SetsFallbackIDAndAverageScore(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"hits": map[string]any{
			"total": map[string]any{"value": 2},
			"hits": []map[string]any{
				{"_id": "a", "_score": 3.0, "_source": map[string]any{"title": "First"}},
				{"_id": "b", "_score": 1.0, "_source": map[string]any{"id": "pre-set", "title": "Second"}},
			},
		},
	})
	assert.NoError(t, err)

	resp, err := parseOSResponse(strings.NewReader(string(body)), time.Now(), 10)
	assert.NoError(t, err)
	assert.False(t, resp.FallbackTriggered)
	assert.Len(t, resp.Papers, 2)
	assert.Equal(t, "a", resp.Papers[0]["id"])
	assert.Equal(t, 3.0, resp.Papers[0]["_score"])
	assert.Equal(t, "pre-set", resp.Papers[1]["id"])
	assert.Equal(t, 2.0, resp.QualitySignals["avg_bm25_score"])
	assert.Equal(t, 2.0, resp.QualitySignals["result_count"])
	assert.Equal(t, 2, resp.TotalFound)
}

func TestParseOSResponse_UsesExistingIDWithoutOverwrite(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"hits": map[string]any{
			"total": map[string]any{"value": 1},
			"hits": []map[string]any{
				{"_id": "fallback", "_score": 4.2, "_source": map[string]any{"id": "kept-id", "title": "Has source id"}},
			},
		},
	})
	assert.NoError(t, err)

	resp, err := parseOSResponse(strings.NewReader(string(body)), time.Now(), 10)
	assert.NoError(t, err)
	assert.Len(t, resp.Papers, 1)
	assert.Equal(t, "kept-id", resp.Papers[0]["id"])
	assert.Equal(t, 4.2, resp.Papers[0]["_score"])
}

func TestParseOSResponse_EmptyHitsAveragesZero(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"hits": map[string]any{
			"total": map[string]any{"value": 0},
			"hits":  []map[string]any{},
		},
	})
	assert.NoError(t, err)

	resp, err := parseOSResponse(strings.NewReader(string(body)), time.Now(), 10)
	assert.NoError(t, err)
	assert.Len(t, resp.Papers, 0)
	assert.Equal(t, float64(0), resp.QualitySignals["result_count"])
	assert.Equal(t, 0.0, resp.QualitySignals["avg_bm25_score"])
}

func TestResolveOpenSearchConfig_TrimsSlashAndDefaultsIndex(t *testing.T) {
	t.Setenv("OPENSEARCH_URL", "http://localhost:9200/")
	t.Setenv("OPENSEARCH_INDEX", "")
	t.Setenv("OPENSEARCH_USER", "")
	t.Setenv("OPENSEARCH_PASSWORD", "")

	baseURL, index, user, password, ok := resolveOpenSearchConfig()
	assert.True(t, ok)
	assert.Equal(t, "http://localhost:9200", baseURL)
	assert.Equal(t, "wisdev-papers", index)
	assert.Equal(t, "", user)
	assert.Equal(t, "", password)
}

func TestOSFallback(t *testing.T) {
	reason := "timeout"
	start := time.Now()
	resp := osFallback(start, reason)
	assert.True(t, resp.FallbackTriggered)
	assert.Equal(t, "opensearch_hybrid", resp.BackendUsed)
	assert.Equal(t, 0, resp.TotalFound)
	assert.Equal(t, reason, resp.FallbackReason)
	assert.Empty(t, resp.Papers)
}

func TestOpenSearchHybridSearch_StatusFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "_search") {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"boom"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("OPENSEARCH_URL", server.URL)
	t.Setenv("OPENSEARCH_INDEX", "idx")

	resp, err := OpenSearchHybridSearch(context.Background(), OpenSearchHybridRequest{Query: "query", Limit: 2})
	assert.NoError(t, err)
	assert.True(t, resp.FallbackTriggered)
	assert.Contains(t, resp.FallbackReason, "opensearch_status_500")
}

func TestOpenSearchHybridSearch_ClientError(t *testing.T) {
	oldClient := osClient
	defer func() {
		osClient = oldClient
	}()

	t.Setenv("OPENSEARCH_URL", "http://localhost")
	t.Setenv("OPENSEARCH_INDEX", "idx")
	osClient = stubOSClient{err: errors.New("timeout")}

	resp, err := OpenSearchHybridSearch(context.Background(), OpenSearchHybridRequest{Query: "query"})
	assert.NoError(t, err)
	assert.True(t, resp.FallbackTriggered)
	assert.Contains(t, resp.FallbackReason, "opensearch_unreachable")
	assert.Contains(t, resp.FallbackReason, "timeout")
}

func TestOpenSearchHybridSearch_ResponseDecodeFallback(t *testing.T) {
	oldClient := osClient
	defer func() {
		osClient = oldClient
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, strings.NewReader(`not-json`))
	}))
	defer server.Close()

	osClient = stubOSClient{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`not-json`)),
		},
	}
	t.Setenv("OPENSEARCH_URL", server.URL)
	t.Setenv("OPENSEARCH_INDEX", "idx")

	resp, err := OpenSearchHybridSearch(context.Background(), OpenSearchHybridRequest{Query: "query"})
	assert.NoError(t, err)
	assert.True(t, resp.FallbackTriggered)
	assert.Contains(t, resp.FallbackReason, "response_parse_failed")
}
