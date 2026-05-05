package search

import (
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

const searchStructuredOutputSchemaInstruction = "Use the supplied structured output schema exactly."

func appendSearchStructuredOutputInstruction(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return searchStructuredOutputSchemaInstruction
	}
	return trimmed + "\n\n" + searchStructuredOutputSchemaInstruction
}

func applySearchStandardGeneratePolicy(req *llmv1.GenerateRequest) *llmv1.GenerateRequest {
	return llm.ApplyGeneratePolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "standard",
	}))
}

func applySearchStandardStructuredPolicy(req *llmv1.StructuredRequest) *llmv1.StructuredRequest {
	return llm.ApplyStructuredPolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "standard",
		Structured:    true,
		HighValue:     false,
	}))
}
