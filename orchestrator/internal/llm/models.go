package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

type ScholarModels struct {
	Heavy    string `json:"heavy"`
	Standard string `json:"standard"`
	Light    string `json:"light"`
}

type ScholarEmbeddingModels struct {
	Primary  string `json:"primary"`
	Standard string `json:"standard"`
	Fallback string `json:"fallback"`
}

type scholarModelConfig struct {
	Tiers      ScholarModels          `json:"tiers"`
	Embeddings ScholarEmbeddingModels `json:"embeddings"`
}

var (
	cachedModels     ScholarModels
	cachedEmbeddings ScholarEmbeddingModels
	modelsOnce       sync.Once
	embeddingsOnce   sync.Once
)

func FetchModelConfig() ScholarModels {
	modelsOnce.Do(func() {
		cachedModels = loadModelConfig()
	})
	return cachedModels
}

func FetchEmbeddingConfig() ScholarEmbeddingModels {
	embeddingsOnce.Do(func() {
		cachedEmbeddings = loadEmbeddingConfig()
	})
	return cachedEmbeddings
}

func (m ScholarModels) normalized() ScholarModels {
	return ScholarModels{
		Heavy:    strings.TrimSpace(m.Heavy),
		Standard: strings.TrimSpace(m.Standard),
		Light:    strings.TrimSpace(m.Light),
	}
}

func (m ScholarModels) valid() bool {
	m = m.normalized()
	return m.Heavy != "" && m.Standard != "" && m.Light != ""
}

func (m ScholarEmbeddingModels) normalized() ScholarEmbeddingModels {
	return ScholarEmbeddingModels{
		Primary:  strings.TrimSpace(m.Primary),
		Standard: strings.TrimSpace(m.Standard),
		Fallback: strings.TrimSpace(m.Fallback),
	}
}

func (m ScholarEmbeddingModels) valid() bool {
	m = m.normalized()
	return m.Primary != "" && m.Standard != "" && m.Fallback != ""
}

func modelConfigPaths() []string {
	return []string{
		strings.TrimSpace(os.Getenv("SCHOLAR_MODELS_CONFIG")),
		"/scholar_models.json",
		"scholar_models.json",
		"../scholar_models.json",
		"../../scholar_models.json",
		"../../../scholar_models.json",
		"../../../../scholar_models.json",
	}
}

func decodeScholarModels(data []byte) (ScholarModels, error) {
	var cfg scholarModelConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ScholarModels{}, err
	}
	if models := cfg.Tiers.normalized(); models.valid() {
		return models, nil
	}

	var legacy ScholarModels
	if err := json.Unmarshal(data, &legacy); err != nil {
		return ScholarModels{}, err
	}
	if models := legacy.normalized(); models.valid() {
		return models, nil
	}

	return ScholarModels{}, fmt.Errorf("missing required tiers: light, standard, heavy")
}

func decodeScholarEmbeddings(data []byte) (ScholarEmbeddingModels, error) {
	var cfg scholarModelConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ScholarEmbeddingModels{}, err
	}
	if embeddings := cfg.Embeddings.normalized(); embeddings.valid() {
		return embeddings, nil
	}
	return ScholarEmbeddingModels{}, fmt.Errorf("missing required embeddings: primary, standard, fallback")
}

func modelsFromEnvironment() ScholarModels {
	return ScholarModels{
		Heavy: strings.TrimSpace(firstEnv(
			"AI_MODEL_HEAVY_ID",
			"GEMINI_HEAVY_MODEL",
		)),
		Standard: strings.TrimSpace(firstEnv(
			"AI_MODEL_STANDARD_ID",
			"AI_MODEL_BALANCED_ID",
			"GEMINI_STANDARD_MODEL",
		)),
		Light: strings.TrimSpace(firstEnv(
			"AI_MODEL_LIGHT_ID",
			"GEMINI_LIGHT_MODEL",
		)),
	}.normalized()
}

func embeddingsFromEnvironment() ScholarEmbeddingModels {
	standard := strings.TrimSpace(firstEnv("EMBEDDING_MODEL_STANDARD_ID"))
	primary := strings.TrimSpace(firstEnv("EMBEDDING_MODEL_PRIMARY_ID"))
	if primary == "" {
		primary = standard
	}
	return ScholarEmbeddingModels{
		Primary:  primary,
		Standard: standard,
		Fallback: strings.TrimSpace(firstEnv("EMBEDDING_MODEL_FALLBACK_ID")),
	}.normalized()
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func loadModelConfig() ScholarModels {
	for _, p := range modelConfigPaths() {
		f, err := os.Open(p)
		if err == nil {
			defer f.Close()
			data, err := io.ReadAll(f)
			if err == nil {
				sm, err := decodeScholarModels(data)
				if err == nil {
					slog.Info("loaded scholar model config", "path", p)
					return sm
				} else {
					slog.Warn("failed to unmarshal scholar model config", "path", p, "error", err)
				}
			}
		}
	}
	if envModels := modelsFromEnvironment(); envModels.valid() {
		slog.Warn("scholar model config file unavailable; using explicit environment model overrides")
		return envModels
	}
	slog.Warn("could not load scholar model config; using default model tier fallbacks")
	return ScholarModels{
		Heavy:    "gemini-2.5-pro",
		Standard: "gemini-2.5-flash",
		Light:    "gemini-2.5-flash-lite",
	}
}

func loadEmbeddingConfig() ScholarEmbeddingModels {
	for _, p := range modelConfigPaths() {
		f, err := os.Open(p)
		if err == nil {
			defer f.Close()
			data, err := io.ReadAll(f)
			if err == nil {
				embeddings, err := decodeScholarEmbeddings(data)
				if err == nil {
					slog.Info("loaded scholar embedding model config", "path", p)
					return embeddings
				}
				slog.Warn("failed to unmarshal scholar embedding model config", "path", p, "error", err)
			}
		}
	}
	if envEmbeddings := embeddingsFromEnvironment(); envEmbeddings.valid() {
		slog.Warn("scholar embedding model config file unavailable; using explicit environment model overrides")
		return envEmbeddings
	}
	slog.Warn("could not load scholar embedding model config; using default embedding fallbacks")
	return ScholarEmbeddingModels{
		Primary:  "text-embedding-004",
		Standard: "text-embedding-004",
		Fallback: "text-embedding-004",
	}
}

func ResolveHeavyModel() string {
	if model := strings.TrimSpace(firstEnv("AI_MODEL_HEAVY_ID", "GEMINI_HEAVY_MODEL")); model != "" {
		return model
	}
	return FetchModelConfig().Heavy
}

func ResolveStandardModel() string {
	if model := strings.TrimSpace(firstEnv("AI_MODEL_STANDARD_ID", "AI_MODEL_BALANCED_ID", "GEMINI_STANDARD_MODEL")); model != "" {
		return model
	}
	return FetchModelConfig().Standard
}

func ResolveLightModel() string {
	if model := strings.TrimSpace(firstEnv("AI_MODEL_LIGHT_ID", "GEMINI_LIGHT_MODEL")); model != "" {
		return model
	}
	return FetchModelConfig().Light
}

// ResolveModelForTier resolves a canonical model ID for the given tier name.
// Accepted tier values are "light", "standard", "heavy", and the compatibility name
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

func ResolveEmbeddingModel(key string) string {
	embeddings := FetchEmbeddingConfig()
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "primary":
		if model := strings.TrimSpace(firstEnv("EMBEDDING_MODEL_PRIMARY_ID", "EMBEDDING_MODEL_STANDARD_ID")); model != "" {
			return model
		}
		return embeddings.Primary
	case "fallback":
		if model := strings.TrimSpace(firstEnv("EMBEDDING_MODEL_FALLBACK_ID")); model != "" {
			return model
		}
		return embeddings.Fallback
	case "standard", "":
		if model := strings.TrimSpace(firstEnv("EMBEDDING_MODEL_STANDARD_ID")); model != "" {
			return model
		}
		return embeddings.Standard
	default:
		if model := strings.TrimSpace(firstEnv("EMBEDDING_MODEL_FALLBACK_ID")); model != "" {
			return model
		}
		return embeddings.Fallback
	}
}
