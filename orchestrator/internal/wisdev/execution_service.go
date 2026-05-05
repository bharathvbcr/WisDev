package wisdev

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type ExecutionStartResult struct {
	SessionID      string        `json:"sessionId"`
	Status         SessionStatus `json:"status"`
	ExecutionID    string        `json:"executionId"`
	AlreadyRunning bool          `json:"alreadyRunning"`
}

type DurableExecutionService struct {
	gateway *AgentGateway

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func NewDurableExecutionService(gateway *AgentGateway) *DurableExecutionService {
	return &DurableExecutionService{
		gateway: gateway,
		cancels: map[string]context.CancelFunc{},
	}
}

func (s *DurableExecutionService) IsActive(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.cancels[strings.TrimSpace(sessionID)]
	return ok
}

func (s *DurableExecutionService) Start(ctx context.Context, sessionID string) (*ExecutionStartResult, error) {
	if s == nil || s.gateway == nil || s.gateway.Executor == nil {
		return nil, fmt.Errorf("execution service unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("sessionId is required")
	}

	session, err := s.gateway.Store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	resolvedQuery := strings.TrimSpace(ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery))
	if resolvedQuery == "" {
		return nil, fmt.Errorf("session query is required")
	}
	if session.Plan == nil {
		session.Plan = BuildDefaultPlan(session)
		if err := s.gateway.Store.Put(ctx, session, s.gateway.SessionTTL); err != nil {
			return nil, err
		}
	}

	slog.Info("Starting durable WisDev execution",
		"sessionId", sessionID,
		"userId", session.UserID,
		"status", string(session.Status),
		"queryPreview", truncateExecutionQueryPreview(resolvedQuery),
		"queryLength", len(resolvedQuery),
		"hasPlan", session.Plan != nil,
	)

	s.mu.Lock()
	if _, ok := s.cancels[sessionID]; ok {
		s.mu.Unlock()
		return &ExecutionStartResult{
			SessionID:      sessionID,
			Status:         session.Status,
			ExecutionID:    sessionID,
			AlreadyRunning: true,
		}, nil
	}
	runCtx, cancel := context.WithCancel(durableExecutionRootContext(ctx))
	s.cancels[sessionID] = cancel
	s.mu.Unlock()

	go s.run(runCtx, sessionID)

	return &ExecutionStartResult{
		SessionID:   sessionID,
		Status:      session.Status,
		ExecutionID: sessionID,
	}, nil
}

func (s *DurableExecutionService) run(ctx context.Context, sessionID string) {
	defer func() {
		s.mu.Lock()
		delete(s.cancels, sessionID)
		s.mu.Unlock()
	}()
	_, err := executeSessionRun(ctx, s.gateway, sessionID)
	if err != nil {
		if ctx.Err() != nil {
			slog.Info("DurableExecutionService: execution context cancelled; leaving session status managed by cancel path",
				"service", "go_orchestrator",
				"component", "wisdev.execution_service",
				"operation", "run",
				"stage", "execute_session_cancelled",
				"sessionId", sessionID,
				"error", err.Error(),
			)
			return
		}
		slog.Error("DurableExecutionService: executeSessionRun failed — marking session failed so the stream loop can terminate",
			"service", "go_orchestrator",
			"component", "wisdev.execution_service",
			"operation", "run",
			"stage", "execute_session_run_failed",
			"sessionId", sessionID,
			"error", err.Error(),
		)
		// Persist a failed status so the streaming poll can detect the
		// terminal state and stop waiting, rather than spinning until its
		// own deadline expires.
		persistCtx, cancel := context.WithTimeout(durableExecutionRootContext(ctx), 3*time.Second)
		defer cancel()
		if session, getErr := s.gateway.Store.Get(persistCtx, sessionID); getErr == nil {
			session.Status = SessionFailed
			session.UpdatedAt = NowMillis()
			_ = s.gateway.Store.Put(persistCtx, session, s.gateway.SessionTTL)
		}
	}
}

func (s *DurableExecutionService) Cancel(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId is required")
	}
	session, err := s.gateway.Store.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	s.stopActive(sessionID)
	session.Status = SessionPaused
	session.UpdatedAt = NowMillis()
	if err := s.gateway.Store.Put(ctx, session, s.gateway.SessionTTL); err != nil {
		return err
	}
	appendExecutionEvent(s.gateway, session, PlanExecutionEvent{
		Type:      EventProgress,
		TraceID:   NewTraceID(),
		SessionID: sessionID,
		PlanID:    session.PlanID(),
		Message:   "execution paused",
		CreatedAt: NowMillis(),
		Payload: map[string]any{
			"status": "paused",
		},
	})
	return nil
}

func (s *DurableExecutionService) Abandon(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId is required")
	}
	session, err := s.gateway.Store.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	s.stopActive(sessionID)
	session.Status = StatusAbandoned
	session.UpdatedAt = NowMillis()
	if err := s.gateway.Store.Put(ctx, session, s.gateway.SessionTTL); err != nil {
		return err
	}
	appendExecutionEvent(s.gateway, session, PlanExecutionEvent{
		Type:      EventPlanCancelled,
		TraceID:   NewTraceID(),
		SessionID: sessionID,
		PlanID:    session.PlanID(),
		Message:   "Plan cancelled",
		CreatedAt: NowMillis(),
		Payload: map[string]any{
			"status": "cancelled",
		},
	})
	return nil
}

func (s *DurableExecutionService) stopActive(sessionID string) {
	s.mu.Lock()
	cancel, ok := s.cancels[sessionID]
	if ok {
		cancel()
		delete(s.cancels, sessionID)
	}
	s.mu.Unlock()
}

func (s *DurableExecutionService) Stream(ctx context.Context, sessionID string, emit func(PlanExecutionEvent) error) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId is required")
	}
	seen := map[string]struct{}{}
	terminalEventSeen := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(300 * time.Millisecond):
		}

		entries := []RuntimeJournalEntry(nil)
		if s.gateway != nil && s.gateway.Journal != nil {
			entries = s.gateway.Journal.ReadSession(sessionID, 256)
		}
		for _, entry := range entries {
			key := firstNonEmpty(entry.EventID, fmt.Sprintf("%s-%d", entry.EventType, entry.CreatedAt))
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			if err := emit(PlanExecutionEvent{
				Type:      PlanExecutionEventType(entry.EventType),
				EventID:   entry.EventID,
				TraceID:   entry.TraceID,
				SessionID: entry.SessionID,
				PlanID:    entry.PlanID,
				StepID:    entry.StepID,
				Message:   entry.Summary,
				Payload:   cloneEventPayload(entry.Payload),
				CreatedAt: entry.CreatedAt,
			}); err != nil {
				return err
			}
			if isTerminalExecutionEventType(PlanExecutionEventType(entry.EventType)) {
				terminalEventSeen = true
			}
		}

		session, err := s.gateway.Store.Get(ctx, sessionID)
		if err == nil && (session.Status == SessionComplete || session.Status == SessionFailed || session.Status == StatusAbandoned) && !s.IsActive(sessionID) {
			if !terminalEventSeen {
				if err := emit(syntheticTerminalExecutionEvent(session)); err != nil {
					return err
				}
			}
			return nil
		}
	}
}

func NewApprovalToken() (string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(raw[:])
	return token, HashApprovalToken(token), nil
}

func HashApprovalToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func (s *AgentSession) PlanID() string {
	if s == nil || s.Plan == nil {
		return ""
	}
	return s.Plan.PlanID
}

func cloneEventPayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	return out
}

func isTerminalExecutionEventType(eventType PlanExecutionEventType) bool {
	return eventType == EventCompleted || eventType == EventStepFailed || eventType == EventPlanCancelled
}

func syntheticTerminalExecutionEvent(session *AgentSession) PlanExecutionEvent {
	eventType := EventStepFailed
	message := "Plan failed"
	payloadStatus := "failed"

	switch session.Status {
	case SessionComplete:
		eventType = EventCompleted
		message = "Plan completed"
		payloadStatus = "completed"
	case StatusAbandoned:
		eventType = EventPlanCancelled
		message = "Plan cancelled"
		payloadStatus = "cancelled"
	}

	return PlanExecutionEvent{
		Type:      eventType,
		TraceID:   NewTraceID(),
		SessionID: session.SessionID,
		PlanID:    session.PlanID(),
		Message:   message,
		CreatedAt: NowMillis(),
		Payload: map[string]any{
			"status":          payloadStatus,
			"synthetic":       true,
			"sessionTerminal": true,
		},
	}
}

func executeSessionRun(ctx context.Context, gateway *AgentGateway, sessionID string) (*AgentSession, error) {
	if gateway == nil || gateway.Executor == nil {
		return nil, fmt.Errorf("execution service unavailable")
	}
	session, err := gateway.Store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	resolvedQuery := strings.TrimSpace(ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery))
	if resolvedQuery == "" {
		return nil, fmt.Errorf("session query is required")
	}
	if session.Plan == nil {
		session.Plan = BuildDefaultPlan(session)
		if err := gateway.Store.Put(ctx, session, gateway.SessionTTL); err != nil {
			return nil, err
		}
	}

	slog.Info("wisdev execution run started",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "wisdev.execution_service",
		"operation", "execute_session_run",
		"stage", "run_started",
		"session_id", session.SessionID,
		"plan_id", session.PlanID(),
		"status", string(session.Status),
		"query_preview", truncateExecutionQueryPreview(resolvedQuery),
		"query_length", len(resolvedQuery),
		"step_count", len(session.Plan.Steps),
	)

	appendExecutionEvent(gateway, session, PlanExecutionEvent{
		Type:      EventProgress,
		TraceID:   NewTraceID(),
		SessionID: sessionID,
		PlanID:    session.PlanID(),
		Message:   "execution started",
		CreatedAt: NowMillis(),
		Payload: map[string]any{
			"status":                 "running",
			"queryPreview":           truncateExecutionQueryPreview(resolvedQuery),
			"queryLength":            len(resolvedQuery),
			"hasPlanningQuery":       strings.TrimSpace(session.Query) != "",
			"hasCorrectedQuery":      strings.TrimSpace(session.CorrectedQuery) != "",
			"hasOriginalQuery":       strings.TrimSpace(session.OriginalQuery) != "",
			"orchestrationStepCount": len(session.Plan.Steps),
		},
	})

	events := make(chan PlanExecutionEvent, 16)
	go gateway.Executor.Execute(ctx, session, events)

	for event := range events {
		appendExecutionEvent(gateway, session, event)
		logExecutionEventLifecycle(session, event)
		if err := gateway.Store.Put(ctx, session, gateway.SessionTTL); err != nil {
			slog.Error("wisdev execution session persist failed",
				"service", "go_orchestrator",
				"runtime", "go",
				"component", "wisdev.execution_service",
				"operation", "execute_session_run",
				"stage", "session_persist_failed",
				"session_id", session.SessionID,
				"plan_id", session.PlanID(),
				"event_type", string(event.Type),
				"step_id", event.StepID,
				"error", err.Error(),
			)
			return session, err
		}
	}

	if err := gateway.Store.Put(ctx, session, gateway.SessionTTL); err != nil {
		return session, err
	}
	slog.Info("wisdev execution run finished",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "wisdev.execution_service",
		"operation", "execute_session_run",
		"stage", "run_finished",
		"session_id", session.SessionID,
		"plan_id", session.PlanID(),
		"status", string(session.Status),
		"completed_steps", len(session.Plan.CompletedStepIDs),
		"failed_steps", len(session.Plan.FailedStepIDs),
		"total_steps", len(session.Plan.Steps),
	)
	return session, nil
}

func logExecutionEventLifecycle(session *AgentSession, event PlanExecutionEvent) {
	if session == nil {
		return
	}
	payload := event.Payload
	attempts := intFromAny(payload["attempts"])
	if attempts == 0 {
		attempts = intFromAny(payload["attempt"])
	}
	resultCount := intFromAny(payload["resultCount"])
	degraded := false
	if payload != nil {
		degraded, _ = payload["degraded"].(bool)
	}

	attrs := []any{
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "wisdev.execution_service",
		"operation", "execute_session_run",
		"stage", "event_observed",
		"session_id", session.SessionID,
		"plan_id", firstNonEmpty(event.PlanID, session.PlanID()),
		"event_type", string(event.Type),
		"step_id", event.StepID,
		"attempts", attempts,
		"result_count", resultCount,
		"degraded", degraded,
		"event_message", event.Message,
		"status", string(session.Status),
	}

	switch event.Type {
	case EventStepFailed:
		slog.Warn("wisdev execution event",
			append(attrs,
				"error_code", AsOptionalString(payload["errorCode"]),
				"failure_count", intFromAny(payload["failureCount"]),
			)...,
		)
	case EventStepCompleted, EventStepStarted, EventPlanRevised, EventProgress, EventCompleted:
		slog.Info("wisdev execution event", attrs...)
	}
}

func durableExecutionRootContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func truncateExecutionQueryPreview(query string) string {
	normalized := strings.TrimSpace(query)
	if len(normalized) <= 120 {
		return normalized
	}
	return normalized[:120]
}

func appendExecutionEvent(gateway *AgentGateway, session *AgentSession, event PlanExecutionEvent) {
	if gateway == nil || session == nil || gateway.Journal == nil {
		return
	}
	eventID := firstNonEmpty(event.EventID, event.TraceID, NewTraceID())
	traceID := firstNonEmpty(event.TraceID, eventID)
	payload := cloneEventPayload(event.Payload)
	if payload == nil {
		payload = map[string]any{}
	}
	if event.Type != "" {
		payload["eventType"] = event.Type
	}
	payload["eventId"] = eventID
	gateway.Journal.Append(RuntimeJournalEntry{
		EventID:   eventID,
		TraceID:   traceID,
		SessionID: session.SessionID,
		UserID:    session.UserID,
		PlanID:    event.PlanID,
		StepID:    event.StepID,
		EventType: string(event.Type),
		Path:      fmt.Sprintf("/agent/sessions/%s/events", session.SessionID),
		Status:    strings.ToLower(string(session.Status)),
		CreatedAt: event.CreatedAt,
		Summary:   event.Message,
		Payload:   payload,
	})
}
