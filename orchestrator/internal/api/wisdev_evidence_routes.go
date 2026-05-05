package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevServer) registerEvidenceRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/wisdev/evidence-dossier", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodGet, http.MethodPost},
			})
			return
		}
		if agentGateway == nil || agentGateway.StateStore == nil {
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "state store unavailable", nil)
			return
		}

		dossierID := strings.TrimSpace(r.URL.Query().Get("dossierId"))
		if r.Method == http.MethodPost {
			var req struct {
				DossierID string `json:"dossierId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
				return
			}
			if dossierID == "" {
				dossierID = strings.TrimSpace(req.DossierID)
			}
		}
		if dossierID == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "dossierId is required", map[string]any{"field": "dossierId"})
			return
		}

		payload, err := agentGateway.StateStore.LoadEvidenceDossier(dossierID)
		if err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound, "evidence dossier not found", map[string]any{"dossierId": dossierID})
			return
		}
		ownerID := strings.TrimSpace(wisdev.AsOptionalString(payload["userId"]))
		if ownerID != "" && !requireOwnerAccess(w, r, ownerID) {
			return
		}

		traceID := writeEnvelope(w, "evidenceDossier", payload)
		s.journalEvent(
			"evidence_dossier_loaded",
			"/wisdev/evidence-dossier",
			traceID,
			"",
			GetUserID(r),
			"",
			"",
			"Evidence dossier loaded.",
			map[string]any{"dossierId": dossierID},
			nil,
		)
	})
}
