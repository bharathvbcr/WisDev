package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

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

func TestNewRouter_WiresWisDevAliasesWithoutInjectedGateway(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{Version: "test"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wisdev-brain/memory/profile/get?userId=u1", nil)
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code)
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

func TestNewRouter_WiresLegacyWisDevAgentAnalyzeAlias(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{Version: "test"})
	req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/analyze-query", strings.NewReader(`{"query":"graph neural networks in medicine"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNewRouter_WiresFriendlyWisDevV2Aliases(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{Version: "test"})

	analyzeReq := httptest.NewRequest(http.MethodPost, "/v2/wisdev/analyze-query", strings.NewReader(`{"query":"graph neural networks in medicine"}`))
	analyzeReq.Header.Set("Content-Type", "application/json")
	analyzeReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	analyzeRec := httptest.NewRecorder()
	router.ServeHTTP(analyzeRec, analyzeReq)

	assert.Equal(t, http.StatusOK, analyzeRec.Code)

	paper2SkillReq := httptest.NewRequest(http.MethodPost, "/v2/wisdev/paper2skill", strings.NewReader(`{"arxiv_id":"1234.5678"}`))
	paper2SkillReq.Header.Set("Content-Type", "application/json")
	paper2SkillReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	paper2SkillRec := httptest.NewRecorder()
	router.ServeHTTP(paper2SkillRec, paper2SkillReq)

	assert.Equal(t, http.StatusOK, paper2SkillRec.Code)
}

func TestNewRouter_WiresManuscriptAndReviewerAliases(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{Version: "test"})

	manuscriptReq := httptest.NewRequest(http.MethodPost, "/api/v1/wisdev-brain/manuscript/draft", strings.NewReader(`{"title":"Research Synthesis","findings":["f1"]}`))
	manuscriptReq.Header.Set("Content-Type", "application/json")
	manuscriptReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	manuscriptRec := httptest.NewRecorder()
	router.ServeHTTP(manuscriptRec, manuscriptReq)

	assert.NotEqual(t, http.StatusNotFound, manuscriptRec.Code)
	assert.NotEqual(t, http.StatusUnauthorized, manuscriptRec.Code)

	reviewerReq := httptest.NewRequest(http.MethodPost, "/api/v1/wisdev-brain/reviewer/rebuttal", strings.NewReader(`{"reviewer_comments":["please clarify the methods section"]}`))
	reviewerReq.Header.Set("Content-Type", "application/json")
	reviewerReq.Header.Set("X-Internal-Service-Key", "test-internal-key")

	reviewerRec := httptest.NewRecorder()
	router.ServeHTTP(reviewerRec, reviewerReq)

	assert.NotEqual(t, http.StatusNotFound, reviewerRec.Code)
	assert.NotEqual(t, http.StatusUnauthorized, reviewerRec.Code)
}
