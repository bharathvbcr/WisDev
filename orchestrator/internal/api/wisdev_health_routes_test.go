package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

func TestRegisterHealthRoutesReportsADKReadiness(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	journal := wisdev.NewRuntimeJournal(nil)
	gateway := &wisdev.AgentGateway{
		PolicyConfig: policy.DefaultPolicyConfig(),
		StateStore:   wisdev.NewRuntimeStateStore(nil, journal),
		Journal:      journal,
		ADKRuntime: &wisdev.ADKRuntime{
			Config:    wisdev.DefaultADKRuntimeConfig(),
			InitError: "missing GOOGLE_API_KEY",
		},
	}
	server := &wisdevServer{gateway: gateway}
	mux := http.NewServeMux()
	server.registerHealthRoutes(mux, gateway)

	req := httptest.NewRequest(http.MethodGet, "/runtime/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var envelope map[string]any
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
	health, ok := envelope["health"].(map[string]any)
	if assert.True(t, ok) {
		assert.Equal(t, false, health["ok"])
		assert.Equal(t, false, health["ready"])
		adk, ok := health["adk"].(map[string]any)
		if assert.True(t, ok) {
			assert.Equal(t, "init_error", adk["status"])
			assert.Equal(t, false, adk["ready"])
		}
	}
}
