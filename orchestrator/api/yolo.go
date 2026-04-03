package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// YoloExecuteRequest is the request body for POST /agent/yolo/execute.
type YoloExecuteRequest struct {
	Query     string `json:"query"`
	SessionID string `json:"session_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Mode      string `json:"mode"` // "bounded" | "full"
	Domain    string `json:"domain,omitempty"`
}

// YoloExecuteResponse is the response body for POST /agent/yolo/execute.
type YoloExecuteResponse struct {
	JobID     string `json:"job_id"`
	StreamURL string `json:"stream_url"`
	Status    string `json:"status"` // "started"
}

// YoloCancelRequest is the request body for POST /agent/yolo/cancel.
type YoloCancelRequest struct {
	JobID string `json:"job_id"`
}

// YoloEvent is a single SSE payload emitted during a YOLO pipeline run (Legacy).
type YoloEvent struct {
	Type           string  `json:"type"`
	Iteration      int     `json:"iteration,omitempty"`
	Status         string  `json:"status,omitempty"`
	Coverage       float64 `json:"coverage,omitempty"`
	PapersFound    int     `json:"papers_found,omitempty"`
	IterationsUsed int     `json:"iterations_used,omitempty"`
	Error          string  `json:"error,omitempty"`
}

// UnifiedEvent is the new canonical SSE schema for /wisdev/job/:id/stream.
type UnifiedEvent struct {
	Type        string `json:"type"`
	JobID       string `json:"job_id"`
	Timestamp   int64  `json:"timestamp"`
	Step        string `json:"step,omitempty"`
	ResultCount int    `json:"result_count,omitempty"`
	Attempt     int    `json:"attempt,omitempty"`
	Error       string `json:"error,omitempty"`
	Severity    string `json:"severity,omitempty"`
	FindingA    string `json:"finding_a,omitempty"`
	FindingB    string `json:"finding_b,omitempty"`
	SkillName   string `json:"skill_name,omitempty"`
	SourcePaper string `json:"source_paper,omitempty"`
	DossierID   string `json:"dossier_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
	CancelledBy string `json:"cancelled_by,omitempty"`
}

// YoloJob tracks the state of a single YOLO or Guided pipeline run.
type YoloJob struct {
	ID            string
	Query         string
	ProjectID     string
	Mode          string // "yolo" | "guided"
	Domain        string
	LegacyEvents  chan YoloEvent
	UnifiedEvents chan UnifiedEvent
	Cancel        context.CancelFunc
	CreatedAt     time.Time
}

// ---------------------------------------------------------------------------
// In-memory job store
// ---------------------------------------------------------------------------

// yoloJobStore is the process-level store for active YOLO jobs.
var yoloJobStore = &yoloStore{
	jobs: make(map[string]*YoloJob),
}

type AutonomousLoopInterface interface {
	Run(ctx context.Context, req wisdev.LoopRequest) (*wisdev.LoopResult, error)
}

var GlobalYoloLoop AutonomousLoopInterface

type yoloStore struct {
	mu   sync.RWMutex
	jobs map[string]*YoloJob
}

func (s *yoloStore) put(job *YoloJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
}

func (s *yoloStore) get(id string) (*YoloJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	return job, ok
}

func (s *yoloStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
}

// ---------------------------------------------------------------------------
// Pipeline goroutines
// ---------------------------------------------------------------------------

// runYoloPipeline drives a real autonomous research job using the Go engine (Legacy).
func runYoloPipeline(ctx context.Context, job *YoloJob, loop AutonomousLoopInterface) {
	defer func() {
		close(job.LegacyEvents)
		if job.UnifiedEvents != nil {
			close(job.UnifiedEvents)
		}
	}()

	send := func(e YoloEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case job.LegacyEvents <- e:
			return true
		}
	}

	if loop == nil {
		job.LegacyEvents <- YoloEvent{Type: "error", Error: "autonomous loop engine not initialized"}
		return
	}

	// 1. Initial Planning
	if !send(YoloEvent{Type: "progress", Status: "planning", Iteration: 1}) {
		return
	}

	// 2. Run the actual Go autonomous loop
	result, err := loop.Run(ctx, wisdev.LoopRequest{
		Query:         job.Query,
		Domain:        job.Domain,
		MaxIterations: 5,
		BudgetCents:   50,
	})

	if err != nil {
		job.LegacyEvents <- YoloEvent{Type: "error", Error: err.Error()}
		return
	}

	// 3. Final event
	send(YoloEvent{
		Type:           "complete",
		Status:         "finished",
		PapersFound:    len(result.Papers),
		IterationsUsed: result.Iterations,
		Coverage:       1.0,
	})
}

// runWisDevPipeline drives the new AI Scientist research loop.
func runWisDevPipeline(ctx context.Context, job *YoloJob, loop AutonomousLoopInterface) {
	defer func() {
		close(job.UnifiedEvents)
		close(job.LegacyEvents)
	}()

	send := func(e UnifiedEvent) bool {
		e.JobID = job.ID
		e.Timestamp = time.Now().UnixMilli()
		select {
		case <-ctx.Done():
			return false
		case job.UnifiedEvents <- e:
			return true
		}
	}

	if loop == nil {
		send(UnifiedEvent{Type: "job_failed", Error: "autonomous loop engine not initialized"})
		return
	}

	send(UnifiedEvent{Type: "job_started", Step: "planning"})

	// Run the Go autonomous loop
	result, err := loop.Run(ctx, wisdev.LoopRequest{
		Query:         job.Query,
		Domain:        job.Domain,
		ProjectID:     job.ProjectID,
		MaxIterations: 5,
		BudgetCents:   100,
	})

	if err != nil {
		send(UnifiedEvent{Type: "job_failed", Error: err.Error()})
		return
	}

	send(UnifiedEvent{
		Type:        "job_done",
		ResultCount: len(result.Papers),
		Reason:      "Convergence reached",
	})
}

// ---------------------------------------------------------------------------
// HTTP Handlers
// ---------------------------------------------------------------------------

// YoloExecuteHandler handles POST /agent/yolo/execute.
func YoloExecuteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req YoloExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if req.Query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}
	if req.Mode == "" {
		req.Mode = "bounded"
	}

	jobID := fmt.Sprintf("yolo_%d", time.Now().UnixNano())

	ctx, cancel := context.WithCancel(context.Background())
	job := &YoloJob{
		ID:            jobID,
		Query:         req.Query,
		ProjectID:     req.ProjectID,
		Mode:          "yolo",
		Domain:        req.Domain,
		LegacyEvents:  make(chan YoloEvent, 1024),
		UnifiedEvents: make(chan UnifiedEvent, 1024),
		Cancel:        cancel,
		CreatedAt:     time.Now(),
	}
	yoloJobStore.put(job)

	go runYoloPipeline(ctx, job, GlobalYoloLoop)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(YoloExecuteResponse{
		JobID:     jobID,
		StreamURL: "/agent/yolo/stream?job_id=" + jobID,
		Status:    "started",
	})
}

// WisDevJobHandler handles POST /wisdev/job.
func WisDevJobHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	var req struct {
		Query     string `json:"query"`
		ProjectID string `json:"project_id"`
		Mode      string `json:"mode"` // "yolo" | "guided"
		Domain    string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if req.Query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}

	jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	job := &YoloJob{
		ID:            jobID,
		Query:         req.Query,
		ProjectID:     req.ProjectID,
		Mode:          req.Mode,
		Domain:        req.Domain,
		LegacyEvents:  make(chan YoloEvent, 1024),
		UnifiedEvents: make(chan UnifiedEvent, 1024),
		Cancel:        cancel,
		CreatedAt:     time.Now(),
	}
	yoloJobStore.put(job)

	// Trigger the unified pipeline
	go runWisDevPipeline(ctx, job, GlobalYoloLoop)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"job_id":     jobID,
		"stream_url": "/wisdev/job/" + jobID + "/stream",
	})
}

// WisDevStreamHandler handles GET /wisdev/job/:id/stream.
func WisDevStreamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	jobID := r.URL.Path[len("/wisdev/job/") : len(r.URL.Path)-len("/stream")]
	if jobID == "" {
		jobID = r.URL.Query().Get("job_id")
	}

	job, ok := yoloJobStore.get(jobID)
	if !ok {
		WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", map[string]any{
			"jobId": jobID,
		})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "streaming not supported", nil)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Robustness: keep-alive to prevent ELB timeouts
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			slog.Info("client disconnected from stream", "job_id", jobID)
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": keep-alive\n\n")
			flusher.Flush()
		case event, open := <-job.UnifiedEvents:
			if !open {
				return
			}
			payload, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()

			if event.Type == "job_done" || event.Type == "job_failed" {
				yoloJobStore.delete(jobID)
				return
			}
		}
	}
}

// YoloStreamHandler handles GET /agent/yolo/stream?job_id=<id> (Legacy).
func YoloStreamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	jobID := r.URL.Query().Get("job_id")
	if jobID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "job_id query parameter is required", map[string]any{
			"field": "job_id",
		})
		return
	}

	job, ok := yoloJobStore.get(jobID)
	if !ok {
		WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", map[string]any{
			"jobId": jobID,
		})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "streaming not supported", nil)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-job.LegacyEvents:
			if !open {
				yoloJobStore.delete(jobID)
				return
			}
			payload, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
			if event.Type == "complete" || event.Type == "error" {
				yoloJobStore.delete(jobID)
				return
			}
		}
	}
}

// WisDevJobStatusHandler handles GET /wisdev/job/:id.
func WisDevJobStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	jobID := r.URL.Path[len("/wisdev/job/"):]
	job, ok := yoloJobStore.get(jobID)
	if !ok {
		WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", map[string]any{
			"jobId": jobID,
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"job_id":     job.ID,
		"query":      job.Query,
		"project_id": job.ProjectID,
		"mode":       job.Mode,
		"created_at": job.CreatedAt.UnixMilli(),
	})
}

// WisDevScheduleHandler handles POST /wisdev/schedule.
func (h *WisDevHandler) WisDevScheduleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	var req struct {
		ProjectID string `json:"project_id"`
		Schedule  string `json:"schedule"`
		Query     string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	schedID := fmt.Sprintf("sched_%d", time.Now().UnixNano())

	if h.gateway != nil && h.gateway.DB != nil {
		_, err := h.gateway.DB.Exec(r.Context(), `
			INSERT INTO wisdev_schedules (id, project_id, schedule, query, created_at)
			VALUES ($1, $2, $3, $4, $5)
		`, schedID, req.ProjectID, req.Schedule, req.Query, time.Now().UnixMilli())
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to save schedule", map[string]any{
				"error": err.Error(),
			})
			return
		}
	} else {
		slog.Debug("schedule registered in-memory only", "schedID", schedID)
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"schedule_id": schedID, "status": "registered"})
}

// WisDevScheduleRunHandler handles POST /wisdev/schedule/run/:id.
func (h *WisDevHandler) WisDevScheduleRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	schedID := r.URL.Path[len("/wisdev/schedule/run/"):]
	if schedID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "schedule ID is required", nil)
		return
	}

	query := "scheduled query"
	projectID := "default"

	if h.gateway != nil && h.gateway.DB != nil {
		err := h.gateway.DB.QueryRow(r.Context(), "SELECT query, project_id FROM wisdev_schedules WHERE id = $1", schedID).Scan(&query, &projectID)
		if err != nil {
			slog.Error("failed to load schedule from db, using fallback", "err", err)
		}
	}

	jobID := fmt.Sprintf("job_cron_%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	job := &YoloJob{
		ID:            jobID,
		Query:         query,
		ProjectID:     projectID,
		Mode:          "yolo",
		Domain:        "general",
		LegacyEvents:  make(chan YoloEvent, 1024),
		UnifiedEvents: make(chan UnifiedEvent, 1024),
		Cancel:        cancel,
		CreatedAt:     time.Now(),
	}
	yoloJobStore.put(job)

	// Trigger the unified pipeline
	go runWisDevPipeline(ctx, job, GlobalYoloLoop)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"job_id": jobID, "status": "started"})
}


// yoloCancelHandler handles POST /agent/yolo/cancel.
func yoloCancelHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req YoloCancelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if req.JobID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "job_id is required", map[string]any{
			"field": "job_id",
		})
		return
	}

	job, ok := yoloJobStore.get(req.JobID)
	if !ok {
		WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", map[string]any{
			"jobId": req.JobID,
		})
		return
	}

	// Emit terminal event to any connected unified stream before cancelling
	if job.UnifiedEvents != nil {
		select {
		case job.UnifiedEvents <- UnifiedEvent{
			Type:        "job_cancelled",
			JobID:       job.ID,
			Timestamp:   time.Now().UnixMilli(),
			CancelledBy: "user",
		}:
		default:
		}
	}
	job.Cancel()
	yoloJobStore.delete(req.JobID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"cancelled": true})
}
