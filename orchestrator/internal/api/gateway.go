package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

var agentSessionEventKeepAliveInterval = 15 * time.Second

func isGatewayQueryValidationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "query is required")
}

func writeGatewayExecutionStartError(
	w http.ResponseWriter,
	err error,
	operation string,
	sessionID string,
) {
	if isGatewayQueryValidationError(err) {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
			"sessionId": sessionID,
			"field":     "query",
		})
		return
	}
	WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, operation, map[string]any{
		"error":     err.Error(),
		"sessionId": sessionID,
	})
}

func writeGatewayCreateSessionError(w http.ResponseWriter, err error, userID string) {
	if isGatewayQueryValidationError(err) {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
			"userId": userID,
			"field":  "originalQuery",
		})
		return
	}
	WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to create agent session", map[string]any{
		"error":  err.Error(),
		"userId": userID,
	})
}

func resolveGatewaySessionQuery(session *wisdev.AgentSession) string {
	if session == nil {
		return ""
	}
	return wisdev.ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery)
}

func writeGatewayMissingSessionQueryError(w http.ResponseWriter, sessionID string) {
	WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "session query is required", map[string]any{
		"sessionId": sessionID,
		"field":     "query",
	})
}

type GatewayHandler struct {
	gateway *wisdev.AgentGateway
}

type resumeSessionRequest struct {
	CheckpointID  string         `json:"checkpointId"`
	ApprovalToken string         `json:"approvalToken"`
	Action        string         `json:"action"`
	PayloadEdits  map[string]any `json:"payloadEdits,omitempty"`
}

func NewGatewayHandler(gateway *wisdev.AgentGateway) *GatewayHandler {
	if gateway != nil && gateway.Execution == nil {
		gateway.Execution = wisdev.NewDurableExecutionService(gateway)
	}
	return &GatewayHandler{gateway: gateway}
}

func (h *GatewayHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/agent-card.json", h.HandleAgentCard)
	mux.HandleFunc("/agent/card", h.HandleAgentCard)
	mux.HandleFunc("/agent/sessions", h.HandleSessions)
	mux.HandleFunc("/agent/sessions/", h.HandleSessionByID)
	mux.HandleFunc("/agent/tools", h.HandleTools)
}

func (h *GatewayHandler) HandleAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	if h.gateway == nil || h.gateway.ADKRuntime == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "agent runtime is not initialized", nil)
		return
	}
	card := cloneAnyMap(h.gateway.ADKRuntime.BuildA2ACard())
	if len(card) == 0 {
		WriteError(w, http.StatusNotFound, ErrNotFound, "agent card is not exposed", nil)
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, r.Host)
	card["url"] = baseURL + r.URL.Path
	card["preferredTransport"] = "http-json"
	writeJSONResponse(w, http.StatusOK, map[string]any{"agentCard": card})
}

func (h *GatewayHandler) HandleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	// Simplified implementation for now
	var req struct {
		UserID        string `json:"userId"`
		OriginalQuery string `json:"originalQuery"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.UserID) == "" {
		req.UserID = GetUserID(r)
	}
	if req.UserID == "" {
		WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
		return
	}
	if caller := GetUserID(r); caller != "" && caller != req.UserID && caller != "admin" && caller != "internal-service" {
		WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied to resource", nil)
		return
	}
	if h.gateway != nil && h.gateway.Idempotency != nil {
		if statusCode, body, ok := h.gateway.Idempotency.Get(h.idempotencyKey(r, req.UserID, req.OriginalQuery)); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			_, _ = w.Write(body)
			return
		}
	}
	session, err := h.gateway.CreateSession(r.Context(), req.UserID, req.OriginalQuery)
	if err != nil {
		writeGatewayCreateSessionError(w, err, req.UserID)
		return
	}
	response := map[string]any{"session": session}
	if h.gateway != nil && h.gateway.Idempotency != nil {
		h.gateway.Idempotency.Put(h.idempotencyKey(r, req.UserID, req.OriginalQuery), http.StatusOK, response)
	}
	writeJSONResponse(w, http.StatusOK, response)
}

func (h *GatewayHandler) HandleSessionByID(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")

	sessionsIdx := -1
	for i, segment := range parts {
		if segment == "sessions" {
			sessionsIdx = i
			break
		}
	}

	if sessionsIdx == -1 || len(parts) <= sessionsIdx+1 {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session route not found", nil)
		return
	}

	sessionID := strings.TrimSpace(parts[sessionsIdx+1])
	if sessionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
			"field": "sessionId",
		})
		return
	}

	action := ""
	if len(parts) > sessionsIdx+2 {
		action = strings.TrimSpace(parts[sessionsIdx+2])
	}

	switch action {
	case "execute":
		h.handleExecuteSession(w, r, sessionID)
	case "events":
		h.handleStreamSessionEvents(w, r, sessionID)
	case "cancel":
		h.handleCancelSession(w, r, sessionID)
	case "resume":
		h.handleResumeSession(w, r, sessionID)
	case "abandon":
		h.handleAbandonSession(w, r, sessionID)
	default:
		WriteError(w, http.StatusNotFound, ErrNotFound, "session action not found", map[string]any{
			"action": action,
		})
	}
}

func (h *GatewayHandler) HandleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	if h.gateway == nil || h.gateway.Registry == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrWisdevFailed, "tool registry is not initialized", nil)
		return
	}
	tools := h.gateway.Registry.List()
	writeJSONResponse(w, http.StatusOK, map[string]any{"tools": tools})
}

func (h *GatewayHandler) handleExecuteSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	session, err := h.gateway.Store.Get(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}
	if !requireOwnerAccess(w, r, session.UserID) {
		return
	}
	if h.gateway != nil && h.gateway.Idempotency != nil {
		if statusCode, body, ok := h.gateway.Idempotency.Get(h.idempotencyKey(r, session.UserID, sessionID)); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			_, _ = w.Write(body)
			return
		}
	}
	result, err := h.gateway.Execution.Start(r.Context(), sessionID)
	if err != nil {
		writeGatewayExecutionStartError(w, err, "failed to start agent session execution", sessionID)
		return
	}
	response := map[string]any{
		"ok":             true,
		"sessionId":      result.SessionID,
		"status":         result.Status,
		"executionId":    result.ExecutionID,
		"alreadyRunning": result.AlreadyRunning,
	}
	if h.gateway != nil && h.gateway.Idempotency != nil {
		h.gateway.Idempotency.Put(h.idempotencyKey(r, session.UserID, sessionID), http.StatusOK, response)
	}
	writeJSONResponse(w, http.StatusOK, response)
}

func (h *GatewayHandler) handleCancelSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	session, err := h.gateway.Store.Get(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}
	if !requireOwnerAccess(w, r, session.UserID) {
		return
	}
	if h.gateway != nil && h.gateway.Idempotency != nil {
		if statusCode, body, ok := h.gateway.Idempotency.Get(h.idempotencyKey(r, session.UserID, sessionID)); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			_, _ = w.Write(body)
			return
		}
	}
	if err := h.gateway.Execution.Cancel(r.Context(), sessionID); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist session", map[string]any{
			"error":     err.Error(),
			"sessionId": sessionID,
		})
		return
	}
	response := map[string]any{"ok": true, "sessionId": sessionID, "status": wisdev.SessionPaused}
	if h.gateway != nil && h.gateway.Idempotency != nil {
		h.gateway.Idempotency.Put(h.idempotencyKey(r, session.UserID, sessionID), http.StatusOK, response)
	}
	writeJSONResponse(w, http.StatusOK, response)
}

func (h *GatewayHandler) handleResumeSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	session, err := h.gateway.Store.Get(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}
	if !requireOwnerAccess(w, r, session.UserID) {
		return
	}
	if resolveGatewaySessionQuery(session) == "" {
		writeGatewayMissingSessionQueryError(w, sessionID)
		return
	}
	if h.gateway != nil && h.gateway.Idempotency != nil {
		if statusCode, body, ok := h.gateway.Idempotency.Get(h.idempotencyKey(r, session.UserID, sessionID)); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			_, _ = w.Write(body)
			return
		}
	}
	preResumeSession := cloneExecutionSession(session)
	if session.Plan == nil {
		session.Plan = wisdev.BuildDefaultPlan(session)
	}

	var req resumeSessionRequest
	if r.Body != nil {
		if err := decodeStrictJSONBody(r.Body, &req); err != nil && !errors.Is(err, io.EOF) {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
	}

	approvalToken := strings.TrimSpace(req.ApprovalToken)
	if approvalToken == "" {
		approvalToken = strings.TrimSpace(req.CheckpointID)
	}

	action := wisdev.CanonicalizeConfirmationAction(req.Action)
	if action == "" {
		action = "approve"
	}

	if pendingApprovalID := strings.TrimSpace(session.Plan.PendingApprovalID); pendingApprovalID != "" {
		if expiresAt := session.Plan.PendingApprovalExpiresAt; expiresAt > 0 && wisdev.NowMillis() > expiresAt {
			if _, err := clearExpiredPendingApproval(r.Context(), h.gateway, session); err != nil {
				WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist session", map[string]any{
					"error":     err.Error(),
					"sessionId": sessionID,
				})
				return
			}
			WriteError(w, http.StatusConflict, ErrInvalidParameters, "approval token has expired", map[string]any{
				"sessionId": sessionID,
				"action":    action,
				"expiresAt": expiresAt,
			})
			return
		}
		if approvalToken == "" {
			WriteError(w, http.StatusConflict, ErrInvalidParameters, "approval token is required to resume this session", map[string]any{
				"sessionId": sessionID,
				"action":    action,
			})
			return
		}
		if wisdev.HashApprovalToken(approvalToken) != session.Plan.PendingApprovalTokenHash {
			WriteError(w, http.StatusConflict, ErrInvalidParameters, "approval token does not match the pending confirmation", map[string]any{
				"sessionId": sessionID,
				"action":    action,
			})
			return
		}
		switch action {
		case "approve":
			if session.Plan.ApprovedStepIDs == nil {
				session.Plan.ApprovedStepIDs = map[string]bool{}
			}
			if stepID := strings.TrimSpace(session.Plan.PendingApprovalStepID); stepID != "" {
				session.Plan.ApprovedStepIDs[stepID] = true
			}
		case "skip":
			if session.Plan.CompletedStepIDs == nil {
				session.Plan.CompletedStepIDs = map[string]bool{}
			}
			if stepID := strings.TrimSpace(session.Plan.PendingApprovalStepID); stepID != "" {
				session.Plan.CompletedStepIDs[stepID] = true
			}
		case "reject_and_replan":
			if session.Plan.FailedStepIDs == nil {
				session.Plan.FailedStepIDs = map[string]string{}
			}
			if stepID := strings.TrimSpace(session.Plan.PendingApprovalStepID); stepID != "" {
				session.Plan.FailedStepIDs[stepID] = "human_rejected_replan"
			}
		case "edit_payload":
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "edit_payload resume action is not supported by this endpoint yet", map[string]any{
				"sessionId": sessionID,
			})
			return
		default:
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "unsupported resume action", map[string]any{
				"sessionId": sessionID,
				"action":    action,
			})
			return
		}

		session.Plan.PendingApprovalID = ""
		session.Plan.PendingApprovalTokenHash = ""
		session.Plan.PendingApprovalStepID = ""
		session.Plan.PendingApprovalExpiresAt = 0
	} else if approvalToken != "" {
		WriteError(w, http.StatusConflict, ErrInvalidParameters, "approval token replayed without a pending confirmation", map[string]any{
			"sessionId": sessionID,
			"action":    action,
		})
		return
	}

	session.Status = wisdev.SessionExecutingPlan
	session.UpdatedAt = wisdev.NowMillis()
	if err := h.gateway.Store.Put(r.Context(), session, h.gateway.SessionTTL); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist session", map[string]any{
			"error":     err.Error(),
			"sessionId": sessionID,
		})
		return
	}
	result, err := h.gateway.Execution.Start(r.Context(), sessionID)
	if err != nil {
		if preResumeSession != nil && h.gateway != nil && h.gateway.Store != nil {
			if rollbackErr := h.gateway.Store.Put(r.Context(), preResumeSession, h.gateway.SessionTTL); rollbackErr != nil {
				WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to restore session after execution start failure", map[string]any{
					"error":         err.Error(),
					"rollbackError": rollbackErr.Error(),
					"sessionId":     sessionID,
				})
				return
			}
		}
		writeGatewayExecutionStartError(w, err, "failed to resume agent session execution", sessionID)
		return
	}
	response := map[string]any{"ok": true, "sessionId": sessionID, "status": result.Status, "action": action}
	if h.gateway != nil && h.gateway.Idempotency != nil {
		h.gateway.Idempotency.Put(h.idempotencyKey(r, session.UserID, sessionID), http.StatusOK, response)
	}
	writeJSONResponse(w, http.StatusOK, response)
}

func (h *GatewayHandler) handleAbandonSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	session, err := h.gateway.Store.Get(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}
	if !requireOwnerAccess(w, r, session.UserID) {
		return
	}
	if err := h.gateway.Execution.Abandon(r.Context(), sessionID); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to abandon session", map[string]any{
			"error":     err.Error(),
			"sessionId": sessionID,
		})
		return
	}
	if updated, getErr := h.gateway.Store.Get(r.Context(), sessionID); getErr == nil {
		session = updated
	} else {
		session.Status = wisdev.StatusAbandoned
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"ok": true, "sessionId": sessionID, "status": session.Status})
}

func (h *GatewayHandler) handleStreamSessionEvents(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	session, err := h.gateway.Store.Get(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}
	if !requireOwnerAccess(w, r, session.UserID) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "streaming is not supported", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	streamStarted := false
	var writeMu sync.Mutex
	hasStreamStarted := func() bool {
		writeMu.Lock()
		defer writeMu.Unlock()
		return streamStarted
	}
	stopKeepAlive := make(chan struct{})
	var keepAliveWG sync.WaitGroup
	keepAliveWG.Add(1)
	go func() {
		defer keepAliveWG.Done()
		ticker := time.NewTicker(agentSessionEventKeepAliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-stopKeepAlive:
				return
			case <-ticker.C:
				writeMu.Lock()
				streamStarted = true
				_, _ = fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
				writeMu.Unlock()
			}
		}
	}()
	defer func() {
		close(stopKeepAlive)
		keepAliveWG.Wait()
	}()

	err = h.gateway.Execution.Stream(r.Context(), sessionID, func(event wisdev.PlanExecutionEvent) error {
		payload, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return marshalErr
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		streamStarted = true
		_, _ = fmt.Fprintf(w, "event: %s\n", event.Type)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		return nil
	})
	if err != nil && r.Context().Err() == nil {
		if hasStreamStarted() {
			slog.Warn("agent session event stream terminated after partial SSE response",
				"component", "api.gateway",
				"operation", "streamSessionEvents",
				"sessionId", sessionID,
				"error", err.Error(),
			)
			return
		}
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to stream agent session events", map[string]any{
			"error":     err.Error(),
			"sessionId": sessionID,
		})
	}
}

func (h *GatewayHandler) idempotencyKey(r *http.Request, owner string, subject string) string {
	raw := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("X-Idempotency-Key"))
	}
	if raw == "" {
		return ""
	}
	return fmt.Sprintf("%s|%s|%s|%s", r.Method, strings.TrimSpace(owner), strings.TrimSpace(subject), raw)
}
