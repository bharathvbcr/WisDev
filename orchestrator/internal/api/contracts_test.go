package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWisDevServer_ContractRoutes(t *testing.T) {
	s := &wisdevServer{
		gateway: nil,
	}
	mux := http.NewServeMux()
	s.registerContractRoutes(mux, nil) // gateway is nil, but we might hit some paths

	t.Run("PlanRevision_MethodNotAllowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/plan/revision", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("Subtopics_EmptyQuery", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"query": ""})
		req := httptest.NewRequest(http.MethodPost, "/wisdev/subtopics/generate", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("StudyTypes_Success", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"query": "test"})
		req := httptest.NewRequest(http.MethodPost, "/wisdev/study-types/generate", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("RecommendedAnswers_MissingQuestionID", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"query": "sleep and memory"})
		req := httptest.NewRequest(http.MethodPost, "/wisdev/recommended-answers", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("RecommendedAnswers_UsesCanonicalGoHeuristic", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"questionId":   "primary_domain",
			"query":        "sleep and memory",
			"domain":       "medicine",
			"optionValues": []string{"medicine", "computer science"},
			"queryAnalysis": map[string]any{
				"suggestedDomains": []string{"medicine"},
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/wisdev/recommended-answers", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		recommended, ok := resp["recommendedAnswers"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, "heuristic", recommended["source"])
		assert.Equal(t, "primary_domain", recommended["questionId"])
		assert.Equal(t, []any{"medicine"}, recommended["values"])
	})
}

func TestWisDevServer_QuestioningRoutes_AnalyzeQueryTraceEnvelope(t *testing.T) {
	s := &wisdevServer{gateway: nil}
	mux := http.NewServeMux()
	s.registerQuestioningRoutes(mux, nil)

	body, err := json.Marshal(map[string]any{
		"query":   " graph neural networks in medicine ",
		"traceId": "trace-route-analyze-1",
	})
	assert.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/analyze-query", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "trace-route-analyze-1", rec.Header().Get("X-Trace-Id"))

	var resp map[string]any
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "trace-route-analyze-1", resp["traceId"])
	assert.Equal(t, "graph neural networks in medicine", resp["queryUsed"])
	assert.Equal(t, false, resp["cache_hit"])
}

func TestBuildPlanRevisionMessage_Extra(t *testing.T) {
	msg := buildPlanRevisionMessage("s1", "reason")
	assert.Contains(t, msg, "s1")
	assert.Contains(t, msg, "reason")

	msg2 := buildPlanRevisionMessage("", "general")
	assert.Contains(t, msg2, "general")
}

func TestUniqueStrings(t *testing.T) {
	in := []string{"a", "b", "a", "c"}
	out := uniqueStrings(in)
	assert.Len(t, out, 3)
	assert.ElementsMatch(t, []string{"a", "b", "c"}, out)
}
