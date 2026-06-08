package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func TestRouter_NewRouter_Minimal(t *testing.T) {
	cfg := ServerConfig{
		Version: "1.0.0",
	}
	handler := NewRouter(cfg)
	assert.NotNil(t, handler)

	t.Run("Health Check", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var res map[string]any
		json.NewDecoder(w.Body).Decode(&res)
		assert.Equal(t, "healthy", res["status"])
		assert.Equal(t, "1.0.0", res["version"])
	})
}

func TestEnsureAgentGateway_WithClients(t *testing.T) {
	cfg := ServerConfig{
		LLMClient:      llm.NewClient(),
		SearchRegistry: search.NewProviderRegistry(),
	}
	gateway := ensureAgentGateway(cfg)
	assert.NotNil(t, gateway)
	assert.NotNil(t, gateway.LLMClient)
	assert.NotNil(t, gateway.Brain)
	assert.NotNil(t, gateway.Gate)
	assert.NotNil(t, gateway.Loop)
}
