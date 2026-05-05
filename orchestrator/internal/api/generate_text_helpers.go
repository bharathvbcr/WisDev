package api

import (
	"fmt"
	"strings"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func normalizeGeneratedResponseText(operation string, resp *llmv1.GenerateResponse) (string, error) {
	if resp == nil {
		return "", fmt.Errorf("%s returned nil response", operation)
	}

	text := strings.TrimSpace(resp.GetText())
	if text == "" {
		return "", fmt.Errorf("%s returned empty text", operation)
	}

	return text, nil
}
