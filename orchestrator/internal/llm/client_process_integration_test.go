//go:build integration
// +build integration

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/testsupport"
	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClientSpawnedPythonSidecarSmoke(t *testing.T) {
	sidecar := testsupport.StartPythonSidecar(t)
	defer sidecar.Stop()

	t.Setenv("PYTHON_SIDECAR_HTTP_URL", sidecar.BaseURL)
	t.Setenv("PYTHON_SIDECAR_GRPC_ADDR", sidecar.GRPCAddr)
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "grpc")
	t.Setenv("INTERNAL_SERVICE_KEY", sidecar.InternalServiceKey)

	client := NewClient()
	defer func() {
		require.NoError(t, client.Close())
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	health, err := client.Health(ctx)
	require.NoError(t, err)
	require.True(t, health.GetOk())
	require.NotEmpty(t, health.GetVersion())

	runtimeHealth, err := client.RuntimeHealth(ctx)
	require.NoError(t, err)
	require.Equal(t, "python_sidecar", runtimeHealth.Service)
	require.Equal(t, "ok", runtimeHealth.Status)

	dependencyByName := map[string]RuntimeDependency{}
	for _, dep := range runtimeHealth.Dependencies {
		dependencyByName[dep.Name] = dep
	}
	require.Equal(t, "ok", dependencyByName["grpc_sidecar"].Status)
	require.Equal(t, "configured", dependencyByName["gemini_runtime"].Status)

	_, err = client.Generate(ctx, &llmpb.GenerateRequest{Prompt: "   "})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.InvalidArgument, st.Code())

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(st.Message()), &payload))
	errorPayload, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "INVALID_PROMPT", fmt.Sprint(errorPayload["code"]))
}
