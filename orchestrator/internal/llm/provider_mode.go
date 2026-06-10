package llm

import (
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
)

// LLMProviderMode selects how native structured-output backends are wired.
type LLMProviderMode string

const (
	LLMProviderModeLocal  LLMProviderMode = "local"
	LLMProviderModeCloud  LLMProviderMode = "cloud"
	LLMProviderModeHybrid LLMProviderMode = "hybrid"
)

// ResolveLLMProviderMode reads WISDEV_LLM_PROVIDER and infers the routing mode.
// Empty provider with WISDEV_LLM_BASE_URL defaults to hybrid (tier-split routing).
// Empty provider without local config defaults to cloud (Vertex/Gemini, then sidecar).
func ResolveLLMProviderMode() LLMProviderMode {
	provider := normalizeLLMProvider(stackconfig.ResolveEnv("WISDEV_LLM_PROVIDER"))
	switch provider {
	case "ollama", "openai-compatible", "openai_compatible", "openai":
		return LLMProviderModeLocal
	case "vertex", "gemini", "cloud":
		return LLMProviderModeCloud
	case "hybrid":
		return LLMProviderModeHybrid
	default:
		if _, _, _, ok := ResolveOpenAICompatibleConfig(); ok {
			return LLMProviderModeHybrid
		}
		return LLMProviderModeCloud
	}
}

func normalizeLLMProvider(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// LocalLLMProviderForced reports whether WisDev is configured for local-only LLM.
func LocalLLMProviderForced() bool {
	return ResolveLLMProviderMode() == LLMProviderModeLocal
}

func shouldWireLocalProvider(mode LLMProviderMode) bool {
	switch mode {
	case LLMProviderModeLocal, LLMProviderModeHybrid:
		return true
	default:
		return false
	}
}

func shouldWireCloudProvider(mode LLMProviderMode) bool {
	switch mode {
	case LLMProviderModeCloud, LLMProviderModeHybrid:
		return true
	default:
		return false
	}
}

// DescribeProviderChain returns a human-readable backend label for status surfaces.
func DescribeProviderChain(c *Client) string {
	if c == nil {
		return "sidecar"
	}
	mode := ResolveLLMProviderMode()
	local := ""
	if c.OpenAICompatible != nil {
		local = c.OpenAICompatible.BackendName() + "/" + c.OpenAICompatible.DefaultModel()
	}
	cloud := ""
	if c.VertexDirect != nil {
		cloud = c.VertexDirect.BackendName()
	}
	switch {
	case local != "" && cloud != "":
		if mode == LLMProviderModeHybrid {
			return "hybrid:light→" + local + "|heavy→" + cloud
		}
		return local + "→" + cloud
	case local != "":
		return local
	case cloud != "":
		return cloud
	default:
		return "sidecar"
	}
}
