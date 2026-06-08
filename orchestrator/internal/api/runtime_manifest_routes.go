package api

import (
	"context"
	"net/http"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
)

type dependencyStatus struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	Transport string `json:"transport"`
	Status    string `json:"status"`
}

func RegisterRuntimeManifestRoutes(mux *http.ServeMux, llmClient *llm.Client) {
	mux.HandleFunc("/internal/runtime/contract", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodGet,
			})
			return
		}

		overlay := stackconfig.CurrentOverlay()
		env := map[string]any{}
		for key, value := range overlay.Env {
			if key == "INTERNAL_SERVICE_KEY" {
				env[key] = "[redacted]"
				continue
			}
			env[key] = value
		}

		writeJSONResponse(w, http.StatusOK, map[string]any{
			"service":         "go_orchestrator",
			"status":          "ok",
			"manifestVersion": stackconfig.Manifest.Version,
			"environment":     stackconfig.CurrentOverlayName(),
			"contract": map[string]any{
				"version":      stackconfig.Manifest.Version,
				"environment":  stackconfig.Manifest.Environment,
				"services":     stackconfig.Manifest.Services,
				"dependencies": stackconfig.Manifest.Dependencies,
				"httpRoutes":   stackconfig.Manifest.HTTPRoutes,
				"grpcTargets":  stackconfig.Manifest.GRPCTargets,
				"authMode":     stackconfig.Manifest.AuthMode,
				"requiredEnv":  stackconfig.Manifest.RequiredEnv,
				"overlay": map[string]any{
					"name":            stackconfig.CurrentOverlayName(),
					"env":             env,
					"serviceBaseUrls": overlay.ServiceBaseURLs,
				},
			},
		})
	})

	mux.HandleFunc("/internal/runtime/probes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodGet,
			})
			return
		}

		dependency := dependencyStatus{
			Name:      "python_sidecar",
			Target:    stackconfig.ResolveGRPCTarget("python_sidecar"),
			Transport: "grpc-protobuf",
			Status:    "disabled",
		}
		if llmClient != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			dependency.Status = "ok"
			if _, err := llmClient.Health(ctx); err != nil {
				dependency.Status = "unavailable"
			}
		}

		status := "ok"
		code := http.StatusOK
		if dependency.Status == "unavailable" {
			status = "degraded"
			code = http.StatusServiceUnavailable
		}

		writeJSONResponse(w, code, map[string]any{
			"service":         "go_orchestrator",
			"status":          status,
			"manifestVersion": stackconfig.Manifest.Version,
			"environment":     stackconfig.CurrentOverlayName(),
			"dependencies":    []dependencyStatus{dependency},
			"transport":       "http-json",
			"latencyMs":       0,
			"lastCheckedAt":   time.Now().UnixMilli(),
		})
	})
}
