package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestAnalysisHandler_HandleAnalysis_Routing(t *testing.T) {
	is := assert.New(t)
	handler := NewAnalysisHandler(nil, nil)

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/analysis", nil)
		w := httptest.NewRecorder()
		handler.HandleAnalysis(w, req)
		is.Equal(http.StatusMethodNotAllowed, w.Code)
	})

	t.Run("missing action", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/analysis", nil)
		w := httptest.NewRecorder()
		handler.HandleAnalysis(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
		is.Contains(w.Body.String(), "invalid or missing action")
	})
}

func TestAnalysisHandler_HandleTrends_OpenAlexStatus(t *testing.T) {
	is := assert.New(t)
	handler := NewAnalysisHandler(nil, nil)

	t.Run("openalex bad status", func(t *testing.T) {
		handler.SetHTTPClient(&http.Client{
			Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("error")),
				}, nil
			}),
		})
		body, _ := json.Marshal(map[string]any{"query": "test"})
		req := httptest.NewRequest("POST", "/analysis/trends", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.handleTrends(w, req)
		is.Equal(http.StatusBadGateway, w.Code)
		is.Contains(w.Body.String(), "openalex returned unexpected status")
	})

	t.Run("openalex invalid response json", func(t *testing.T) {
		handler.SetHTTPClient(&http.Client{
			Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("not json")),
				}, nil
			}),
		})
		body, _ := json.Marshal(map[string]any{"query": "test"})
		req := httptest.NewRequest("POST", "/analysis/trends", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.handleTrends(w, req)
		is.Equal(http.StatusBadGateway, w.Code)
		is.Contains(w.Body.String(), "failed to decode openalex response")
	})
}

func TestAnalysisHandler_HandleTrends_Success(t *testing.T) {
	is := assert.New(t)
	handler := NewAnalysisHandler(nil, nil)

	handler.SetHTTPClient(&http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			resp := struct {
				GroupBy []struct {
					Key   string `json:"key"`
					Count int    `json:"count"`
				} `json:"group_by"`
			}{
				GroupBy: []struct {
					Key   string `json:"key"`
					Count int    `json:"count"`
				}{
					{Key: "2020", Count: 10},
					{Key: "2021", Count: 20},
				},
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		}),
	})

	body, _ := json.Marshal(map[string]any{
		"query":     "test",
		"yearStart": 2020,
		"yearEnd":   2021,
	})
	req := httptest.NewRequest("POST", "/analysis/trends", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.handleTrends(w, req)

	is.Equal(http.StatusOK, w.Code)
	var res map[string]any
	json.NewDecoder(w.Body).Decode(&res)
	is.Equal("test", res["query"])
	is.Len(res["trends"], 2)
}

func TestAnalysisHandler_HandleAnalysis_SuccessRouting(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewAnalysisHandler(mockLLM, nil)

	// Mock for methodology and gaps
	mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(
		&llmv1.StructuredResponse{JsonResult: `{"methodology":"llm result","studyDesign":"observational","keyVariables":["x"]}`}, nil)
	mockLLM.On("Generate", mock.Anything, mock.Anything).Return(
		&llmv1.GenerateResponse{Text: "llm result"}, nil)

	handler.SetHTTPClient(&http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"group_by": []}`)),
			}, nil
		}),
	})

	t.Run("route to methodology", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"title": "t", "abstract": "a"})
		req := httptest.NewRequest("POST", "/analysis?action=methodology", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.HandleAnalysis(w, req)
		is.Equal(http.StatusOK, w.Code)
	})

	t.Run("route to gaps", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"query": "q", "context": "c"})
		req := httptest.NewRequest("POST", "/analysis?action=gaps", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.HandleAnalysis(w, req)
		is.Equal(http.StatusOK, w.Code)
	})

	t.Run("route to trends", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"query": "q"})
		req := httptest.NewRequest("POST", "/analysis?action=trends", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.HandleAnalysis(w, req)
		is.Equal(http.StatusOK, w.Code)
	})
}

func TestAnalysisHandler_HandleGaps_Context(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewAnalysisHandler(mockLLM, nil)

	t.Run("success with context string", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"topic":   "test",
			"context": "some existing research context",
		})
		req := httptest.NewRequest("POST", "/analysis/gaps", bytes.NewReader(body))
		w := httptest.NewRecorder()

		mockLLM.On("Generate", mock.Anything, mock.Anything).Return(
			&llmv1.GenerateResponse{Text: "gaps from context"}, nil).Once()

		handler.handleGaps(w, req)
		is.Equal(http.StatusOK, w.Code)
		is.Contains(w.Body.String(), "gaps from context")
	})
}
