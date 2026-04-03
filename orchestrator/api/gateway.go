package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	"strings"
)

type GatewayHandler struct {
	gateway *wisdev.AgentGateway
}

func NewGatewayHandler(gateway *wisdev.AgentGateway) *GatewayHandler {
	return &GatewayHandler{gateway: gateway}
}

func (h *GatewayHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v2/agent/sessions", h.HandleSessions)
	mux.HandleFunc("/v2/agent/sessions/", h.HandleSessionByID)
	mux.HandleFunc("/v2/agent/tools", h.HandleTools)
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
	session, err := h.gateway.CreateSession(r.Context(), req.UserID, req.OriginalQuery)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to create agent session", map[string]any{
			"error": err.Error(),
		})
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"session": session})
}

func (h *GatewayHandler) HandleSessionByID(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 4 {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session route not found", nil)
		return
	}

	sessionID := strings.TrimSpace(parts[3])
	if sessionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
			"field": "sessionId",
		})
		return
	}

	action := ""
	if len(parts) > 4 {
		action = strings.TrimSpace(parts[4])
	}

	switch action {
	case "execute":
		h.handleExecuteSession(w, r, sessionID)
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
	tools := h.gateway.Registry.List()
	writeJSONResponse(w, http.StatusOK, map[string]any{"tools": tools})
}

func (h *GatewayHandler) handleExecuteSession(w http.ResponseWriter, r *http.Request, sessionID string) {
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
	if session.Plan == nil {
		session.Plan = wisdev.BuildDefaultPlan(session)
		if putErr := h.gateway.Store.Put(r.Context(), session, h.gateway.SessionTTL); putErr != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist session", map[string]any{
				"error":     putErr.Error(),
				"sessionId": sessionID,
			})
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "streaming is not supported", nil)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events := make(chan wisdev.PlanExecutionEvent, 16)
	go h.gateway.Executor.Execute(r.Context(), session, events)

	for event := range events {
		payload, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			continue
		}
		_, _ = fmt.Fprintf(w, "event: %s\n", event.Type)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}

	_ = h.gateway.Store.Put(r.Context(), session, h.gateway.SessionTTL)
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
	session.Status = wisdev.SessionPaused
	session.UpdatedAt = wisdev.NowMillis()
	if err := h.gateway.Store.Put(r.Context(), session, h.gateway.SessionTTL); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist session", map[string]any{
			"error":     err.Error(),
			"sessionId": sessionID,
		})
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"ok": true, "sessionId": sessionID, "status": session.Status})
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
	if session.Plan == nil {
		session.Plan = wisdev.BuildDefaultPlan(session)
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
	writeJSONResponse(w, http.StatusOK, map[string]any{"ok": true, "sessionId": sessionID, "status": session.Status})
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
	session.Status = wisdev.StatusAbandoned
	session.UpdatedAt = wisdev.NowMillis()
	if err := h.gateway.Store.Put(r.Context(), session, h.gateway.SessionTTL); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist session", map[string]any{
			"error":     err.Error(),
			"sessionId": sessionID,
		})
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"ok": true, "sessionId": sessionID, "status": session.Status})
}
