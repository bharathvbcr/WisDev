//go:build integration
// +build integration

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/testsupport"

	"github.com/stretchr/testify/require"
)

func TestGenerateRouteSpawnedPythonSidecarInvalidPromptIsPermanent(t *testing.T) {
	sidecar := testsupport.StartPythonSidecar(t)
	defer sidecar.Stop()

	t.Setenv("PYTHON_SIDECAR_HTTP_URL", sidecar.BaseURL)
	t.Setenv("PYTHON_SIDECAR_GRPC_ADDR", sidecar.GRPCAddr)
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "grpc")
	t.Setenv("INTERNAL_SERVICE_KEY", sidecar.InternalServiceKey)

	client := llm.NewClient()
	defer func() {
		require.NoError(t, client.Close())
	}()

	router := NewRouter(ServerConfig{
		Version:   "test",
		LLMClient: client,
	})

	req := httptest.NewRequest(http.MethodPost, "/generate", strings.NewReader(`{"prompt":"   ","tier":"heavy"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Key", sidecar.InternalServiceKey)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), `"kind":"permanent"`)
	require.Contains(t, rec.Body.String(), "INVALID_PROMPT")
}
