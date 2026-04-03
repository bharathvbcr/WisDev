package api

import (
	"net/http"
	"os"
	"strings"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func (s *wisdevV2Server) registerHealthRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/runtime/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodGet, http.MethodPost},
			})
			return
		}
		rustBridgeEnabled := strings.TrimSpace(os.Getenv("WISDEV_RUST_BRIDGE_BIN")) != ""
		traceID := writeV2Envelope(w, "health", map[string]any{
			"engine":            "go_control_plane_v2",
			"policyVersion":     agentGateway.PolicyConfig.PolicyVersion,
			"controlPlane":      "go",
			"workerPlane":       "python-docs",
			"adk":               agentGateway.RuntimeMetadata(),
			"rustEnabled":       rustBridgeEnabled,
			"journalPath":       s.gateway.Journal.Path(),
			"journalIndex":      s.gateway.Journal.IndexPath(),
			"journalBackend":    map[bool]string{true: "postgres_indexed", false: "file_indexed"}[s.gateway.DB != nil],
			"stateStoreDir":     agentGateway.StateStore.BaseDir(),
			"stateStoreBackend": map[bool]string{true: "postgres", false: "file"}[s.gateway.DB != nil],
			"budgetDefaults": map[string]any{
				"maxToolCalls":  agentGateway.PolicyConfig.MaxToolCallsPerSession,
				"maxScriptRuns": agentGateway.PolicyConfig.MaxScriptRunsPerSession,
				"maxCostCents":  agentGateway.PolicyConfig.MaxCostPerSessionCents,
			},
		})
		s.journalEvent(
			"health_check",
			"/v2/runtime/health",
			traceID,
			"",
			"",
			"",
			"",
			"Runtime health requested.",
			map[string]any{"policyVersion": agentGateway.PolicyConfig.PolicyVersion},
			map[string]any{"rustEnabled": rustBridgeEnabled},
		)
	})
}
