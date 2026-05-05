package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

type mockGenerateClient struct {
	mock.Mock
}

func (m *mockGenerateClient) Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.GenerateResponse), args.Error(1)
}

func (m *mockGenerateClient) StructuredOutput(ctx context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.StructuredResponse), args.Error(1)
}

func TestAnalysisHandler_HandleTrends_Errors(t *testing.T) {
	is := assert.New(t)
	handler := NewAnalysisHandler(nil, nil)

	t.Run("invalid json", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/analysis/trends", bytes.NewReader([]byte("not json")))
		w := httptest.NewRecorder()
		handler.handleTrends(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("empty query", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"query": ""})
		req := httptest.NewRequest("POST", "/analysis/trends", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.handleTrends(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("openalex network error", func(t *testing.T) {
		handler.SetHTTPClient(&http.Client{
			Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return nil, assert.AnError
			}),
		})
		body, _ := json.Marshal(map[string]any{"query": "test"})
		req := httptest.NewRequest("POST", "/analysis/trends", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.handleTrends(w, req)
		is.Equal(http.StatusBadGateway, w.Code)
	})
}

func TestAnalysisHandler_HandleMethodology(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewAnalysisHandler(mockLLM, nil)

	t.Run("invalid request body", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/analysis/methodology", bytes.NewReader([]byte("invalid")))
		w := httptest.NewRecorder()
		handler.handleMethodology(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("success", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"title": "test", "abstract": "abc"})
		req := httptest.NewRequest("POST", "/analysis/methodology", bytes.NewReader(body))
		w := httptest.NewRecorder()

		mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assert.Contains(t, req.GetPrompt(), structuredOutputSchemaInstruction)
			assert.NotContains(t, req.GetPrompt(), "structured JSON block")
			return req.GetModel() != ""
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"methodology":"qualitative interviews","studyDesign":"cross-sectional","keyVariables":["trust","adoption"]}`}, nil).Once()
		handler.handleMethodology(w, req)
		is.Equal(http.StatusOK, w.Code)
	})

	t.Run("llm error", func(t *testing.T) {
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, assert.AnError).Once()
		body, _ := json.Marshal(map[string]any{"title": "t", "abstract": "a"})
		req := httptest.NewRequest("POST", "/analysis/methodology", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.handleMethodology(w, req)
		is.Equal(http.StatusBadGateway, w.Code)
	})
}

func TestAnalysisHandler_HandleGaps(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewAnalysisHandler(mockLLM, nil)

	t.Run("success", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"topic": "test",
			"papers": []map[string]any{
				{"title": "T1", "abstract": "A1"},
			},
		})
		req := httptest.NewRequest("POST", "/analysis/gaps", bytes.NewReader(body))
		w := httptest.NewRecorder()

		mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return req != nil &&
				req.RequestClass == "standard" &&
				req.RetryProfile == "standard" &&
				req.ServiceTier == "standard" &&
				req.GetThinkingBudget() == 1024 &&
				req.GetLatencyBudgetMs() > 0
		})).Return(&llmv1.GenerateResponse{Text: " gaps "}, nil).Once()
		handler.handleGaps(w, req)
		is.Equal(http.StatusOK, w.Code)
	})

	t.Run("invalid body", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/analysis/gaps", bytes.NewReader([]byte("invalid")))
		w := httptest.NewRecorder()
		handler.handleGaps(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("llm unavailable", func(t *testing.T) {
		emptyHandler := NewAnalysisHandler(nil, nil)
		body, _ := json.Marshal(map[string]any{"context": "c", "topic": "t"})
		req := httptest.NewRequest("POST", "/analysis/gaps", bytes.NewReader(body))
		w := httptest.NewRecorder()
		emptyHandler.handleGaps(w, req)
		is.Equal(http.StatusServiceUnavailable, w.Code)
	})

	t.Run("missing papers and context", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"topic": "test"})
		req := httptest.NewRequest("POST", "/analysis/gaps", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.handleGaps(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("paper validation failure", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"topic": "test",
			"papers": []map[string]any{
				{"title": "", "abstract": "A1"},
			},
		})
		req := httptest.NewRequest("POST", "/analysis/gaps", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handler.handleGaps(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("empty llm output", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"topic": "test",
			"papers": []map[string]any{
				{"title": "T1", "abstract": "A1"},
			},
		})
		req := httptest.NewRequest("POST", "/analysis/gaps", bytes.NewReader(body))
		w := httptest.NewRecorder()

		mockLLM.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "   "}, nil).Once()
		handler.handleGaps(w, req)
		is.Equal(http.StatusBadGateway, w.Code)
	})
}

func TestAnalysisHandler_HandleTrends_DefaultsAndQueryEncoding(t *testing.T) {
	handler := NewAnalysisHandler(nil, nil)
	t.Setenv("OPENALEX_EMAIL", "")

	var requestedURL string
	handler.SetHTTPClient(&http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			requestedURL = r.URL.String()
			resp := map[string]any{
				"group_by": []map[string]any{
					{"key": "2023", "count": 3},
				},
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(body))),
			}, nil
		}),
	})

	body, _ := json.Marshal(map[string]any{"query": "machine learning"})
	req := httptest.NewRequest("POST", "/analysis/trends", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.handleTrends(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	currentYear := time.Now().Year()
	wantStart := fmt.Sprintf("publication_year:%d-%d", currentYear-10, currentYear)
	if !strings.Contains(requestedURL, url.QueryEscape("machine learning")) {
		t.Fatalf("expected query to be encoded in URL, got %s", requestedURL)
	}
	if !strings.Contains(requestedURL, wantStart) {
		t.Fatalf("expected default publication year range %s in URL, got %s", wantStart, requestedURL)
	}
	if !strings.Contains(requestedURL, url.QueryEscape("scholar.focus.app@gmail.com")) {
		t.Fatalf("expected fallback mailto in URL, got %s", requestedURL)
	}
}

func TestAnalysisHandler_HandleTrends_DeadlineExceededFailsQuickly(t *testing.T) {
	previousTimeout := analysisExternalRequestTimeout
	analysisExternalRequestTimeout = 50 * time.Millisecond
	defer func() { analysisExternalRequestTimeout = previousTimeout }()

	handler := NewAnalysisHandler(nil, nil)
	handler.SetHTTPClient(&http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if _, ok := r.Context().Deadline(); !ok {
				t.Fatal("expected trends fetch to carry a deadline")
			}
			select {
			case <-r.Context().Done():
				if r.Context().Err() != context.DeadlineExceeded {
					t.Fatalf("expected deadline exceeded, got %v", r.Context().Err())
				}
				return nil, r.Context().Err()
			case <-time.After(1 * time.Second):
				t.Fatal("expected trends fetch context cancellation")
				return nil, nil
			}
		}),
	})

	body, _ := json.Marshal(map[string]any{"query": "machine learning"})
	req := httptest.NewRequest("POST", "/analysis/trends", bytes.NewReader(body))
	w := httptest.NewRecorder()

	startedAt := time.Now()
	handler.handleTrends(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("expected trends timeout response within 1s, got %s", elapsed)
	}
}

func TestAnalysisHandler_HandleMethodology_JSONExtraction(t *testing.T) {
	mockLLM := &mockGenerateClient{}
	handler := NewAnalysisHandler(mockLLM, nil)

	mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(
		&llmv1.StructuredResponse{JsonResult: `{"methodology":"mixed methods","studyDesign":"comparative","keyVariables":["sample size","exposure"]}`}, nil).Once()

	req := httptest.NewRequest("POST", "/analysis/methodology", bytes.NewReader([]byte(`{"title":"t","abstract":"a"}`)))
	w := httptest.NewRecorder()

	handler.handleMethodology(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	methodology, ok := resp["methodology"].(map[string]any)
	if !ok {
		t.Fatalf("expected methodology object, got %#v", resp["methodology"])
	}
	if methodology["methodology"] != "mixed methods" {
		t.Fatalf("expected extracted methodology JSON, got %#v", methodology)
	}
	if _, ok := resp["rawAnalysis"]; ok {
		t.Fatalf("methodology response should not expose raw structured output: %#v", resp["rawAnalysis"])
	}
}

func TestAnalysisHandler_HandleMethodology_DeadlineExceededFailsQuickly(t *testing.T) {
	previousTimeout := analysisLLMRequestTimeout
	analysisLLMRequestTimeout = 50 * time.Millisecond
	defer func() { analysisLLMRequestTimeout = previousTimeout }()

	mockLLM := &mockGenerateClient{}
	handler := NewAnalysisHandler(mockLLM, nil)

	mockLLM.
		On("StructuredOutput", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			ctx, ok := args.Get(0).(context.Context)
			if !ok {
				t.Fatalf("expected context argument, got %T", args.Get(0))
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected methodology call to carry a deadline")
			}
			select {
			case <-ctx.Done():
				if ctx.Err() != context.DeadlineExceeded {
					t.Fatalf("expected deadline exceeded, got %v", ctx.Err())
				}
			case <-time.After(1 * time.Second):
				t.Fatal("expected methodology context cancellation")
			}
		}).
		Return(nil, context.DeadlineExceeded).
		Once()

	req := httptest.NewRequest("POST", "/analysis/methodology", bytes.NewReader([]byte(`{"title":"t","abstract":"a"}`)))
	w := httptest.NewRecorder()

	startedAt := time.Now()
	handler.handleMethodology(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("expected methodology timeout fallback within 1s, got %s", elapsed)
	}
}

func TestAnalysisHandler_HandleMethodology_BypassesSlowVertexDirectAndUsesSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	var captured structuredRequestCapture
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"methodology":"mixed methods","studyDesign":"comparative","keyVariables":["sample size","exposure"]}`,
			"modelUsed":  "test-analysis-methodology-sidecar",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	client := llm.NewClientWithTimeout(500 * time.Millisecond)
	setUnexportedField(t, client, "transport", "http-json")
	setUnexportedField(t, client, "httpBaseURL", llmServer.URL)

	slowDirect := &slowVertexModelsClient{}
	vertexClient := &llm.VertexClient{}
	setUnexportedField(t, vertexClient, "client", slowDirect)
	setUnexportedField(t, vertexClient, "backend", "vertex_ai")
	client.VertexDirect = vertexClient

	handler := NewAnalysisHandler(client, nil)
	req := httptest.NewRequest("POST", "/analysis?action=methodology", bytes.NewReader([]byte(`{"title":"t","abstract":"a"}`)))
	w := httptest.NewRecorder()

	startedAt := time.Now()
	handler.handleMethodology(w, req)
	elapsed := time.Since(startedAt)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	methodology, ok := resp["methodology"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "mixed methods", methodology["methodology"])
	assert.Less(t, elapsed, time.Second)
	assert.False(t, slowDirect.called.Load())
	assert.Equal(t, llm.ResolveStandardModel(), captured.Model)
	assert.Equal(t, "structured_high_value", captured.RequestClass)
	assert.Equal(t, "standard", captured.RetryProfile)
	assert.Equal(t, "priority", captured.ServiceTier)
	assert.Greater(t, captured.LatencyBudgetMs, int32(0))
	assertStructuredPromptHygiene(t, captured.Prompt)
}

func TestAnalysisHandler_HandleGaps_DeadlineExceededFailsQuickly(t *testing.T) {
	previousTimeout := analysisLLMRequestTimeout
	analysisLLMRequestTimeout = 50 * time.Millisecond
	defer func() { analysisLLMRequestTimeout = previousTimeout }()

	mockLLM := &mockGenerateClient{}
	handler := NewAnalysisHandler(mockLLM, nil)

	mockLLM.
		On("Generate", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			ctx, ok := args.Get(0).(context.Context)
			if !ok {
				t.Fatalf("expected context argument, got %T", args.Get(0))
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected gaps call to carry a deadline")
			}
			select {
			case <-ctx.Done():
				if ctx.Err() != context.DeadlineExceeded {
					t.Fatalf("expected deadline exceeded, got %v", ctx.Err())
				}
			case <-time.After(1 * time.Second):
				t.Fatal("expected gaps context cancellation")
			}
		}).
		Return(nil, context.DeadlineExceeded).
		Once()

	req := httptest.NewRequest("POST", "/analysis/gaps", bytes.NewReader([]byte(`{"topic":"test","papers":[{"title":"T1","abstract":"A1"}]}`)))
	w := httptest.NewRecorder()

	startedAt := time.Now()
	handler.handleGaps(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("expected gaps timeout fallback within 1s, got %s", elapsed)
	}
}
