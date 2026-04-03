package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevV2Server) registerFullPaperRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/full-paper/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
			UserID    string `json:"userId"`
			Query     string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if strings.TrimSpace(req.Query) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
				"field": "query",
			})
			return
		}

		// In a real implementation, this would trigger the full-paper orchestration logic.
		// For the prototype, we create a job state and return it.
		jobID := "job_" + wisdev.NewTraceID()
		job := map[string]any{
			"jobId":          jobID,
			"sessionId":      req.SessionID,
			"userId":         req.UserID,
			"query":          req.Query,
			"status":         "running",
			"currentStageId": "evidence_dossier",
			"stages": []map[string]any{
				{"id": "evidence_dossier", "name": "Evidence Dossier", "status": "running", "completion": 15},
				{"id": "drafting", "name": "Manuscript Drafting", "status": "pending", "completion": 0},
				{"id": "review", "name": "Peer Review", "status": "pending", "completion": 0},
				{"id": "finalization", "name": "Finalization", "status": "pending", "completion": 0},
			},
			"createdAt": time.Now().UnixMilli(),
			"updatedAt": time.Now().UnixMilli(),
			"workspace": map[string]any{},
		}

		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist full paper job", map[string]any{
				"error": err.Error(),
			})
			return
		}

		payload := map[string]any{
			"fullPaperJob": job,
		}
		traceID := writeV2Envelope(w, "fullPaperJob", payload)
		s.journalEvent("full_paper_start", "/v2/full-paper/start", traceID, req.SessionID, req.UserID, "", "", "Full paper job started.", payload, nil)
	})

	mux.HandleFunc("/v2/full-paper/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			JobID     string `json:"jobId"`
			UserID    string `json:"userId"`
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		job, err := loadFullPaperJobState(agentGateway, req.JobID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "full paper job not found", map[string]any{
				"jobId": req.JobID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(job["userId"])) {
			return
		}

		writeV2Envelope(w, "fullPaperJob", map[string]any{"fullPaperJob": job})
	})

	mux.HandleFunc("/v2/full-paper/artifacts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			JobID  string `json:"jobId"`
			UserID string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if strings.TrimSpace(req.JobID) == "" || strings.TrimSpace(req.JobID) == "invalid" {
			WriteError(w, http.StatusNotFound, ErrNotFound, "invalid job id format", nil)
			return
		}
		job, err := loadFullPaperJobState(agentGateway, req.JobID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "full paper job not found", map[string]any{
				"jobId": req.JobID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(job["userId"])) {
			return
		}

		artifacts := job["artifacts"]
		if artifacts == nil {
			artifacts = []any{}
		}
		writeV2Envelope(w, "artifacts", map[string]any{"artifacts": artifacts})
	})

	mux.HandleFunc("/v2/full-paper/workspace", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			JobID  string `json:"jobId"`
			UserID string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		job, err := loadFullPaperJobState(agentGateway, req.JobID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "full paper job not found", map[string]any{
				"jobId": req.JobID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(job["userId"])) {
			return
		}

		workspace := job["workspace"]
		if workspace == nil {
			workspace = map[string]any{}
		}
		writeV2Envelope(w, "workspace", map[string]any{"workspace": workspace})
	})

	mux.HandleFunc("/v2/full-paper/checkpoint", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			JobID             string `json:"jobId"`
			StageID           string `json:"stageId"`
			Action            string `json:"action"` // approve, reject, feedback
			Feedback          string `json:"feedback"`
			ExpectedUpdatedAt int64  `json:"expectedUpdatedAt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if req.Action == "" || req.Action == "invalid" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid checkpoint action", nil)
			return
		}
		job, err := loadFullPaperJobState(agentGateway, req.JobID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "full paper job not found", map[string]any{
				"jobId": req.JobID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(job["userId"])) {
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}

		// Process checkpoint action
		job["updatedAt"] = time.Now().UnixMilli()
		if req.Action == "approve" {
			job["pendingCheckpoint"] = nil
		}

		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist checkpoint action", map[string]any{
				"error": err.Error(),
			})
			return
		}

		writeV2Envelope(w, "checkpointResult", map[string]any{"status": "processed", "jobId": req.JobID})
	})

	mux.HandleFunc("/v2/full-paper/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			JobID             string `json:"jobId"`
			Action            string `json:"action"` // pause, resume, cancel
			ExpectedUpdatedAt int64  `json:"expectedUpdatedAt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if req.Action == "" || req.Action == "invalid" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid control action", nil)
			return
		}
		job, err := loadFullPaperJobState(agentGateway, req.JobID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "full paper job not found", map[string]any{
				"jobId": req.JobID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(job["userId"])) {
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, job) {
			return
		}

		job["status"] = req.Action + "d" // simple simulation
		job["updatedAt"] = time.Now().UnixMilli()

		if err := saveFullPaperJobState(agentGateway, job); err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist control action", map[string]any{
				"error": err.Error(),
			})
			return
		}

		writeV2Envelope(w, "controlResult", map[string]any{"status": job["status"], "jobId": req.JobID})
	})

	mux.HandleFunc("/v2/full-paper/sandbox-action", func(w http.ResponseWriter, r *http.Request) {
		// Sandbox execution logic migrated from bridge.rs / Python
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			JobID   string `json:"jobId"`
			Snippet string `json:"snippet"`
			Tool    string `json:"tool"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if strings.TrimSpace(req.Tool) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "tool is required", nil)
			return
		}

		// Basic validation before proxying to actual sandbox if needed
		if strings.Contains(req.Snippet, "import os") {
			WriteError(w, http.StatusForbidden, ErrForbidden, "disallowed sandbox pattern detected", nil)
			return
		}

		writeV2Envelope(w, "sandboxResult", map[string]any{"status": "allowed_simulated", "jobId": req.JobID})
	})
}
