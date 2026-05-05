package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRuntimeManifestRoutesRequireInternalKey(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")
	router := NewRouter(ServerConfig{Version: "test"})

	for _, route := range []string{"/internal/runtime/contract", "/internal/runtime/probes"} {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code, route)
	}
}

func TestRuntimeManifestContractRedactsInternalKey(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")
	router := NewRouter(ServerConfig{Version: "test"})

	req := httptest.NewRequest(http.MethodGet, "/internal/runtime/contract", nil)
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]any
	err := json.Unmarshal(rec.Body.Bytes(), &payload)
	assert.NoError(t, err)
	assert.Equal(t, "go_orchestrator", payload["service"])
	assert.Equal(t, "ok", payload["status"])

	contract, ok := payload["contract"].(map[string]any)
	if !assert.True(t, ok) {
		return
	}
	assert.EqualValues(t, 4, contract["version"])

	overlay, ok := contract["overlay"].(map[string]any)
	if !assert.True(t, ok) {
		return
	}

	env, ok := overlay["env"].(map[string]any)
	if !assert.True(t, ok) {
		return
	}
	assert.Equal(t, "[redacted]", env["INTERNAL_SERVICE_KEY"])
}

func TestRuntimeManifestProbesExposeDependencyEnvelope(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")
	router := NewRouter(ServerConfig{Version: "test"})

	req := httptest.NewRequest(http.MethodGet, "/internal/runtime/probes", nil)
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]any
	err := json.Unmarshal(rec.Body.Bytes(), &payload)
	assert.NoError(t, err)
	assert.Equal(t, "go_orchestrator", payload["service"])

	dependencies, ok := payload["dependencies"].([]any)
	if !assert.True(t, ok) || !assert.Len(t, dependencies, 1) {
		return
	}

	dependency, ok := dependencies[0].(map[string]any)
	if !assert.True(t, ok) {
		return
	}
	assert.Equal(t, "python_sidecar", dependency["name"])
	assert.Equal(t, "grpc-protobuf", dependency["transport"])
	assert.NotEmpty(t, dependency["target"])
}
