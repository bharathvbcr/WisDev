package llm

import (
	"context"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
)

// ModelProvider simplifies model calls using tier aliases.
type ModelProvider struct {
	client *Client
}

// NewModelProvider creates a new provider.
func NewModelProvider(client *Client) *ModelProvider {
	return &ModelProvider{client: client}
}

// Call executes a generation request using a tier alias ('heavy', 'standard', 'light').
func (p *ModelProvider) Call(ctx context.Context, tier string, prompt string) (string, error) {
	var model string
	switch tier {
	case "heavy":
		model = ResolveHeavyModel()
	case "light":
		model = ResolveLightModel()
	case "standard":
		model = ResolveStandardModel()
	default:
		model = ResolveStandardModel()
	}

	resp, err := p.client.Generate(ctx, &llmv1.GenerateRequest{
		Prompt: prompt,
		Model:  model,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}
