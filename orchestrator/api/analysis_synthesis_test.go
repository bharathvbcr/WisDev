package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"

	"github.com/stretchr/testify/assert"
)

type stubGenerateClient struct {
	generate func(context.Context, *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error)
}

func (s stubGenerateClient) Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
	if s.generate == nil {
		return &llmv1.GenerateResponse{Text: "ok"}, nil
	}
	return s.generate(ctx, req)
}

func TestAnalysisHandlerErrors(t *testing.T) {
	handler := NewAnalysisHandler(stubGenerateClient{})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/analysis?action=gaps", nil)
		rec := httptest.NewRecorder()

		handler.HandleAnalysis(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("invalid action", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/analysis?action=unknown", bytes.NewBufferString(`{}`))
		rec := httptest.NewRecorder()

		handler.HandleAnalysis(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("gaps requires papers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/analysis?action=gaps", bytes.NewBufferString(`{"papers":[{"title":"one"}]}`))
		rec := httptest.NewRecorder()

		handler.HandleAnalysis(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("gaps llm failure", func(t *testing.T) {
		failing := NewAnalysisHandler(stubGenerateClient{
			generate: func(context.Context, *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
				return nil, errors.New("sidecar down")
			},
		})
		body := `{"topic":"biology","papers":[{"title":"a","abstract":"x","year":"2020"},{"title":"b","abstract":"y","year":"2021"},{"title":"c","abstract":"z","year":"2022"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v2/analysis?action=gaps", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()

		failing.HandleAnalysis(rec, req)

		assert.Equal(t, http.StatusBadGateway, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrDependencyFailed, resp.Error.Code)
	})
}

func TestSynthesisHandlerErrors(t *testing.T) {
	handler := NewSynthesisHandler(stubGenerateClient{})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/synthesis?action=summary", nil)
		rec := httptest.NewRecorder()

		handler.HandleSynthesis(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("invalid action", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/synthesis?action=unknown", bytes.NewBufferString(`{}`))
		rec := httptest.NewRecorder()

		handler.HandleSynthesis(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("summary requires title", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/synthesis?action=summary", bytes.NewBufferString(`{"abstract":"test"}`))
		rec := httptest.NewRecorder()

		handler.HandleSynthesis(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("compare llm failure", func(t *testing.T) {
		failing := NewSynthesisHandler(stubGenerateClient{
			generate: func(context.Context, *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
				return nil, errors.New("llm unavailable")
			},
		})
		body := `{"papers":[{"title":"a","abstract":"x","authors":"u","year":"2020"},{"title":"b","abstract":"y","authors":"v","year":"2021"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v2/synthesis?action=compare", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()

		failing.HandleSynthesis(rec, req)

		assert.Equal(t, http.StatusBadGateway, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrDependencyFailed, resp.Error.Code)
	})
}
