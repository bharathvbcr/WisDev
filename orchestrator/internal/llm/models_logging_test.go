package llm

import (
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchModelConfigLogsAndFallsBackOnInvalidJSON(t *testing.T) {
	t.Setenv("AI_MODEL_HEAVY_ID", "env-heavy")
	t.Setenv("AI_MODEL_STANDARD_ID", "env-standard")
	t.Setenv("AI_MODEL_LIGHT_ID", "env-light")

	originalWD, err := os.Getwd()
	require.NoError(t, err)

	tempDir := t.TempDir()
	require.NoError(t, os.Chdir(tempDir))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	modelsOnce = sync.Once{}
	embeddingsOnce = sync.Once{}
	cachedModels = ScholarModels{}
	cachedEmbeddings = ScholarEmbeddingModels{}
	t.Cleanup(func() {
		modelsOnce = sync.Once{}
		embeddingsOnce = sync.Once{}
		cachedModels = ScholarModels{}
		cachedEmbeddings = ScholarEmbeddingModels{}
	})

	require.NoError(t, os.WriteFile("scholar_models.json", []byte("{invalid json"), 0o644))

	models := FetchModelConfig()
	assert.Equal(t, ScholarModels{
		Heavy:    "env-heavy",
		Standard: "env-standard",
		Light:    "env-light",
	}, models)
}
