package rag

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

// ClaimCategory defines the type of academic claim.
type ClaimCategory string

const (
	CategoryMethodological ClaimCategory = "methodological"
	CategoryEmpirical      ClaimCategory = "empirical"
	CategoryTheoretical    ClaimCategory = "theoretical"
)

// ExtractedClaim represents a refined claim with category and grounding context.
type ExtractedClaim struct {
	Text      string        `json:"text"`
	Category  ClaimCategory `json:"category"`
	Reasoning string        `json:"reasoning"`
}

// ClaimExtractor handles high-fidelity claim mining using LLMs.
type ClaimExtractor struct {
	llmClient *llm.Client
}

func NewClaimExtractor(client *llm.Client) *ClaimExtractor {
	return &ClaimExtractor{llmClient: client}
}

// ExtractClaimsCategorized uses structured output to get categorized claims.
func (e *ClaimExtractor) ExtractClaimsCategorized(ctx context.Context, text string) ([]ExtractedClaim, error) {
	if e == nil || e.llmClient == nil {
		return nil, fmt.Errorf("claim extractor LLM client is not configured")
	}

	prompt := appendRAGStructuredOutputInstruction(fmt.Sprintf(`Analyze the following academic text and extract key factual claims.
For each claim, categorize it as:
- "methodological": Claims about techniques, data collection, or analysis methods.
- "empirical": Claims about specific findings, statistics, or observed effects.
- "theoretical": Claims about models, hypotheses, or conceptual implications.

Text:
"""
%s
"""
Each claim must include text, category, and reasoning.`, text))

	schema := `{
		"type": "object",
		"properties": {
			"claims": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"text": {"type": "string"},
						"category": {"type": "string", "enum": ["methodological", "empirical", "theoretical"]},
						"reasoning": {"type": "string"}
					},
					"required": ["text", "category", "reasoning"]
				}
			}
		},
		"required": ["claims"]
	}`

	resp, err := e.llmClient.StructuredOutput(ctx, applyRAGHeavyStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveHeavyModel(), // Use heavy model for precision
		JsonSchema: schema,
	}))
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("claim extractor returned nil structured response")
	}

	var result struct {
		Claims []ExtractedClaim `json:"claims"`
	}
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}

	return result.Claims, nil
}

// FilterByCategory returns claims matching the specified category.
func FilterByCategory(claims []ExtractedClaim, category ClaimCategory) []ExtractedClaim {
	var filtered []ExtractedClaim
	for _, c := range claims {
		if c.Category == category {
			filtered = append(filtered, c)
		}
	}
	return filtered
}
