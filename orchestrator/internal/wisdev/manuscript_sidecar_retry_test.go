package wisdev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/resilience"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withFastSidecarRetries shrinks the sidecar retry backoff for the duration of a
// test so exhausted-retry paths stay fast, restoring the production policy after.
func withFastSidecarRetries(t *testing.T) {
	t.Helper()
	prev := manuscriptSidecarRetryPolicy
	manuscriptSidecarRetryPolicy = resilience.RetryPolicy{MaxAttempts: 3, BaseDelay: 2 * time.Millisecond, MaxDelay: 8 * time.Millisecond}
	t.Cleanup(func() { manuscriptSidecarRetryPolicy = prev })
}

// countingSidecar is an httptest sidecar whose handler is chosen per attempt
// number, so tests can script 5xx-then-200 sequences and assert attempt counts.
type countingSidecar struct {
	mu       sync.Mutex
	requests int
	handler  func(attempt int, w http.ResponseWriter, r *http.Request)
	server   *httptest.Server
}

func newCountingSidecar(t *testing.T, handler func(attempt int, w http.ResponseWriter, r *http.Request)) *countingSidecar {
	t.Helper()
	s := &countingSidecar{handler: handler}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		attempt := s.requests
		s.mu.Unlock()
		s.handler(attempt, w, r)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *countingSidecar) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func TestSidecarPostSucceedsFirstTry(t *testing.T) {
	withFastSidecarRetries(t)
	sidecar := newCountingSidecar(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"content": "drafted prose [1]"})
	})

	pipeline := NewManuscriptPipeline(sidecar.server.URL)
	content, err := pipeline.postSectionContent(context.Background(), "/wisdev/manuscript/section/generate", map[string]any{"section_id": "results"})
	require.NoError(t, err)
	assert.Equal(t, "drafted prose [1]", content)
	assert.Equal(t, 1, sidecar.count(), "a successful first attempt must not be retried")
}

func TestSidecarPostRetriesOnceAfterServerError(t *testing.T) {
	withFastSidecarRetries(t)
	sidecar := newCountingSidecar(t, func(attempt int, w http.ResponseWriter, _ *http.Request) {
		if attempt == 1 {
			http.Error(w, "sidecar hiccup", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": "drafted after retry [1]"})
	})

	pipeline := NewManuscriptPipeline(sidecar.server.URL)
	content, err := pipeline.postSectionContent(context.Background(), "/wisdev/manuscript/section/generate", map[string]any{"section_id": "results"})
	require.NoError(t, err)
	assert.Equal(t, "drafted after retry [1]", content)
	assert.Equal(t, 2, sidecar.count(), "one 5xx should cost exactly one retry")
}

func TestSidecarPostExhaustsRetriesAndFallsBackToScaffold(t *testing.T) {
	withFastSidecarRetries(t)
	sidecar := newCountingSidecar(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sidecar down", http.StatusServiceUnavailable)
	})

	pipeline := NewManuscriptPipeline(sidecar.server.URL)
	content, err := pipeline.postSectionContent(context.Background(), "/wisdev/manuscript/section/generate", map[string]any{"section_id": "results"})
	require.Error(t, err, "exhausted retries must surface the last error")
	assert.Empty(t, content)
	assert.Equal(t, 3, sidecar.count(), "the retry budget is exactly three attempts")

	// The pipeline-level contract: generateSectionContent surfaces the error and
	// writeSections keeps the grounded scaffold, never a hard failure.
	brief := SectionBrief{SectionID: "introduction", Title: "Introduction", Goal: "Frame the problem.", WriterRole: "framing_writer"}
	blueprint := ManuscriptBlueprint{Query: "graphene battery anodes", Sections: []SectionBrief{brief}}
	_, genErr := pipeline.generateSectionContent(context.Background(), brief, nil, blueprint)
	require.Error(t, genErr, "the write stage must see the failure so it keeps the scaffold")
	assert.Equal(t, 6, sidecar.count(), "the second call also spends its full retry budget")
	_, scaffold, _ := buildSectionScaffold(brief, nil, blueprint.Query)
	assert.NotEmpty(t, scaffold, "the deterministic scaffold fallback must always exist")
}

func TestSidecarPostDoesNotRetryClientErrors(t *testing.T) {
	withFastSidecarRetries(t)
	sidecar := newCountingSidecar(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad payload", http.StatusBadRequest)
	})

	pipeline := NewManuscriptPipeline(sidecar.server.URL)
	_, err := pipeline.postSectionContent(context.Background(), "/wisdev/manuscript/section/generate", map[string]any{"section_id": "results"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 400")
	assert.Equal(t, 1, sidecar.count(), "4xx responses must never be retried")
}

func TestSidecarPostHonorsCancellationBetweenRetries(t *testing.T) {
	// Long backoff so the mid-retry cancellation is what ends the wait, not the
	// attempt budget.
	prev := manuscriptSidecarRetryPolicy
	manuscriptSidecarRetryPolicy = resilience.RetryPolicy{MaxAttempts: 3, BaseDelay: 5 * time.Second, MaxDelay: 5 * time.Second}
	t.Cleanup(func() { manuscriptSidecarRetryPolicy = prev })

	firstAttemptDone := make(chan struct{}, 1)
	sidecar := newCountingSidecar(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sidecar down", http.StatusInternalServerError)
		select {
		case firstAttemptDone <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-firstAttemptDone
		// Let the client finish reading the 500 and enter the backoff sleep.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	pipeline := NewManuscriptPipeline(sidecar.server.URL)
	_, err := pipeline.postSectionContent(ctx, "/wisdev/manuscript/section/generate", map[string]any{"section_id": "results"})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, sidecar.count(), "cancellation must stop the loop before the next attempt")
	assert.Less(t, time.Since(started), 3*time.Second, "cancellation must interrupt the backoff sleep")
}

func TestSidecarReviewAndDedupeShareRetryPath(t *testing.T) {
	withFastSidecarRetries(t)
	sidecar := newCountingSidecar(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
		if attempt%2 == 1 {
			http.Error(w, "sidecar hiccup", http.StatusBadGateway)
			return
		}
		switch {
		case r.URL.Path == "/wisdev/manuscript/review":
			_ = json.NewEncoder(w).Encode(map[string]any{"content_score": 0.7})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"sections": []map[string]any{{"section_id": "results", "text": "deduped"}}})
		}
	})

	pipeline := NewManuscriptPipeline(sidecar.server.URL)
	review, err := pipeline.postManuscriptReview(context.Background(), map[string]any{"query": "q"})
	require.NoError(t, err)
	assert.InDelta(t, 0.7, review.ContentScore, 1e-9)
	assert.Equal(t, 2, sidecar.count(), "the review helper retries through the shared path")

	deduped, err := pipeline.postCoordinateDedupe(context.Background(), map[string]any{"query": "q"})
	require.NoError(t, err)
	require.Len(t, deduped.Sections, 1)
	assert.Equal(t, "results", deduped.Sections[0].SectionID)
	assert.Equal(t, 4, sidecar.count(), "the dedupe helper retries through the shared path")
}
