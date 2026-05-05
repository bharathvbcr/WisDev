package llm

import (
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchModelConfigLogsAndFallsBackOnInvalidJSON(t *testing.T) {
	originalWD, err := os.Getwd()
	require.NoError(t, err)

	tempDir := t.TempDir()
	require.NoError(t, os.Chdir(tempDir))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	modelsOnce = sync.Once{}
	cachedModels = ModelConfig{}

	require.NoError(t, os.WriteFile("wisdev_models.json", []byte("{invalid json"), 0o644))

	models := FetchModelConfig()
	assert.Equal(t, defaultModels, models)
}
