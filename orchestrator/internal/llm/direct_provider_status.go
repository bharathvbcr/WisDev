package llm

import (
	"context"
	"strings"
	"time"
)

// DirectProviderStatus reports the active native structured-output backend.
func (c *Client) DirectProviderStatus(ctx context.Context) map[string]any {
	status := c.FullDirectProviderStatus(ctx)
	if local, ok := status["local"].(map[string]any); ok && local != nil {
		return local
	}
	if cloud, ok := status["cloud"].(map[string]any); ok && cloud != nil {
		return cloud
	}
	return map[string]any{"configured": false}
}

// FullDirectProviderStatus reports mode, fallback chain, and per-backend health.
func (c *Client) FullDirectProviderStatus(ctx context.Context) map[string]any {
	if c == nil {
		return map[string]any{"configured": false, "mode": ResolveLLMProviderMode()}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	status := map[string]any{
		"configured": c.OpenAICompatible != nil || c.VertexDirect != nil,
		"mode":       ResolveLLMProviderMode(),
		"chain":      DescribeProviderChain(c),
	}
	if c.OpenAICompatible != nil {
		status["local"] = probeOpenAICompatibleStatus(ctx, c.OpenAICompatible)
	}
	if c.VertexDirect != nil {
		status["cloud"] = map[string]any{
			"configured":       true,
			"backend":          c.VertexDirect.BackendName(),
			"credentialSource": c.VertexDirect.CredentialSource(),
			"transport":        "vertex_direct",
			"healthy":          true,
		}
	}
	return status
}

func probeOpenAICompatibleStatus(ctx context.Context, client *OpenAICompatibleClient) map[string]any {
	status := map[string]any{
		"configured":       true,
		"backend":          client.BackendName(),
		"model":            client.DefaultModel(),
		"credentialSource": client.CredentialSource(),
		"transport":        "openai_compatible_http",
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := client.HealthCheck(probeCtx); err != nil {
		status["healthy"] = false
		status["error"] = strings.TrimSpace(err.Error())
		return status
	}
	status["healthy"] = true
	if available, detail := client.ModelAvailable(probeCtx); available {
		status["modelAvailable"] = true
		if detail != "" && detail != client.DefaultModel() {
			status["resolvedModel"] = detail
		}
	} else {
		status["modelAvailable"] = false
		status["modelError"] = detail
	}
	return status
}

// WarmUpDirectProvider probes the configured native LLM backend when present.
func (c *Client) WarmUpDirectProvider(ctx context.Context) error {
	if c == nil || c.OpenAICompatible == nil {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, WarmUpProbeTimeout)
	defer cancel()
	return c.OpenAICompatible.HealthCheck(probeCtx)
}
