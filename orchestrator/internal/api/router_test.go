package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

func removedVersionPath(path string) string {
	suffix := strings.TrimPrefix(path, "/")
	if suffix == "" {
		return "/v" + "2"
	}
	return "/v" + "2/" + suffix
}

func TestRegisterJSONPostAlias_UsesCanonicalErrorEnvelope(t *testing.T) {
	mux := http.NewServeMux()
	registerJSONPostAlias(mux, "/alias", "/target", func(_ *http.Request) map[string]any {
		return map[string]any{"bad": math.Inf(1)}
	})

	req := httptest.NewRequest(http.MethodGet, "/alias", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var resp APIError
	err := json.NewDecoder(rec.Body).Decode(&resp)
	assert.NoError(t, err)
	assert.False(t, resp.OK)
	assert.Equal(t, ErrInternal, resp.Error.Code)
	assert.Equal(t, "failed to marshal alias payload", resp.Error.Message)
	assert.Equal(t, "/alias", resp.Error.Details["aliasPath"])
	assert.Equal(t, "/target", resp.Error.Details["targetPath"])
}

func TestRegisterJSONPostAlias_RewritesRequestAsJSONPost(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var body map[string]any
		err := json.NewDecoder(r.Body).Decode(&body)
		assert.NoError(t, err)
		assert.Equal(t, "u1", body["userId"])
		w.WriteHeader(http.StatusNoContent)
	})

	registerJSONPostAlias(mux, "/alias", "/target", func(r *http.Request) map[string]any {
		return map[string]any{"userId": r.URL.Query().Get("userId")}
	})

	req := httptest.NewRequest(http.MethodGet, "/alias?userId=u1", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestNewRouter_WiresCanonicalWisDevRoutesWithoutInjectedGateway(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{Version: "test"})
	req := httptest.NewRequest(http.MethodGet, "/memory/profile/get?userId=u1", nil)
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code)
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

func TestNewRouter_WiresTopicTreeQueriesRoute(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{Version: "test"})
	req := httptest.NewRequest(http.MethodPost, "/topic-tree/queries", strings.NewReader(`{
		"query":"sleep memory consolidation",
		"maxQueries":4,
		"rootNode":{
			"id":"root",
			"label":"sleep memory consolidation",
			"children":[],
			"isSelected":true,
			"isExpanded":true,
			"depth":0,
			"nodeType":"root"
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEnsureAgentGateway_BackfillsQuestRuntimeOnProvidedGateway(t *testing.T) {
	gateway := &wisdev.AgentGateway{}

	resolved := ensureAgentGateway(ServerConfig{AgentGateway: gateway})

	assert.Same(t, gateway, resolved)
	assert.NotNil(t, resolved.QuestRuntime)
}

func TestNewRouter_WiresWisDevJobStatusRoute(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")
	useIsolatedYoloState(t)

	router := NewRouter(ServerConfig{Version: "test"})
	jobID := "router_status_job"
	job := &YoloJob{
		ID:        jobID,
		TraceID:   "trace-router-status-1",
		CreatedAt: time.Now(),
	}
	yoloJobStore.put(job)
	t.Cleanup(func() {
		yoloJobStore.delete(jobID)
	})

	statusReq := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+jobID, nil)
	statusReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)
	assert.Equal(t, http.StatusOK, statusRec.Code)
}

func TestNewRouter_WiresLegacyYoloStatusRoute(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")
	useIsolatedYoloState(t)

	router := NewRouter(ServerConfig{Version: "test"})
	jobID := "legacy_router_status_job"
	job := &YoloJob{
		ID:        jobID,
		TraceID:   "trace-legacy-router-status-1",
		CreatedAt: time.Now(),
	}
	yoloJobStore.put(job)
	t.Cleanup(func() {
		yoloJobStore.delete(jobID)
	})

	statusReq := httptest.NewRequest(http.MethodGet, "/agent/yolo/status?job_id="+jobID, nil)
	statusReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)
	assert.Equal(t, http.StatusOK, statusRec.Code)
}

func TestNewRouter_WiresWisDevJobStreamRoute(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")
	useIsolatedYoloState(t)

	router := NewRouter(ServerConfig{Version: "test"})

	streamJobID := "router_stream_job"
	streamJob := &YoloJob{
		ID:            streamJobID,
		UnifiedEvents: make(chan UnifiedEvent, 1),
	}
	yoloJobStore.put(streamJob)
	t.Cleanup(func() {
		yoloJobStore.delete(streamJobID)
	})
	streamJob.UnifiedEvents <- UnifiedEvent{Type: "job_done"}
	close(streamJob.UnifiedEvents)

	streamReq := httptest.NewRequest(http.MethodGet, "/wisdev/job/"+streamJobID+"/stream", nil)
	streamReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	streamRec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	router.ServeHTTP(streamRec, streamReq)
	assert.Equal(t, http.StatusOK, streamRec.Code)
}

func TestNewRouter_WiresWisDevJobRoutes(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")
	useIsolatedYoloState(t)

	originalRunUnifiedWisDevJobLoop := runUnifiedWisDevJobLoop
	t.Cleanup(func() { runUnifiedWisDevJobLoop = originalRunUnifiedWisDevJobLoop })

	called := make(chan struct{}, 1)
	runUnifiedWisDevJobLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.UnifiedResearchResult, error) {
		called <- struct{}{}
		return &wisdev.UnifiedResearchResult{LoopResult: &wisdev.LoopResult{Papers: []search.Paper{}}}, nil
	}

	router := NewRouter(ServerConfig{Version: "test"})

	req := httptest.NewRequest(http.MethodPost, "/wisdev/job", strings.NewReader(`{"query":"rlhf"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	err := json.NewDecoder(rec.Body).Decode(&resp)
	assert.NoError(t, err)
	waitForMockCall(t, called)
	waitForUnifiedJobCompletion(t, fmt.Sprint(resp["job_id"]))
}

func TestNewRouter_DoesNotLogDuplicateAliasWarnings(t *testing.T) {
	var logBuffer bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(originalLogger)

	_ = NewRouter(ServerConfig{Version: "test"})

	logOutput := logBuffer.String()
	assert.Contains(t, logOutput, "Registering WisDev routes")
	assert.NotContains(t, logOutput, "skipping duplicate path alias registration")
	assert.NotContains(t, logOutput, "alias_path=")
}

func TestNewRouter_WiresLLMEmbedBatchRoute(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{
		Version: "test",
	})

	req := httptest.NewRequest(http.MethodPost, "/llm/embed/batch", strings.NewReader(`{"texts":["alpha","beta"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code)

	var payload map[string]any
	err := json.NewDecoder(rec.Body).Decode(&payload)
	assert.NoError(t, err)
	assert.Equal(t, "transient", payload["kind"])
}

func TestNewRouter_WiresRAGEmbedBatchActionRoute(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{
		Version: "test",
	})

	req := httptest.NewRequest(http.MethodPost, "/rag?action=embed-batch", strings.NewReader(`{"texts":["alpha","beta"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code)

	var payload map[string]any
	err := json.NewDecoder(rec.Body).Decode(&payload)
	assert.NoError(t, err)
	assert.Equal(t, "transient", payload["kind"])
}

func TestNewRouter_RemovesLegacyWisDevAgentAnalyzeAlias(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{Version: "test"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wisdev-agent/analyze-query", strings.NewReader(`{"query":"graph neural networks in medicine"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNewRouter_RemovesWisDevV2Aliases(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{Version: "test"})

	analyzeReq := httptest.NewRequest(http.MethodPost, removedVersionPath("wisdev/analyze-query"), strings.NewReader(`{"query":"graph neural networks in medicine"}`))
	analyzeReq.Header.Set("Content-Type", "application/json")
	analyzeReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	analyzeRec := httptest.NewRecorder()
	router.ServeHTTP(analyzeRec, analyzeReq)

	assert.Equal(t, http.StatusNotFound, analyzeRec.Code)

	paper2SkillReq := httptest.NewRequest(http.MethodPost, removedVersionPath("wisdev/paper2skill"), strings.NewReader(`{"arxiv_id":"1234.5678"}`))
	paper2SkillReq.Header.Set("Content-Type", "application/json")
	paper2SkillReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	paper2SkillRec := httptest.NewRecorder()
	router.ServeHTTP(paper2SkillRec, paper2SkillReq)

	assert.Equal(t, http.StatusNotFound, paper2SkillRec.Code)
}

func TestNewRouter_RemovesGeneralV2Aliases(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{Version: "test"})

	requests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: removedVersionPath("verifier/blind-contract")},
		{method: http.MethodPost, path: removedVersionPath("query/categories"), body: `{"query":"graph neural networks"}`},
		{method: http.MethodPost, path: removedVersionPath("images/generate"), body: `{"prompt":"diagram"}`},
		{method: http.MethodPost, path: removedVersionPath("search/parallel"), body: `{"query":"sleep and memory"}`},
		{method: http.MethodPost, path: removedVersionPath("topic-tree/queries"), body: `{"query":"sleep and memory"}`},
	}

	for _, tc := range requests {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("X-Internal-Service-Key", "test-internal-key")
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code, tc.path)
	}
}

func TestNewRouter_RemovesManuscriptAndReviewerLegacyAliases(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{Version: "test"})

	manuscriptReq := httptest.NewRequest(http.MethodPost, "/api/v1/wisdev-brain/manuscript/draft", strings.NewReader(`{"title":"Research Synthesis","findings":["f1"]}`))
	manuscriptReq.Header.Set("Content-Type", "application/json")
	manuscriptReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	manuscriptRec := httptest.NewRecorder()
	router.ServeHTTP(manuscriptRec, manuscriptReq)

	assert.Equal(t, http.StatusNotFound, manuscriptRec.Code)

	reviewerReq := httptest.NewRequest(http.MethodPost, "/api/v1/wisdev-brain/reviewer/rebuttal", strings.NewReader(`{"reviewer_comments":["please clarify the methods section"]}`))
	reviewerReq.Header.Set("Content-Type", "application/json")
	reviewerReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	reviewerRec := httptest.NewRecorder()
	router.ServeHTTP(reviewerRec, reviewerReq)

	assert.Equal(t, http.StatusNotFound, reviewerRec.Code)
}
