package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

type streamTicker struct {
	C    <-chan time.Time
	Stop func()
}

var newStreamTicker = func(d time.Duration) streamTicker {
	ticker := time.NewTicker(d)
	return streamTicker{
		C:    ticker.C,
		Stop: ticker.Stop,
	}
}

const terminalYoloJobRetention = 5 * time.Minute

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
	TraceID   string `json:"traceId,omitempty"`
	Status    string `json:"status"` // "started"
}

// YoloCancelRequest is the request body for POST /agent/yolo/cancel.
type YoloCancelRequest struct {
	JobID string `json:"job_id"`
}

// YoloEvent is a single SSE payload emitted during a YOLO pipeline run (Legacy).
type YoloEvent struct {
	Type           string  `json:"type"`
	TraceID        string  `json:"traceId,omitempty"`
	Iteration      int     `json:"iteration,omitempty"`
	Status         string  `json:"status,omitempty"`
	Coverage       float64 `json:"coverage,omitempty"`
	PapersFound    int     `json:"papers_found,omitempty"`
	IterationsUsed int     `json:"iterations_used,omitempty"`
	Error          string  `json:"error,omitempty"`
}

// UnifiedEvent is the new canonical SSE schema for /wisdev/job/:id/stream.
type UnifiedEvent struct {
	Type           string                  `json:"type"`
	JobID          string                  `json:"job_id"`
	Timestamp      int64                   `json:"timestamp"`
	TraceID        string                  `json:"traceId,omitempty"`
	Step           string                  `json:"step,omitempty"`
	StepID         string                  `json:"stepId,omitempty"`
	Message        string                  `json:"message,omitempty"`
	ResultCount    int                     `json:"result_count,omitempty"`
	Attempt        int                     `json:"attempt,omitempty"`
	Error          string                  `json:"error,omitempty"`
	Severity       string                  `json:"severity,omitempty"`
	FindingA       string                  `json:"finding_a,omitempty"`
	FindingB       string                  `json:"finding_b,omitempty"`
	SkillName      string                  `json:"skill_name,omitempty"`
	SourcePaper    string                  `json:"source_paper,omitempty"`
	DossierID      string                  `json:"dossier_id,omitempty"`
	Reason         string                  `json:"reason,omitempty"`
	CancelledBy    string                  `json:"cancelled_by,omitempty"`
	Mode           string                  `json:"mode,omitempty"`
	ServiceTier    string                  `json:"serviceTier,omitempty"`
	Payload        map[string]any          `json:"payload,omitempty"`
	ReasoningGraph *wisdev.ReasoningGraph  `json:"reasoningGraph,omitempty"`
	MemoryTiers    *wisdev.MemoryTierState `json:"memoryTiers,omitempty"`
	Hypotheses     any                     `json:"hypotheses,omitempty"`
	Synthesis      any                     `json:"synthesis,omitempty"`
}

// YoloJob tracks the state of a single YOLO or Guided pipeline run.
type YoloJob struct {
	ID                            string
	TraceID                       string
	UserID                        string
	Query                         string
	ProjectID                     string
	Status                        string
	Mode                          string // "yolo" | "guided"
	Domain                        string
	LegacyEvents                  chan YoloEvent
	UnifiedEvents                 chan UnifiedEvent
	Cancel                        context.CancelFunc
	CreatedAt                     time.Time
	RetainUntil                   time.Time
	mu                            sync.RWMutex
	terminalLegacyEvent           *YoloEvent
	terminalUnifiedEvent          *UnifiedEvent
	terminalLegacyEventJournaled  bool
	terminalUnifiedEventJournaled bool
}

func (j *YoloJob) setTerminalLegacyEvent(event YoloEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	cloned := event
	j.terminalLegacyEvent = &cloned
	j.Status = statusFromLegacyTerminalEvent(cloned.Type)
	j.RetainUntil = time.Now().Add(terminalYoloJobRetention)
}

func (j *YoloJob) terminalLegacyEventSnapshot() (YoloEvent, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.terminalLegacyEvent == nil {
		return YoloEvent{}, false
	}
	cloned := *j.terminalLegacyEvent
	return cloned, true
}

func (j *YoloJob) setTerminalUnifiedEvent(event UnifiedEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	cloned := event
	cloned.Payload = cloneUnifiedPayload(event.Payload)
	j.terminalUnifiedEvent = &cloned
	j.Status = statusFromUnifiedTerminalEvent(cloned.Type)
	j.RetainUntil = time.Now().Add(terminalYoloJobRetention)
}

func (j *YoloJob) terminalUnifiedEventSnapshot() (UnifiedEvent, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.terminalUnifiedEvent == nil {
		return UnifiedEvent{}, false
	}
	cloned := *j.terminalUnifiedEvent
	cloned.Payload = cloneUnifiedPayload(cloned.Payload)
	return cloned, true
}

func (j *YoloJob) statusSnapshot() string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if status := strings.TrimSpace(j.Status); status != "" {
		return status
	}
	return "running"
}

func (j *YoloJob) retentionExpired(now time.Time) bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return !j.RetainUntil.IsZero() && !now.Before(j.RetainUntil)
}

func (j *YoloJob) claimUnifiedTerminalJournalAppend() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.terminalUnifiedEventJournaled {
		return false
	}
	j.terminalUnifiedEventJournaled = true
	return true
}

func (j *YoloJob) claimLegacyTerminalJournalAppend() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.terminalLegacyEventJournaled {
		return false
	}
	j.terminalLegacyEventJournaled = true
	return true
}

// ---------------------------------------------------------------------------
// In-memory job store
// ---------------------------------------------------------------------------

// yoloJobStore is the process-level store for active YOLO jobs.
var yoloJobStore = &yoloStore{
	jobs: make(map[string]*YoloJob),
}

type AutonomousLoopInterface interface {
	Run(ctx context.Context, req wisdev.LoopRequest, onEvent ...func(wisdev.PlanExecutionEvent)) (*wisdev.LoopResult, error)
}

var GlobalYoloLoop AutonomousLoopInterface
var GlobalYoloGateway *wisdev.AgentGateway

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
	job, ok := s.jobs[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if job.retentionExpired(time.Now()) {
		s.delete(id)
		return nil, false
	}
	return job, ok
}

func isLegacyTerminalEventType(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "complete", "error", "cancelled":
		return true
	default:
		return false
	}
}

func isUnifiedTerminalEventType(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "job_done", "job_failed", "job_cancelled":
		return true
	default:
		return false
	}
}

func statusFromLegacyTerminalEvent(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "complete":
		return "completed"
	case "cancelled":
		return "cancelled"
	default:
		return "failed"
	}
}

func statusFromUnifiedTerminalEvent(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "job_done":
		return "completed"
	case "job_cancelled":
		return "cancelled"
	default:
		return "failed"
	}
}

func journalStatusFromUnifiedEventType(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "job_done":
		return "completed"
	case "job_cancelled":
		return "cancelled"
	case "job_failed":
		return "failed"
	default:
		return "running"
	}
}

func journalStatusFromLegacyEventType(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "complete":
		return "completed"
	case "cancelled":
		return "cancelled"
	case "error":
		return "failed"
	default:
		return "running"
	}
}

func buildLegacyCancelledEvent(traceID string) YoloEvent {
	return YoloEvent{
		Type:    "cancelled",
		Status:  "cancelled",
		Error:   "autonomous research cancelled",
		TraceID: strings.TrimSpace(traceID),
	}
}

func markSyntheticUnifiedTerminalEvent(event UnifiedEvent, reason string) UnifiedEvent {
	cloned := event
	if cloned.Timestamp == 0 {
		cloned.Timestamp = time.Now().UnixMilli()
	}
	payload := cloneUnifiedPayload(cloned.Payload)
	if payload == nil {
		payload = make(map[string]any, 3)
	}
	payload["synthetic"] = true
	payload["sessionTerminal"] = true
	if trimmedReason := strings.TrimSpace(reason); trimmedReason != "" {
		payload["syntheticReason"] = trimmedReason
	}
	cloned.Payload = payload
	return cloned
}

func buildSyntheticLegacyStreamClosedEvent(job *YoloJob) YoloEvent {
	traceID := strings.TrimSpace(job.TraceID)
	if traceID == "" {
		traceID = wisdev.NewTraceID()
		job.TraceID = traceID
	}
	return YoloEvent{
		Type:    "error",
		Status:  "error",
		Error:   "job stream closed before terminal event was emitted",
		TraceID: traceID,
	}
}

func buildLegacyStateLostEvent(job *YoloJob) YoloEvent {
	traceID := strings.TrimSpace(job.TraceID)
	if traceID == "" {
		traceID = wisdev.NewTraceID()
		job.TraceID = traceID
	}
	return YoloEvent{
		Type:    "error",
		Status:  "error",
		Error:   "JOB_STATE_LOST: autonomous job state was lost before terminal delivery",
		TraceID: traceID,
	}
}

func buildLegacyJournalPayload(event YoloEvent, createdAt int64) map[string]any {
	payload := map[string]any{
		"type":      strings.TrimSpace(event.Type),
		"timestamp": createdAt,
	}
	if trimmed := strings.TrimSpace(event.TraceID); trimmed != "" {
		payload["traceId"] = trimmed
	}
	if event.Iteration > 0 {
		payload["iteration"] = event.Iteration
	}
	if trimmed := strings.TrimSpace(event.Status); trimmed != "" {
		payload["status"] = trimmed
	}
	if event.Coverage > 0 {
		payload["coverage"] = event.Coverage
	}
	if event.PapersFound > 0 {
		payload["papers_found"] = event.PapersFound
	}
	if event.IterationsUsed > 0 {
		payload["iterations_used"] = event.IterationsUsed
	}
	if trimmed := strings.TrimSpace(event.Error); trimmed != "" {
		payload["error"] = trimmed
	}
	return payload
}

func buildSyntheticUnifiedStreamClosedEvent(job *YoloJob) UnifiedEvent {
	traceID := strings.TrimSpace(job.TraceID)
	if traceID == "" {
		traceID = wisdev.NewTraceID()
		job.TraceID = traceID
	}
	return buildUnifiedFailureEvent(
		job.ID,
		normalizeWisDevJobMode(job.Mode),
		traceID,
		"JOB_STREAM_CLOSED",
		"job stream closed before terminal event was emitted",
	)
}

func buildUnifiedJournalPayload(event UnifiedEvent) map[string]any {
	payload := map[string]any{
		"type":      strings.TrimSpace(event.Type),
		"job_id":    strings.TrimSpace(event.JobID),
		"timestamp": event.Timestamp,
	}
	if trimmed := strings.TrimSpace(event.TraceID); trimmed != "" {
		payload["traceId"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.Step); trimmed != "" {
		payload["step"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.StepID); trimmed != "" {
		payload["stepId"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.Message); trimmed != "" {
		payload["message"] = trimmed
	}
	if event.ResultCount > 0 {
		payload["result_count"] = event.ResultCount
	}
	if event.Attempt > 0 {
		payload["attempt"] = event.Attempt
	}
	if trimmed := strings.TrimSpace(event.Error); trimmed != "" {
		payload["error"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.Severity); trimmed != "" {
		payload["severity"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.FindingA); trimmed != "" {
		payload["finding_a"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.FindingB); trimmed != "" {
		payload["finding_b"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.SkillName); trimmed != "" {
		payload["skill_name"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.SourcePaper); trimmed != "" {
		payload["source_paper"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.DossierID); trimmed != "" {
		payload["dossier_id"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.Reason); trimmed != "" {
		payload["reason"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.CancelledBy); trimmed != "" {
		payload["cancelled_by"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.Mode); trimmed != "" {
		payload["mode"] = trimmed
	}
	if trimmed := strings.TrimSpace(event.ServiceTier); trimmed != "" {
		payload["serviceTier"] = trimmed
	}
	if event.Payload != nil {
		payload["payload"] = cloneUnifiedPayload(event.Payload)
	}
	if event.ReasoningGraph != nil {
		payload["reasoningGraph"] = event.ReasoningGraph
	}
	if event.MemoryTiers != nil {
		payload["memoryTiers"] = event.MemoryTiers
	}
	if event.Hypotheses != nil {
		payload["hypotheses"] = event.Hypotheses
	}
	if event.Synthesis != nil {
		payload["synthesis"] = event.Synthesis
	}
	return payload
}

func appendUnifiedAutonomousJournalEvent(job *YoloJob, event UnifiedEvent) {
	if job == nil || GlobalYoloGateway == nil || GlobalYoloGateway.Journal == nil {
		return
	}
	if isUnifiedTerminalEventType(event.Type) && !job.claimUnifiedTerminalJournalAppend() {
		return
	}
	normalized := event
	if strings.TrimSpace(normalized.JobID) == "" {
		normalized.JobID = strings.TrimSpace(job.ID)
	}
	if strings.TrimSpace(normalized.TraceID) == "" {
		normalized.TraceID = strings.TrimSpace(job.TraceID)
	}
	if normalized.Timestamp == 0 {
		normalized.Timestamp = time.Now().UnixMilli()
	}
	mode := strings.TrimSpace(normalized.Mode)
	if mode == "" {
		mode = normalizeWisDevJobMode(job.Mode)
	}
	summary := strings.TrimSpace(normalized.Message)
	if summary == "" {
		summary = strings.ReplaceAll(strings.TrimSpace(normalized.Type), "_", " ")
	}

	metadata := map[string]any{
		"source": "wisdev_autonomous_job",
		"jobId":  strings.TrimSpace(job.ID),
		"mode":   mode,
		"userId": strings.TrimSpace(job.UserID),
	}
	if payload := normalized.Payload; payload != nil {
		if synthetic, ok := payload["synthetic"].(bool); ok {
			metadata["synthetic"] = synthetic
		}
		if reason, ok := payload["syntheticReason"].(string); ok && strings.TrimSpace(reason) != "" {
			metadata["syntheticReason"] = strings.TrimSpace(reason)
		}
	}

	GlobalYoloGateway.Journal.Append(wisdev.RuntimeJournalEntry{
		EventID:   wisdev.NewTraceID(),
		TraceID:   strings.TrimSpace(normalized.TraceID),
		SessionID: strings.TrimSpace(job.ProjectID),
		UserID:    strings.TrimSpace(job.UserID),
		StepID:    strings.TrimSpace(normalized.StepID),
		EventType: strings.TrimSpace(normalized.Type),
		Path:      fmt.Sprintf("/wisdev/job/%s/stream", strings.TrimSpace(job.ID)),
		Status:    journalStatusFromUnifiedEventType(normalized.Type),
		CreatedAt: normalized.Timestamp,
		Summary:   summary,
		Payload:   buildUnifiedJournalPayload(normalized),
		Metadata:  metadata,
	})
}

func appendLegacyAutonomousJournalEvent(job *YoloJob, event YoloEvent) {
	if job == nil || GlobalYoloGateway == nil || GlobalYoloGateway.Journal == nil {
		return
	}
	if isLegacyTerminalEventType(event.Type) && !job.claimLegacyTerminalJournalAppend() {
		return
	}
	normalized := event
	if strings.TrimSpace(normalized.TraceID) == "" {
		normalized.TraceID = strings.TrimSpace(job.TraceID)
	}
	createdAt := time.Now().UnixMilli()
	summary := strings.TrimSpace(normalized.Error)
	if summary == "" {
		summary = strings.TrimSpace(normalized.Status)
	}
	if summary == "" {
		summary = strings.ReplaceAll(strings.TrimSpace(normalized.Type), "_", " ")
	}
	payload := buildLegacyJournalPayload(normalized, createdAt)
	payload["job_id"] = strings.TrimSpace(job.ID)
	payload["mode"] = firstNonEmptyTrimmed(strings.TrimSpace(job.Mode), "yolo")
	payload["query"] = strings.TrimSpace(job.Query)
	payload["project_id"] = strings.TrimSpace(job.ProjectID)
	payload["userId"] = strings.TrimSpace(job.UserID)

	GlobalYoloGateway.Journal.Append(wisdev.RuntimeJournalEntry{
		EventID:   wisdev.NewTraceID(),
		TraceID:   strings.TrimSpace(normalized.TraceID),
		SessionID: strings.TrimSpace(job.ProjectID),
		UserID:    strings.TrimSpace(job.UserID),
		EventType: strings.TrimSpace(normalized.Type),
		Path:      fmt.Sprintf("/agent/yolo/stream?job_id=%s", strings.TrimSpace(job.ID)),
		Status:    journalStatusFromLegacyEventType(normalized.Type),
		CreatedAt: createdAt,
		Summary:   summary,
		Payload:   payload,
		Metadata: map[string]any{
			"source": "legacy_autonomous_job",
			"jobId":  strings.TrimSpace(job.ID),
			"mode":   firstNonEmptyTrimmed(strings.TrimSpace(job.Mode), "yolo"),
			"userId": strings.TrimSpace(job.UserID),
		},
	})
}

func appendWisDevJobRegistrationJournalEvent(job *YoloJob) {
	if job == nil || GlobalYoloGateway == nil || GlobalYoloGateway.Journal == nil {
		return
	}
	mode := normalizeWisDevJobMode(job.Mode)
	traceID := strings.TrimSpace(job.TraceID)
	if traceID == "" {
		traceID = wisdev.NewTraceID()
		job.TraceID = traceID
	}
	createdAt := job.CreatedAt.UnixMilli()
	if createdAt == 0 {
		createdAt = time.Now().UnixMilli()
	}
	GlobalYoloGateway.Journal.Append(wisdev.RuntimeJournalEntry{
		EventID:   wisdev.NewTraceID(),
		TraceID:   traceID,
		SessionID: strings.TrimSpace(job.ProjectID),
		UserID:    strings.TrimSpace(job.UserID),
		EventType: "job_registered",
		Path:      "/wisdev/job",
		Status:    "running",
		CreatedAt: createdAt,
		Summary:   "autonomous job accepted",
		Payload: map[string]any{
			"type":       "job_registered",
			"job_id":     strings.TrimSpace(job.ID),
			"traceId":    traceID,
			"mode":       mode,
			"project_id": strings.TrimSpace(job.ProjectID),
			"userId":     strings.TrimSpace(job.UserID),
			"query":      strings.TrimSpace(job.Query),
			"timestamp":  createdAt,
		},
		Metadata: map[string]any{
			"source": "wisdev_autonomous_job",
			"jobId":  strings.TrimSpace(job.ID),
			"mode":   mode,
			"userId": strings.TrimSpace(job.UserID),
		},
	})
}

func appendLegacyYoloRegistrationJournalEvent(job *YoloJob) {
	if job == nil || GlobalYoloGateway == nil || GlobalYoloGateway.Journal == nil {
		return
	}
	traceID := strings.TrimSpace(job.TraceID)
	if traceID == "" {
		traceID = wisdev.NewTraceID()
		job.TraceID = traceID
	}
	createdAt := job.CreatedAt.UnixMilli()
	if createdAt == 0 {
		createdAt = time.Now().UnixMilli()
	}
	GlobalYoloGateway.Journal.Append(wisdev.RuntimeJournalEntry{
		EventID:   wisdev.NewTraceID(),
		TraceID:   traceID,
		SessionID: strings.TrimSpace(job.ProjectID),
		UserID:    strings.TrimSpace(job.UserID),
		EventType: "job_registered",
		Path:      "/agent/yolo/execute",
		Status:    "running",
		CreatedAt: createdAt,
		Summary:   "legacy autonomous job accepted",
		Payload: map[string]any{
			"type":       "job_registered",
			"job_id":     strings.TrimSpace(job.ID),
			"traceId":    traceID,
			"mode":       firstNonEmptyTrimmed(strings.TrimSpace(job.Mode), "yolo"),
			"project_id": strings.TrimSpace(job.ProjectID),
			"userId":     strings.TrimSpace(job.UserID),
			"query":      strings.TrimSpace(job.Query),
			"timestamp":  createdAt,
		},
		Metadata: map[string]any{
			"source": "legacy_autonomous_job",
			"jobId":  strings.TrimSpace(job.ID),
			"mode":   firstNonEmptyTrimmed(strings.TrimSpace(job.Mode), "yolo"),
			"userId": strings.TrimSpace(job.UserID),
		},
	})
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func newWisDevJobID(prefix string) string {
	id := strings.TrimPrefix(strings.TrimSpace(wisdev.NewTraceID()), "trace_")
	if id == "" || id == "fallback" {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "job"
	}
	return prefix + "_" + id
}

func requireWisDevJobOwnerAccess(w http.ResponseWriter, r *http.Request, ownerID string) bool {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || ownerID == "anonymous" {
		callerID := strings.TrimSpace(GetUserID(r))
		if callerID == "admin" || callerID == "internal-service" {
			return true
		}
		WriteError(w, http.StatusForbidden, ErrUnauthorized, "job owner unavailable", nil)
		return false
	}
	return requireOwnerAccess(w, r, ownerID)
}

func requireWisDevJobAccess(w http.ResponseWriter, r *http.Request, job *YoloJob) bool {
	if job == nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", nil)
		return false
	}
	return requireWisDevJobOwnerAccess(w, r, job.UserID)
}

func requirePersistedResearchJobAccess(w http.ResponseWriter, r *http.Request, payload map[string]any) bool {
	if len(payload) == 0 {
		WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", nil)
		return false
	}
	return requireWisDevJobOwnerAccess(w, r, wisdev.AsOptionalString(payload["userId"]))
}

func firstNonNilValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func anyToInt64(value any, fallback int64) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
	case string:
		var parsed int64
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

func decodeReasoningGraph(raw any) *wisdev.ReasoningGraph {
	if raw == nil {
		return nil
	}
	if graph, ok := raw.(*wisdev.ReasoningGraph); ok {
		return graph
	}
	encoded, err := json.Marshal(raw)
	if err != nil || len(encoded) == 0 || string(encoded) == "null" {
		return nil
	}
	var decoded wisdev.ReasoningGraph
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	return &decoded
}

func decodeMemoryTiers(raw any) *wisdev.MemoryTierState {
	if raw == nil {
		return nil
	}
	if tiers, ok := raw.(*wisdev.MemoryTierState); ok {
		return tiers
	}
	encoded, err := json.Marshal(raw)
	if err != nil || len(encoded) == 0 || string(encoded) == "null" {
		return nil
	}
	var decoded wisdev.MemoryTierState
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	return &decoded
}

func buildUnifiedEventFromJournalEntry(entry wisdev.RuntimeJournalEntry) UnifiedEvent {
	payload := cloneUnifiedPayload(entry.Payload)
	nestedPayload := map[string]any(nil)
	if rawPayload, ok := payload["payload"].(map[string]any); ok {
		nestedPayload = cloneUnifiedPayload(rawPayload)
	}
	traceID := firstNonEmptyTrimmed(
		wisdev.AsOptionalString(payload["traceId"]),
		entry.TraceID,
	)
	mode := firstNonEmptyTrimmed(
		wisdev.AsOptionalString(payload["mode"]),
		wisdev.AsOptionalString(entry.Metadata["mode"]),
	)
	return UnifiedEvent{
		Type:        firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["type"]), entry.EventType),
		JobID:       firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["job_id"]), wisdev.AsOptionalString(payload["jobId"]), wisdev.AsOptionalString(entry.Metadata["jobId"])),
		Timestamp:   anyToInt64(payload["timestamp"], entry.CreatedAt),
		TraceID:     traceID,
		Step:        firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["step"]), wisdev.AsOptionalString(entry.Payload["step"]), entry.StepID),
		StepID:      firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["stepId"]), wisdev.AsOptionalString(entry.Payload["stepId"]), entry.StepID),
		Message:     firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["message"]), entry.Summary),
		ResultCount: int(anyToInt64(payload["result_count"], 0)),
		Attempt:     int(anyToInt64(payload["attempt"], 0)),
		Error:       firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["error"])),
		Severity:    firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["severity"])),
		FindingA:    firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["finding_a"])),
		FindingB:    firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["finding_b"])),
		SkillName:   firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["skill_name"])),
		SourcePaper: firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["source_paper"])),
		DossierID:   firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["dossier_id"])),
		Reason:      firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["reason"])),
		CancelledBy: firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["cancelled_by"])),
		Mode:        mode,
		ServiceTier: firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["serviceTier"])),
		Payload:     nestedPayload,
		ReasoningGraph: decodeReasoningGraph(
			firstNonNilValue(payload["reasoningGraph"], payload["reasoning_graph"]),
		),
		MemoryTiers: decodeMemoryTiers(
			firstNonNilValue(payload["memoryTiers"], payload["memory_tiers"]),
		),
		Hypotheses: payload["hypotheses"],
		Synthesis:  payload["synthesis"],
	}
}

func buildUnifiedStateLostEvent(job *YoloJob) UnifiedEvent {
	traceID := strings.TrimSpace(job.TraceID)
	if traceID == "" {
		traceID = wisdev.NewTraceID()
		job.TraceID = traceID
	}
	event := buildUnifiedFailureEvent(
		job.ID,
		normalizeWisDevJobMode(job.Mode),
		traceID,
		"JOB_STATE_LOST",
		"autonomous job state was lost before terminal delivery",
	)
	payload := cloneUnifiedPayload(event.Payload)
	if payload == nil {
		payload = map[string]any{}
	}
	payload["synthetic"] = true
	payload["syntheticReason"] = "job_state_lost_after_process_restart"
	event.Payload = payload
	return event
}

func buildLegacyEventFromJournalEntry(entry wisdev.RuntimeJournalEntry) YoloEvent {
	payload := entry.Payload
	return YoloEvent{
		Type:           firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["type"]), entry.EventType),
		TraceID:        firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["traceId"]), entry.TraceID),
		Iteration:      int(anyToInt64(payload["iteration"], 0)),
		Status:         firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["status"]), entry.Status),
		Coverage:       float64(anyToInt64(payload["coverage"], 0)),
		PapersFound:    int(anyToInt64(payload["papers_found"], 0)),
		IterationsUsed: int(anyToInt64(payload["iterations_used"], 0)),
		Error:          firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["error"]), entry.Summary),
	}
}

func readLegacyYoloJournalEntries(jobID string) []wisdev.RuntimeJournalEntry {
	normalizedJobID := strings.TrimSpace(jobID)
	if normalizedJobID == "" || GlobalYoloGateway == nil || GlobalYoloGateway.Journal == nil {
		return nil
	}
	entries := GlobalYoloGateway.Journal.ReadJob(normalizedJobID, 32)
	if len(entries) == 0 {
		return nil
	}
	legacyEntries := make([]wisdev.RuntimeJournalEntry, 0, len(entries))
	for _, entry := range entries {
		source := strings.TrimSpace(wisdev.AsOptionalString(entry.Metadata["source"]))
		if source == "legacy_autonomous_job" || strings.HasPrefix(strings.TrimSpace(entry.Path), "/agent/yolo") {
			legacyEntries = append(legacyEntries, entry)
		}
	}
	return legacyEntries
}

func recoverWisDevJobFromJournal(jobID string) (*YoloJob, bool) {
	normalizedJobID := strings.TrimSpace(jobID)
	if normalizedJobID == "" || GlobalYoloGateway == nil || GlobalYoloGateway.Journal == nil {
		return nil, false
	}
	entries := GlobalYoloGateway.Journal.ReadJob(normalizedJobID, 32)
	if len(entries) == 0 {
		return nil, false
	}
	firstEntry := entries[0]
	lastEntry := entries[len(entries)-1]
	latestEvent := buildUnifiedEventFromJournalEntry(lastEntry)
	job := &YoloJob{
		ID:        normalizedJobID,
		TraceID:   firstNonEmptyTrimmed(latestEvent.TraceID, lastEntry.TraceID, firstEntry.TraceID),
		UserID:    firstNonEmptyTrimmed(lastEntry.UserID, firstEntry.UserID, wisdev.AsOptionalString(lastEntry.Payload["userId"]), wisdev.AsOptionalString(firstEntry.Payload["userId"]), wisdev.AsOptionalString(lastEntry.Metadata["userId"]), wisdev.AsOptionalString(firstEntry.Metadata["userId"])),
		Query:     firstNonEmptyTrimmed(wisdev.AsOptionalString(firstEntry.Payload["query"])),
		ProjectID: firstNonEmptyTrimmed(lastEntry.SessionID, firstEntry.SessionID),
		Status:    firstNonEmptyTrimmed(lastEntry.Status),
		Mode:      firstNonEmptyTrimmed(latestEvent.Mode, wisdev.AsOptionalString(lastEntry.Metadata["mode"]), wisdev.AsOptionalString(firstEntry.Metadata["mode"])),
		CreatedAt: time.UnixMilli(firstEntry.CreatedAt),
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}
	if job.Mode == "" {
		job.Mode = string(wisdev.WisDevModeYOLO)
	}
	if isUnifiedTerminalEventType(latestEvent.Type) {
		job.setTerminalUnifiedEvent(latestEvent)
		return job, true
	}
	synthetic := buildUnifiedStateLostEvent(job)
	job.setTerminalUnifiedEvent(synthetic)
	return job, true
}

func recoverLegacyYoloJobFromEntries(jobID string, legacyEntries []wisdev.RuntimeJournalEntry) (*YoloJob, bool) {
	normalizedJobID := strings.TrimSpace(jobID)
	if len(legacyEntries) == 0 {
		return nil, false
	}
	firstEntry := legacyEntries[0]
	lastEntry := legacyEntries[len(legacyEntries)-1]
	latestEvent := buildLegacyEventFromJournalEntry(lastEntry)
	job := &YoloJob{
		ID:        normalizedJobID,
		TraceID:   firstNonEmptyTrimmed(latestEvent.TraceID, lastEntry.TraceID, firstEntry.TraceID),
		UserID:    firstNonEmptyTrimmed(lastEntry.UserID, firstEntry.UserID, wisdev.AsOptionalString(lastEntry.Payload["userId"]), wisdev.AsOptionalString(firstEntry.Payload["userId"]), wisdev.AsOptionalString(lastEntry.Metadata["userId"]), wisdev.AsOptionalString(firstEntry.Metadata["userId"])),
		Query:     firstNonEmptyTrimmed(wisdev.AsOptionalString(firstEntry.Payload["query"])),
		ProjectID: firstNonEmptyTrimmed(lastEntry.SessionID, firstEntry.SessionID),
		Status:    firstNonEmptyTrimmed(lastEntry.Status),
		Mode:      firstNonEmptyTrimmed(wisdev.AsOptionalString(lastEntry.Metadata["mode"]), wisdev.AsOptionalString(firstEntry.Metadata["mode"]), wisdev.AsOptionalString(lastEntry.Payload["mode"]), wisdev.AsOptionalString(firstEntry.Payload["mode"]), "yolo"),
		CreatedAt: time.UnixMilli(firstEntry.CreatedAt),
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}
	if isLegacyTerminalEventType(latestEvent.Type) {
		job.setTerminalLegacyEvent(latestEvent)
		return job, true
	}
	synthetic := buildLegacyStateLostEvent(job)
	job.setTerminalLegacyEvent(synthetic)
	return job, true
}

func recoverLegacyYoloJobFromJournal(jobID string) (*YoloJob, bool) {
	return recoverLegacyYoloJobFromEntries(jobID, readLegacyYoloJournalEntries(jobID))
}

func writeLegacyCancelResponse(w http.ResponseWriter, job *YoloJob, recovered bool) {
	w.Header().Set("Content-Type", "application/json")
	if traceID := strings.TrimSpace(job.TraceID); traceID != "" {
		w.Header().Set("X-Trace-Id", traceID)
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"cancelled": true,
		"traceId":   strings.TrimSpace(job.TraceID),
		"status":    job.statusSnapshot(),
		"recovered": recovered,
	})
}

func writeYoloJobStatusResponse(w http.ResponseWriter, job *YoloJob) {
	w.Header().Set("Content-Type", "application/json")
	if traceID := strings.TrimSpace(job.TraceID); traceID != "" {
		w.Header().Set("X-Trace-Id", traceID)
	}
	response := map[string]any{
		"job_id":     job.ID,
		"traceId":    job.TraceID,
		"query":      job.Query,
		"project_id": job.ProjectID,
		"status":     job.statusSnapshot(),
		"mode":       job.Mode,
		"created_at": job.CreatedAt.UnixMilli(),
	}
	if event, ok := job.terminalUnifiedEventSnapshot(); ok {
		response["lastEvent"] = event
		if event.Payload != nil {
			if durableJob, exists := event.Payload["durableJob"]; exists {
				response["durableJob"] = durableJob
			}
			if sourceAcquisition, exists := event.Payload["sourceAcquisition"]; exists {
				response["sourceAcquisition"] = sourceAcquisition
			}
		}
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func loadPersistedResearchJob(jobID string) (map[string]any, bool) {
	normalizedJobID := strings.TrimSpace(jobID)
	if normalizedJobID == "" || GlobalYoloGateway == nil || GlobalYoloGateway.StateStore == nil {
		return nil, false
	}
	payload, err := GlobalYoloGateway.StateStore.LoadResearchJob(normalizedJobID)
	if err != nil || len(payload) == 0 {
		return nil, false
	}
	return payload, true
}

func writePersistedResearchJobStatusResponse(w http.ResponseWriter, payload map[string]any) {
	if len(payload) == 0 {
		WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", nil)
		return
	}
	if traceID := strings.TrimSpace(wisdev.AsOptionalString(payload["traceId"])); traceID != "" {
		w.Header().Set("X-Trace-Id", traceID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	response := map[string]any{
		"job_id":                firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["jobId"]), wisdev.AsOptionalString(payload["job_id"])),
		"traceId":               wisdev.AsOptionalString(payload["traceId"]),
		"query":                 wisdev.AsOptionalString(payload["query"]),
		"project_id":            wisdev.AsOptionalString(payload["sessionId"]),
		"sessionId":             wisdev.AsOptionalString(payload["sessionId"]),
		"status":                firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["status"]), "unknown"),
		"mode":                  firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["plane"]), wisdev.AsOptionalString(payload["mode"])),
		"created_at":            payload["startedAt"],
		"updated_at":            payload["updatedAt"],
		"durableJob":            payload,
		"budgetUsed":            payload["budgetUsed"],
		"resumeToken":           payload["resumeToken"],
		"source":                "runtime_state_store",
		"replayable":            payload["replayable"],
		"resumable":             payload["resumeSupported"],
		"resumeSupported":       payload["resumeSupported"],
		"cancellable":           payload["cancellationSupported"],
		"cancellationSupported": payload["cancellationSupported"],
		"finalStopReason":       payload["stopReason"],
	}
	if runtimeState := persistedResearchJobRuntimeStatePayload(payload); len(runtimeState) > 0 {
		response["runtimeState"] = runtimeState
		if reasoningRuntime, ok := runtimeState["reasoningRuntime"].(map[string]any); ok && len(reasoningRuntime) > 0 {
			response["reasoningRuntime"] = reasoningRuntime
		}
	}
	_ = json.NewEncoder(w).Encode(response)
}

func persistedResearchJobUnifiedEvent(payload map[string]any) UnifiedEvent {
	status := strings.TrimSpace(wisdev.AsOptionalString(payload["status"]))
	eventType := "job_status"
	switch status {
	case "completed":
		eventType = "job_done"
	case "cancelled":
		eventType = "job_cancelled"
	case "failed":
		eventType = "job_failed"
	}
	timestamp := int64(0)
	if updated, ok := asInt64(payload["updatedAt"]); ok {
		timestamp = updated
	}
	if timestamp == 0 {
		timestamp = time.Now().UnixMilli()
	}
	eventPayload := map[string]any{
		"durableJob": payload,
		"replayed":   true,
		"source":     "runtime_state_store",
	}
	runtimeState := persistedResearchJobRuntimeStatePayload(payload)
	if len(runtimeState) > 0 {
		eventPayload["runtimeState"] = runtimeState
		if reasoningRuntime, ok := runtimeState["reasoningRuntime"].(map[string]any); ok && len(reasoningRuntime) > 0 {
			eventPayload["reasoningRuntime"] = reasoningRuntime
		}
	}
	return UnifiedEvent{
		Type:      eventType,
		JobID:     firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["jobId"]), wisdev.AsOptionalString(payload["job_id"])),
		Timestamp: timestamp,
		TraceID:   wisdev.AsOptionalString(payload["traceId"]),
		Message:   "durable research job state replayed",
		Mode:      wisdev.AsOptionalString(payload["plane"]),
		Reason:    wisdev.AsOptionalString(payload["stopReason"]),
		Payload:   eventPayload,
	}
}

func persistedResearchJobRuntimeStatePayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	plane := firstNonEmptyTrimmed(wisdev.AsOptionalString(payload["plane"]), wisdev.AsOptionalString(payload["mode"]))
	runtimeState := map[string]any{
		"sessionId":  wisdev.AsOptionalString(payload["sessionId"]),
		"plane":      plane,
		"stopReason": wisdev.AsOptionalString(payload["stopReason"]),
	}
	if budgetUsed, ok := payload["budgetUsed"].(map[string]any); ok {
		runtimeState["openLedgerCount"] = int(anyToInt64(budgetUsed["openLedgerCount"], 0))
	}
	if reasoningRuntime := persistedResearchJobReasoningRuntimePayload(payload, plane); len(reasoningRuntime) > 0 {
		runtimeState["reasoningRuntime"] = reasoningRuntime
	}
	return runtimeState
}

func persistedResearchJobReasoningRuntimePayload(payload map[string]any, plane string) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	if existing, ok := payload["reasoningRuntime"].(map[string]any); ok && len(existing) > 0 {
		return cloneUnifiedPayload(existing)
	}
	return wisdev.BuildResearchReasoningRuntimeMetadata(wisdev.LoopRequest{
		Query:                       wisdev.AsOptionalString(payload["query"]),
		Domain:                      wisdev.AsOptionalString(payload["domain"]),
		ProjectID:                   wisdev.AsOptionalString(payload["sessionId"]),
		Mode:                        wisdev.AsOptionalString(payload["mode"]),
		DisableProgrammaticPlanning: persistedResearchJobBool(payload["disableProgrammaticPlanning"]),
		DisableHypothesisGeneration: persistedResearchJobBool(payload["disableHypothesisGeneration"]),
	}, wisdev.ResearchExecutionPlane(plane), nil)
}

func persistedResearchJobBool(value any) bool {
	typed, ok := value.(bool)
	return ok && typed
}

func asInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func legacyCancelConflictMessage(status string) string {
	switch strings.TrimSpace(status) {
	case "completed":
		return "job already completed"
	case "cancelled":
		return "job already cancelled"
	default:
		return "job already failed"
	}
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

	stableTraceID := strings.TrimSpace(job.TraceID)
	if stableTraceID == "" {
		stableTraceID = wisdev.NewTraceID()
		job.TraceID = stableTraceID
	}

	send := func(e YoloEvent) bool {
		if strings.TrimSpace(e.TraceID) == "" {
			e.TraceID = stableTraceID
		}
		appendLegacyAutonomousJournalEvent(job, e)
		select {
		case <-ctx.Done():
			return false
		case job.LegacyEvents <- e:
			return true
		}
	}

	sendTerminal := func(e YoloEvent) bool {
		if strings.TrimSpace(e.TraceID) == "" {
			e.TraceID = stableTraceID
		}
		job.setTerminalLegacyEvent(e)
		appendLegacyAutonomousJournalEvent(job, e)
		select {
		case <-ctx.Done():
			return false
		case job.LegacyEvents <- e:
			return true
		}
	}

	if loop == nil {
		sendTerminal(YoloEvent{Type: "error", Error: "autonomous loop engine not initialized", TraceID: stableTraceID})
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
	}, func(wisdev.PlanExecutionEvent) {})

	if err != nil {
		sendTerminal(YoloEvent{Type: "error", Error: err.Error(), TraceID: stableTraceID})
		return
	}

	// 3. Final event
	sendTerminal(YoloEvent{
		Type:           "complete",
		Status:         "finished",
		PapersFound:    len(result.Papers),
		IterationsUsed: result.Iterations,
		Coverage:       1.0,
	})
}

var runUnifiedWisDevJobLoop = func(
	ctx context.Context,
	runtime *wisdev.UnifiedResearchRuntime,
	req wisdev.LoopRequest,
	onEvent func(wisdev.PlanExecutionEvent),
) (*wisdev.UnifiedResearchResult, error) {
	return runtime.RunLoop(ctx, req, wisdev.ResearchExecutionPlaneJob, onEvent)
}

// runWisDevPipeline drives the new AI Scientist research loop.
func runWisDevPipeline(ctx context.Context, job *YoloJob, loop AutonomousLoopInterface) {
	defer func() {
		if job.UnifiedEvents != nil {
			close(job.UnifiedEvents)
		}
		if job.LegacyEvents != nil {
			close(job.LegacyEvents)
		}
	}()

	stableTraceID := strings.TrimSpace(job.TraceID)
	if stableTraceID == "" {
		stableTraceID = wisdev.NewTraceID()
		job.TraceID = stableTraceID
	}

	normalizeUnifiedEvent := func(e UnifiedEvent) UnifiedEvent {
		if strings.TrimSpace(e.JobID) == "" {
			e.JobID = job.ID
		}
		if strings.TrimSpace(e.TraceID) == "" {
			e.TraceID = stableTraceID
		}
		if e.Timestamp == 0 {
			e.Timestamp = time.Now().UnixMilli()
		}
		return e
	}

	send := func(e UnifiedEvent) bool {
		e = normalizeUnifiedEvent(e)
		appendUnifiedAutonomousJournalEvent(job, e)
		select {
		case <-ctx.Done():
			return false
		case job.UnifiedEvents <- e:
			return true
		}
	}

	sendTerminal := func(e UnifiedEvent) bool {
		e = normalizeUnifiedEvent(e)
		job.setTerminalUnifiedEvent(e)
		appendUnifiedAutonomousJournalEvent(job, e)
		select {
		case <-ctx.Done():
			return false
		case job.UnifiedEvents <- e:
			return true
		}
	}

	jobMode := normalizeWisDevJobMode(job.Mode)
	latestTraceID := stableTraceID

	loopReq := wisdev.LoopRequest{
		Query:         job.Query,
		Domain:        job.Domain,
		ProjectID:     firstNonEmptyTrimmed(job.ProjectID, job.ID),
		DurableJobID:  job.ID,
		MaxIterations: 5,
		BudgetCents:   100,
		Mode:          jobMode,
	}
	runtime := resolveUnifiedResearchRuntime(GlobalYoloGateway)
	if runtime == nil {
		if loop == nil {
			sendTerminal(buildUnifiedFailureEvent(job.ID, jobMode, latestTraceID, "WISDEV_UNIFIED_RUNTIME_UNAVAILABLE", "wisdev unified research runtime not initialized"))
			return
		}
		if !send(UnifiedEvent{Type: "job_started", Step: "planning", Message: "autonomous loop started", Mode: jobMode, TraceID: latestTraceID}) {
			return
		}
		result, err := loop.Run(ctx, loopReq)
		if err != nil {
			sendTerminal(buildUnifiedFailureEvent(job.ID, jobMode, latestTraceID, "AUTONOMOUS_LOOP_FAILED", err.Error()))
			return
		}
		if result == nil {
			sendTerminal(buildUnifiedFailureEvent(job.ID, jobMode, latestTraceID, "AUTONOMOUS_LOOP_EMPTY", "wisdev unified research runtime returned no result"))
			return
		}
		sendTerminal(UnifiedEvent{
			Type:        "job_done",
			ResultCount: len(result.Papers),
			Reason:      completionReason(result),
			Message:     "autonomous loop completed",
			TraceID:     latestTraceID,
			Mode:        jobMode,
			ServiceTier: string(wisdev.ResolveServiceTier(wisdev.NormalizeWisDevMode(jobMode), false)),
			Payload:     buildUnifiedLoopPayload(result),
		})
		return
	}
	if !send(UnifiedEvent{Type: "job_started", Step: "planning", Message: "autonomous loop started", Mode: jobMode, TraceID: latestTraceID}) {
		return
	}
	emit := func(event wisdev.PlanExecutionEvent) {
		if event.Type != wisdev.EventProgress {
			return
		}
		send(UnifiedEvent{
			Type:    string(event.Type),
			Step:    strings.TrimSpace(event.StepID),
			StepID:  strings.TrimSpace(event.StepID),
			Message: strings.TrimSpace(event.Message),
			Payload: cloneUnifiedPayload(event.Payload),
			Mode:    jobMode,
		})
	}
	var result *wisdev.LoopResult
	runtimeResult, err := runUnifiedWisDevJobLoop(ctx, runtime, loopReq, emit)
	if runtimeResult != nil {
		result = runtimeResult.LoopResult
	}

	if err != nil {
		sendTerminal(buildUnifiedFailureEvent(job.ID, jobMode, latestTraceID, "AUTONOMOUS_LOOP_FAILED", err.Error()))
		return
	}
	if result == nil {
		sendTerminal(buildUnifiedFailureEvent(job.ID, jobMode, latestTraceID, "AUTONOMOUS_LOOP_EMPTY", "wisdev unified research runtime returned no result"))
		return
	}

	resultMode := strings.TrimSpace(string(result.Mode))
	if resultMode == "" {
		resultMode = jobMode
	}
	serviceTier := strings.TrimSpace(string(result.ServiceTier))
	if serviceTier == "" {
		serviceTier = string(wisdev.ResolveServiceTier(wisdev.NormalizeWisDevMode(resultMode), false))
	}

	sendTerminal(UnifiedEvent{
		Type:           "job_done",
		ResultCount:    len(result.Papers),
		Reason:         completionReason(result),
		Message:        "autonomous loop completed",
		TraceID:        latestTraceID,
		Mode:           resultMode,
		ServiceTier:    serviceTier,
		Payload:        buildUnifiedLoopPayload(result),
		ReasoningGraph: result.ReasoningGraph,
		MemoryTiers:    result.MemoryTiers,
	})
}

func normalizeWisDevJobMode(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return string(wisdev.WisDevModeYOLO)
	}
	switch wisdev.NormalizeWisDevMode(raw) {
	case wisdev.WisDevModeYOLO:
		return string(wisdev.WisDevModeYOLO)
	default:
		return string(wisdev.WisDevModeGuided)
	}
}

func completionReason(result *wisdev.LoopResult) string {
	if result != nil && result.Converged {
		return "Convergence reached"
	}
	return "Autonomous loop completed"
}

func cloneUnifiedPayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func buildUnifiedLoopPayload(result *wisdev.LoopResult) map[string]any {
	if result == nil {
		return nil
	}
	payload := map[string]any{
		"papers":          serializeLoopPapers(result.Papers),
		"iterations_used": result.Iterations,
		"converged":       result.Converged,
	}
	if strings.TrimSpace(result.FinalAnswer) != "" {
		payload["finalAnswer"] = strings.TrimSpace(result.FinalAnswer)
	}
	if len(result.Evidence) > 0 {
		payload["evidence"] = result.Evidence
	}
	if result.GapAnalysis != nil {
		payload["gapAnalysis"] = result.GapAnalysis
		if len(result.GapAnalysis.Ledger) > 0 {
			payload["coverageLedger"] = result.GapAnalysis.Ledger
		}
	}
	if result.DraftCritique != nil {
		payload["draftCritique"] = result.DraftCritique
	}
	attachUnifiedRuntimeStatePayload(payload, result)
	enforceLoopFinalizationPayload(payload, result)
	return payload
}

func buildUnifiedFailureEvent(jobID, mode, traceID, code, message string) UnifiedEvent {
	trimmedMessage := strings.TrimSpace(message)
	return UnifiedEvent{
		Type:    "job_failed",
		JobID:   strings.TrimSpace(jobID),
		TraceID: strings.TrimSpace(traceID),
		Mode:    strings.TrimSpace(mode),
		Error:   trimmedMessage,
		Message: trimmedMessage,
		Payload: map[string]any{
			"error": map[string]any{
				"code":    strings.TrimSpace(code),
				"message": trimmedMessage,
				"traceId": strings.TrimSpace(traceID),
				"status":  http.StatusInternalServerError,
			},
		},
	}
}

func buildUnifiedCancelledEvent(jobID string, traceID string) UnifiedEvent {
	return UnifiedEvent{
		Type:        "job_cancelled",
		JobID:       strings.TrimSpace(jobID),
		TraceID:     strings.TrimSpace(traceID),
		CancelledBy: "user",
		Message:     "autonomous research cancelled",
		Payload: map[string]any{
			"error": map[string]any{
				"code":    "JOB_CANCELLED",
				"message": "autonomous research cancelled",
				"traceId": strings.TrimSpace(traceID),
				"status":  http.StatusConflict,
			},
		},
	}
}

func serializeLoopPapers(papers []search.Paper) []map[string]any {
	out := make([]map[string]any, 0, len(papers))
	for _, paper := range papers {
		authors := make([]map[string]any, 0, len(paper.Authors))
		for _, author := range paper.Authors {
			if trimmed := strings.TrimSpace(author); trimmed != "" {
				authors = append(authors, map[string]any{"name": trimmed})
			}
		}
		pubDate := map[string]any{"year": paper.Year}
		if paper.Month >= 1 && paper.Month <= 12 {
			pubDate["month"] = paper.Month
		}
		entry := map[string]any{
			"id":            strings.TrimSpace(paper.ID),
			"title":         strings.TrimSpace(paper.Title),
			"abstract":      strings.TrimSpace(paper.Abstract),
			"link":          strings.TrimSpace(paper.Link),
			"doi":           strings.TrimSpace(paper.DOI),
			"authors":       authors,
			"publication":   strings.TrimSpace(paper.Venue),
			"venue":         strings.TrimSpace(paper.Venue),
			"publishDate":   pubDate,
			"citationCount": paper.CitationCount,
			"keywords":      append([]string(nil), paper.Keywords...),
			"source":        strings.TrimSpace(paper.Source),
		}
		if paper.ReferenceCount > 0 {
			entry["referenceCount"] = paper.ReferenceCount
		}
		if paper.InfluentialCitationCount > 0 {
			entry["influentialCitationCount"] = paper.InfluentialCitationCount
		}
		if paper.OpenAccessUrl != "" {
			entry["openAccessUrl"] = paper.OpenAccessUrl
		}
		if paper.PdfUrl != "" {
			entry["pdfUrl"] = paper.PdfUrl
		}
		out = append(out, entry)
	}
	return out
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
	req.Query = strings.TrimSpace(req.Query)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Domain = strings.TrimSpace(req.Domain)
	if req.Query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}
	if req.Mode == "" {
		req.Mode = "bounded"
	}

	traceID := resolveWisdevRouteTraceID(r, "")
	jobID := newWisDevJobID("yolo")

	ctx, cancel := context.WithCancel(context.Background())
	job := &YoloJob{
		ID:            jobID,
		TraceID:       traceID,
		UserID:        strings.TrimSpace(GetUserID(r)),
		Query:         req.Query,
		ProjectID:     firstNonEmptyTrimmed(req.ProjectID, req.SessionID),
		Status:        "running",
		Mode:          "yolo",
		Domain:        req.Domain,
		LegacyEvents:  make(chan YoloEvent, 1024),
		UnifiedEvents: make(chan UnifiedEvent, 1024),
		Cancel:        cancel,
		CreatedAt:     time.Now(),
	}
	yoloJobStore.put(job)
	appendLegacyYoloRegistrationJournalEvent(job)

	go runYoloPipeline(ctx, job, GlobalYoloLoop)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Trace-Id", traceID)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(YoloExecuteResponse{
		JobID:     jobID,
		StreamURL: "/agent/yolo/stream?job_id=" + jobID,
		TraceID:   traceID,
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
	req.Query = strings.TrimSpace(req.Query)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Domain = strings.TrimSpace(req.Domain)
	if req.Mode == "" {
		req.Mode = string(wisdev.WisDevModeYOLO)
	}

	slog.Info("wisdev job request received",
		"component", "wisdev_job_handler",
		"operation", "WisDevJobHandler",
		"stage", "request_received",
		"query_preview", wisdev.QueryPreview(req.Query),
		"project_id", req.ProjectID,
		"mode", req.Mode,
		"domain", req.Domain,
	)
	if req.Query == "" {
		slog.Warn("wisdev job request rejected due to empty query",
			"component", "wisdev_job_handler",
			"operation", "WisDevJobHandler",
			"stage", "validation_failed",
			"project_id", req.ProjectID,
			"mode", req.Mode,
			"domain", req.Domain,
		)
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}
	userID, err := resolveAuthorizedUserID(r, "")
	if err != nil {
		WriteError(w, http.StatusForbidden, ErrUnauthorized, err.Error(), nil)
		return
	}

	traceID := resolveWisdevRouteTraceID(r, "")
	jobID := newWisDevJobID("job")
	ctx, cancel := context.WithCancel(context.Background())
	job := &YoloJob{
		ID:            jobID,
		TraceID:       traceID,
		UserID:        userID,
		Query:         req.Query,
		ProjectID:     req.ProjectID,
		Status:        "running",
		Mode:          req.Mode,
		Domain:        req.Domain,
		LegacyEvents:  make(chan YoloEvent, 1024),
		UnifiedEvents: make(chan UnifiedEvent, 1024),
		Cancel:        cancel,
		CreatedAt:     time.Now(),
	}
	yoloJobStore.put(job)
	appendWisDevJobRegistrationJournalEvent(job)
	slog.Info("wisdev job created",
		"component", "wisdev_job_handler",
		"operation", "WisDevJobHandler",
		"stage", "job_created",
		"job_id", jobID,
		"trace_id", traceID,
		"query_preview", wisdev.QueryPreview(req.Query),
		"project_id", req.ProjectID,
		"mode", req.Mode,
		"domain", req.Domain,
	)

	// Trigger the unified pipeline
	go runWisDevPipeline(ctx, job, GlobalYoloLoop)

	w.Header().Set("X-Trace-Id", traceID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"job_id":     jobID,
		"stream_url": "/wisdev/job/" + jobID + "/stream",
		"traceId":    traceID,
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
		job, ok = recoverWisDevJobFromJournal(jobID)
		if !ok {
			if payload, found := loadPersistedResearchJob(jobID); found {
				if !requirePersistedResearchJobAccess(w, r, payload) {
					return
				}
				writePersistedResearchJobStream(w, r, payload)
				return
			}
			WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", map[string]any{
				"jobId": jobID,
			})
			return
		}
	}
	if !requireWisDevJobAccess(w, r, job) {
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
	if strings.TrimSpace(job.TraceID) != "" {
		w.Header().Set("X-Trace-Id", strings.TrimSpace(job.TraceID))
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if job.UnifiedEvents == nil {
		terminalEvent, ok := job.terminalUnifiedEventSnapshot()
		if !ok {
			terminalEvent = buildUnifiedStateLostEvent(job)
			job.setTerminalUnifiedEvent(terminalEvent)
		}
		payload, _ := json.Marshal(terminalEvent)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		return
	}

	// Robustness: keep-alive to prevent ELB timeouts
	ticker := newStreamTicker(15 * time.Second)
	defer ticker.Stop()
	terminalEventSeen := false

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
				if !terminalEventSeen {
					terminalEvent, ok := job.terminalUnifiedEventSnapshot()
					if !ok {
						terminalEvent = buildSyntheticUnifiedStreamClosedEvent(job)
						job.setTerminalUnifiedEvent(terminalEvent)
					}
					appendUnifiedAutonomousJournalEvent(job, terminalEvent)
					payload, _ := json.Marshal(markSyntheticUnifiedTerminalEvent(terminalEvent, "channel_closed_before_terminal_delivery"))
					_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
					flusher.Flush()
				}
				return
			}
			payload, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()

			if isUnifiedTerminalEventType(event.Type) {
				job.setTerminalUnifiedEvent(event)
				terminalEventSeen = true
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
		job, ok = recoverLegacyYoloJobFromJournal(jobID)
		if !ok {
			WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", map[string]any{
				"jobId": jobID,
			})
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "streaming not supported", nil)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if strings.TrimSpace(job.TraceID) != "" {
		w.Header().Set("X-Trace-Id", strings.TrimSpace(job.TraceID))
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if job.LegacyEvents == nil {
		terminalEvent, ok := job.terminalLegacyEventSnapshot()
		if !ok {
			terminalEvent = buildLegacyStateLostEvent(job)
			job.setTerminalLegacyEvent(terminalEvent)
		}
		payload, _ := json.Marshal(terminalEvent)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		return
	}
	terminalEventSeen := false

	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-job.LegacyEvents:
			if !open {
				if !terminalEventSeen {
					terminalEvent, ok := job.terminalLegacyEventSnapshot()
					if !ok {
						terminalEvent = buildSyntheticLegacyStreamClosedEvent(job)
						job.setTerminalLegacyEvent(terminalEvent)
					}
					appendLegacyAutonomousJournalEvent(job, terminalEvent)
					payload, _ := json.Marshal(terminalEvent)
					_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
					flusher.Flush()
				}
				return
			}
			payload, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
			if isLegacyTerminalEventType(event.Type) {
				job.setTerminalLegacyEvent(event)
				terminalEventSeen = true
				return
			}
		}
	}
}

// YoloStatusHandler handles GET /agent/yolo/status?job_id=<id> (Legacy).
func YoloStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	jobID := strings.TrimSpace(r.URL.Query().Get("job_id"))
	if jobID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "job_id query parameter is required", map[string]any{
			"field": "job_id",
		})
		return
	}

	job, ok := yoloJobStore.get(jobID)
	if !ok {
		job, ok = recoverLegacyYoloJobFromJournal(jobID)
		if !ok {
			WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", map[string]any{
				"jobId": jobID,
			})
			return
		}
	}

	writeYoloJobStatusResponse(w, job)
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
		job, ok = recoverWisDevJobFromJournal(jobID)
		if !ok {
			if payload, found := loadPersistedResearchJob(jobID); found {
				if !requirePersistedResearchJobAccess(w, r, payload) {
					return
				}
				writePersistedResearchJobStatusResponse(w, payload)
				return
			}
			WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", map[string]any{
				"jobId": jobID,
			})
			return
		}
	}
	if !requireWisDevJobAccess(w, r, job) {
		return
	}
	writeYoloJobStatusResponse(w, job)
}

func WisDevJobCancelHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	jobID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/wisdev/job/"), "/cancel")
	jobID = strings.Trim(jobID, "/")
	if jobID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "job_id is required", map[string]any{
			"field": "job_id",
		})
		return
	}

	if job, ok := yoloJobStore.get(jobID); ok {
		if !requireWisDevJobAccess(w, r, job) {
			return
		}
		if event, terminal := job.terminalUnifiedEventSnapshot(); terminal {
			if event.Type == "job_cancelled" {
				writeWisDevCancelResponse(w, jobID, job.TraceID, "cancelled", false)
				return
			}
			if traceID := strings.TrimSpace(job.TraceID); traceID != "" {
				w.Header().Set("X-Trace-Id", traceID)
			}
			WriteError(w, http.StatusConflict, ErrConflict, legacyCancelConflictMessage(job.statusSnapshot()), map[string]any{
				"jobId":   job.ID,
				"traceId": strings.TrimSpace(job.TraceID),
				"status":  job.statusSnapshot(),
			})
			return
		}
		cancelled := buildUnifiedCancelledEvent(job.ID, job.TraceID)
		job.setTerminalUnifiedEvent(cancelled)
		appendUnifiedAutonomousJournalEvent(job, cancelled)
		select {
		case job.UnifiedEvents <- cancelled:
		default:
		}
		if job.Cancel != nil {
			job.Cancel()
		}
		_, _, _ = cancelPersistedResearchJob(jobID)
		writeWisDevCancelResponse(w, jobID, job.TraceID, "cancelled", false)
		return
	}

	if job, ok := recoverWisDevJobFromJournal(jobID); ok {
		if !requireWisDevJobAccess(w, r, job) {
			return
		}
		if event, terminal := job.terminalUnifiedEventSnapshot(); terminal && event.Type != "job_cancelled" {
			if traceID := strings.TrimSpace(job.TraceID); traceID != "" {
				w.Header().Set("X-Trace-Id", traceID)
			}
			WriteError(w, http.StatusConflict, ErrConflict, legacyCancelConflictMessage(job.statusSnapshot()), map[string]any{
				"jobId":   job.ID,
				"traceId": strings.TrimSpace(job.TraceID),
				"status":  job.statusSnapshot(),
			})
			return
		}
		writeWisDevCancelResponse(w, jobID, job.TraceID, "cancelled", true)
		return
	}

	if payload, found := loadPersistedResearchJob(jobID); found {
		if !requirePersistedResearchJobAccess(w, r, payload) {
			return
		}
	}
	payload, cancelled, err := cancelPersistedResearchJob(jobID)
	if err != nil {
		WriteError(w, http.StatusConflict, ErrConflict, err.Error(), map[string]any{
			"jobId":  jobID,
			"status": wisdev.AsOptionalString(payload["status"]),
		})
		return
	}
	if !cancelled {
		WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", map[string]any{
			"jobId": jobID,
		})
		return
	}
	writeWisDevCancelResponse(w, jobID, wisdev.AsOptionalString(payload["traceId"]), wisdev.AsOptionalString(payload["status"]), true, payload)
}

func writeWisDevCancelResponse(w http.ResponseWriter, jobID string, traceID string, status string, recovered bool, payloads ...map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if strings.TrimSpace(traceID) != "" {
		w.Header().Set("X-Trace-Id", strings.TrimSpace(traceID))
	}
	w.WriteHeader(http.StatusOK)
	response := map[string]any{
		"cancelled": true,
		"job_id":    strings.TrimSpace(jobID),
		"traceId":   strings.TrimSpace(traceID),
		"status":    firstNonEmptyTrimmed(status, "cancelled"),
		"recovered": recovered,
	}
	if len(payloads) > 0 {
		if runtimeState := persistedResearchJobRuntimeStatePayload(payloads[0]); len(runtimeState) > 0 {
			response["runtimeState"] = runtimeState
			if reasoningRuntime, ok := runtimeState["reasoningRuntime"].(map[string]any); ok && len(reasoningRuntime) > 0 {
				response["reasoningRuntime"] = reasoningRuntime
			}
		}
	}
	_ = json.NewEncoder(w).Encode(response)
}

func cancelPersistedResearchJob(jobID string) (map[string]any, bool, error) {
	payload, found := loadPersistedResearchJob(jobID)
	if !found {
		return nil, false, nil
	}
	status := strings.TrimSpace(wisdev.AsOptionalString(payload["status"]))
	switch status {
	case "cancelled":
		return payload, true, nil
	case "completed", "failed":
		return payload, true, fmt.Errorf("job already %s", status)
	}
	now := time.Now().UnixMilli()
	payload["status"] = "cancelled"
	payload["stopReason"] = "cancelled"
	payload["cancelledAt"] = now
	payload["updatedAt"] = now
	payload["cancellationRequested"] = true
	if GlobalYoloGateway != nil && GlobalYoloGateway.Execution != nil {
		sessionID := strings.TrimSpace(wisdev.AsOptionalString(payload["sessionId"]))
		if sessionID != "" && executionServiceActive(GlobalYoloGateway.Execution, sessionID) {
			_ = GlobalYoloGateway.Execution.Cancel(context.Background(), sessionID)
		}
	}
	if GlobalYoloGateway != nil && GlobalYoloGateway.StateStore != nil {
		if err := GlobalYoloGateway.StateStore.SaveResearchJob(jobID, payload); err != nil {
			return payload, true, err
		}
	}
	appendPersistedResearchJobJournalEvent(jobID, payload, "job_cancelled")
	return payload, true, nil
}

func executionServiceActive(execution wisdev.ExecutionService, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if execution == nil || sessionID == "" {
		return false
	}
	activeChecker, ok := execution.(interface {
		IsActive(sessionID string) bool
	})
	if !ok {
		return true
	}
	return activeChecker.IsActive(sessionID)
}

func appendPersistedResearchJobJournalEvent(jobID string, payload map[string]any, eventType string) {
	if GlobalYoloGateway == nil || GlobalYoloGateway.Journal == nil || len(payload) == 0 {
		return
	}
	now := time.Now().UnixMilli()
	journalPayload := map[string]any{
		"jobId":      strings.TrimSpace(jobID),
		"durableJob": payload,
		"timestamp":  now,
	}
	if runtimeState := persistedResearchJobRuntimeStatePayload(payload); len(runtimeState) > 0 {
		journalPayload["runtimeState"] = runtimeState
		if reasoningRuntime, ok := runtimeState["reasoningRuntime"].(map[string]any); ok && len(reasoningRuntime) > 0 {
			journalPayload["reasoningRuntime"] = reasoningRuntime
		}
	}
	GlobalYoloGateway.Journal.Append(wisdev.RuntimeJournalEntry{
		EventID:   wisdev.NewTraceID(),
		TraceID:   strings.TrimSpace(wisdev.AsOptionalString(payload["traceId"])),
		SessionID: strings.TrimSpace(wisdev.AsOptionalString(payload["sessionId"])),
		UserID:    strings.TrimSpace(wisdev.AsOptionalString(payload["userId"])),
		PlanID:    strings.TrimSpace(jobID),
		EventType: strings.TrimSpace(eventType),
		Path:      fmt.Sprintf("/wisdev/job/%s", strings.TrimSpace(jobID)),
		Status:    strings.TrimSpace(wisdev.AsOptionalString(payload["status"])),
		CreatedAt: now,
		Summary:   "durable research job control event",
		Payload:   journalPayload,
		Metadata: map[string]any{
			"source": "wisdev_research_job",
			"jobId":  strings.TrimSpace(jobID),
			"mode":   wisdev.AsOptionalString(payload["plane"]),
			"userId": wisdev.AsOptionalString(payload["userId"]),
		},
	})
}

func writePersistedResearchJobStream(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "streaming not supported", nil)
		return
	}
	event := persistedResearchJobUnifiedEvent(payload)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if strings.TrimSpace(event.TraceID) != "" {
		w.Header().Set("X-Trace-Id", strings.TrimSpace(event.TraceID))
	}
	w.WriteHeader(http.StatusOK)
	select {
	case <-r.Context().Done():
		return
	default:
		encoded, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		flusher.Flush()
	}
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
	traceID := wisdev.NewTraceID()

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

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Trace-Id", traceID)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"schedule_id": schedID,
		"status":      "registered",
		"traceId":     traceID,
	})
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

	jobID := newWisDevJobID("job_cron")
	traceID := wisdev.NewTraceID()
	ctx, cancel := context.WithCancel(context.Background())
	job := &YoloJob{
		ID:            jobID,
		TraceID:       traceID,
		UserID:        "internal-service",
		Query:         query,
		ProjectID:     projectID,
		Status:        "running",
		Mode:          "yolo",
		Domain:        "general",
		LegacyEvents:  make(chan YoloEvent, 1024),
		UnifiedEvents: make(chan UnifiedEvent, 1024),
		Cancel:        cancel,
		CreatedAt:     time.Now(),
	}
	yoloJobStore.put(job)
	appendWisDevJobRegistrationJournalEvent(job)

	// Trigger the unified pipeline
	go runWisDevPipeline(ctx, job, GlobalYoloLoop)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Trace-Id", traceID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"job_id": jobID, "status": "started", "traceId": traceID})
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
	if strings.TrimSpace(req.JobID) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "job_id is required", map[string]any{
			"field": "job_id",
		})
		return
	}

	jobID := strings.TrimSpace(req.JobID)
	job, ok := yoloJobStore.get(jobID)
	recovered := false
	if !ok {
		legacyEntries := readLegacyYoloJournalEntries(jobID)
		job, ok = recoverLegacyYoloJobFromEntries(jobID, legacyEntries)
		if !ok {
			WriteError(w, http.StatusNotFound, ErrNotFound, "job not found", map[string]any{
				"jobId": jobID,
			})
			return
		}
		recovered = true
		lastEntry := legacyEntries[len(legacyEntries)-1]
		if isLegacyTerminalEventType(lastEntry.EventType) {
			terminalEvent, _ := job.terminalLegacyEventSnapshot()
			if terminalEvent.Type == "cancelled" {
				yoloJobStore.put(job)
				writeLegacyCancelResponse(w, job, true)
				return
			}
			if traceID := strings.TrimSpace(job.TraceID); traceID != "" {
				w.Header().Set("X-Trace-Id", traceID)
			}
			WriteError(w, http.StatusConflict, ErrConflict, legacyCancelConflictMessage(job.statusSnapshot()), map[string]any{
				"jobId":   job.ID,
				"traceId": strings.TrimSpace(job.TraceID),
				"status":  job.statusSnapshot(),
			})
			return
		}
		cancelled := buildLegacyCancelledEvent(job.TraceID)
		job.setTerminalLegacyEvent(cancelled)
		appendLegacyAutonomousJournalEvent(job, cancelled)
		yoloJobStore.put(job)
		writeLegacyCancelResponse(w, job, true)
		return
	}

	if terminalEvent, terminal := job.terminalLegacyEventSnapshot(); terminal {
		if terminalEvent.Type == "cancelled" {
			writeLegacyCancelResponse(w, job, recovered)
			return
		}
		if traceID := strings.TrimSpace(job.TraceID); traceID != "" {
			w.Header().Set("X-Trace-Id", traceID)
		}
		WriteError(w, http.StatusConflict, ErrConflict, legacyCancelConflictMessage(job.statusSnapshot()), map[string]any{
			"jobId":   job.ID,
			"traceId": strings.TrimSpace(job.TraceID),
			"status":  job.statusSnapshot(),
		})
		return
	}

	// Emit terminal event to any connected unified stream before cancelling
	if job.UnifiedEvents != nil {
		cancelled := buildUnifiedCancelledEvent(job.ID, job.TraceID)
		job.setTerminalUnifiedEvent(cancelled)
		appendUnifiedAutonomousJournalEvent(job, cancelled)
		select {
		case job.UnifiedEvents <- cancelled:
		default:
		}
	}
	if job.LegacyEvents != nil {
		cancelled := buildLegacyCancelledEvent(job.TraceID)
		job.setTerminalLegacyEvent(cancelled)
		appendLegacyAutonomousJournalEvent(job, cancelled)
		select {
		case job.LegacyEvents <- cancelled:
		default:
		}
	}
	if job.Cancel != nil {
		job.Cancel()
	}

	writeLegacyCancelResponse(w, job, recovered)
}
