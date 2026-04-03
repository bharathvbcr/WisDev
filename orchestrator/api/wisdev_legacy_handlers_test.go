package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

func newLegacyWisDevHandler() *WisDevHandler {
	return NewWisDevHandler(
		wisdev.NewSessionManager(""),
		wisdev.NewGuidedFlow(),
		nil,
		nil,
		nil,
		nil,
	)
}

func TestWisDevLegacyHandlers_ErrorEnvelopes(t *testing.T) {
	handler := newLegacyWisDevHandler()

	t.Run("initialize method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/initialize-session", nil)
		rec := httptest.NewRecorder()

		handler.HandleInitialize(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("initialize missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/initialize-session", bytes.NewBufferString(`{"userId":"u1"}`))
		rec := httptest.NewRecorder()

		handler.HandleInitialize(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("get session missing id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/get-session", nil)
		rec := httptest.NewRecorder()

		handler.HandleGetSession(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("get session not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/get-session?sessionId=missing", nil)
		rec := httptest.NewRecorder()

		handler.HandleGetSession(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("process answer invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/process-answer", bytes.NewBufferString(`{bad`))
		rec := httptest.NewRecorder()

		handler.HandleProcessAnswer(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("process answer session not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/process-answer", bytes.NewBufferString(`{"sessionId":"missing","questionId":"q1","values":["v1"]}`))
		rec := httptest.NewRecorder()

		handler.HandleProcessAnswer(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("question options missing session id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/question/options?questionId=q1", nil)
		rec := httptest.NewRecorder()

		handler.HandleQuestionOptions(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("analyze query invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/analyze-query", bytes.NewBufferString(`{bad`))
		rec := httptest.NewRecorder()

		handler.HandleAnalyzeQuery(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("analyze query missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/analyze-query", bytes.NewBufferString(`{"query":"   "}`))
		rec := httptest.NewRecorder()

		handler.HandleAnalyzeQuery(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("deep research method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/wisdev/deep-research", nil)
		rec := httptest.NewRecorder()

		handler.HandleDeepResearch(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("deep research unavailable without gateway", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/deep-research", bytes.NewBufferString(`{"query":"sleep and memory"}`))
		rec := httptest.NewRecorder()

		handler.HandleDeepResearch(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrServiceUnavailable, resp.Error.Code)
	})

	t.Run("autonomous research unavailable without gateway", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/autonomous-research", bytes.NewBufferString(`{"session":{"correctedQuery":"sleep and memory"}}`))
		rec := httptest.NewRecorder()

		handler.HandleAutonomousResearch(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrServiceUnavailable, resp.Error.Code)
	})

	t.Run("decompose task unavailable without brain caps", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/decompose-task", bytes.NewBufferString(`{"query":"sleep and memory"}`))
		rec := httptest.NewRecorder()

		handler.HandleDecomposeTask(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrServiceUnavailable, resp.Error.Code)
	})

	t.Run("propose hypotheses unavailable without brain caps", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/propose-hypotheses", bytes.NewBufferString(`{"query":"sleep and memory"}`))
		rec := httptest.NewRecorder()

		handler.HandleProposeHypotheses(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrServiceUnavailable, resp.Error.Code)
	})

	t.Run("coordinate replan unavailable without brain caps", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/coordinate-replan", bytes.NewBufferString(`{"failedStepId":"step-1"}`))
		rec := httptest.NewRecorder()

		handler.HandleCoordinateReplan(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrServiceUnavailable, resp.Error.Code)
	})
}

func TestWisDevLegacyHandlers_InitializeAndAnalyzeSuccess(t *testing.T) {
	handler := newLegacyWisDevHandler()

	req := httptest.NewRequest(http.MethodPost, "/v2/agent/initialize-session", bytes.NewBufferString(`{"userId":"u1","query":"effects of sleep on memory"}`))
	rec := httptest.NewRecorder()
	handler.HandleInitialize(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var session map[string]any
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&session))
	sessionID, _ := session["sessionId"].(string)
	assert.NotEmpty(t, sessionID)

	getReq := httptest.NewRequest(http.MethodGet, "/v2/agent/get-session?sessionId="+sessionID, nil)
	getRec := httptest.NewRecorder()
	handler.HandleGetSession(getRec, getReq)
	assert.Equal(t, http.StatusOK, getRec.Code)

	analyzeReq := httptest.NewRequest(http.MethodPost, "/v2/wisdev/analyze-query", bytes.NewBufferString(`{"query":"effects of sleep on memory"}`))
	analyzeRec := httptest.NewRecorder()
	handler.HandleAnalyzeQuery(analyzeRec, analyzeReq)
	assert.Equal(t, http.StatusOK, analyzeRec.Code)
}
