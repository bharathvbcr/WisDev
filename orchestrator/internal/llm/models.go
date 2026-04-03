package llm

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"sync"
)

type ScholarModels struct {
	Heavy    string `json:"heavy"`
	Standard string `json:"standard"`
	Light    string `json:"light"`
}

var (
	defaultModels = ScholarModels{
		Heavy:    "gemini-2.5-pro",
		Standard: "gemini-2.5-flash",
		Light:    "gemini-2.5-flash-lite",
	}
	cachedModels ScholarModels
	modelsOnce   sync.Once
)

func FetchModelConfig() ScholarModels {
	modelsOnce.Do(func() {
		cachedModels = loadModelConfig()
	})
	return cachedModels
}

func loadModelConfig() ScholarModels {
	paths := []string{
		"scholar_models.json",
		"../scholar_models.json",
		"../../scholar_models.json",
		"../../../scholar_models.json",
		"../../../../scholar_models.json",
	}

	for _, p := range paths {
		f, err := os.Open(p)
		if err == nil {
			defer f.Close()
			data, err := io.ReadAll(f)
			if err == nil {
				var sm ScholarModels
				if err := json.Unmarshal(data, &sm); err == nil {
					return sm
				} else {
					log.Printf("Failed to unmarshal %s: %v", p, err)
				}
			}
		}
	}
	log.Println("Could not find scholar_models.json, using fallback defaults.")
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
