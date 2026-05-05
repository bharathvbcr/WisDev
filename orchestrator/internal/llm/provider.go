package llm

import (
	"context"
	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
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

	resp, err := p.client.Generate(ctx, &llmpb.GenerateRequest{
		Prompt: prompt,
		Model:  model,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}
