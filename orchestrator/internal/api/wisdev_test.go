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

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"

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
