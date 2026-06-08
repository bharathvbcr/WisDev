package api

import (
	"net/http"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevServer) registerHealthRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/runtime/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodGet, http.MethodPost},
			})
			return
		}

		if agentGateway == nil || s.gateway == nil {
			writeEnvelope(w, "health", map[string]any{
				"ok":          false,
				"error":       "gateway_initializing",
				"description": "The WisDev gateway components are not yet initialized.",
			})
			return
		}

		policyConfig := agentGateway.PolicyConfig
		citationBroker := wisdev.ResolveCitationBrokerGateConfig()
		runtimeMeta := agentGateway.RuntimeMetadata()
		adkReady := runtimeMeta["ready"] == true

		traceID := writeEnvelope(w, "health", map[string]any{
			"ok":                adkReady,
			"ready":             adkReady,
			"engine":            "go_control_plane",
			"policyVersion":     policyConfig.PolicyVersion,
			"controlPlane":      "go",
			"workerPlane":       "python-docs",
			"adk":               runtimeMeta,
			"journalPath":       s.gateway.Journal.Path(),
			"journalIndex":      s.gateway.Journal.IndexPath(),
			"journalBackend":    map[bool]string{true: "postgres_indexed", false: "file_indexed"}[s.gateway.DB != nil],
			"stateStoreDir":     agentGateway.StateStore.BaseDir(),
			"stateStoreBackend": map[bool]string{true: "postgres", false: "file"}[s.gateway.DB != nil],
			"citationBroker":    citationBroker.Map(),
			"budgetDefaults": map[string]any{
				"maxToolCalls":  policyConfig.MaxToolCallsPerSession,
				"maxScriptRuns": policyConfig.MaxScriptRunsPerSession,
				"maxCostCents":  policyConfig.MaxCostPerSessionCents,
			},
		})
		s.journalEvent(
			"health_check",
			"/runtime/health",
			traceID,
			"",
			"",
			"",
			"",
			"Runtime health requested.",
			map[string]any{"policyVersion": policyConfig.PolicyVersion},
			map[string]any{"citationBrokerMode": citationBroker.Mode},
		)
	})
}
