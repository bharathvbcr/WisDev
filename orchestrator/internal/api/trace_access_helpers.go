package api

import (
	"net/http"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func resolveTraceSessionOwner(r *http.Request, agentGateway *wisdev.AgentGateway, sessionID string) string {
	if agentGateway == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	if agentGateway.Store != nil {
		if session, err := agentGateway.Store.Get(r.Context(), sessionID); err == nil {
			if ownerID := strings.TrimSpace(session.UserID); ownerID != "" {
				return ownerID
			}
		}
	}
	if agentGateway.StateStore != nil {
		if session, err := agentGateway.StateStore.LoadAgentSession(sessionID); err == nil {
			if ownerID := strings.TrimSpace(wisdev.AsOptionalString(session["userId"])); ownerID != "" {
				return ownerID
			}
		}
	}
	return ""
}

func requireTraceSessionAccess(w http.ResponseWriter, r *http.Request, agentGateway *wisdev.AgentGateway, sessionID string) (string, bool) {
	ownerID := resolveTraceSessionOwner(r, agentGateway, sessionID)
	if ownerID == "" {
		WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied to resource", map[string]any{
			"sessionId": strings.TrimSpace(sessionID),
		})
		return "", false
	}
	if !requireOwnerAccess(w, r, ownerID) {
		return "", false
	}
	return ownerID, true
}

func requireSessionBindingAccess(w http.ResponseWriter, r *http.Request, agentGateway *wisdev.AgentGateway, sessionID string, expectedOwnerID string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", true
	}
	ownerID := resolveTraceSessionOwner(r, agentGateway, sessionID)
	if ownerID == "" {
		WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
			"sessionId": sessionID,
		})
		return "", false
	}
	expectedOwnerID = strings.TrimSpace(expectedOwnerID)
	if expectedOwnerID != "" && ownerID != expectedOwnerID {
		WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied to resource", map[string]any{
			"sessionId": sessionID,
		})
		return "", false
	}
	if !requireOwnerAccess(w, r, ownerID) {
		return "", false
	}
	return ownerID, true
}
