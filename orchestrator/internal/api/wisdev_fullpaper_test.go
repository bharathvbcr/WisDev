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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestWisDev_FullPaperHandlers(t *testing.T) {
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
	RegisterWisDevRoutes(mux, gw, nil, nil)

	// Create a session for tests
	session, _ := gw.CreateSession(context.Background(), "u1", "query")
	ctx := context.WithValue(context.Background(), contextKey("user_id"), "u1")

	t.Run("POST /full-paper/start - Success", func(t *testing.T) {
		body := map[string]any{
			"sessionId": session.SessionID,
			"userId":    "u1",
			"query":     "test query",
			"traceId":   "trace-fullpaper-start-1",
			"options": map[string]any{
				"papers": []map[string]any{
					{
						"id":       "arxiv:2501.00001",
						"title":    "Grounded Retrieval for Science",
						"abstract": "The system improves recall. However, benchmark coverage remains narrow.",
						"fullText": "A chart reports 18 percent improvement over the baseline.",
						"structureMap": []map[string]any{
							{"type": "figure", "title": "Figure 1", "caption": "Recall improves by 18 percent."},
						},
					},
				},
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full-paper/start", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "trace-fullpaper-start-1", rec.Header().Get("X-Trace-Id"))
		assert.Equal(t, "trace-fullpaper-start-1", resp["traceId"])
		job := mapAny(resp["job"])
		assert.NotNil(t, job)
		workspace := mapAny(job["workspace"])
		drafting := mapAny(workspace["drafting"])
		assert.NotEmpty(t, drafting["sectionArtifactIds"])
		assert.NotEmpty(t, drafting["claimPacketIds"])
		assert.NotNil(t, workspace["rawMaterialSet"])
		assert.NotNil(t, workspace["blueprint"])
		assert.NotNil(t, workspace["visualArtifacts"])
		assert.NotNil(t, workspace["evidenceDossier"])
		assert.Equal(t, "awaiting_approval", job["status"])
	})

	t.Run("POST /full-paper/status - Success", func(t *testing.T) {
		// Start a job first to have one in store
		jobID := "job1"
		jobState := map[string]any{
			"jobId":     jobID,
			"userId":    "u1",
			"status":    "running",
			"workspace": map[string]any{"drafting": map[string]any{}},
			"metrics":   map[string]any{"sourceCount": 5, "verifiedClaimCount": 2},
		}
		_ = stateStore.PersistFullPaperMutation(jobID, jobState, wisdev.RuntimeJournalEntry{})

		body := map[string]any{"jobId": jobID, "userId": "u1", "sessionId": session.SessionID, "trace_id": "trace-fullpaper-status-legacy-1"}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full-paper/status", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "trace-fullpaper-status-legacy-1", rec.Header().Get("X-Trace-Id"))
		assert.Equal(t, "trace-fullpaper-status-legacy-1", resp["traceId"])
	})

	t.Run("POST /full-paper/artifacts - Success", func(t *testing.T) {
		jobID := "job_art"
		jobState := map[string]any{
			"jobId":     jobID,
			"userId":    "u1",
			"artifacts": []any{"a1"},
		}
		_ = stateStore.PersistFullPaperMutation(jobID, jobState, wisdev.RuntimeJournalEntry{})

		body := map[string]any{"jobId": jobID, "userId": "u1", "traceId": "trace-fullpaper-artifacts-1"}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full-paper/artifacts", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "trace-fullpaper-artifacts-1", rec.Header().Get("X-Trace-Id"))
		assert.Equal(t, "trace-fullpaper-artifacts-1", resp["traceId"])
	})

	t.Run("POST /full-paper/workspace - Success", func(t *testing.T) {
		jobID := "job_ws"
		jobState := map[string]any{
			"jobId":     jobID,
			"userId":    "u1",
			"workspace": map[string]any{"key": "val"},
		}
		_ = stateStore.PersistFullPaperMutation(jobID, jobState, wisdev.RuntimeJournalEntry{})

		body := map[string]any{"jobId": jobID, "userId": "u1", "traceId": "trace-fullpaper-workspace-1"}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full-paper/workspace", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "trace-fullpaper-workspace-1", rec.Header().Get("X-Trace-Id"))
		assert.Equal(t, "trace-fullpaper-workspace-1", resp["traceId"])
	})

	t.Run("POST /full-paper/checkpoint - Success", func(t *testing.T) {
		jobID := "job_cp"
		jobState := map[string]any{
			"jobId":             jobID,
			"userId":            "u1",
			"status":            "running",
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
			"jobId":             jobID,
			"stageId":           "s1",
			"action":            "approve",
			"expectedUpdatedAt": actualUpdatedAt,
			"traceId":           "trace-fullpaper-checkpoint-1",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full-paper/checkpoint", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "trace-fullpaper-checkpoint-1", rec.Header().Get("X-Trace-Id"))
		assert.Equal(t, "trace-fullpaper-checkpoint-1", resp["traceId"])
		result := mapAny(resp["job"])
		assert.Equal(t, "completed", result["status"])
	})

	t.Run("POST /full-paper/control - Success pause", func(t *testing.T) {
		jobID := "job_ctrl"
		jobState := map[string]any{
			"jobId":  jobID,
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
			"jobId":             jobID,
			"action":            "pause",
			"expectedUpdatedAt": actualUpdatedAt,
			"trace_id":          "trace-fullpaper-control-legacy-1",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full-paper/control", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "trace-fullpaper-control-legacy-1", rec.Header().Get("X-Trace-Id"))
		assert.Equal(t, "trace-fullpaper-control-legacy-1", resp["traceId"])
		result := mapAny(resp["job"])
		assert.Equal(t, "paused", result["status"])
	})

	t.Run("POST /full-paper/rewrite-section - Success", func(t *testing.T) {
		startBody := map[string]any{
			"sessionId": session.SessionID,
			"userId":    "u1",
			"query":     "test query",
		}
		startJSON, _ := json.Marshal(startBody)
		startReq := httptest.NewRequest(http.MethodPost, "/full-paper/start", bytes.NewReader(startJSON))
		startReq = startReq.WithContext(ctx)
		startRec := httptest.NewRecorder()
		mux.ServeHTTP(startRec, startReq)
		var startResp map[string]any
		json.Unmarshal(startRec.Body.Bytes(), &startResp)
		job := mapAny(startResp["job"])
		workspace := mapAny(job["workspace"])
		drafting := mapAny(workspace["drafting"])
		sectionIDs := sliceStrings(drafting["sectionArtifactIds"])
		sectionDrafts := sliceAnyMap(workspace["sectionDraftArtifacts"])
		require.NotEmpty(t, sectionDrafts)
		sectionID := wisdev.AsOptionalString(sectionDrafts[0]["sectionId"])
		actualUpdatedAt := int64(IntValue(job["updatedAt"]))

		body := map[string]any{
			"jobId":             job["jobId"],
			"sectionId":         sectionID,
			"instructions":      "Tighten the evidence grounding.",
			"expectedUpdatedAt": actualUpdatedAt,
			"traceId":           "trace-fullpaper-rewrite-1",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full-paper/rewrite-section", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "trace-fullpaper-rewrite-1", rec.Header().Get("X-Trace-Id"))
		assert.Equal(t, "trace-fullpaper-rewrite-1", resp["traceId"])
		result := mapAny(resp["result"])
		rewritten := mapAny(result["sectionArtifact"])
		assert.Equal(t, "ready_for_review", rewritten["reviewStatus"])
		assert.Contains(t, wisdev.AsOptionalString(rewritten["content"]), "Revision focus")
		assert.NotEmpty(t, sectionIDs)
	})

	t.Run("POST /full-paper/regenerate-visual - Success", func(t *testing.T) {
		startBody := map[string]any{
			"sessionId": session.SessionID,
			"userId":    "u1",
			"query":     "test query",
		}
		startJSON, _ := json.Marshal(startBody)
		startReq := httptest.NewRequest(http.MethodPost, "/full-paper/start", bytes.NewReader(startJSON))
		startReq = startReq.WithContext(ctx)
		startRec := httptest.NewRecorder()
		mux.ServeHTTP(startRec, startReq)
		var startResp map[string]any
		json.Unmarshal(startRec.Body.Bytes(), &startResp)
		job := mapAny(startResp["job"])
		workspace := mapAny(job["workspace"])
		visualArtifacts := sliceAnyMap(workspace["visualArtifacts"])
		require.NotEmpty(t, visualArtifacts)
		actualUpdatedAt := int64(IntValue(job["updatedAt"]))

		body := map[string]any{
			"jobId":             job["jobId"],
			"visualId":          visualArtifacts[0]["artifactId"],
			"instructions":      "Refresh the review annotation.",
			"expectedUpdatedAt": actualUpdatedAt,
			"trace_id":          "trace-fullpaper-visual-legacy-1",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full-paper/regenerate-visual", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "trace-fullpaper-visual-legacy-1", rec.Header().Get("X-Trace-Id"))
		assert.Equal(t, "trace-fullpaper-visual-legacy-1", resp["traceId"])
		result := mapAny(resp["result"])
		visual := mapAny(result["visualArtifact"])
		assert.Equal(t, "ready_for_review", visual["reviewStatus"])
	})

	t.Run("POST /full-paper/sandbox-action - Success", func(t *testing.T) {
		body := map[string]any{
			"jobId":   "job_ws",
			"tool":    "inspect",
			"traceId": "trace-fullpaper-sandbox-1",
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/full-paper/sandbox-action", bytes.NewReader(jsonBody))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Equal(t, "trace-fullpaper-sandbox-1", rec.Header().Get("X-Trace-Id"))
		assert.Equal(t, "trace-fullpaper-sandbox-1", resp["traceId"])
		result := mapAny(resp["result"])
		assert.Equal(t, true, result["ok"])
	})
}
