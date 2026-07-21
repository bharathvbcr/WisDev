package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
)

func TestQueryIntentHandler_ErrorEnvelopes(t *testing.T) {
	handler := NewQueryIntentHandler(stubGenerateClient{})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/expand/intent", nil)
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/expand/intent", bytes.NewBufferString(`{bad`))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/expand/intent", bytes.NewBufferString(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("llm unavailable", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/expand/intent", bytes.NewBufferString(`{"query":"future tech"}`))
		rec := httptest.NewRecorder()
		NewQueryIntentHandler(nil).Handle(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

func TestQueryIntentHandler_Success(t *testing.T) {
	handler := NewQueryIntentHandler(stubGenerateClient{
		structuredOutput: func(_ context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
			assert.Contains(t, req.GetPrompt(), "future of technology")
			assert.Contains(t, req.GetJsonSchema(), `"intent"`)
			return &llmv1.StructuredResponse{JsonResult: `{"intent":"trends"}`}, nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/expand/intent", bytes.NewBufferString(`{"query":"future of technology"}`))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Intent search.QueryIntent `json:"intent"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, search.IntentTrends, resp.Intent)
}

func TestQueryIntentHandler_HeuristicSkipsLLM(t *testing.T) {
	called := false
	handler := NewQueryIntentHandler(stubGenerateClient{
		structuredOutput: func(_ context.Context, _ *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
			called = true
			return &llmv1.StructuredResponse{JsonResult: `{"intent":"papers"}`}, nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/expand/intent", bytes.NewBufferString(`{"query":"what is CRISPR"}`))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Intent search.QueryIntent `json:"intent"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, search.IntentDefinition, resp.Intent)
	assert.False(t, called, "heuristic hit must not call the LLM")
}

func TestQueryPolicyHandler_ErrorEnvelopes(t *testing.T) {
	handler := &QueryPolicyHandler{}

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/expand/query-policy?action=precached", nil)
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("invalid action", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/expand/query-policy?action=unknown", bytes.NewBufferString(`{"query":"test"}`))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/expand/query-policy?action=specificity", bytes.NewBufferString(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestQueryPolicyHandler_Precached(t *testing.T) {
	handler := &QueryPolicyHandler{}
	req := httptest.NewRequest(http.MethodPost, "/expand/query-policy?action=precached", bytes.NewBufferString(`{"query":"pytorch"}`))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Match  bool                   `json:"match"`
		Result *search.EnhancedQuery  `json:"result"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.True(t, resp.Match)
	assert.NotNil(t, resp.Result)
	assert.Contains(t, resp.Result.Keywords, "PyTorch")
}

func TestQueryPolicyHandler_Specificity(t *testing.T) {
	handler := &QueryPolicyHandler{}
	req := httptest.NewRequest(http.MethodPost, "/expand/query-policy?action=specificity", bytes.NewBufferString(`{"query":"machine learning 2023"}`))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp search.SpecificityResult
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.True(t, resp.IsSpecific)
}

func TestQueryPolicyHandler_Optimized(t *testing.T) {
	handler := &QueryPolicyHandler{}
	body := `{
		"query":"machine learning",
		"expansion":{
			"original":"machine learning",
			"expansions":{"synonyms":["ML"],"meshTerms":[],"relatedConcepts":[],"broaderTerms":["artificial intelligence"]},
			"expandedQuery":"machine learning OR ML"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/expand/query-policy?action=optimized", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Queries []string `json:"queries"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp.Queries, "machine learning")
}

func TestQueryPolicyHandler_EmbeddingTargets(t *testing.T) {
	handler := &QueryPolicyHandler{}
	req := httptest.NewRequest(http.MethodPost, "/expand/query-policy?action=embedding-targets", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Targets []string `json:"targets"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp.Targets, "machine learning")
}
