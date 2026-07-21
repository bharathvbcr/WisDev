package envload

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDotEnvFileDoesNotOverrideExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte("WISDEV_LLM_MODEL=from-file\n"), 0o600))

	t.Setenv("WISDEV_LLM_MODEL", "from-shell")
	loadDotEnvFile(path, false)
	assert.Equal(t, "from-shell", os.Getenv("WISDEV_LLM_MODEL"))
}

func TestLoadDotEnvFileSetsMissingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte(`WISDEV_LLM_BASE_URL="http://127.0.0.1:11434/v1"`+"\n"), 0o600))

	key := "WISDEV_LLM_BASE_URL"
	_ = os.Unsetenv(key)
	t.Cleanup(func() { _ = os.Unsetenv(key) })

	loadDotEnvFile(path, false)
	assert.Equal(t, "http://127.0.0.1:11434/v1", os.Getenv(key))
}

func TestLoadDotEnvFilePortsOnlyRejectsNonPortKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.env")
	// A hostile ports.env mixing a legitimate port key with an injected one.
	content := "PYTHON_SIDECAR_PORT=9999\nWISDEV_LLM_BASE_URL=http://evil.example/v1\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	for _, key := range []string{"PYTHON_SIDECAR_PORT", "WISDEV_LLM_BASE_URL"} {
		_ = os.Unsetenv(key)
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}

	loadDotEnvFile(path, true)

	// The allowlisted port key is honored; the injected key is dropped.
	assert.Equal(t, "9999", os.Getenv("PYTHON_SIDECAR_PORT"))
	assert.Empty(t, os.Getenv("WISDEV_LLM_BASE_URL"))
}

func TestDotEnvWalkIsDepthBounded(t *testing.T) {
	// From a deep directory with no repo-root marker anywhere above it, the
	// walk must stop after maxDotEnvWalkDepth ancestors rather than climbing to
	// the filesystem root.
	root := t.TempDir()
	deep := root
	for i := 0; i < maxDotEnvWalkDepth+5; i++ {
		deep = filepath.Join(deep, "d")
	}
	require.NoError(t, os.MkdirAll(deep, 0o755))

	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	require.NoError(t, os.Chdir(deep))

	var portsCandidates int
	for _, src := range dotEnvCandidates() {
		if src.portsOnly {
			portsCandidates++
		}
	}
	// One ports.env per climbed level, capped by the depth bound (+1 for the
	// starting dir, +1 for the exe-relative candidate). Assert it never grows
	// unbounded toward the filesystem root.
	assert.LessOrEqual(t, portsCandidates, maxDotEnvWalkDepth+2)
}
