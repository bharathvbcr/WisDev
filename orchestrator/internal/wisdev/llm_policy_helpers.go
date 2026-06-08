package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

const wisdevStructuredOutputSchemaInstruction = "Use the supplied structured output schema exactly."

const wisdevResearchComplexitySchema = `{"type":"object","required":["complexity"],"properties":{"complexity":{"type":"string","enum":["low","medium","high"]}}}`

const wisdevRecoverableStructuredTimeout = 12 * time.Second

func appendWisdevStructuredOutputInstruction(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return wisdevStructuredOutputSchemaInstruction
	}
	return trimmed + "\n\n" + wisdevStructuredOutputSchemaInstruction
}

func applyWisdevHeavyStructuredPolicy(req *llmv1.StructuredRequest) *llmv1.StructuredRequest {
	return llm.ApplyStructuredPolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "heavy",
		Structured:    true,
		HighValue:     true,
	}))
}

func applyWisdevStandardStructuredPolicy(req *llmv1.StructuredRequest) *llmv1.StructuredRequest {
	return llm.ApplyStructuredPolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "standard",
		Structured:    true,
		HighValue:     false,
	}))
}

func applyWisdevRecoverableStructuredPolicy(req *llmv1.StructuredRequest) *llmv1.StructuredRequest {
	return llm.ApplyStructuredPolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "standard",
		Structured:    true,
		HighValue:     false,
	}))
}

func applyWisdevLightStructuredPolicy(req *llmv1.StructuredRequest) *llmv1.StructuredRequest {
	return llm.ApplyStructuredPolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "light",
		Structured:    true,
		TaskType:      "light",
	}))
}

func applyWisdevStandardGeneratePolicy(req *llmv1.GenerateRequest) *llmv1.GenerateRequest {
	return llm.ApplyGeneratePolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "standard",
	}))
}

func applyWisdevLightGeneratePolicy(req *llmv1.GenerateRequest) *llmv1.GenerateRequest {
	return llm.ApplyGeneratePolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "light",
		TaskType:      "light",
	}))
}

func wisdevRecoverableStructuredContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, wisdevRecoverableStructuredTimeout)
}

func normalizeWisdevGeneratedText(operation string, resp *llmv1.GenerateResponse) (string, error) {
	if resp == nil {
		return "", fmt.Errorf("%s returned nil response", operation)
	}

	text := strings.TrimSpace(resp.GetText())
	if text == "" {
		return "", fmt.Errorf("%s returned empty text", operation)
	}

	return text, nil
}

func parseResearchComplexity(jsonResult string) (string, error) {
	var payload struct {
		Complexity string `json:"complexity"`
	}
	if err := json.Unmarshal([]byte(jsonResult), &payload); err != nil {
		return "", err
	}

	complexity := strings.ToLower(strings.TrimSpace(payload.Complexity))
	switch complexity {
	case "low", "medium", "high":
		return complexity, nil
	default:
		return "", fmt.Errorf("invalid research complexity %q", payload.Complexity)
	}
}
