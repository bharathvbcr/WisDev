package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevV2Server) registerObserveRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/wisdev/observe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		hash := wisdev.ComputeTraceIntegrityHash(payload)
		responsePayload := map[string]any{
			"acknowledged": true,
			"traceHash":    hash,
		}
		traceID := writeV2Envelope(w, "observation", responsePayload)
		s.journalEvent(
			"observe",
			"/v2/wisdev/observe",
			traceID,
			strings.TrimSpace(fmt.Sprintf("%v", payload["sessionId"])),
			strings.TrimSpace(fmt.Sprintf("%v", payload["userId"])),
			strings.TrimSpace(fmt.Sprintf("%v", payload["planId"])),
			strings.TrimSpace(fmt.Sprintf("%v", payload["stepId"])),
			"Observation acknowledged.",
			responsePayload,
			map[string]any{"outcome": payload},
		)
	})
}
