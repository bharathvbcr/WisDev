package llm

import (
	"context"
	"log/slog"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
)

// WireDirectProviders attaches native structured-output backends to the client.
//
// Modes (WISDEV_LLM_PROVIDER):
//   - ollama / openai-compatible: local OpenAI-compatible or Ollama only
//   - vertex / gemini / cloud: Vertex/Gemini only
//   - hybrid (or unset with WISDEV_LLM_BASE_URL): tier-split local + cloud routing
//   - unset without local config: Vertex/Gemini when available, else sidecar
func WireDirectProviders(ctx context.Context, client *Client) {
	if client == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	mode := ResolveLLMProviderMode()
	wireLocalProvider(ctx, client, mode)
	wireCloudProvider(ctx, client, mode)
}

func wireLocalProvider(ctx context.Context, client *Client, mode LLMProviderMode) {
	if !shouldWireLocalProvider(mode) {
		return
	}

	provider := normalizeLLMProvider(stackconfig.ResolveEnv("WISDEV_LLM_PROVIDER"))
	baseURL, model, apiKey, localConfigured := ResolveOpenAICompatibleConfig()
	if !localConfigured {
		if provider == "ollama" || provider == "openai-compatible" || provider == "openai_compatible" || provider == "openai" {
			slog.Warn("llm local provider requested but WISDEV_LLM_BASE_URL is not configured",
				"component", "llm.client",
				"operation", "wire_direct_providers",
				"provider", provider,
				"mode", mode,
			)
		}
		return
	}

	client.OpenAICompatible = NewOpenAICompatibleClient(baseURL, model, apiKey)
	slog.Info("llm direct provider wired",
		"component", "llm.client",
		"operation", "wire_direct_providers",
		"mode", mode,
		"backend", client.OpenAICompatible.BackendName(),
		"base_url", baseURL,
		"model", client.OpenAICompatible.DefaultModel(),
		"credential_source", client.OpenAICompatible.CredentialSource(),
	)
}

func wireCloudProvider(ctx context.Context, client *Client, mode LLMProviderMode) {
	if !shouldWireCloudProvider(mode) {
		return
	}

	vertexInitCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	vertexClient, err := NewVertexClient(vertexInitCtx, "", "")
	if err != nil {
		if client.OpenAICompatible == nil {
			slog.Warn("llm cloud provider unavailable and no local OpenAI-compatible backend configured",
				"component", "llm.client",
				"operation", "wire_direct_providers",
				"mode", mode,
				"error", err.Error(),
			)
		} else if mode == LLMProviderModeHybrid {
			slog.Warn("llm hybrid mode: cloud fallback unavailable; local provider only",
				"component", "llm.client",
				"operation", "wire_direct_providers",
				"mode", mode,
				"error", err.Error(),
			)
		}
		return
	}
	client.VertexDirect = vertexClient
	slog.Info("llm direct provider wired",
		"component", "llm.client",
		"operation", "wire_direct_providers",
		"mode", mode,
		"backend", vertexClient.BackendName(),
		"credential_source", vertexClient.CredentialSource(),
	)
}
