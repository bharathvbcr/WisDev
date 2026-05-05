package api

import (
	"encoding/json"
	"fmt"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	"net/http"
	"strings"
)

func (h *WisDevHandler) HandleGetTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}

	if h.gateway == nil || h.gateway.Journal == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := fmt.Sscanf(raw, "%d", &limit); n != 1 || err != nil || limit <= 0 {
			limit = 50
		}
	}
	if limit > 200 {
		limit = 200
	}

	var result any
	if sessionID != "" {
		if _, ok := requireTraceSessionAccess(w, r, h.gateway, sessionID); !ok {
			return
		}
		entries := h.gateway.Journal.ReadSession(sessionID, limit)
		if entries == nil {
			entries = []wisdev.RuntimeJournalEntry{}
		}
		result = entries
	} else {
		resolvedUserID, err := resolveAuthorizedUserID(r, userID)
		if err != nil {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, err.Error(), nil)
			return
		}
		summary := h.gateway.Journal.SummarizeRecentOutcomes(resolvedUserID, limit)
		result = summary
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
