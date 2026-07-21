package stackconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPortsEnvPathWalkDepthBounded(t *testing.T) {
	t.Setenv("WISDEV_PORTS_ENV", "")

	root := t.TempDir()
	deep := root
	for i := 0; i < maxPortsEnvWalkDepth+4; i++ {
		deep = filepath.Join(deep, "nested")
		requireMkdir(t, deep)
	}

	cwd, err := os.Getwd()
	assert.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})
	requireChdir(t, deep)

	path := PortsEnvPath()
	home, err := os.UserHomeDir()
	assert.NoError(t, err)
	expectedFallback := filepath.Join(home, ".wisdev", portsEnvFileName)
	assert.Equal(t, expectedFallback, path)
}

func requireMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func requireChdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
}

func TestPortsEnvPathHonorsExplicitOverride(t *testing.T) {
	t.Setenv("WISDEV_PORTS_ENV", "/tmp/custom-ports.env")
	assert.Equal(t, "/tmp/custom-ports.env", PortsEnvPath())
}

func TestPortsEnvPathFindsWisdevArcRoot(t *testing.T) {
	t.Setenv("WISDEV_PORTS_ENV", "")

	root := t.TempDir()
	orchestratorDir := filepath.Join(root, "orchestrator", "cmd", "wisdev")
	requireMkdir(t, orchestratorDir)

	cwd, err := os.Getwd()
	assert.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})
	requireChdir(t, root)

	path := PortsEnvPath()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	assert.NoError(t, err)
	expected := filepath.Join(resolvedRoot, ".wisdev", portsEnvFileName)
	assert.Equal(t, expected, path)
}
