package rag

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

// HindsightRefinementAgent performs a second pass over the synthesis to correct
// mis-citations and add missing nuances found in the source text.
type HindsightRefinementAgent struct {
	llmClient *llm.Client
}

func NewHindsightRefinementAgent(client *llm.Client) *HindsightRefinementAgent {
	return &HindsightRefinementAgent{llmClient: client}
}

// Refine synthesis by comparing it directly against the source papers.
func (a *HindsightRefinementAgent) Refine(ctx context.Context, query string, currentAnswer string, papers []search.Paper) (string, error) {
	slog.Info("Running Hindsight Refinement pass", "paper_count", len(papers))

	// 1. Prepare evidence context
	var evidenceBuilder strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&evidenceBuilder, "PAPER [%d]: %s\n", i+1, p.Title)
		if abstract := strings.TrimSpace(p.Abstract); abstract != "" {
			fmt.Fprintf(&evidenceBuilder, "ABSTRACT: %s\n", abstract)
		}
		if grounding := paperGroundingText(p, maxGroundingChars); grounding != "" {
			fmt.Fprintf(&evidenceBuilder, "EVIDENCE:\n%s\n", grounding)
		}
		evidenceBuilder.WriteString("\n")
	}

	systemPrompt := `You are a precision research editor. Your task is to refine a synthesized research summary.
You must:
1. Identify any claims in the summary that are not fully supported by the provided abstracts.
2. Correct any mis-citations (e.g., if a claim is attributed to [1] but actually found in [2]).
3. Add specific statistical nuances or methodological constraints mentioned in the papers but missed in the summary.
4. DO NOT hallucinate new findings. Only use the provided papers.
5. Maintain the original professional tone but improve factual density.`

	userPrompt := fmt.Sprintf(`Research Query: %s

Original Summary:
"""
%s
"""

Source Evidence:
%s

Provide the refined summary. If no changes are needed, return the original summary exactly.`,
		query, currentAnswer, evidenceBuilder.String())

	resp, err := a.llmClient.Generate(ctx, applyRAGHeavyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:       userPrompt,
		SystemPrompt: systemPrompt,
		Model:        llm.ResolveHeavyModel(), // Precision pass requires heavy model
		Temperature:  0.1,
	}))
	if err != nil {
		return "", err
	}

	return normalizeRAGGeneratedText("hindsight refinement", resp)
}
