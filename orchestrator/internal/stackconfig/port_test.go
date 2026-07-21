package stackconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPickListenPortPrefersFreePreferred(t *testing.T) {
	port, err := PickListenPort(0)
	require.NoError(t, err)
	assert.Greater(t, port, 0)
}

func TestAllocateLocalStackPortsWritesEnv(t *testing.T) {
	t.Setenv("WISDEV_AUTO_PORT", "1")
	ports, err := AllocateLocalStackPorts()
	require.NoError(t, err)
	assert.Greater(t, ports.OrchestratorHTTP, 0)
	assert.Greater(t, ports.SidecarHTTP, 0)

	allocated := []int{
		ports.OrchestratorHTTP,
		ports.OrchestratorMetrics,
		ports.OrchestratorGRPC,
		ports.SidecarHTTP,
		ports.SidecarGRPC,
	}
	assert.Equal(t, len(allocated), len(uniqueInts(allocated)), "allocated stack ports must be unique")

	env := ports.Env()
	assert.Contains(t, env["WISDEV_ORCHESTRATOR_URL"], "127.0.0.1")
	assert.Contains(t, env["PYTHON_SIDECAR_HTTP_URL"], "127.0.0.1")

	dir := t.TempDir()
	path := filepath.Join(dir, "ports.env")
	require.NoError(t, WritePortsEnv(path, ports))
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(body), "WISDEV_AUTO_PORT=1")
	assert.Contains(t, string(body), "PYTHON_SIDECAR_HTTP_URL=")
}

func TestAllocateLocalStackPortsAndWriteHoldsUntilPersisted(t *testing.T) {
	t.Setenv("WISDEV_AUTO_PORT", "1")
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.env")

	ports, err := AllocateLocalStackPortsAndWrite(path)
	require.NoError(t, err)
	assert.Greater(t, ports.OrchestratorHTTP, 0)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(body), fmt.Sprintf("PORT=%d", ports.OrchestratorHTTP))
}

func uniqueInts(values []int) map[int]struct{} {
	out := make(map[int]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func TestAutoPortEnabled(t *testing.T) {
	t.Setenv("WISDEV_AUTO_PORT", "1")
	assert.True(t, AutoPortEnabled())
	t.Setenv("WISDEV_AUTO_PORT", "0")
	assert.False(t, AutoPortEnabled())
}

func TestIsAutoPortSpec(t *testing.T) {
	assert.True(t, IsAutoPortSpec("auto"))
	assert.True(t, IsAutoPortSpec("0"))
	assert.False(t, IsAutoPortSpec("8081"))
}
