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
		origDir, err := os.Getwd()
		assert.NoError(t, err)
		tmpDir := t.TempDir()
		assert.NoError(t, os.Chdir(tmpDir))

		t.Cleanup(func() {
			assert.NoError(t, os.Chdir(origDir))
			modelsOnce = sync.Once{}
			cachedModels = ModelConfig{}
		})

		if setupFile != nil {
			assert.NoError(t, setupFile())
		}
	}

	t.Run("fallback to defaults when file missing", func(t *testing.T) {
		resetAndUseTempDir(t, nil)
		// Ensure no model config exists in any checked path
		// (this is hard to guarantee, but we can check the result)
		models := FetchModelConfig()
		is.Equal(defaultModels.Heavy, models.Heavy)
		is.Equal(defaultModels.Standard, models.Standard)
		is.Equal(defaultModels.Light, models.Light)
	})

	t.Run("load from file", func(t *testing.T) {
		resetAndUseTempDir(t, func() error {
			content := `{"heavy": "custom-heavy", "standard": "custom-std", "light": "custom-light"}`
			return os.WriteFile("wisdev_models.json", []byte(content), 0644)
		})

		models := FetchModelConfig()
		is.Equal("custom-heavy", models.Heavy)
		is.Equal("custom-std", models.Standard)
		is.Equal("custom-light", models.Light)
	})

	t.Run("Resolve helpers", func(t *testing.T) {
		resetAndUseTempDir(t, nil)
		// Use defaults for simplicity
		is.Equal(defaultModels.Heavy, ResolveHeavyModel())
		is.Equal(defaultModels.Standard, ResolveStandardModel())
		is.Equal(defaultModels.Light, ResolveLightModel())
		is.Equal(defaultModels.Standard, ResolveBalancedModel())
	})

	t.Run("ResolveModelForTier", func(t *testing.T) {
		resetAndUseTempDir(t, nil)
		is.Equal(defaultModels.Light, ResolveModelForTier("light"))
		is.Equal(defaultModels.Standard, ResolveModelForTier("standard"))
		is.Equal(defaultModels.Standard, ResolveModelForTier("balanced"))
		is.Equal(defaultModels.Standard, ResolveModelForTier(""))
		is.Equal(defaultModels.Standard, ResolveModelForTier("legacy"))
		is.Equal(defaultModels.Heavy, ResolveModelForTier("heavy"))
	})
}
