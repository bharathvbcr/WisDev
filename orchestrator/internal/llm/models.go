package llm

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

type ModelConfig struct {
	Heavy    string `json:"heavy"`
	Standard string `json:"standard"`
	Light    string `json:"light"`
}

var (
	defaultModels = ModelConfig{
		Heavy:    "gemini-2.5-pro",
		Standard: "gemini-2.5-flash",
		Light:    "gemini-2.5-flash-lite",
	}
	cachedModels ModelConfig
	modelsOnce   sync.Once
)

func FetchModelConfig() ModelConfig {
	modelsOnce.Do(func() {
		cachedModels = loadModelConfig()
	})
	return cachedModels
}

func loadModelConfig() ModelConfig {
	paths := []string{
		strings.TrimSpace(os.Getenv("WISDEV_MODEL_CONFIG")),
		"wisdev_models.json",
		"scholar_models.json",
	}

	for _, p := range paths {
		if p == "" {
			continue
		}
		f, err := os.Open(p)
		if err == nil {
			defer f.Close()
			data, err := io.ReadAll(f)
			if err == nil {
				var sm ModelConfig
				if err := json.Unmarshal(data, &sm); err == nil {
					slog.Info("loaded WisDev model config", "path", p)
					return sm
				} else {
					slog.Warn("failed to unmarshal WisDev model config", "path", p, "error", err)
				}
			}
		}
	}
	slog.Info("could not find WisDev model config, using fallback defaults")
	return defaultModels
}

func ResolveHeavyModel() string {
	return FetchModelConfig().Heavy
}

func ResolveStandardModel() string {
	return FetchModelConfig().Standard
}

func ResolveLightModel() string {
	return FetchModelConfig().Light
}

// ResolveBalancedModel is an alias for ResolveStandardModel kept for API
// compatibility with callers that use the "balanced" terminology.
func ResolveBalancedModel() string {
	return FetchModelConfig().Standard
}

// ResolveModelForTier resolves a canonical model ID for the given tier name.
// Accepted tier values are "light", "standard", "heavy", and the legacy alias
// "balanced" (treated as "standard").  Any unknown or empty tier defaults to
// the standard model.
func ResolveModelForTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "light":
		return ResolveLightModel()
	case "heavy":
		return ResolveHeavyModel()
	case "balanced", "standard", "":
		return ResolveStandardModel()
	default:
		return ResolveStandardModel()
	}
}
