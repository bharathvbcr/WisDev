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
	loadDotEnvFile(path)
	assert.Equal(t, "from-shell", os.Getenv("WISDEV_LLM_MODEL"))
}

func TestLoadDotEnvFileSetsMissingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte(`WISDEV_LLM_BASE_URL="http://127.0.0.1:11434/v1"`+"\n"), 0o600))

	key := "WISDEV_LLM_BASE_URL"
	_ = os.Unsetenv(key)
	t.Cleanup(func() { _ = os.Unsetenv(key) })

	loadDotEnvFile(path)
	assert.Equal(t, "http://127.0.0.1:11434/v1", os.Getenv(key))
}
