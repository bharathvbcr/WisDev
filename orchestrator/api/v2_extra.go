package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevV2Server) registerExtraRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/outcomes/recent", func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(r.URL.Query().Get("userId"))
		if userID == "" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
			return
		}
		writeV2Envelope(w, "outcomes", []any{})
	})

	mux.HandleFunc("/v2/feedback/save", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID    string `json:"userId"`
			SessionID string `json:"sessionId"`
			Feedback  string `json:"feedback"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.SessionID) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId and sessionId are required", nil)
			return
		}
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/feedback/get", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID    string `json:"userId"`
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.SessionID) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId and sessionId are required", nil)
			return
		}
		writeV2Envelope(w, "feedback", map[string]any{})
	})

	mux.HandleFunc("/v2/feedback/analytics", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.UserID) == "" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
			return
		}
		writeV2Envelope(w, "analytics", map[string]any{})
	})

	mux.HandleFunc("/v2/memory/profile/learn", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		if strings.TrimSpace(req.UserID) == "" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
			return
		}
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/memory/profile/get", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string `json:"userId"`
		}
		// Try to decode if body exists, otherwise use query
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.UserID == "" {
			req.UserID = r.URL.Query().Get("userId")
		}
		
		if strings.TrimSpace(req.UserID) == "" {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "userId is required", nil)
			return
		}
		writeV2Envelope(w, "policyBundle", agentGateway.PolicyConfig)
	})

	mux.HandleFunc("/v2/telemetry/delete-session", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/runtime/retention/run", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/evaluate/replay", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/evaluate/shadow", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/evaluate/canary", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/v2/policy/gates/get", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "gates", []any{})
	})

	mux.HandleFunc("/v2/policy/canary-config/get", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "config", map[string]any{})
	})

	// Additional WisDev Contract Routes
	mux.HandleFunc("/v2/wisdev/plan/revision", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"applied": true, "reason": "Go native replan"})
	})
	mux.HandleFunc("/v2/wisdev/subtopics/generate", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"subtopics": []string{"Methods", "Results", "Discussion"}})
	})
	mux.HandleFunc("/v2/wisdev/search-coverage/evaluate", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"coverage": 0.8, "gaps": []string{}})
	})
	mux.HandleFunc("/v2/wisdev/follow-up/check", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"needsFollowUp": true})
	})
	
	mux.HandleFunc("/v2/agent/abandon-session", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})
	mux.HandleFunc("/v2/agent/question/options", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "options", []any{})
	})
	mux.HandleFunc("/v2/agent/question/recommendations", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "recommendations", []any{})
	})
	mux.HandleFunc("/v2/agent/question/regenerate", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"ok": true})
	})
	mux.HandleFunc("/v2/wisdev/analyze-query", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "result", map[string]any{"complexity": 0.5})
	})
	mux.HandleFunc("/v2/wisdev/traces", func(w http.ResponseWriter, r *http.Request) {
		writeV2Envelope(w, "traces", []any{})
	})
}
