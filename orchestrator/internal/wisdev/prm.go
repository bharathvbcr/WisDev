package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

// Process Reward Model (PRM) for the iterative research loop.
//
// The reward for a research step is scored by an LLM judge that sees the
// research question, the queries executed, and the retrieved papers — so the
// reward reflects topical relevance, aspect coverage, and grounding quality
// rather than raw paper counts. The original hand-tuned formula remains as the
// deterministic fallback whenever the judge is unavailable (no client, provider
// cooldown, call failure) or when callers provide only aggregate signals.

const maxPRMPapersInPrompt = 12

// prmJudgeVerdict is the structured output of the LLM process-reward judge.
type prmJudgeVerdict struct {
	Reward    float64 `json:"reward"`
	Relevance float64 `json:"relevance"`
	Coverage  float64 `json:"coverage"`
	Grounding float64 `json:"grounding"`
	Reasoning string  `json:"reasoning"`
}

// scoreResearchStepWithLLM asks the judge to score one research iteration.
// Returns an error when the judge cannot run; callers fall back to the
// heuristic formula.
func scoreResearchStepWithLLM(
	ctx context.Context,
	query string,
	queries []string,
	papers []Source,
	iteration int,
) (float64, error) {
	client := GlobalLLMClient
	if client == nil {
		return 0, fmt.Errorf("llm client unavailable for PRM")
	}
	if remaining := client.ProviderCooldownRemaining(); remaining > 0 {
		return 0, fmt.Errorf("provider cooldown active; retry after %s", remaining)
	}
	query = strings.TrimSpace(query)
	if query == "" || len(papers) == 0 {
		return 0, fmt.Errorf("insufficient context for PRM judge")
	}

	var paperLines []string
	for idx, paper := range papers {
		if idx >= maxPRMPapersInPrompt {
			break
		}
		abstract := strings.TrimSpace(paper.Summary)
		if len(abstract) > 200 {
			abstract = abstract[:200] + "…"
		}
		grounded := "metadata-only"
		if abstract != "" {
			grounded = "abstract available"
		}
		paperLines = append(paperLines, fmt.Sprintf("%d. %s [%s] %s", idx+1, strings.TrimSpace(paper.Title), grounded, abstract))
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`You are a process reward model judging ONE iteration of an automated literature research loop.

Research question: %s
Iteration: %d
Queries executed this iteration: %s

Papers retrieved so far (top %d shown of %d):
%s

Score this research step from 0.0 to 1.0:
- relevance: do the retrieved papers actually address the research question (not just keyword-match)?
- coverage: do the papers span distinct aspects of the question, or do they cluster on one sub-topic?
- grounding: how much of the evidence has usable text (abstracts) rather than bare metadata?
- reward: overall step quality combining the above. A step that found many papers about the WRONG topic must score LOW.`,
		query,
		iteration,
		strings.Join(queries, " | "),
		minInt(len(papers), maxPRMPapersInPrompt),
		len(papers),
		strings.Join(paperLines, "\n"),
	))

	reqCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	resp, err := client.StructuredOutput(reqCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveLightModel(),
		JsonSchema: `{"type":"object","required":["reward","relevance","coverage","grounding","reasoning"],"properties":{"reward":{"type":"number"},"relevance":{"type":"number"},"coverage":{"type":"number"},"grounding":{"type":"number"},"reasoning":{"type":"string"}}}`,
	}))
	if err != nil {
		return 0, err
	}

	var verdict prmJudgeVerdict
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.JsonResult)), &verdict); err != nil {
		return 0, fmt.Errorf("PRM judge returned unparseable output: %w", err)
	}

	reward := clampFloat64(verdict.Reward, 0, 1)
	slog.Debug("PRM judge scored research step",
		"component", "wisdev.prm",
		"iteration", iteration,
		"reward", reward,
		"relevance", verdict.Relevance,
		"coverage", verdict.Coverage,
		"grounding", verdict.Grounding,
	)
	return reward, nil
}

// heuristicProcessReward is the deterministic fallback reward — the original
// hand-tuned formula over aggregate retrieval signals.
func heuristicProcessReward(output map[string]any) float64 {
	paperCount := intFromAny(output["paperCount"])
	searchSuccess := clampFloat64(floatFromAny(output["searchSuccess"]), 0, 1)
	citationVerifiedRatio := clampFloat64(floatFromAny(output["citationVerifiedRatio"]), 0, 1)
	coverageScore := clampFloat64(floatFromAny(output["coverageScore"]), 0, 1)
	success := boolFromAny(output["success"])

	if paperCount == 0 {
		return 0
	}

	reward := 0.15 + (0.35 * searchSuccess) + (0.35 * citationVerifiedRatio) + (0.15 * coverageScore)
	if success {
		reward += 0.05
	}
	return clampFloat64(reward, 0, 1)
}

// prmPapersFromAny extracts the Source slice that IterativeResearch attaches
// for the LLM judge; nil when callers supply only aggregate signals.
func prmPapersFromAny(value any) []Source {
	papers, _ := value.([]Source)
	return papers
}
