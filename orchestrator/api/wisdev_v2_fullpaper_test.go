package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	"github.com/stretchr/testify/assert"
)

func TestWisDevV2_FullPaperHandlers(t *testing.T) {
	// Setup a temporary state store directory via env var
	tmpDir, err := os.MkdirTemp("", "wisdev_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	os.Setenv("WISDEV_STATE_DIR", tmpDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		StateStore:   stateStore,
		PolicyConfig: policy.DefaultPolicyConfig(),
		SessionTTL:   1 * time.Hour,
	}
	mux := http.NewServeMux()
	RegisterV2WisDevRoutes(mux, gw, nil)

	// Create a session for tests
	session, _ := gw.CreateSession(context.Background(), "u1", "query")
	ctx := context.WithValue(context.Background(), contextKey("user_id"), "u1")

	t.Run("POST /v2/full-paper/start - Success", func(t *testing.T) {
		body := map[string]any{
			"sessionId": session.SessionID,
			"userId":    "u1",
			"query":     "test query",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/start", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["fullPaperJob"])
	})

	t.Run("POST /v2/full-paper/status - Success", func(t *testing.T) {
		// Start a job first to have one in store
		jobID := "job1"
		jobState := map[string]any{
			"jobId": jobID,
			"userId": "u1",
			"status": "running",
			"workspace": map[string]any{"drafting": map[string]any{}},
			"metrics": map[string]any{"sourceCount": 5, "verifiedClaimCount": 2},
		}
		_ = stateStore.PersistFullPaperMutation(jobID, jobState, wisdev.RuntimeJournalEntry{})

		body := map[string]any{"jobId": jobID, "userId": "u1", "sessionId": session.SessionID}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/status", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /v2/full-paper/artifacts - Success", func(t *testing.T) {
		jobID := "job_art"
		jobState := map[string]any{
			"jobId": jobID,
			"userId": "u1",
			"artifacts": []any{"a1"},
		}
		_ = stateStore.PersistFullPaperMutation(jobID, jobState, wisdev.RuntimeJournalEntry{})

		body := map[string]any{"jobId": jobID, "userId": "u1"}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/artifacts", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /v2/full-paper/workspace - Success", func(t *testing.T) {
		jobID := "job_ws"
		jobState := map[string]any{
			"jobId": jobID,
			"userId": "u1",
			"workspace": map[string]any{"key": "val"},
		}
		_ = stateStore.PersistFullPaperMutation(jobID, jobState, wisdev.RuntimeJournalEntry{})

		body := map[string]any{"jobId": jobID, "userId": "u1"}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/workspace", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /v2/full-paper/checkpoint - Success", func(t *testing.T) {
		jobID := "job_cp"
		jobState := map[string]any{
			"jobId": jobID,
			"userId": "u1",
			"status": "running",
			"pendingCheckpoint": map[string]any{"stageId": "s1"},
		}
		_ = stateStore.PersistFullPaperMutation(jobID, jobState, wisdev.RuntimeJournalEntry{})

		// Load back to get the updatedAt that was set by store
		loaded, _ := stateStore.LoadFullPaperJob(jobID)
		actualUpdatedAt := int64(IntValue(loaded["updatedAt"]))
		if actualUpdatedAt == 0 {
			actualUpdatedAt = int64(AsFloat(loaded["updatedAt"]))
		}

		body := map[string]any{
			"jobId": jobID,
			"stageId": "s1",
			"action": "approve",
			"expectedUpdatedAt": actualUpdatedAt,
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/checkpoint", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("POST /v2/full-paper/control - Success pause", func(t *testing.T) {
		jobID := "job_ctrl"
		jobState := map[string]any{
			"jobId": jobID,
			"userId": "u1",
			"status": "running",
		}
		_ = stateStore.PersistFullPaperMutation(jobID, jobState, wisdev.RuntimeJournalEntry{})

		loaded, _ := stateStore.LoadFullPaperJob(jobID)
		actualUpdatedAt := int64(IntValue(loaded["updatedAt"]))
		if actualUpdatedAt == 0 {
			actualUpdatedAt = int64(AsFloat(loaded["updatedAt"]))
		}

		body := map[string]any{
			"jobId": jobID,
			"action": "pause",
			"expectedUpdatedAt": actualUpdatedAt,
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v2/full-paper/control", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
