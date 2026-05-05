package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockLLMRequester struct {
	mock.Mock
}

func (m *mockLLMRequester) Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.GenerateResponse), args.Error(1)
}

func (m *mockLLMRequester) StructuredOutput(ctx context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*llmv1.StructuredResponse), args.Error(1)
}

func TestWisDevHandler_Initialize(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev_handler_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sessions := wisdev.NewSessionManager(tempDir)
	h := NewWisDevHandler(sessions, wisdev.NewGuidedFlow(), nil, nil, nil, nil, nil)

	reqBody := `{"userId": "u1", "query": "test query"}`
	req := httptest.NewRequest(http.MethodPost, "/wisdev/initialize", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleInitialize(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var session wisdev.Session
	err = json.NewDecoder(w.Body).Decode(&session)
	assert.NoError(t, err)
	assert.Equal(t, "u1", session.UserID)
	assert.Equal(t, "test query", session.OriginalQuery)
}

func TestWisDevHandler_GetSession(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev_handler_get_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sessions := wisdev.NewSessionManager(tempDir)
	session, _ := sessions.CreateSession(context.Background(), "u1", "query")

	h := NewWisDevHandler(sessions, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/wisdev/session?sessionId="+session.ID, nil)
	w := httptest.NewRecorder()

	h.HandleGetSession(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var loaded wisdev.Session
	json.NewDecoder(w.Body).Decode(&loaded)
	assert.Equal(t, session.ID, loaded.ID)
}

func TestWisDevHandler_AnalyzeQuery(t *testing.T) {
	h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)

	reqBody := `{"query": " quantum gravity in string theory ","traceId":"trace-analyze-1"}`
	req := httptest.NewRequest(http.MethodPost, "/wisdev/analyze", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleAnalyzeQuery(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "trace-analyze-1", w.Header().Get("X-Trace-Id"))
	var resp map[string]any
	assert.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "broad_topic", resp["intent"])
	assert.Contains(t, resp["entities"], "quantum")
	assert.Equal(t, "trace-analyze-1", resp["traceId"])
	assert.Equal(t, "quantum gravity in string theory", resp["queryUsed"])
	assert.Equal(t, false, resp["cache_hit"])
	assert.EqualValues(t, 4, resp["suggested_question_count"])
}

func TestWisDevHandler_ProcessAnswer(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev_handler_answer_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sessions := wisdev.NewSessionManager(tempDir)
	guided := wisdev.NewGuidedFlow()
	h := NewWisDevHandler(sessions, guided, nil, nil, nil, nil, nil)

	session, _ := sessions.CreateSession(context.Background(), "u1", "machine learning")
	q, _ := guided.GetNextQuestion(session)

	reqBody, _ := json.Marshal(map[string]any{
		"sessionId":  session.ID,
		"questionId": q.ID,
		"values":     []string{"value1"},
	})
	req := httptest.NewRequest(http.MethodPost, "/wisdev/answer", bytes.NewBuffer(reqBody))
	w := httptest.NewRecorder()

	h.HandleProcessAnswer(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var updated wisdev.Session
	json.NewDecoder(w.Body).Decode(&updated)
	assert.Equal(t, 1, updated.CurrentQuestionIndex)
	assert.Equal(t, []string{"value1"}, updated.Answers[q.ID].Values)
}

func TestWisDevHandler_NextQuestion(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev_handler_next_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sessions := wisdev.NewSessionManager(tempDir)
	guided := wisdev.NewGuidedFlow()
	h := NewWisDevHandler(sessions, guided, nil, nil, nil, nil, nil)

	session, _ := sessions.CreateSession(context.Background(), "u1", "query")

	req := httptest.NewRequest(http.MethodGet, "/wisdev/next?sessionId="+session.ID, nil)
	w := httptest.NewRecorder()

	h.HandleNextQuestion(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var question wisdev.Question
	err = json.NewDecoder(w.Body).Decode(&question)
	assert.NoError(t, err)
	assert.NotEmpty(t, question.ID)
}

func TestWisDevHandler_CompleteSession(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev_handler_complete_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sessions := wisdev.NewSessionManager(tempDir)
	h := NewWisDevHandler(sessions, nil, nil, nil, nil, nil, nil)

	session, _ := sessions.CreateSession(context.Background(), "u1", "query")

	req := httptest.NewRequest(http.MethodPost, "/wisdev/complete?sessionId="+session.ID, nil)
	w := httptest.NewRecorder()

	h.HandleCompleteSession(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "complete", resp["status"])

	// Verify session status in store
	updated, _ := sessions.GetSession(context.Background(), session.ID)
	assert.Equal(t, wisdev.StatusComplete, updated.Status)
}

func TestWisDevHandler_QuestionOptions(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev_handler_options_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sessions := wisdev.NewSessionManager(tempDir)
	guided := wisdev.NewGuidedFlow()
	h := NewWisDevHandler(sessions, guided, nil, nil, nil, nil, nil)

	session, _ := sessions.CreateSession(context.Background(), "u1", "query")
	q, _ := guided.GetNextQuestion(session)

	req := httptest.NewRequest(http.MethodGet, "/wisdev/options?sessionId="+session.ID+"&questionId="+q.ID, nil)
	w := httptest.NewRecorder()

	h.HandleQuestionOptions(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, q.ID, resp["questionId"])
	assert.NotNil(t, resp["options"])
}

func TestWisDevHandler_QuestionRecommendations(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev_handler_recs_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sessions := wisdev.NewSessionManager(tempDir)
	guided := wisdev.NewGuidedFlow()
	h := NewWisDevHandler(sessions, guided, nil, nil, nil, nil, nil)

	session, _ := sessions.CreateSession(context.Background(), "u1", "query")
	q, _ := guided.GetNextQuestion(session)

	req := httptest.NewRequest(http.MethodGet, "/wisdev/recommendations?sessionId="+session.ID+"&questionId="+q.ID, nil)
	w := httptest.NewRecorder()

	h.HandleQuestionRecommendations(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, q.ID, resp["questionId"])
	assert.NotNil(t, resp["values"])
}

func TestWisDevHandler_RegenerateQuestionOptions(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev_handler_regen_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sessions := wisdev.NewSessionManager(tempDir)
	guided := wisdev.NewGuidedFlow()
	h := NewWisDevHandler(sessions, guided, nil, nil, nil, nil, nil)

	session, _ := sessions.CreateSession(context.Background(), "u1", "machine learning")
	// Setup session state so it can look up options
	session.DetectedDomain = "cs"
	session.QuestionSequence = []string{"q1_domain"}
	sessions.SaveSession(context.Background(), session)

	reqBody, _ := json.Marshal(map[string]any{
		"sessionId":  session.ID,
		"questionId": "q1_domain",
	})
	req := httptest.NewRequest(http.MethodPost, "/wisdev/regenerate-options", bytes.NewBuffer(reqBody))
	w := httptest.NewRecorder()

	h.HandleRegenerateQuestionOptions(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "q1_domain", resp["questionId"])
	assert.NotNil(t, resp["options"])
}

func TestWisDevHandler_DecomposeTask(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	llmClient := llm.NewClient()
	llmClient.SetClient(mockLLM)
	brainCaps := wisdev.NewBrainCapabilities(llmClient)
	h := NewWisDevHandler(nil, nil, nil, nil, brainCaps, nil, nil)

	reqBody := `{"query": "test query", "domain": "science"}`
	req := httptest.NewRequest(http.MethodPost, "/wisdev/decompose", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	expectedTasks := []wisdev.ResearchTask{{ID: "1", Name: "task 1"}}
	jsonResp, _ := json.Marshal(map[string]any{"tasks": expectedTasks})
	mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	h.HandleDecomposeTask(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotNil(t, resp["tasks"])
}

func TestWisDevHandler_ProposeHypotheses(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	llmClient := llm.NewClient()
	llmClient.SetClient(mockLLM)
	brainCaps := wisdev.NewBrainCapabilities(llmClient)
	h := NewWisDevHandler(nil, nil, nil, nil, brainCaps, nil, nil)

	reqBody := `{"query": "test query", "intent": "discovery"}`
	req := httptest.NewRequest(http.MethodPost, "/wisdev/hypotheses", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	expectedHypotheses := []wisdev.Hypothesis{{Claim: "claim 1"}}
	jsonResp, _ := json.Marshal(map[string]any{"hypotheses": expectedHypotheses})
	mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	h.HandleProposeHypotheses(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotNil(t, resp["hypotheses"])
}

func TestWisDevHandler_CoordinateReplan(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	llmClient := llm.NewClient()
	llmClient.SetClient(mockLLM)
	brainCaps := wisdev.NewBrainCapabilities(llmClient)
	h := NewWisDevHandler(nil, nil, nil, nil, brainCaps, nil, nil)

	reqBody := `{"failedStepId": "1", "reason": "fail", "context": {}}`
	req := httptest.NewRequest(http.MethodPost, "/wisdev/replan", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	expectedTasks := []wisdev.ResearchTask{{ID: "retry_1", Name: "retry"}}
	jsonResp, _ := json.Marshal(map[string]any{"tasks": expectedTasks})
	mockLLM.On("StructuredOutput", mock.Anything, mock.Anything).Return(&llmv1.StructuredResponse{JsonResult: string(jsonResp)}, nil)

	h.HandleCoordinateReplan(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotNil(t, resp["tasks"])
}

func TestWisDevHandler_GenerateQueries(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev_handler_queries_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	sessions := wisdev.NewSessionManager(tempDir)
	h := NewWisDevHandler(sessions, nil, nil, nil, nil, nil, nil)

	session, _ := sessions.CreateSession(context.Background(), "u1", "machine learning")
	session.Answers["q4_subtopics"] = wisdev.Answer{Values: []string{"deep learning", "neural networks"}}
	sessions.SaveSession(context.Background(), session)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/generate-queries?sessionId="+session.ID, nil)
	w := httptest.NewRecorder()

	h.HandleGenerateQueries(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	queries := resp["queries"].([]any)
	assert.NotEmpty(t, queries)
}

func TestWisDevHandler_GetTraces(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev_handler_traces_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		Journal: journal,
		Store:   wisdev.NewInMemorySessionStore(),
	}
	h := NewWisDevHandler(nil, nil, nil, gw, nil, nil, nil)
	session, err := gw.CreateSession(context.Background(), "u1", "trace query")
	require.NoError(t, err)

	t.Run("By SessionID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/traces?sessionId="+session.SessionID, nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		w := httptest.NewRecorder()
		h.HandleGetTraces(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("By UserID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/traces?userId=u1", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		w := httptest.NewRecorder()
		h.HandleGetTraces(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("By SessionID denies non-owner", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/traces?sessionId="+session.SessionID, nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u2"))
		w := httptest.NewRecorder()
		h.HandleGetTraces(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("Rejects non-GET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/traces", nil)
		w := httptest.NewRecorder()
		h.HandleGetTraces(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})

	t.Run("Returns empty array without journal", func(t *testing.T) {
		handler := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/wisdev/traces", nil)
		w := httptest.NewRecorder()
		handler.HandleGetTraces(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, "[]", w.Body.String())
	})

	t.Run("Falls back to default limit when invalid", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/traces?userId=u1&limit=abc", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		w := httptest.NewRecorder()
		h.HandleGetTraces(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestWisDevHandler_Paper2Skill(t *testing.T) {
	mockLLM := new(mockLLMRequester)
	// We don't need a full llm.Client for the compiler, it takes LLMRequester
	compiler := wisdev.NewPaper2SkillCompiler(mockLLM)

	// Mock the Python sidecar for PDF extraction
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text": "test paper content", "metadata": {"title": "Test"}}`))
	}))
	defer server.Close()
	compiler.PDFWorkerURL = server.URL
	compiler.RegistryURL = server.URL // Mock registry too

	h := NewWisDevHandler(nil, nil, nil, nil, nil, compiler, nil)

	reqBody := `{"arxiv_id": "2101.12345"}`
	req := httptest.NewRequest(http.MethodPost, "/wisdev/paper2skill", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	// Mock LLM calls inside CompileArxivID
	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return contains(req.Prompt, "Extract the core methodology")
	})).Return(&llmv1.GenerateResponse{Text: "methodology"}, nil)

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return contains(req.Prompt, "Compile the extracted methodology")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"name": "test_skill"}`}, nil)

	h.HandlePaper2Skill(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var respData map[string]any
	json.NewDecoder(w.Body).Decode(&respData)
	assert.Equal(t, "completed", respData["status"])
	assert.Equal(t, "2101.12345", respData["arxiv_id"])
}

func TestWisDevHandler_HandleDeepResearch(t *testing.T) {
	ctx := context.Background()
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	defer func() { runUnifiedResearchLoop = originalRunUnifiedResearchLoop }()
	// Mock canonical retrieval
	originalSearch := wisdev.RetrieveCanonicalPapers
	defer func() { wisdev.RetrieveCanonicalPapers = originalSearch }()

	wisdev.RetrieveCanonicalPapers = func(ctx context.Context, rdb redis.UniversalClient, query string, limit int) ([]wisdev.Source, map[string]any, error) {
		return []wisdev.Source{{ID: "p1", Title: "Paper 1"}}, map[string]any{
			"count":    1,
			"query":    query,
			"traceId":  "wisdev-test-trace",
			"contract": "paperBundle.v1",
		}, nil
	}

	tempDir, err := os.MkdirTemp("", "deep_research")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{Journal: journal, Loop: wisdev.NewAutonomousLoop(nil, nil)}
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		return testResearchLoopResult(req.Query, []string{req.Query}, []search.Paper{{ID: "p1", Title: "Paper 1", Source: "crossref"}}), nil
	}
	h := NewWisDevHandler(nil, nil, nil, gw, nil, nil, nil)

	reqBody := `{"query": "test query", "categories": ["cat1"], "quality_mode": "fast"}`
	req := httptest.NewRequest(http.MethodPost, "/deep-research", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleDeepResearch(w, req.WithContext(ctx))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.True(t, resp["ok"].(bool))
	payload, ok := resp["deepResearch"].(map[string]any)
	require.True(t, ok)
	_, ok = payload["evidenceDossier"].(map[string]any)
	require.True(t, ok)
}

func TestWisDevHandler_HandleDeepResearch_UsesAutonomousLoopWhenAvailable(t *testing.T) {
	ctx := context.Background()
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{
		name: "compat_deep_loop",
		SearchFunc: func(_ context.Context, query string, _ search.SearchOpts) ([]search.Paper, error) {
			assertResearchOrCitationGraphQuery(t, query, "test query")
			return []search.Paper{{ID: "p1", Title: "Loop Paper", Abstract: "Loop abstract", Source: "crossref"}}, nil
		},
	})
	reg.SetDefaultOrder([]string{"compat_deep_loop"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return isAutonomousHypothesisProposalPrompt(req)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"hypotheses":[]}`}, nil).Maybe()
	allowAutonomousCritique(msc)
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Evaluate if the following papers")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Maybe()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Extract the top 2-3")
	})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Maybe()
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Loop synthesis"}, nil).Maybe()

	journal := wisdev.NewRuntimeJournal(nil)
	loop := wisdev.NewAutonomousLoop(reg, lc)
	gw := &wisdev.AgentGateway{Journal: journal, Loop: loop}
	gw.Runtime = wisdev.NewUnifiedResearchRuntime(loop, reg, lc, gw.ProgrammaticLoopExecutor())
	h := NewWisDevHandler(nil, nil, nil, gw, nil, nil, nil)

	reqBody := `{"query": "test query", "categories": ["cat1"], "quality_mode": "balanced"}`
	req := httptest.NewRequest(http.MethodPost, "/deep-research", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleDeepResearch(w, req.WithContext(ctx))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	payload, ok := resp["deepResearch"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "go_canonical_runtime", mapAny(payload["metadata"])["executionPlane"])
	_, ok = payload["reasoningGraph"].(map[string]any)
	require.True(t, ok)
	_, ok = payload["evidenceDossier"].(map[string]any)
	require.True(t, ok)
}

func TestWisDevHandler_HandleDeepResearch_FallsBackToStoredSessionQuery(t *testing.T) {
	ctx := context.Background()
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	defer func() { runUnifiedResearchLoop = originalRunUnifiedResearchLoop }()
	originalSearch := wisdev.RetrieveCanonicalPapers
	defer func() { wisdev.RetrieveCanonicalPapers = originalSearch }()

	capturedQuery := ""
	wisdev.RetrieveCanonicalPapers = func(ctx context.Context, rdb redis.UniversalClient, query string, limit int) ([]wisdev.Source, map[string]any, error) {
		capturedQuery = query
		return []wisdev.Source{{ID: "p1", Title: "Paper 1"}}, map[string]any{}, nil
	}

	sessions := wisdev.NewSessionManager("")
	session, err := sessions.CreateSession(ctx, "user-1", "seed query survives")
	require.NoError(t, err)

	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{Journal: journal, Loop: wisdev.NewAutonomousLoop(nil, nil)}
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		capturedQuery = req.Query
		return testResearchLoopResult(req.Query, []string{req.Query}, []search.Paper{{ID: "p1", Title: "Paper 1", Source: "crossref"}}), nil
	}
	h := NewWisDevHandler(sessions, nil, nil, gw, nil, nil, nil)

	reqBody := `{"sessionId":"` + session.ID + `","query":"   ","quality_mode":"balanced"}`
	req := httptest.NewRequest(http.MethodPost, "/deep-research", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleDeepResearch(w, req.WithContext(ctx))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "seed query survives", capturedQuery)
}

func TestWisDevHandler_HandleDeepResearch_RequiresCanonicalQuery(t *testing.T) {
	ctx := context.Background()
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{Journal: journal}
	h := NewWisDevHandler(nil, nil, nil, gw, nil, nil, nil)

	reqBody := `{"query":"   ","session":{"originalQuery":"   ","correctedQuery":"   "},"plan":{"query":"   "}}`
	req := httptest.NewRequest(http.MethodPost, "/deep-research", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleDeepResearch(w, req.WithContext(ctx))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp APIError
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
}

func TestWisDevHandler_HandleAutonomousResearch(t *testing.T) {
	ctx := context.Background()
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	defer func() { runUnifiedResearchLoop = originalRunUnifiedResearchLoop }()
	// Mock canonical retrieval
	originalSearch := wisdev.RetrieveCanonicalPapers
	defer func() { wisdev.RetrieveCanonicalPapers = originalSearch }()

	wisdev.RetrieveCanonicalPapers = func(ctx context.Context, rdb redis.UniversalClient, query string, limit int) ([]wisdev.Source, map[string]any, error) {
		return []wisdev.Source{{ID: "p1", Title: "Paper 1"}}, map[string]any{
			"count":    1,
			"query":    query,
			"traceId":  "wisdev-test-trace",
			"contract": "paperBundle.v1",
		}, nil
	}

	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{Journal: journal, Loop: wisdev.NewAutonomousLoop(nil, nil)}
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		return testResearchLoopResult(req.Query, []string{req.Query}, []search.Paper{{ID: "p1", Title: "Paper 1", Source: "crossref"}}), nil
	}
	h := NewWisDevHandler(nil, nil, nil, gw, nil, nil, nil)

	reqBody := `{"session": {"correctedQuery": "test query"}, "plan": {"query": "test query"}}`
	req := httptest.NewRequest(http.MethodPost, "/autonomous", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleAutonomousResearch(w, req.WithContext(ctx))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	assert.True(t, resp["ok"].(bool))
	payload, ok := resp["autonomousResearch"].(map[string]any)
	require.True(t, ok)
	_, ok = payload["evidenceDossier"].(map[string]any)
	require.True(t, ok)
}

func TestWisDevHandler_HandleAutonomousResearch_UsesLoopWhenAvailable(t *testing.T) {
	ctx := context.Background()
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{
		name: "compat_autonomous_loop",
		SearchFunc: func(_ context.Context, query string, _ search.SearchOpts) ([]search.Paper, error) {
			assertResearchOrCitationGraphQuery(t, query, "test query")
			return []search.Paper{{ID: "p1", Title: "Loop Paper", Abstract: "Loop abstract", Source: "crossref"}}, nil
		},
	})
	reg.SetDefaultOrder([]string{"compat_autonomous_loop"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return isAutonomousHypothesisProposalPrompt(req)
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"hypotheses":[]}`}, nil).Maybe()
	allowAutonomousCritique(msc)
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Evaluate if the following papers")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"sufficient": true, "reasoning": "enough", "nextQuery": ""}`}, nil).Maybe()
	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return strings.Contains(req.Prompt, "Extract the top 2-3")
	})).Return(&llmv1.StructuredResponse{JsonResult: `[]`}, nil).Maybe()
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Loop synthesis"}, nil).Maybe()

	journal := wisdev.NewRuntimeJournal(nil)
	loop := wisdev.NewAutonomousLoop(reg, lc)
	gw := &wisdev.AgentGateway{Journal: journal, Loop: loop}
	gw.Runtime = wisdev.NewUnifiedResearchRuntime(loop, reg, lc, gw.ProgrammaticLoopExecutor())
	h := NewWisDevHandler(nil, nil, nil, gw, nil, nil, nil)

	reqBody := `{"session": {"correctedQuery": "test query"}, "plan": {"query": "test query"}}`
	req := httptest.NewRequest(http.MethodPost, "/autonomous", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleAutonomousResearch(w, req.WithContext(ctx))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	payload, ok := resp["autonomousResearch"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "go_canonical_runtime", mapAny(payload["metadata"])["executionPlane"])
	assert.Equal(t, true, mapAny(payload["metadata"])["loopBacked"])
	_, ok = payload["committee"].(map[string]any)
	require.True(t, ok)
	_, ok = payload["evidenceDossier"].(map[string]any)
	require.True(t, ok)
}

func TestWisDevHandler_HandleAutonomousResearch_FallsBackToOriginalQuery(t *testing.T) {
	ctx := context.Background()
	originalRunUnifiedResearchLoop := runUnifiedResearchLoop
	defer func() { runUnifiedResearchLoop = originalRunUnifiedResearchLoop }()
	originalSearch := wisdev.RetrieveCanonicalPapers
	defer func() { wisdev.RetrieveCanonicalPapers = originalSearch }()

	capturedQuery := ""
	wisdev.RetrieveCanonicalPapers = func(ctx context.Context, rdb redis.UniversalClient, query string, limit int) ([]wisdev.Source, map[string]any, error) {
		capturedQuery = query
		return []wisdev.Source{{ID: "p1", Title: "Paper 1"}}, map[string]any{}, nil
	}

	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{Journal: journal, Loop: wisdev.NewAutonomousLoop(nil, nil)}
	runUnifiedResearchLoop = func(_ context.Context, _ *wisdev.UnifiedResearchRuntime, _ wisdev.ResearchExecutionPlane, req wisdev.LoopRequest, _ func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error) {
		capturedQuery = req.Query
		return testResearchLoopResult(req.Query, []string{req.Query}, []search.Paper{{ID: "p1", Title: "Paper 1", Source: "crossref"}}), nil
	}
	h := NewWisDevHandler(nil, nil, nil, gw, nil, nil, nil)

	reqBody := `{"session": {"originalQuery": "seed query survives", "correctedQuery": "   "}, "plan": {"query": ""}}`
	req := httptest.NewRequest(http.MethodPost, "/autonomous", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()

	h.HandleAutonomousResearch(w, req.WithContext(ctx))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "seed query survives", capturedQuery)
}

func TestWisDevHandler_JournalEvent_NoopWhenGatewayOrJournalMissing(t *testing.T) {
	h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
	assert.NotPanics(t, func() {
		h.journalEvent("test.event", "/path", "trace-1", " session-1 ", " user-1 ", "plan-1 ", "step-1 ", "summary", nil, nil)
	})

	gw := &wisdev.AgentGateway{}
	h = NewWisDevHandler(nil, nil, nil, gw, nil, nil, nil)
	assert.NotPanics(t, func() {
		h.journalEvent("test.event", "/path", "trace-2", "session-2", "user-2", "", "", "summary", nil, nil)
	})
}

func TestWisDevHandler_JournalEventWritesTrimmedAndClonedData(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wisdev-journal-event")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)
	journalPath := tempDir + "/journal.jsonl"
	t.Setenv("WISDEV_JOURNAL_PATH", journalPath)

	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{Journal: journal}
	h := NewWisDevHandler(nil, nil, nil, gw, nil, nil, nil)

	payload := map[string]any{"title": "original"}
	metadata := map[string]any{"source": "test"}
	h.journalEvent(
		"event.type",
		"/wisdev/route",
		" trace-1 ",
		" session-1 ",
		" user-1 ",
		"plan-1",
		" step-1 ",
		"summary text",
		payload,
		metadata,
	)

	payload["title"] = "mutated"
	metadata["source"] = "mutated"

	content, err := os.ReadFile(journalPath)
	require.NoError(t, err)
	lines := strings.Split(string(content), "\n")
	assert.Len(t, lines, 2)

	var entry wisdev.RuntimeJournalEntry
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &entry))
	assert.Equal(t, "event.type", entry.EventType)
	assert.Equal(t, "/wisdev/route", entry.Path)
	assert.Equal(t, "trace-1", strings.TrimSpace(entry.TraceID))
	assert.Equal(t, "session-1", entry.SessionID)
	assert.Equal(t, "user-1", entry.UserID)
	assert.Equal(t, "step-1", entry.StepID)
	assert.Equal(t, "summary text", entry.Summary)
	assert.Equal(t, "ok", entry.Status)
	assert.Equal(t, "original", entry.Payload["title"])
	assert.Equal(t, "test", entry.Metadata["source"])
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestNormalizeDeepResearchQualityMode(t *testing.T) {
	assert.Equal(t, "quality", normalizeDeepResearchQualityMode("high"))
	assert.Equal(t, "quality", normalizeDeepResearchQualityMode("deep"))
	assert.Equal(t, "fast", normalizeDeepResearchQualityMode("fast"))
	assert.Equal(t, "balanced", normalizeDeepResearchQualityMode("unknown"))
}

func TestGetDeepResearchBudget(t *testing.T) {
	b1 := getDeepResearchBudget("quality")
	assert.Equal(t, 16, b1.maxSearchTerms)
	assert.Equal(t, 16, b1.hitsPerSearch)

	b2 := getDeepResearchBudget("fast")
	assert.Equal(t, 2, b2.maxSearchTerms)
	assert.Equal(t, 4, b2.hitsPerSearch)

	b3 := getDeepResearchBudget("balanced")
	assert.Equal(t, 6, b3.maxSearchTerms)
	assert.Equal(t, 12, b3.hitsPerSearch)
}

func TestWisDevHandler_HandleDeepResearch_Invalid(t *testing.T) {
	h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/deep-research", strings.NewReader(`invalid`))
	w := httptest.NewRecorder()
	h.HandleDeepResearch(w, req)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestWisDevHandler_HandleAutonomousResearch_Invalid(t *testing.T) {
	h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/autonomous", strings.NewReader(`invalid`))
	w := httptest.NewRecorder()
	h.HandleAutonomousResearch(w, req)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
