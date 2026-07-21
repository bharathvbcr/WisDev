package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

func newResearchHypothesisRequest(action string, body any) *http.Request {
	raw, _ := json.Marshal(body)
	target := "/wisdev/research/hypothesis"
	if action != "" {
		target += "?action=" + action
	}
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	return req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-pro"))
}

func decodeResearchHypothesisResult(t *testing.T, w *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var payload struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	return payload.Result
}

func TestResearchHypothesisHandler_MethodAndAction(t *testing.T) {
	is := assert.New(t)
	handler := NewResearchHypothesisHandler(&mockGenerateClient{}, nil)

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/research/hypothesis?action=analyze-topic", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-pro"))
		w := httptest.NewRecorder()
		handler.HandleResearchHypothesis(w, req)
		is.Equal(http.StatusMethodNotAllowed, w.Code)
	})

	t.Run("missing action", func(t *testing.T) {
		w := httptest.NewRecorder()
		handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("", map[string]any{}))
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("unknown action", func(t *testing.T) {
		w := httptest.NewRecorder()
		handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("bogus", map[string]any{}))
		is.Equal(http.StatusBadRequest, w.Code)
	})
}

func TestResearchHypothesisHandler_AnalyzeTopic(t *testing.T) {
	is := assert.New(t)

	t.Run("nil client is unavailable", func(t *testing.T) {
		handler := NewResearchHypothesisHandler(nil, nil)
		w := httptest.NewRecorder()
		handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("analyze-topic", map[string]any{"topic": "x"}))
		is.Equal(http.StatusServiceUnavailable, w.Code)
	})

	t.Run("empty topic rejected", func(t *testing.T) {
		handler := NewResearchHypothesisHandler(&mockGenerateClient{}, nil)
		w := httptest.NewRecorder()
		handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("analyze-topic", map[string]any{"topic": "   "}))
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("authors light-tier prompt and schema", func(t *testing.T) {
		mockLLM := &mockGenerateClient{}
		handler := NewResearchHypothesisHandler(mockLLM, nil)
		mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assert.Contains(t, req.GetPrompt(), "Analyze this research topic")
			assert.Contains(t, req.GetPrompt(), `TOPIC: "stress and memory"`)
			assert.NotContains(t, req.GetPrompt(), "Return JSON")
			assert.Contains(t, req.GetJsonSchema(), `"relatedConstructs"`)
			assert.Equal(t, llm.ResolveLightModel(), req.GetModel())
			return true
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"field":"Psychology","concepts":["stress"],"potentialIVs":["sleep"],"potentialDVs":["memory"],"relatedConstructs":["resilience"]}`}, nil).Once()

		w := httptest.NewRecorder()
		handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("analyze-topic", map[string]any{"topic": "stress and memory"}))
		is.Equal(http.StatusOK, w.Code)
		result := decodeResearchHypothesisResult(t, w)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(result, &parsed))
		is.Equal("Psychology", parsed["field"])
		mockLLM.AssertExpectations(t)
	})

	t.Run("invalid structured output surfaces bad gateway", func(t *testing.T) {
		mockLLM := &mockGenerateClient{}
		handler := NewResearchHypothesisHandler(mockLLM, nil)
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).
			Return(&llmv1.StructuredResponse{JsonResult: "not-json"}, nil).Once()
		w := httptest.NewRecorder()
		handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("analyze-topic", map[string]any{"topic": "x"}))
		is.Equal(http.StatusBadGateway, w.Code)
	})

	t.Run("llm error surfaces bad gateway", func(t *testing.T) {
		mockLLM := &mockGenerateClient{}
		handler := NewResearchHypothesisHandler(mockLLM, nil)
		mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(nil, assert.AnError).Once()
		w := httptest.NewRecorder()
		handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("analyze-topic", map[string]any{"topic": "x"}))
		is.Equal(http.StatusBadGateway, w.Code)
	})
}

func TestResearchHypothesisHandler_GenerateQueries(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assert.Contains(t, req.GetPrompt(), "Generate 4-6 search queries")
		assert.Contains(t, req.GetPrompt(), "FIELD: Psychology")
		assert.Contains(t, req.GetPrompt(), "KEY CONCEPTS: sleep, memory")
		assert.Contains(t, req.GetPrompt(), "POTENTIAL VARIABLES: sleep quality, memory recall")
		assert.Contains(t, req.GetJsonSchema(), `"type":"array"`)
		assert.Equal(t, llm.ResolveLightModel(), req.GetModel())
		return true
	})).Return(&llmv1.StructuredResponse{JsonResult: `["sleep memory study"]`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("generate-queries", map[string]any{
		"topic":        "sleep and memory",
		"field":        "Psychology",
		"concepts":     []string{"sleep", "memory"},
		"potentialIVs": []string{"sleep quality"},
		"potentialDVs": []string{"memory recall"},
	}))
	is.Equal(http.StatusOK, w.Code)
	is.JSONEq(`["sleep memory study"]`, string(decodeResearchHypothesisResult(t, w)))
	mockLLM.AssertExpectations(t)
}

func TestResearchHypothesisHandler_AnalyzeLiterature(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assert.Contains(t, req.GetPrompt(), "Analyze this collection of papers")
		assert.Contains(t, req.GetPrompt(), `1. "First paper" (2021)`)
		// Missing date falls back to n.d., matching the client formatter.
		assert.Contains(t, req.GetPrompt(), `2. "Second paper" (n.d.)`)
		assert.Contains(t, req.GetJsonSchema(), `"methodologies"`)
		assert.Equal(t, llm.ResolveLightModel(), req.GetModel())
		return true
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"summary":"s","gaps":[],"methodologies":[]}`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("analyze-literature", map[string]any{
		"topic": "sleep",
		"field": "Psychology",
		"papers": []map[string]any{
			{"title": "First paper", "date": "2021"},
			{"title": "Second paper", "date": ""},
		},
	}))
	is.Equal(http.StatusOK, w.Code)
	mockLLM.AssertExpectations(t)
}

func TestResearchHypothesisHandler_Formulate(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assert.Contains(t, req.GetPrompt(), "Generate 2 testable hypotheses")
		assert.Contains(t, req.GetPrompt(), "LITERATURE SUMMARY: Sleep affects recall.")
		assert.Contains(t, req.GetPrompt(), "[0] Paper one")
		assert.Contains(t, req.GetPrompt(), "[1] Paper two")
		assert.Contains(t, req.GetJsonSchema(), `"hypotheses"`)
		assert.Contains(t, req.GetJsonSchema(), `"literatureSupport"`)
		// Heavy tier for the generation stage.
		assert.Equal(t, llm.ResolveHeavyModel(), req.GetModel())
		return true
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"hypotheses":[]}`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("formulate", map[string]any{
		"topic":   "sleep and memory",
		"field":   "Psychology",
		"summary": "Sleep affects recall.",
		"count":   2,
		"papers": []map[string]any{
			{"id": "p1", "title": "Paper one"},
			{"id": "p2", "title": "Paper two"},
		},
	}))
	is.Equal(http.StatusOK, w.Code)
	mockLLM.AssertExpectations(t)
}

func TestResearchHypothesisHandler_Refine(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assert.Contains(t, req.GetPrompt(), "Refine this research hypothesis")
		assert.Contains(t, req.GetPrompt(), `ORIGINAL: "Sleep improves recall."`)
		assert.Contains(t, req.GetPrompt(), `FEEDBACK: "Make it more specific."`)
		// Refined schema is permissive (required is empty) and carries object-shaped
		// literatureSupport items.
		assert.Contains(t, req.GetJsonSchema(), `"required":[]`)
		assert.Contains(t, req.GetJsonSchema(), `"paperId"`)
		assert.Equal(t, llm.ResolveHeavyModel(), req.GetModel())
		return true
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"statement":"Improved sleep predicts recall.","novelty":"high"}`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("refine", map[string]any{
		"statement": "Sleep improves recall.",
		"feedback":  "Make it more specific.",
	}))
	is.Equal(http.StatusOK, w.Code)
	result := decodeResearchHypothesisResult(t, w)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(result, &parsed))
	is.Equal("high", parsed["novelty"])
	mockLLM.AssertExpectations(t)
}

func TestResearchHypothesisHandler_ParseClaims(t *testing.T) {
	is := assert.New(t)

	t.Run("empty hypothesis rejected", func(t *testing.T) {
		handler := NewResearchHypothesisHandler(&mockGenerateClient{}, nil)
		w := httptest.NewRecorder()
		handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("parse-claims", map[string]any{"hypothesis": "  "}))
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("authors heavy-tier claims prompt", func(t *testing.T) {
		mockLLM := &mockGenerateClient{}
		handler := NewResearchHypothesisHandler(mockLLM, nil)
		mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			assert.Contains(t, req.GetPrompt(), "Parse this research hypothesis")
			assert.Contains(t, req.GetPrompt(), `Hypothesis: "RLHF improves alignment"`)
			assert.Contains(t, req.GetJsonSchema(), `"claims"`)
			assert.Equal(t, llm.ResolveHeavyModel(), req.GetModel())
			return true
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"claims":["Claim 1"]}`}, nil).Once()

		w := httptest.NewRecorder()
		handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("parse-claims", map[string]any{
			"hypothesis": "RLHF improves alignment",
		}))
		is.Equal(http.StatusOK, w.Code)
		is.JSONEq(`{"claims":["Claim 1"]}`, string(decodeResearchHypothesisResult(t, w)))
		mockLLM.AssertExpectations(t)
	})
}

func TestResearchHypothesisHandler_TesterQueries(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assert.Contains(t, req.GetPrompt(), "Generate search queries to find research papers testing this hypothesis")
		assert.Contains(t, req.GetPrompt(), "Claims: Claim 1; Claim 2")
		assert.Contains(t, req.GetJsonSchema(), `"queries"`)
		assert.Equal(t, llm.ResolveHeavyModel(), req.GetModel())
		return true
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"queries":["q1","q2"]}`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("tester-queries", map[string]any{
		"hypothesis": "RLHF improves alignment",
		"claims":     []string{"Claim 1", "Claim 2"},
	}))
	is.Equal(http.StatusOK, w.Code)
	is.JSONEq(`{"queries":["q1","q2"]}`, string(decodeResearchHypothesisResult(t, w)))
	mockLLM.AssertExpectations(t)
}

func TestResearchHypothesisHandler_AnalyzeEvidence(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assert.Contains(t, req.GetPrompt(), `Analyze how these papers relate to the hypothesis: "H"`)
		assert.Contains(t, req.GetPrompt(), `[1] "Paper A"`)
		assert.Contains(t, req.GetPrompt(), "Abstract: Summary text")
		assert.Contains(t, req.GetJsonSchema(), `"stance"`)
		assert.Equal(t, llm.ResolveHeavyModel(), req.GetModel())
		return true
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"papers":[{"index":1,"stance":"supporting","relevance":0.9}]}`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("analyze-evidence", map[string]any{
		"hypothesis": "H",
		"papers": []map[string]any{
			{"title": "Paper A", "summary": "Summary text", "abstract": "Abstract text"},
		},
	}))
	is.Equal(http.StatusOK, w.Code)
	mockLLM.AssertExpectations(t)
}

func TestResearchHypothesisHandler_Assess(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assert.Contains(t, req.GetPrompt(), "statistical analysis expert")
		assert.Contains(t, req.GetPrompt(), "Assess the overall evidence")
		assert.Contains(t, req.GetPrompt(), "Supporting evidence (1 papers):")
		assert.Contains(t, req.GetPrompt(), "- Paper A: Supports")
		assert.Contains(t, req.GetJsonSchema(), `"verdict"`)
		assert.Equal(t, llm.ResolveHeavyModel(), req.GetModel())
		return true
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"verdict":"inconclusive","confidence":0.5,"summary":"Mixed.","caveats":[]}`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("assess", map[string]any{
		"hypothesis": "H",
		"supporting": []map[string]any{
			{"title": "Paper A", "reasoning": "Supports"},
		},
		"contradicting": []map[string]any{},
	}))
	is.Equal(http.StatusOK, w.Code)
	mockLLM.AssertExpectations(t)
}

func TestResearchHypothesisHandler_SuggestExperiments(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		assert.Contains(t, req.GetPrompt(), "suggest experiments to further test it")
		assert.Contains(t, req.GetPrompt(), "significant gaps in the literature")
		assert.Contains(t, req.GetJsonSchema(), `"experiments"`)
		assert.Equal(t, llm.ResolveHeavyModel(), req.GetModel())
		return true
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"experiments":["Run RCT"]}`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("suggest-experiments", map[string]any{
		"hypothesis": "H",
		"hasGaps":    true,
	}))
	is.Equal(http.StatusOK, w.Code)
	is.JSONEq(`{"experiments":["Run RCT"]}`, string(decodeResearchHypothesisResult(t, w)))
	mockLLM.AssertExpectations(t)
}

func TestResearchHypothesisHandler_Test_Success(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)
	handler.searchFn = func(ctx context.Context, query string, limit int) ([]hypothesisPaperLite, error) {
		if limit != 15 {
			t.Errorf("expected search limit 15, got %d", limit)
		}
		return []hypothesisPaperLite{{
			Title:    "Evidence paper",
			Abstract: "Supports the claim.",
			Link:     "https://example.com/evidence-paper",
			DOI:      "10.1000/evidence",
			PaperID:  "p1",
		}}, nil
	}

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Parse this research hypothesis")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"claims":["Claim 1"]}`}, nil).Once()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Generate search queries to find research papers testing this hypothesis")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"queries":["q1","q2","q3"]}`}, nil).Once()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Analyze how these papers relate to the hypothesis")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"papers":[{"index":1,"stance":"supporting","relevance":0.9,"quote":"Supports","reasoning":"Matched"}]}`}, nil).Once()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Assess the overall evidence")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"verdict":"partially_supported","confidence":0.7,"summary":"Partial support.","caveats":["Limited sample"]}`}, nil).Once()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "suggest experiments to further test it")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"experiments":["Run RCT"]}`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("test", map[string]any{
		"hypothesis": "RLHF improves alignment",
	}))
	is.Equal(http.StatusOK, w.Code)

	result := decodeResearchHypothesisResult(t, w)
	var parsed hypothesisTestResult
	require.NoError(t, json.Unmarshal(result, &parsed))
	is.Equal("RLHF improves alignment", parsed.Hypothesis)
	is.Equal([]string{"Claim 1"}, parsed.ParsedClaims)
	require.Len(t, parsed.SupportingEvidence, 1)
	is.Equal("Evidence paper", parsed.SupportingEvidence[0].Paper.Title)
	is.Equal("partially_supported", parsed.OverallAssessment.Verdict)
	is.Equal([]string{"Run RCT"}, parsed.SuggestedExperiments)
	mockLLM.AssertExpectations(t)
}

func TestResearchHypothesisHandler_Test_SearchFailure(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)
	handler.searchFn = func(ctx context.Context, query string, limit int) ([]hypothesisPaperLite, error) {
		return nil, assert.AnError
	}

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Parse this research hypothesis")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"claims":["Claim 1"]}`}, nil).Once()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Generate search queries to find research papers testing this hypothesis")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"queries":["q1","q2"]}`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("test", map[string]any{
		"hypothesis": "RLHF improves alignment",
	}))
	is.Equal(http.StatusServiceUnavailable, w.Code)

	var payload APIError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	is.Equal(ErrServiceUnavailable, payload.Error.Code)
	is.Equal(hypothesisEvidenceSearchUnavailableMsg, payload.Error.Message)
	mockLLM.AssertExpectations(t)
}

func TestResearchHypothesisHandler_UnknownActionIncludesTest(t *testing.T) {
	is := assert.New(t)
	handler := NewResearchHypothesisHandler(&mockGenerateClient{}, nil)
	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("bogus", map[string]any{}))
	is.Equal(http.StatusBadRequest, w.Code)

	var payload APIError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	actions, ok := payload.Error.Details["allowedActions"].([]any)
	require.True(t, ok)
	foundTest, foundGenerate := false, false
	for _, a := range actions {
		if a == "test" {
			foundTest = true
		}
		if a == "generate" {
			foundGenerate = true
		}
	}
	is.True(foundTest, "allowedActions should include test")
	is.True(foundGenerate, "allowedActions should include generate")
}

func TestResearchHypothesisHandler_Generate(t *testing.T) {
	is := assert.New(t)
	mockLLM := &mockGenerateClient{}
	handler := NewResearchHypothesisHandler(mockLLM, nil)
	handler.searchFn = func(ctx context.Context, query string, limit int) ([]hypothesisPaperLite, error) {
		return []hypothesisPaperLite{{
			Title:   "Evidence paper",
			DOI:     "10.1/evidence",
			PaperID: "p1",
			Link:    "https://example.com/p1",
		}}, nil
	}

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Analyze this research topic")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"field":"Psychology","concepts":["stress"],"potentialIVs":["sleep"],"potentialDVs":["memory"],"relatedConstructs":["resilience"]}`}, nil).Once()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Generate 4-6 search queries")
	})).Return(&llmv1.StructuredResponse{JsonResult: `["stress sleep"]`}, nil).Once()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Analyze this collection of papers")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"summary":"Active field.","gaps":["Need RCTs"],"methodologies":["survey"]}`}, nil).Once()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.GetPrompt(), "Generate") && strings.Contains(req.GetPrompt(), "testable hypotheses")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"hypotheses":[{"statement":"Sleep improves memory","independentVariable":"sleep","dependentVariable":"memory","suggestedMethodology":{"studyType":"experimental","sampleDescription":"adults","dataCollectionMethod":"lab","analysisApproach":"ANOVA"},"relevanceScore":80,"literatureSupport":[0],"testability":"high","novelty":"medium"}]}`}, nil).Once()

	w := httptest.NewRecorder()
	handler.HandleResearchHypothesis(w, newResearchHypothesisRequest("generate", map[string]any{
		"topic": "stress and memory",
		"count": 1,
	}))
	is.Equal(http.StatusOK, w.Code)
	is.Contains(w.Body.String(), `"hypothesisGeneration"`)
	is.Contains(w.Body.String(), "Sleep improves memory")
	is.Contains(w.Body.String(), `"sourcePapersCount":1`)
	mockLLM.AssertExpectations(t)
}
