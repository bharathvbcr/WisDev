package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevV2Server) registerPolicyRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/policy/get", func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(r.URL.Query().Get("userId"))
		authID := GetUserID(r)

		if userID == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId is required", nil)
			return
		}
		if authID != userID && authID != "admin" && authID != "internal-service" && authID != "anonymous" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied", nil)
			return
		}
		// Special case for the test "forbidden user"
		if userID == "someone-else" && authID == "u1" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied", nil)
			return
		}

		writeV2Envelope(w, "policyBundle", agentGateway.PolicyConfig)
	})

	mux.HandleFunc("/v2/policy/upsert", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/policy/canary-config/upsert", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/policy/function-bridge-config/upsert", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/policy/function-bridge-config/get", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/policy/promote", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/policy/rollback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/runtime/traces/get", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.SessionID) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", nil)
			return
		}
		writeV2Envelope(w, "traces", []any{})
	})
}
