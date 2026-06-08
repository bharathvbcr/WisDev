package llm

import (
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestModels(t *testing.T) {
	is := assert.New(t)
	resetAndUseTempDir := func(t *testing.T, setupFile func() error) {
		t.Helper()
		modelsOnce = sync.Once{}
		embeddingsOnce = sync.Once{}
		cachedModels = ScholarModels{}
		cachedEmbeddings = ScholarEmbeddingModels{}

		origDir, err := os.Getwd()
		assert.NoError(t, err)
		tmpDir := t.TempDir()
		assert.NoError(t, os.Chdir(tmpDir))

		t.Cleanup(func() {
			assert.NoError(t, os.Chdir(origDir))
			modelsOnce = sync.Once{}
			embeddingsOnce = sync.Once{}
			cachedModels = ScholarModels{}
			cachedEmbeddings = ScholarEmbeddingModels{}
		})

		if setupFile != nil {
			assert.NoError(t, setupFile())
		}
	}

	t.Run("uses environment overrides when file missing", func(t *testing.T) {
		resetAndUseTempDir(t, nil)
		t.Setenv("AI_MODEL_HEAVY_ID", "env-heavy")
		t.Setenv("AI_MODEL_STANDARD_ID", "env-standard")
		t.Setenv("AI_MODEL_LIGHT_ID", "env-light")
		models := FetchModelConfig()
		is.Equal("env-heavy", models.Heavy)
		is.Equal("env-standard", models.Standard)
		is.Equal("env-light", models.Light)
	})

	t.Run("missing file and missing env returns empty config", func(t *testing.T) {
		resetAndUseTempDir(t, nil)
		is.Equal(ScholarModels{}, FetchModelConfig())
	})

	t.Run("load from file", func(t *testing.T) {
		resetAndUseTempDir(t, func() error {
			content := `{"tiers":{"heavy":"custom-heavy","standard":"custom-std","light":"custom-light"}}`
			return os.WriteFile("scholar_models.json", []byte(content), 0644)
		})

		models := FetchModelConfig()
		is.Equal("custom-heavy", models.Heavy)
		is.Equal("custom-std", models.Standard)
		is.Equal("custom-light", models.Light)
	})

	t.Run("load from explicit scholar models config path", func(t *testing.T) {
		var configPath string
		resetAndUseTempDir(t, func() error {
			configPath = t.TempDir() + string(os.PathSeparator) + "models.json"
			content := `{"tiers":{"heavy":"env-path-heavy","standard":"env-path-standard","light":"env-path-light"}}`
			return os.WriteFile(configPath, []byte(content), 0644)
		})
		t.Setenv("SCHOLAR_MODELS_CONFIG", configPath)

		models := FetchModelConfig()
		is.Equal("env-path-heavy", models.Heavy)
		is.Equal("env-path-standard", models.Standard)
		is.Equal("env-path-light", models.Light)
	})

	t.Run("load embeddings from file", func(t *testing.T) {
		resetAndUseTempDir(t, func() error {
			content := `{"tiers":{"heavy":"custom-heavy","standard":"custom-std","light":"custom-light"},"embeddings":{"primary":"embed-primary","standard":"embed-standard","fallback":"embed-fallback"}}`
			return os.WriteFile("scholar_models.json", []byte(content), 0644)
		})

		embeddings := FetchEmbeddingConfig()
		is.Equal("embed-primary", embeddings.Primary)
		is.Equal("embed-standard", ResolveEmbeddingModel("standard"))
		is.Equal("embed-fallback", ResolveEmbeddingModel("unknown"))
	})

	t.Run("load from legacy top-level file", func(t *testing.T) {
		resetAndUseTempDir(t, func() error {
			content := `{"heavy":"legacy-heavy","standard":"legacy-standard","light":"legacy-light"}`
			return os.WriteFile("scholar_models.json", []byte(content), 0644)
		})

		models := FetchModelConfig()
		is.Equal("legacy-heavy", models.Heavy)
		is.Equal("legacy-standard", models.Standard)
		is.Equal("legacy-light", models.Light)
	})

	t.Run("Resolve helpers", func(t *testing.T) {
		resetAndUseTempDir(t, func() error {
			content := `{"tiers":{"heavy":"helper-heavy","standard":"helper-standard","light":"helper-light"}}`
			return os.WriteFile("scholar_models.json", []byte(content), 0644)
		})
		is.Equal("helper-heavy", ResolveHeavyModel())
		is.Equal("helper-standard", ResolveStandardModel())
		is.Equal("helper-light", ResolveLightModel())
		is.Equal("helper-standard", ResolveBalancedModel())
	})

	t.Run("Resolve helpers honor explicit environment overrides", func(t *testing.T) {
		resetAndUseTempDir(t, func() error {
			content := `{"tiers":{"heavy":"file-heavy","standard":"file-standard","light":"file-light"},"embeddings":{"primary":"file-primary","standard":"file-embedding","fallback":"file-fallback"}}`
			return os.WriteFile("scholar_models.json", []byte(content), 0644)
		})
		t.Setenv("AI_MODEL_STANDARD_ID", "env-standard")
		t.Setenv("EMBEDDING_MODEL_STANDARD_ID", "env-embedding")

		is.Equal("env-standard", ResolveStandardModel())
		is.Equal("file-light", ResolveLightModel())
		is.Equal("env-embedding", ResolveEmbeddingModel("standard"))
		is.Equal("env-embedding", ResolveEmbeddingModel("primary"))
	})

	t.Run("ResolveModelForTier", func(t *testing.T) {
		resetAndUseTempDir(t, func() error {
			content := `{"tiers":{"heavy":"tier-heavy","standard":"tier-standard","light":"tier-light"}}`
			return os.WriteFile("scholar_models.json", []byte(content), 0644)
		})
		is.Equal("tier-light", ResolveModelForTier("light"))
		is.Equal("tier-standard", ResolveModelForTier("standard"))
		is.Equal("tier-standard", ResolveModelForTier("balanced"))
		is.Equal("tier-standard", ResolveModelForTier(""))
		is.Equal("tier-standard", ResolveModelForTier("legacy"))
		is.Equal("tier-heavy", ResolveModelForTier("heavy"))
	})
}
