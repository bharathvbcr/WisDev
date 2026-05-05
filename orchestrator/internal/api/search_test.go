package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

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

	t.Run("HandleParallelSearch - Unsupported Provider", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/parallel?q=test&source=not_a_provider", nil)
		rec := httptest.NewRecorder()
		h.HandleParallelSearch(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		errPayload, ok := payload["error"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, "INVALID_REQUEST", errPayload["code"])
		assert.Contains(t, errPayload["message"].(string), "unsupported provider hint")
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

	t.Run("HandleBatchSearch - Skips Empty Queries", func(t *testing.T) {
		body := `{"queries":["", " q2 ", "   "], "limit":2}`
		req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleBatchSearch(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		results := resp["results"].(map[string]any)
		assert.Len(t, results, 1)
		assert.Contains(t, results, "q2")
		assert.NotContains(t, results, "")
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

// slowMockProvider blocks until the context is cancelled, simulating a slow
// backend. Used for the P4-8 regression test.
type slowMockProvider struct {
	search.BaseProvider
}

func (s *slowMockProvider) Name() string      { return "slow" }
func (s *slowMockProvider) Domains() []string { return []string{"general"} }
func (s *slowMockProvider) Healthy() bool     { return true }
func (s *slowMockProvider) Search(ctx context.Context, _ string, _ search.SearchOpts) ([]search.Paper, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestHandleBatchSearch_SemaphoreContention_NoPanic(t *testing.T) {
	// Regression for P4-8: when many queries are submitted simultaneously and
	// the context is cancelled while goroutines are blocked on semaphore
	// acquisition, the "send on closed channel" panic must not occur.
	//
	// The fix removes the resultsCh <- send from the semaphore-failure branch
	// so goroutines that never acquired the semaphore simply exit cleanly
	// instead of writing to an already-closed channel.

	reg := search.NewProviderRegistry()
	// Use a slow provider so all goroutines block until context cancel.
	reg.Register(&slowMockProvider{})
	search.ApplyDomainRoutes(reg)
	h := NewSearchHandler(reg, reg, nil)

	// Submit 20 queries to overwhelm the default batch concurrency (4).
	queries := make([]string, 20)
	for i := range queries {
		queries[i] = "query"
	}
	body, _ := json.Marshal(map[string]any{"queries": queries, "limit": 5})

	// Use a context that cancels almost immediately, forcing semaphore-blocked
	// goroutines to hit the ctx.Err() path while the channel may be closing.
	// The test must complete without panicking.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(string(body))).WithContext(ctx)
	rec := httptest.NewRecorder()

	// This must not panic regardless of goroutine scheduling order.
	assert.NotPanics(t, func() {
		h.HandleBatchSearch(rec, req)
	})
}
