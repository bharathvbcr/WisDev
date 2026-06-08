package rag

import (
	"fmt"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

const ragStructuredOutputSchemaInstruction = "Use the supplied structured output schema exactly."

func appendRAGStructuredOutputInstruction(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return ragStructuredOutputSchemaInstruction
	}
	return trimmed + "\n\n" + ragStructuredOutputSchemaInstruction
}

func normalizeRAGGeneratedText(operation string, resp *llmv1.GenerateResponse) (string, error) {
	if resp == nil {
		return "", fmt.Errorf("%s returned nil response", operation)
	}

	text := strings.TrimSpace(resp.GetText())
	if text == "" {
		return "", fmt.Errorf("%s returned empty text", operation)
	}

	return text, nil
}

func applyRAGGeneratePolicy(req *llmv1.GenerateRequest, requestedTier string) *llmv1.GenerateRequest {
	return llm.ApplyGeneratePolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: requestedTier,
	}))
}

func applyRAGLightGeneratePolicy(req *llmv1.GenerateRequest) *llmv1.GenerateRequest {
	return llm.ApplyGeneratePolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "light",
		TaskType:      "light",
	}))
}

func applyRAGHeavyGeneratePolicy(req *llmv1.GenerateRequest) *llmv1.GenerateRequest {
	return llm.ApplyGeneratePolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "heavy",
	}))
}

func applyRAGHeavyStructuredPolicy(req *llmv1.StructuredRequest) *llmv1.StructuredRequest {
	return llm.ApplyStructuredPolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "heavy",
		Structured:    true,
		HighValue:     true,
	}))
}

func applyRAGLightStructuredPolicy(req *llmv1.StructuredRequest) *llmv1.StructuredRequest {
	return llm.ApplyStructuredPolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "light",
		Structured:    true,
		HighValue:     false,
	}))
}
