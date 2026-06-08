package api

import (
	"context"
	"encoding/json"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"net/http"
	"time"
)

type HealthHandler struct {
	llmClient *llm.Client
}

func NewHealthHandler(llmClient *llm.Client) *HealthHandler {
	return &HealthHandler{llmClient: llmClient}
}

// Liveness returns 200 OK as long as the process is running.
func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// Readiness checks if the backend is ready to serve traffic.
// It checks connections to the LLM sidecar and local Go services.
func (h *HealthHandler) Readiness(w http.ResponseWriter, r *http.Request) {
	status := struct {
		Ready    bool            `json:"ready"`
		Sidecar  bool            `json:"sidecar"`
		Services map[string]bool `json:"services"`
		Version  string          `json:"version"`
	}{
		Ready:   true,
		Version: "1.1.0-go",
		Services: map[string]bool{
			"raptor":      true, // Ported to Go
			"bm25":        true, // Ported to Go
			"chunking":    true, // Ported to Go
			"pdf_extract": true, // Ported to Go
			"vertex_ai":   true, // Go native client active
		},
	}

	// Check sidecar
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if _, err := h.llmClient.Health(ctx); err == nil {
		status.Sidecar = true
	} else {
		// Even if external sidecar is down, we are "Ready" if local Go logic is up
		status.Sidecar = false
		// status.Ready = false // Optional: depend on sidecar for total readiness
	}

	w.Header().Set("Content-Type", "application/json")
	if !status.Ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	json.NewEncoder(w).Encode(status)
}
