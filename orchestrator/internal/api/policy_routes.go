package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevServer) registerPolicyRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	handleDeepAgentsCapabilities := func(w http.ResponseWriter, r *http.Request) {
		registry := (*wisdev.ToolRegistry)(nil)
		if agentGateway != nil {
			registry = agentGateway.Registry
		}
		writeEnvelope(w, "capabilities", wisdev.BuildDeepAgentsCapabilities(registry))
	}
	for _, path := range wisdevDeepAgentsCapabilitiesPaths {
		mux.HandleFunc(path, handleDeepAgentsCapabilities)
	}

	handleDeepAgentsPolicyResolve := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		var req struct {
			Mode                     string   `json:"mode"`
			EnableWisdevTools        *bool    `json:"enableWisdevTools"`
			AllowlistedTools         []string `json:"allowlistedTools"`
			RequireHumanConfirmation *bool    `json:"requireHumanConfirmation"`
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
				return
			}
		} else {
			req.Mode = strings.TrimSpace(r.URL.Query().Get("mode"))
		}
		registry := (*wisdev.ToolRegistry)(nil)
		if agentGateway != nil {
			registry = agentGateway.Registry
		}
		caps := wisdev.BuildDeepAgentsCapabilities(registry)
		executionPolicy := wisdev.ResolveDeepAgentsExecutionPolicy(
			caps,
			req.Mode,
			req.EnableWisdevTools,
			req.AllowlistedTools,
			req.RequireHumanConfirmation,
		)
		writeEnvelope(w, "policy", executionPolicy)
	}
	for _, path := range wisdevDeepAgentsPolicyResolvePaths {
		mux.HandleFunc(path, handleDeepAgentsPolicyResolve)
	}

	mux.HandleFunc("/policy/get", func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(r.URL.Query().Get("userId"))

		if userID == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId is required", nil)
			return
		}
		if _, err := resolveAuthorizedUserID(r, userID); err != nil {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, err.Error(), nil)
			return
		}

		policyBundle := policy.DefaultPolicyConfig()
		if agentGateway != nil {
			policyBundle = agentGateway.PolicyConfig
		}
		writeEnvelope(w, "policyBundle", policyBundle)
	})

	mux.HandleFunc("/policy/upsert", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		if !requireInternalPolicyAccess(w, r) {
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		writeEnvelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/policy/canary-config/upsert", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		if !requireInternalPolicyAccess(w, r) {
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		writeEnvelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/policy/function-bridge-config/upsert", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		if !requireInternalPolicyAccess(w, r) {
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		writeEnvelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/policy/function-bridge-config/get", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		if !requireInternalPolicyAccess(w, r) {
			return
		}
		writeEnvelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/policy/promote", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		if !requireInternalPolicyAccess(w, r) {
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		writeEnvelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/policy/rollback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		if !requireInternalPolicyAccess(w, r) {
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
			return
		}
		writeEnvelope(w, "result", map[string]any{"ok": true})
	})

	mux.HandleFunc("/runtime/traces/get", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", nil)
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
			UserID    string `json:"userId"`
			Limit     int    `json:"limit"`
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid body", nil)
				return
			}
		}
		if strings.TrimSpace(req.SessionID) == "" {
			req.SessionID = strings.TrimSpace(r.URL.Query().Get("sessionId"))
		}
		if strings.TrimSpace(req.UserID) == "" {
			req.UserID = strings.TrimSpace(r.URL.Query().Get("userId"))
		}
		if req.Limit <= 0 {
			if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
				var parsed int
				if _, err := fmt.Sscanf(rawLimit, "%d", &parsed); err == nil && parsed > 0 {
					req.Limit = parsed
				}
			}
		}
		if strings.TrimSpace(req.SessionID) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", nil)
			return
		}
		if strings.TrimSpace(req.UserID) != "" {
			if _, err := resolveAuthorizedUserID(r, strings.TrimSpace(req.UserID)); err != nil {
				WriteError(w, http.StatusForbidden, ErrUnauthorized, err.Error(), nil)
				return
			}
		}
		if _, ok := requireTraceSessionAccess(w, r, agentGateway, strings.TrimSpace(req.SessionID)); !ok {
			return
		}
		limit := req.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}

		traces := []wisdev.RuntimeJournalEntry{}
		if agentGateway != nil && agentGateway.Journal != nil {
			if entries := agentGateway.Journal.ReadSession(strings.TrimSpace(req.SessionID), limit); len(entries) > 0 {
				traces = entries
			}
		}
		writeEnvelope(w, "traces", traces)
	})
}
