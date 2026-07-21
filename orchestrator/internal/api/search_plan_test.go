package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
)

func TestSearchPlanHandler_Success(t *testing.T) {
	handler := NewSearchPlanHandler(stubGenerateClient{
		structuredOutput: func(_ context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
			assert.Contains(t, req.GetPrompt(), "research architect")
			assert.Contains(t, req.GetPrompt(), "machine learning in healthcare")
			assert.Contains(t, req.GetPrompt(), "DOMAIN: Medicine/Healthcare")
			assert.Contains(t, req.GetPrompt(), "MeSH")
			assert.Contains(t, req.GetJsonSchema(), `"buckets"`)
			assert.Equal(t, float32(0.3), req.GetTemperature())
			return &llmv1.StructuredResponse{
				JsonResult: `{"buckets":[{"name":"Clinical Applications","queries":["ML AND clinical","healthcare AND deep learning","EHR AND prediction"]}]}`,
			}, nil
		},
	})

	body := `{"query":"machine learning in healthcare","domain":"medicine","intent":"papers"}`
	req := httptest.NewRequest(http.MethodPost, "/search/plan", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Buckets []struct {
			Name    string   `json:"name"`
			Queries []string `json:"queries"`
		} `json:"buckets"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Len(t, resp.Buckets, 1)
	assert.Equal(t, "Clinical Applications", resp.Buckets[0].Name)
	assert.Len(t, resp.Buckets[0].Queries, 3)
}

func TestSearchPlanHandler_ExistingCategories(t *testing.T) {
	handler := NewSearchPlanHandler(stubGenerateClient{
		structuredOutput: func(_ context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
			assert.Contains(t, req.GetPrompt(), `["Clinical","Methods"]`)
			assert.Contains(t, req.GetPrompt(), "user has selected these research buckets")
			return &llmv1.StructuredResponse{
				JsonResult: `{"buckets":[{"name":"Clinical","queries":["q1","q2","q3"]},{"name":"Methods","queries":["m1","m2","m3"]}]}`,
			}, nil
		},
	})

	body := `{"query":"RLHF","existing_categories":["Clinical","Methods"]}`
	req := httptest.NewRequest(http.MethodPost, "/search/plan", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"Clinical"`)
}

func TestSearchPlanHandler_Validation(t *testing.T) {
	t.Run("method not allowed", func(t *testing.T) {
		handler := NewSearchPlanHandler(stubGenerateClient{})
		req := httptest.NewRequest(http.MethodGet, "/search/plan", nil)
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("missing query", func(t *testing.T) {
		handler := NewSearchPlanHandler(stubGenerateClient{})
		req := httptest.NewRequest(http.MethodPost, "/search/plan", bytes.NewBufferString(`{}`))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("nil client", func(t *testing.T) {
		handler := NewSearchPlanHandler(nil)
		req := httptest.NewRequest(http.MethodPost, "/search/plan", bytes.NewBufferString(`{"query":"x"}`))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("upstream failure", func(t *testing.T) {
		handler := NewSearchPlanHandler(stubGenerateClient{
			structuredOutput: func(_ context.Context, _ *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
				return nil, assert.AnError
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/search/plan", bytes.NewBufferString(`{"query":"x"}`))
		rec := httptest.NewRecorder()
		handler.Handle(rec, req)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
	})
}
