package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestFullPaperRoutes_SuccessPaths(t *testing.T) {
	is := assert.New(t)
	mux := http.NewServeMux()
	server := &wisdevServer{}

	// Setup gateway with file-based state store in temp dir
	tmpDir, _ := os.MkdirTemp("", "wisdev_state_test")
	defer os.RemoveAll(tmpDir)

	os.Setenv("WISDEV_STATE_DIR", tmpDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := wisdev.NewRuntimeStateStore(nil, nil)
	gw := &wisdev.AgentGateway{
		StateStore: store,
	}
	server.registerFullPaperRoutes(mux, gw)

	jobID := "test-job-1"
	jobData := map[string]any{
		"jobId":     jobID,
		"userId":    "u1",
		"status":    "awaiting_approval",
		"artifacts": []any{map[string]any{"artifactId": "a1", "type": "manuscript"}},
		"workspace": map[string]any{"id": "w1"},
		"updatedAt": int64(123456789),
		"pendingCheckpoint": map[string]any{
			"stageId": "s1",
		},
	}

	// Pre-populate the store
	err := store.SaveFullPaperJob(jobID, jobData)
	assert.NoError(t, err)

	t.Run("status success", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"jobId": jobID})
		req := httptest.NewRequest("POST", "/full-paper/status", bytes.NewReader(body))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var resp map[string]any
		json.Unmarshal(w.Body.Bytes(), &resp)
		job := resp["job"].(map[string]any)
		is.Equal(jobID, job["jobId"])
	})

	t.Run("artifacts success", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"jobId": jobID})
		req := httptest.NewRequest("POST", "/full-paper/artifacts", bytes.NewReader(body))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var resp map[string]any
		json.Unmarshal(w.Body.Bytes(), &resp)
		artifacts := resp["artifacts"].([]any)
		is.Len(artifacts, 1)
	})

	t.Run("workspace success", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"jobId": jobID})
		req := httptest.NewRequest("POST", "/full-paper/workspace", bytes.NewReader(body))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var resp map[string]any
		json.Unmarshal(w.Body.Bytes(), &resp)
		ws := resp["workspace"].(map[string]any)
		is.Equal("w1", ws["id"])
	})

	t.Run("checkpoint skip success", func(t *testing.T) {
		jobData["status"] = "awaiting_approval"
		jobData["pendingCheckpoint"] = map[string]any{"stageId": "s1"}
		store.SaveFullPaperJob(jobID, jobData)

		body, _ := json.Marshal(map[string]any{
			"jobId":             jobID,
			"stageId":           "s1",
			"action":            "skip",
			"expectedUpdatedAt": int64(0),
		})
		req := httptest.NewRequest("POST", "/full-paper/checkpoint", bytes.NewReader(body))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var resp map[string]any
		json.Unmarshal(w.Body.Bytes(), &resp)
		is.True(resp["ok"].(bool))

		// Check status updated - skip defaults to "running"
		updated, _ := store.LoadFullPaperJob(jobID)
		is.Equal("running", updated["status"])
	})

	t.Run("checkpoint approve success", func(t *testing.T) {
		jobData["status"] = "awaiting_approval"
		jobData["pendingCheckpoint"] = map[string]any{"stageId": "s1"}
		store.SaveFullPaperJob(jobID, jobData)

		body, _ := json.Marshal(map[string]any{
			"jobId":             jobID,
			"stageId":           "s1",
			"action":            "approve",
			"expectedUpdatedAt": int64(0),
		})
		req := httptest.NewRequest("POST", "/full-paper/checkpoint", bytes.NewReader(body))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)
		updated, _ := store.LoadFullPaperJob(jobID)
		is.Equal("completed", updated["status"])
	})

	t.Run("checkpoint request_revision success", func(t *testing.T) {
		// Reset status
		jobData["status"] = "awaiting_approval"
		jobData["pendingCheckpoint"] = map[string]any{"stageId": "s1"}
		store.SaveFullPaperJob(jobID, jobData)

		body, _ := json.Marshal(map[string]any{
			"jobId":             jobID,
			"stageId":           "s1",
			"action":            "request_revision",
			"expectedUpdatedAt": int64(0),
		})
		req := httptest.NewRequest("POST", "/full-paper/checkpoint", bytes.NewReader(body))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)
		updated, _ := store.LoadFullPaperJob(jobID)
		is.Equal("awaiting_review", updated["status"])
	})

	t.Run("checkpoint reject success", func(t *testing.T) {
		jobData["status"] = "awaiting_approval"
		jobData["pendingCheckpoint"] = map[string]any{"stageId": "s1"}
		store.SaveFullPaperJob(jobID, jobData)

		body, _ := json.Marshal(map[string]any{
			"jobId":             jobID,
			"stageId":           "s1",
			"action":            "reject",
			"expectedUpdatedAt": int64(0),
		})
		req := httptest.NewRequest("POST", "/full-paper/checkpoint", bytes.NewReader(body))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)
		updated, _ := store.LoadFullPaperJob(jobID)
		is.Equal("paused", updated["status"])
	})

	t.Run("start query required", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"query": ""})
		req := httptest.NewRequest("POST", "/full-paper/start", bytes.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("start denies foreign session binding", func(t *testing.T) {
		sessionID := "foreign-session"
		err := store.PersistAgentSessionMutation(sessionID, "u2", map[string]any{
			"sessionId": sessionID,
			"userId":    "u2",
		}, wisdev.RuntimeJournalEntry{})
		assert.NoError(t, err)

		body, _ := json.Marshal(map[string]any{
			"query":     "compile a manuscript",
			"sessionId": sessionID,
		})
		req := httptest.NewRequest("POST", "/full-paper/start", bytes.NewReader(body))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusForbidden, w.Code)
	})

	t.Run("status invalid job id", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"jobId": ""})
		req := httptest.NewRequest("POST", "/full-paper/status", bytes.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("artifacts invalid job id", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"jobId": "invalid"})
		req := httptest.NewRequest("POST", "/full-paper/artifacts", bytes.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("checkpoint invalid action", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"jobId":  jobID,
			"action": "invalid",
		})
		req := httptest.NewRequest("POST", "/full-paper/checkpoint", bytes.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("checkpoint rejects action outside pending action list", func(t *testing.T) {
		jobData["status"] = "awaiting_approval"
		jobData["pendingCheckpoint"] = map[string]any{
			"stageId": "s1",
			"actions": []string{"approve", "skip"},
		}
		store.SaveFullPaperJob(jobID, jobData)

		body, _ := json.Marshal(map[string]any{
			"jobId":   jobID,
			"stageId": "s1",
			"action":  "request_revision",
		})
		req := httptest.NewRequest("POST", "/full-paper/checkpoint", bytes.NewReader(body))
		req = withTestUserID(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
		assertAPIErrorCode(t, w.Body.Bytes(), ErrInvalidParameters)

		updated, err := store.LoadFullPaperJob(jobID)
		require.NoError(t, err)
		is.Equal("awaiting_approval", updated["status"])
		is.NotNil(updated["pendingCheckpoint"])
	})

	t.Run("status denies non-owner access", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"jobId": jobID})
		req := httptest.NewRequest("POST", "/full-paper/status", bytes.NewReader(body))
		req = withTestUserID(req, "u2")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusForbidden, w.Code)
	})
}

func TestFullPaperRoutes_MethodGuards(t *testing.T) {
	mux := http.NewServeMux()
	server := &wisdevServer{}
	server.registerFullPaperRoutes(mux, &wisdev.AgentGateway{})

	cases := []struct {
		name string
		path string
	}{
		{name: "start", path: "/full-paper/start"},
		{name: "status", path: "/full-paper/status"},
		{name: "artifacts", path: "/full-paper/artifacts"},
		{name: "workspace", path: "/full-paper/workspace"},
		{name: "checkpoint", path: "/full-paper/checkpoint"},
		{name: "control", path: "/full-paper/control"},
		{name: "rewrite section", path: "/full-paper/rewrite-section"},
		{name: "regenerate visual", path: "/full-paper/regenerate-visual"},
		{name: "sandbox action", path: "/full-paper/sandbox-action"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
		})
	}
}

func TestFullPaperLegacyActionHelpers(t *testing.T) {
	newJob := func() map[string]any {
		return map[string]any{
			"status":         "running",
			"currentStage":   "drafting",
			"currentStageId": "drafting",
			"workspace": map[string]any{
				"pendingReviewTarget": map[string]any{"stageId": "peer_reviewer"},
				"controlHistory":      []any{},
				"checkpointHistory":   []any{},
			},
		}
	}

	t.Run("applyLegacyFullPaperControlAction", func(t *testing.T) {
		pauseJob := newJob()
		applyLegacyFullPaperControlAction(pauseJob, "pause")
		assert.Equal(t, "paused", pauseJob["status"])
		assert.Equal(t, "paused", mapAny(pauseJob["workspace"])["status"])
		assert.Len(t, sliceAnyMap(mapAny(pauseJob["workspace"])["controlHistory"]), 1)

		resumeJob := newJob()
		applyLegacyFullPaperControlAction(resumeJob, "resume")
		assert.Equal(t, "running", resumeJob["status"])

		retryJob := newJob()
		applyLegacyFullPaperControlAction(retryJob, "retry_stage")
		assert.Equal(t, "awaiting_approval", retryJob["status"])
		assert.Equal(t, "peer_reviewer", retryJob["currentStageId"])
		assert.NotNil(t, retryJob["pendingCheckpoint"])

		cancelJob := newJob()
		applyLegacyFullPaperControlAction(cancelJob, "cancel")
		assert.Equal(t, "cancelled", cancelJob["status"])
		assert.Nil(t, cancelJob["pendingCheckpoint"])
		assert.Nil(t, mapAny(cancelJob["workspace"])["pendingReviewTarget"])
	})

	t.Run("updateFullPaperStageStatus", func(t *testing.T) {
		job := map[string]any{
			"stages": []any{
				map[string]any{"id": "peer_reviewer", "status": "pending", "completion": 0},
				map[string]any{"id": "revision_editor", "status": "pending", "completion": 0},
			},
		}

		updateFullPaperStageStatus(job, "peer_reviewer", "completed", 100)
		stages := sliceAnyMap(job["stages"])
		assert.Equal(t, "completed", stages[0]["status"])
		assert.Equal(t, 100, stages[0]["completion"])

		updateFullPaperStageStatus(job, "missing", "skipped", 50)
		assert.Len(t, sliceAnyMap(job["stages"]), 2)

		noStagesJob := map[string]any{"stages": []any{}}
		updateFullPaperStageStatus(noStagesJob, "peer_reviewer", "completed", 100)
		assert.Empty(t, sliceAnyMap(noStagesJob["stages"]))

		absentStagesJob := map[string]any{}
		updateFullPaperStageStatus(absentStagesJob, "peer_reviewer", "completed", 100)
		assert.Empty(t, sliceAnyMap(absentStagesJob["stages"]))
	})
}

func TestFullPaperSerializationHelpers(t *testing.T) {
	t.Run("buildSourceBundleSources", func(t *testing.T) {
		result := wisdev.ManuscriptPipelineResult{
			RawMaterials: evidence.ManuscriptRawMaterialSet{
				CanonicalSources: []evidence.CanonicalCitationRecord{
					{CanonicalID: "c1", Title: "Canonical Source", Abstract: "Canonical source abstract. More text.", Year: 2024, LandingURL: "https://example.com/canonical"},
				},
			},
		}
		paperSources := buildSourceBundleSources(result, []search.Paper{
			{Title: "Search Paper", Abstract: "Search paper abstract. More text.", Year: 2025, CitationCount: 12, Link: "https://example.com/paper"},
		})
		require.Len(t, paperSources, 1)
		assert.Equal(t, "Search Paper", paperSources[0]["title"])
		assert.Equal(t, "https://example.com/paper", paperSources[0]["link"])

		fallbackSources := buildSourceBundleSources(result, nil)
		require.Len(t, fallbackSources, 1)
		assert.Equal(t, "Canonical Source", fallbackSources[0]["title"])
		assert.Equal(t, "https://example.com/canonical", fallbackSources[0]["link"])
	})

	t.Run("decode and convert helpers", func(t *testing.T) {
		papers := decodeSearchPapers([]any{
			map[string]any{"id": "p1", "title": "Kept"},
			map[string]any{"id": "p2", "title": "  "},
		})
		require.Len(t, papers, 1)
		assert.Equal(t, "p1", papers[0].ID)
		assert.Nil(t, decodeSearchPapers(nil))
		assert.Nil(t, decodeSearchPapers(make(chan int)))

		assert.Empty(t, toAnyMap(nil))
		assert.Equal(t, "v", toAnyMap(map[string]any{"k": "v"})["k"])
		assert.Empty(t, toAnyMap(make(chan int)))
		assert.Nil(t, toAnySliceMap("bad"))
		require.Len(t, toAnySliceMap([]any{map[string]any{"id": "x"}}), 1)
		assert.Nil(t, toAnySliceMap(make(chan int)))
	})

	t.Run("updateRevisionTasksForTarget", func(t *testing.T) {
		workspace := map[string]any{
			"revisionTasks": []any{
				map[string]any{"targetType": "section", "targetId": "s1", "status": "pending"},
				map[string]any{"targetType": "visual", "targetId": "v1", "status": "pending"},
			},
			"revisionArtifacts": []any{
				map[string]any{
					"content": map[string]any{
						"tasks": []any{},
					},
				},
			},
		}
		updateRevisionTasksForTarget(workspace, "section", "s1")
		tasks := sliceAnyMap(workspace["revisionTasks"])
		require.Len(t, tasks, 2)
		assert.Equal(t, "completed", tasks[0]["status"])
		artifacts := sliceAnyMap(workspace["revisionArtifacts"])
		require.Len(t, artifacts, 1)
		content := mapAny(artifacts[0]["content"])
		require.NotEmpty(t, content)
		require.NotNil(t, content["tasks"])

		emptyWorkspace := map[string]any{"revisionTasks": []any{}}
		updateRevisionTasksForTarget(emptyWorkspace, "section", "missing")
		assert.Empty(t, sliceAnyMap(emptyWorkspace["revisionTasks"]))

		noArtifactWorkspace := map[string]any{
			"revisionTasks": []any{
				map[string]any{"targetType": "section", "targetId": "s2", "status": "pending"},
			},
		}
		updateRevisionTasksForTarget(noArtifactWorkspace, "section", "s2")
		assert.Len(t, sliceAnyMap(noArtifactWorkspace["revisionTasks"]), 1)
	})
}
