package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

const wisdevStructuredOutputSchemaInstruction = "Use the supplied structured output schema exactly."

const wisdevResearchComplexitySchema = `{"type":"object","required":["complexity"],"properties":{"complexity":{"type":"string","enum":["low","medium","high"]}}}`

// 20s leaves headroom for one full-length structured attempt plus a retry at
// the sidecar; 12s frequently cancelled requests Go-side mid-generation and
// forced deterministic fallbacks.
const wisdevRecoverableStructuredTimeout = 20 * time.Second

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
	return context.WithTimeout(ctx, unleashedTimeout(wisdevRecoverableStructuredTimeout))
}

func wisdevStructuredOutputCanUseDeterministicFallback(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if !strings.Contains(message, "structured output") {
		return false
	}
	return strings.Contains(message, "not valid json") ||
		strings.Contains(message, "invalid json") ||
		strings.Contains(message, "returned empty text") ||
		strings.Contains(message, "no structured candidates returned")
}

func wisdevStructuredOutputCanUseTimeoutFallback(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "deadline exceeded") ||
		strings.Contains(message, "deadline_exceeded") ||
		strings.Contains(message, "deadline expired") ||
		strings.Contains(message, "context deadline") ||
		strings.Contains(message, "gateway timeout") ||
		strings.Contains(message, "timed out") ||
		strings.Contains(message, "timeout")
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
