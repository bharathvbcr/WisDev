package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevV2Server) registerSearchRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/wisdev/filter-web-search-results", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query   string                           `json:"query"`
			Results []wisdev.WebSearchResultItem     `json:"results"`
			Policy  wisdev.NormalizedWebSearchPolicy `json:"policy"`
			Context *wisdev.CapabilityExecuteContext `json:"context"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		policyPayload := req.Policy
		if strings.TrimSpace(policyPayload.Intent) == "" {
			policyPayload = wisdev.DeriveSearchPolicyHints(req.Query, &wisdev.SearchPolicyHints{
				Intent:         req.Policy.Intent,
				AllowedDomains: req.Policy.AllowedDomains,
				BlockedDomains: req.Policy.BlockedDomains,
				FreshnessDays:  req.Policy.FreshnessDays,
				MinSignalScore: req.Policy.MinSignalScore,
				MaxResults:     req.Policy.MaxResults,
			}, req.Context)
		}
		filtered, telemetry := wisdev.FilterAndRankWebSearchResults(req.Query, req.Results, policyPayload)
		writeJSONResponse(w, http.StatusOK, map[string]any{
			"ok": true,
			"result": map[string]any{
				"results":   filtered,
				"telemetry": telemetry,
			},
		})
	})
}
