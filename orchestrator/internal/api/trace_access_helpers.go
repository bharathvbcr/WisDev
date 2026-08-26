package api

import (
	"net/http"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

// traceOwnerJournalProbeLimit bounds the journal read used only to resolve a
// session's owner. Ownership is uniform across a session's entries, so the newest
// few are enough — the handler reads the full window separately once authorized.
const traceOwnerJournalProbeLimit = 5

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
	// Journal fallback. Runs that never mint an AgentSession — /wisdev/research/deep
	// is the main one, it journals under the caller's sessionId but never calls
	// Store.Put — are otherwise unresolvable, so their traces stay permanently
	// inaccessible to the very user who produced them. Journal entries record the
	// authenticated userID at write time, which makes them an authoritative owner
	// source; requireOwnerAccess still enforces caller == owner on top of this.
	if agentGateway.Journal != nil {
		for _, entry := range agentGateway.Journal.ReadSession(sessionID, traceOwnerJournalProbeLimit) {
			if ownerID := strings.TrimSpace(entry.UserID); ownerID != "" {
				return ownerID
			}
		}
	}
	return ""
}

func requireTraceSessionAccess(w http.ResponseWriter, r *http.Request, agentGateway *wisdev.AgentGateway, sessionID string) (string, bool) {
	ownerID := resolveTraceSessionOwner(r, agentGateway, sessionID)
	if ownerID == "" {
		// No owner anywhere means the session is unknown, not forbidden. Reporting
		// this as 403 told pollers "retry with better credentials" for a session
		// that will never resolve; 404 is terminal and matches handleGetSession.
		WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
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
