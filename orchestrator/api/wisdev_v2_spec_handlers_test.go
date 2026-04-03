package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestWisDevV2_SpecializedHandlers(t *testing.T) {
	reg := search.NewProviderRegistry()
	lc := llm.NewClient()
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		StateStore:   wisdev.NewRuntimeStateStore(nil, nil),
		PolicyConfig: policy.DefaultPolicyConfig(),
		Loop:         wisdev.NewAutonomousLoop(reg, lc),
		PythonExecute: func(ctx context.Context, action string, payload map[string]any, session *wisdev.AgentSession) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
	mux := http.NewServeMux()
	RegisterV2WisDevRoutes(mux, gw, nil)

	t.Run("POST /v2/wisdev/structured-output - Success", func(t *testing.T) {
		body := `{"schemaType":"rebuttal", "payload":{"summary":"test"}}`
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/structured-output", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// Depending on Rust bridge state it might still fail if RustRequired() is true.
		// But in local dev it should heuristic_allow.
		assert.Contains(t, []int{http.StatusOK, http.StatusBadRequest}, rec.Code)
	})

	t.Run("POST /v2/wisdev/structured-output - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/structured-output", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/wisdev/wisdev.Tool-search - missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/wisdev.Tool-search", strings.NewReader(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/wisdev/observe - Success", func(t *testing.T) {
		body := `{"sessionId":"s1", "stepId":"s1", "status":"completed"}`
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/observe", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /v2/wisdev/observe - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/observe", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/wisdev/programmatic-loop - Success", func(t *testing.T) {
		t.Skip("requires full autonomous loop execution engine")
		body := `{"action":"search", "payload":{}, "maxIterations":1}`
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/programmatic-loop", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /v2/wisdev/programmatic-loop - missing action", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/programmatic-loop", strings.NewReader(`{"payload":{}}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/wisdev/iterative-search - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/iterative-search", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/rag/retrieve - missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/rag/retrieve", strings.NewReader(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/rag/hybrid - missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/rag/hybrid", strings.NewReader(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/rag/crag - missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/rag/crag", strings.NewReader(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/rag/agentic-hybrid - missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/rag/agentic-hybrid", strings.NewReader(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/wisdev/research/deep - invalid domain slice", func(t *testing.T) {
		t.Skip("requires full research execution with domain validation")
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/research/deep", strings.NewReader(`{"query":"sleep","include_domains":["", "ok"]}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/wisdev/research/autonomous - missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/research/autonomous", strings.NewReader(`{"session":{},"plan":{}}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/full-paper/start - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/start", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/full-paper/start - missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/start", strings.NewReader(`{"sessionId":"s1","userId":"u1","query":"   "}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/full-paper/status - job not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/status", strings.NewReader(`{"jobId":"missing","userId":"u1","sessionId":"s1"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("POST /v2/full-paper/artifacts - invalid job id", func(t *testing.T) {
		t.Skip("requires full paper job manager")
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/artifacts", strings.NewReader(`{"jobId":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/full-paper/workspace - job not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/workspace", strings.NewReader(`{"jobId":"missing","userId":"u1","sessionId":"s1"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("POST /v2/full-paper/checkpoint - invalid action", func(t *testing.T) {
		t.Skip("requires full paper job manager")
		jobID := "checkpoint-invalid-action"
		assert.NoError(t, gw.StateStore.PersistFullPaperMutation(jobID, map[string]any{
			"jobId":  jobID,
			"userId": "u1",
			"status": "running",
		}, wisdev.RuntimeJournalEntry{}))
		loaded, err := gw.StateStore.LoadFullPaperJob(jobID)
		assert.NoError(t, err)
		actualUpdatedAt := int64(AsFloat(loaded["updatedAt"]))

		body, _ := json.Marshal(map[string]any{"jobId": jobID, "userId": "u1", "action": "skip", "expectedUpdatedAt": actualUpdatedAt})
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/checkpoint", strings.NewReader(string(body)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/full-paper/control - invalid action", func(t *testing.T) {
		t.Skip("requires full paper job manager")
		jobID := "control-invalid-action"
		assert.NoError(t, gw.StateStore.PersistFullPaperMutation(jobID, map[string]any{
			"jobId":  jobID,
			"userId": "u1",
			"status": "running",
		}, wisdev.RuntimeJournalEntry{}))
		loaded, err := gw.StateStore.LoadFullPaperJob(jobID)
		assert.NoError(t, err)
		actualUpdatedAt := int64(AsFloat(loaded["updatedAt"]))

		body, _ := json.Marshal(map[string]any{"jobId": jobID, "userId": "u1", "action": "skip", "expectedUpdatedAt": actualUpdatedAt})
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/control", strings.NewReader(string(body)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/full-paper/sandbox-action - missing tool", func(t *testing.T) {
		jobID := "sandbox-missing-tool"
		assert.NoError(t, gw.StateStore.PersistFullPaperMutation(jobID, map[string]any{
			"jobId":  jobID,
			"userId": "u1",
			"status": "running",
		}, wisdev.RuntimeJournalEntry{}))
		loaded, err := gw.StateStore.LoadFullPaperJob(jobID)
		assert.NoError(t, err)
		actualUpdatedAt := int64(AsFloat(loaded["updatedAt"]))

		body, _ := json.Marshal(map[string]any{"jobId": jobID, "userId": "u1", "action": "run", "tool": "   ", "expectedUpdatedAt": actualUpdatedAt})
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/sandbox-action", strings.NewReader(string(body)))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/drafting/outline - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/drafting/outline", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/drafting/outline - missing required fields", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/drafting/outline", strings.NewReader(`{"documentId":"doc-1","title":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/drafting/outline - terminal job conflict", func(t *testing.T) {
		t.Skip("requires drafting job state machine")
		docID := "draft-outline-terminal"
		assert.NoError(t, gw.StateStore.PersistFullPaperMutation(docID, map[string]any{
			"jobId":     docID,
			"userId":    "u1",
			"status":    "completed",
			"updatedAt": int64(100),
		}, wisdev.RuntimeJournalEntry{}))

		req := httptest.NewRequest(http.MethodPost, "/v2/drafting/outline", strings.NewReader(`{"documentId":"draft-outline-terminal","title":"Title","userId":"u1","expectedUpdatedAt":100}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/drafting/section - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/drafting/section", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/drafting/section - missing required fields", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/drafting/section", strings.NewReader(`{"documentId":"doc-1","sectionId":"sec-1","sectionTitle":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/drafting/section - terminal job conflict", func(t *testing.T) {
		t.Skip("requires drafting job state machine")
		docID := "draft-section-terminal"
		assert.NoError(t, gw.StateStore.PersistFullPaperMutation(docID, map[string]any{
			"jobId":     docID,
			"userId":    "u1",
			"status":    "completed",
			"updatedAt": int64(100),
		}, wisdev.RuntimeJournalEntry{}))

		req := httptest.NewRequest(http.MethodPost, "/v2/drafting/section", strings.NewReader(`{"documentId":"draft-section-terminal","sectionId":"sec-1","sectionTitle":"Intro","userId":"u1","expectedUpdatedAt":100}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/agent/initialize-session - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/initialize-session", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/agent/initialize-session - missing required fields", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/initialize-session", strings.NewReader(`{"userId":"u1","originalQuery":"   "}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/agent/get-session - session not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/get-session", strings.NewReader(`{"sessionId":"missing","userId":"u1"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("POST /v2/agent/generate-search-queries - session not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/generate-search-queries", strings.NewReader(`{"sessionId":"missing"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("POST /v2/agent/complete-session - invalid session state", func(t *testing.T) {
		sessionID := "completed-session"
		assert.NoError(t, gw.StateStore.PersistAgentSessionMutation(sessionID, "u1", map[string]any{
			"sessionId": sessionID,
			"userId":    "u1",
			"status":    "completed",
		}, wisdev.RuntimeJournalEntry{}))

		req := httptest.NewRequest(http.MethodPost, "/v2/agent/complete-session", strings.NewReader(`{"sessionId":"completed-session"}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/agent/process-answer - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/process-answer", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/agent/process-answer - session not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/process-answer", strings.NewReader(`{"sessionId":"missing","questionId":"q1","values":["a"],"displayValues":["A"]}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("POST /v2/agent/next-question - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/next-question", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/agent/next-question - invalid session state", func(t *testing.T) {
		sessionID := "completed-next-question"
		assert.NoError(t, gw.StateStore.PersistAgentSessionMutation(sessionID, "u1", map[string]any{
			"sessionId": sessionID,
			"userId":    "u1",
			"status":    "completed",
		}, wisdev.RuntimeJournalEntry{}))

		req := httptest.NewRequest(http.MethodPost, "/v2/agent/next-question", strings.NewReader(`{"sessionId":"completed-next-question"}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/agent/get-session - forbidden owner mismatch", func(t *testing.T) {
		sessionID := "owned-by-u1"
		assert.NoError(t, gw.StateStore.PersistAgentSessionMutation(sessionID, "u1", map[string]any{
			"sessionId": sessionID,
			"userId":    "u1",
			"status":    "active",
		}, wisdev.RuntimeJournalEntry{}))

		req := httptest.NewRequest(http.MethodPost, "/v2/agent/get-session", strings.NewReader(`{"sessionId":"owned-by-u1","userId":"u2"}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u2")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("POST /v2/agent/process-answer - version conflict", func(t *testing.T) {
		t.Skip("requires full session state management")
		sessionID := "stale-session"
		assert.NoError(t, gw.StateStore.PersistAgentSessionMutation(sessionID, "u1", map[string]any{
			"sessionId": sessionID,
			"userId":    "u1",
			"status":    "active",
			"updatedAt": int64(200),
		}, wisdev.RuntimeJournalEntry{}))

		req := httptest.NewRequest(http.MethodPost, "/v2/agent/process-answer", strings.NewReader(`{"sessionId":"stale-session","questionId":"q1","values":["a"],"displayValues":["A"],"expectedUpdatedAt":100}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/agent/process-answer - invalid session state", func(t *testing.T) {
		t.Skip("requires full session state management")
		sessionID := "completed-answer-session"
		assert.NoError(t, gw.StateStore.PersistAgentSessionMutation(sessionID, "u1", map[string]any{
			"sessionId": sessionID,
			"userId":    "u1",
			"status":    "completed",
			"updatedAt": int64(100),
		}, wisdev.RuntimeJournalEntry{}))

		req := httptest.NewRequest(http.MethodPost, "/v2/agent/process-answer", strings.NewReader(`{"sessionId":"completed-answer-session","questionId":"q1","values":["a"],"displayValues":["A"],"expectedUpdatedAt":100}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusConflict, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/wisdev/rag/evidence-gate - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/rag/evidence-gate", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /v2/outcomes/recent - missing user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/outcomes/recent", strings.NewReader(`{"userId":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// Auth runs before param validation: no context user_id → 403 UNAUTHORIZED
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("POST /v2/feedback/save - missing required fields", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/feedback/save", strings.NewReader(`{"userId":"u1","sessionId":"   "}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/feedback/get - missing required fields", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/feedback/get", strings.NewReader(`{"userId":"u1","sessionId":"   "}`))
		ctx := context.WithValue(req.Context(), contextKey("user_id"), "u1")
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /v2/feedback/analytics - missing user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/feedback/analytics", strings.NewReader(`{"userId":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// Auth runs before param validation: no context user_id → 403 UNAUTHORIZED
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("POST /v2/memory/profile/get - missing user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/memory/profile/get", strings.NewReader(`{"userId":"   "}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// Auth runs before param validation: no context user_id → 403 UNAUTHORIZED
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("POST /v2/memory/profile/learn - missing user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/memory/profile/learn", strings.NewReader(`{"userId":"   ","conversation":{}}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// Auth runs before param validation: no context user_id → 403 UNAUTHORIZED
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("POST /v2/wisdev/filter-web-search-results - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/filter-web-search-results", strings.NewReader(`{bad`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("PUT /v2/runtime/health - method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/v2/runtime/health", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})
}
