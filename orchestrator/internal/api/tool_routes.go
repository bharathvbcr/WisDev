package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevServer) registerToolRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	registry := wisdev.NewToolRegistry()
	if agentGateway != nil && agentGateway.Registry != nil {
		registry = agentGateway.Registry
	}

	toolSearchHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
				"field": "query",
			})
			return
		}
		ranked := wisdev.RankTools(query, registry.List(), req.Limit)
		payload := map[string]any{
			"query": query,
			"tools": ranked,
			"count": len(ranked),
		}
		traceID := writeEnvelope(w, "toolSearch", payload)
		s.journalEvent("tool_search", r.URL.Path, traceID, "", "", "", "", "Tool search completed.", payload, nil)
	}

	mux.HandleFunc("/wisdev/tool-search", toolSearchHandler)
	mux.HandleFunc("/wisdev/wisdev.Tool-search", toolSearchHandler)

	mux.HandleFunc("/wisdev/structured-output", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SchemaType string         `json:"schemaType"`
			Payload    map[string]any `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		converted := make(map[string]any, len(req.Payload))
		for key, value := range req.Payload {
			converted[key] = value
		}
		validation := policy.ValidateStructuredOutput(req.SchemaType, converted)
		status := http.StatusOK
		if !validation.Valid {
			status = http.StatusBadRequest
		}
		payload := map[string]any{
			"schemaType": req.SchemaType,
			"valid":      validation.Valid,
			"reason":     validation.Reason,
			"normalized": validation.Normalized,
		}
		traceID := writeEnvelopeStatus(w, status, "structuredOutput", payload)
		s.journalEvent("structured_output", "/wisdev/structured-output", traceID, "", "", "", "", "Structured output validated.", payload, nil)
	})
}
