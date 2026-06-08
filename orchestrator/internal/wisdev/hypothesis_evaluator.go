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

// HypothesisEvaluator evaluates hypotheses against collected evidence
type HypothesisEvaluator struct {
	brainCaps *BrainCapabilities
}

// NewHypothesisEvaluator creates a new hypothesis evaluator
func NewHypothesisEvaluator(brainCaps *BrainCapabilities) *HypothesisEvaluator {
	return &HypothesisEvaluator{
		brainCaps: brainCaps,
	}
}

// Evaluate evaluates a single hypothesis against collected evidence
func (he *HypothesisEvaluator) Evaluate(ctx context.Context, hypothesis *Hypothesis, evidence []EvidenceFinding) (*EvaluationResult, error) {
	if hypothesis == nil {
		return nil, fmt.Errorf("hypothesis is nil")
	}

	// Build evidence summary for evaluation prompt
	evidenceSummary := he.buildEvidenceSummary(hypothesis, evidence)

	prompt := fmt.Sprintf(`You are evaluating a research hypothesis against collected evidence.

Hypothesis: %s
Falsifiability Condition: %s

Evidence collected so far:
%s

Guidelines:
- "branch" if the hypothesis is too broad and needs to be split into more specific sub-hypotheses.
- "backtrack" if current evidence suggests this path is a dead end but an alternative sibling hypothesis should be explored.
- "prune" if evidence refuted the hypothesis.
- "keep" if you just need more evidence for this specific hypothesis.

Be rigorous. A hypothesis is "supported" only with strong, consistent evidence.
"Uncertain" means evidence is weak or contradictory.
"Refuted" means evidence contradicts the hypothesis.`,
		hypothesis.Claim,
		hypothesis.FalsifiabilityCondition,
		evidenceSummary)

	if he.brainCaps == nil || he.brainCaps.llmClient == nil {
		// Fallback: heuristic scoring based on evidence count and confidence
		result := he.heuristicEvaluation(hypothesis, evidence)
		result.HypothesisID = hypothesis.ID
		return result, nil
	}
	if remaining := brainCapabilityCooldownRemaining(he.brainCaps); remaining > 0 {
		slog.Warn("Hypothesis evaluation using cooldown fallback",
			"component", "wisdev.autonomous",
			"operation", "hypothesis_evaluation",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
			"hypothesis", hypothesis.Claim,
		)
		result := he.heuristicEvaluation(hypothesis, evidence)
		result.HypothesisID = hypothesis.ID
		return result, nil
	}

	resp, err := he.evaluateWithLLM(ctx, prompt)
	if err != nil {
		slog.Warn("LLM evaluation failed, using heuristic", "error", err, "hypothesis", hypothesis.Claim)
		result := he.heuristicEvaluation(hypothesis, evidence)
		result.HypothesisID = hypothesis.ID
		return result, nil
	}

	result, err := he.parseEvaluationResponse(resp.JsonResult)
	if err != nil {
		slog.Warn("Failed to parse evaluation response, using heuristic", "error", err)
		result := he.heuristicEvaluation(hypothesis, evidence)
		result.HypothesisID = hypothesis.ID
		return result, nil
	}

	result.HypothesisID = hypothesis.ID
	result.EvaluatedAt = NowMillis()
	return result, nil
}

func (he *HypothesisEvaluator) evaluateWithLLM(ctx context.Context, prompt string) (resp *llmv1.StructuredResponse, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("hypothesis evaluation panic: %v", recovered)
			resp = nil
		}
	}()

	// Use light model for cost efficiency (evaluation is frequent)
	return he.brainCaps.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     appendWisdevStructuredOutputInstruction(prompt),
		Model:      llm.ResolveLightModel(),
		JsonSchema: `{"type":"object","properties":{"score":{"type":"number"},"verdict":{"type":"string"},"reasoning":{"type":"string"},"branchingDecision":{"type":"string"},"subHypotheses":{"type":"array","items":{"type":"string"}},"missingEvidence":{"type":"array","items":{"type":"string"}},"suggestedQueries":{"type":"array","items":{"type":"string"}}},"required":["score","verdict","reasoning"]}`,
	}, "light", false))
}

// EvaluateAllBatched evaluates hypotheses in batches using a single LLM call per batch,
// reducing latency by up to 70% compared to sequential evaluation. Falls back to
// sequential EvaluateAll on LLM or parse failure.
func (he *HypothesisEvaluator) EvaluateAllBatched(ctx context.Context, hypotheses []*Hypothesis, evidence []EvidenceFinding, batchSize int) ([]*EvaluationResult, []*Hypothesis) {
	if batchSize <= 0 || batchSize > 10 {
		batchSize = 8
	}

	active := make([]*Hypothesis, 0, len(hypotheses))
	for _, h := range hypotheses {
		if !h.IsTerminated {
			active = append(active, h)
		}
	}

	if len(active) <= 1 || he.brainCaps == nil || he.brainCaps.llmClient == nil {
		return he.EvaluateAll(ctx, hypotheses, evidence)
	}
	if remaining := brainCapabilityCooldownRemaining(he.brainCaps); remaining > 0 {
		slog.Warn("Batched hypothesis evaluation using cooldown fallback",
			"component", "wisdev.autonomous",
			"operation", "hypothesis_evaluation",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
			"activeHypotheses", len(active),
		)
		return he.evaluateActiveHypothesesHeuristically(active, evidence)
	}

	allResults := make([]*EvaluationResult, 0, len(hypotheses))
	var allNewHypotheses []*Hypothesis

	for batchStart := 0; batchStart < len(active); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(active) {
			batchEnd = len(active)
		}
		batch := active[batchStart:batchEnd]

		batchResults, err := he.evaluateBatch(ctx, batch, evidence)
		if err != nil || len(batchResults) != len(batch) {
			if llm.IsProviderRateLimitError(err) {
				slog.Warn("Batched evaluation skipped sequential fallback during provider cooldown",
					"component", "wisdev.autonomous",
					"operation", "hypothesis_evaluation",
					"error", err,
					"batchSize", len(batch))
				for _, h := range batch {
					result := he.heuristicEvaluation(h, evidence)
					result.HypothesisID = h.ID
					newHyps := he.applyEvaluationResult(h, result)
					allResults = append(allResults, result)
					allNewHypotheses = append(allNewHypotheses, newHyps...)
				}
				continue
			}
			slog.Warn("Batched evaluation failed, falling back to sequential",
				"error", err, "batchSize", len(batch))
			for _, h := range batch {
				result, evalErr := he.Evaluate(ctx, h, evidence)
				if evalErr != nil {
					result = he.heuristicEvaluation(h, evidence)
				}
				newHyps := he.applyEvaluationResult(h, result)
				allResults = append(allResults, result)
				allNewHypotheses = append(allNewHypotheses, newHyps...)
			}
			continue
		}

		for i, h := range batch {
			newHyps := he.applyEvaluationResult(h, batchResults[i])
			allResults = append(allResults, batchResults[i])
			allNewHypotheses = append(allNewHypotheses, newHyps...)
		}
	}

	return allResults, allNewHypotheses
}

func (he *HypothesisEvaluator) evaluateBatch(ctx context.Context, batch []*Hypothesis, evidence []EvidenceFinding) ([]*EvaluationResult, error) {
	if remaining := brainCapabilityCooldownRemaining(he.brainCaps); remaining > 0 {
		return nil, fmt.Errorf("provider cooldown active; retry after %s", remaining.Round(0))
	}
	var sb strings.Builder
	sb.WriteString("Evaluate ALL of the following research hypotheses against the evidence. Provide one evaluation per hypothesis, in the same order, using the supplied structured output schema.\n\n")

	for i, h := range batch {
		summary := he.buildEvidenceSummary(h, evidence)
		sb.WriteString(fmt.Sprintf("=== Hypothesis %d ===\nClaim: %s\nFalsifiability: %s\nEvidence:\n%s\n\n",
			i, h.Claim, h.FalsifiabilityCondition, summary))
	}

	sb.WriteString(`Each evaluation should include hypothesisIndex, score, verdict, reasoning, branchingDecision, subHypotheses, missingEvidence, and suggestedQueries.

Guidelines:
- "branch" if the hypothesis is too broad and needs splitting.
- "prune" if evidence refuted the hypothesis.
- "keep" if you just need more evidence for this specific hypothesis.
Be rigorous. "supported" requires strong consistent evidence.`)

	arraySchema := `{"type":"array","items":{"type":"object","properties":{"hypothesisIndex":{"type":"integer"},"score":{"type":"number"},"verdict":{"type":"string"},"reasoning":{"type":"string"},"branchingDecision":{"type":"string"},"subHypotheses":{"type":"array","items":{"type":"string"}},"missingEvidence":{"type":"array","items":{"type":"string"}},"suggestedQueries":{"type":"array","items":{"type":"string"}}},"required":["hypothesisIndex","score","verdict","reasoning"]}}`

	resp, err := he.brainCaps.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     appendWisdevStructuredOutputInstruction(sb.String()),
		Model:      llm.ResolveStandardModel(),
		JsonSchema: arraySchema,
	}, "standard", false))
	if err != nil {
		return nil, fmt.Errorf("batched evaluation LLM call failed: %w", err)
	}

	var rawResults []struct {
		HypothesisIndex   int      `json:"hypothesisIndex"`
		Score             float64  `json:"score"`
		Verdict           string   `json:"verdict"`
		Reasoning         string   `json:"reasoning"`
		BranchingDecision string   `json:"branchingDecision"`
		SubHypotheses     []string `json:"subHypotheses"`
		MissingEvidence   []string `json:"missingEvidence"`
		SuggestedQueries  []string `json:"suggestedQueries"`
	}

	jsonStr := strings.TrimSpace(resp.JsonResult)
	if err := json.Unmarshal([]byte(jsonStr), &rawResults); err != nil {
		return nil, fmt.Errorf("failed to parse batch response: %w", err)
	}

	resultsByIndex := make(map[int]*EvaluationResult, len(rawResults))
	for _, raw := range rawResults {
		verdict := raw.Verdict
		if verdict != "supported" && verdict != "uncertain" && verdict != "refuted" {
			verdict = "uncertain"
		}
		score := raw.Score
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		resultsByIndex[raw.HypothesisIndex] = &EvaluationResult{
			Score:             score,
			Verdict:           verdict,
			Reasoning:         raw.Reasoning,
			BranchingDecision: raw.BranchingDecision,
			SubHypotheses:     raw.SubHypotheses,
			MissingEvidence:   raw.MissingEvidence,
			SuggestedQueries:  raw.SuggestedQueries,
			EvaluatedAt:       NowMillis(),
		}
	}

	results := make([]*EvaluationResult, len(batch))
	for i, h := range batch {
		if r, ok := resultsByIndex[i]; ok {
			r.HypothesisID = h.ID
			results[i] = r
		} else {
			fallback := he.heuristicEvaluation(h, evidence)
			fallback.HypothesisID = h.ID
			results[i] = fallback
		}
	}
	return results, nil
}

func (he *HypothesisEvaluator) evaluateActiveHypothesesHeuristically(active []*Hypothesis, evidence []EvidenceFinding) ([]*EvaluationResult, []*Hypothesis) {
	allResults := make([]*EvaluationResult, 0, len(active))
	var allNewHypotheses []*Hypothesis
	for _, h := range active {
		result := he.heuristicEvaluation(h, evidence)
		result.HypothesisID = h.ID
		newHyps := he.applyEvaluationResult(h, result)
		allResults = append(allResults, result)
		allNewHypotheses = append(allNewHypotheses, newHyps...)
	}
	return allResults, allNewHypotheses
}

// applyEvaluationResult updates a hypothesis from its evaluation result and returns any new sub-hypotheses.
func (he *HypothesisEvaluator) applyEvaluationResult(hypothesis *Hypothesis, result *EvaluationResult) []*Hypothesis {
	hypothesis.ConfidenceScore = result.Score
	hypothesis.EvaluatedAt = result.EvaluatedAt
	switch result.Verdict {
	case "supported":
		hypothesis.Status = "supported"
		hypothesis.IsTerminated = false
	case "refuted":
		hypothesis.Status = "refuted"
		hypothesis.IsTerminated = true
	default:
		hypothesis.Status = "uncertain"
	}

	var newHypotheses []*Hypothesis
	if result.BranchingDecision == "branch" && len(result.SubHypotheses) > 0 {
		for _, claim := range result.SubHypotheses {
			if strings.TrimSpace(claim) == "" {
				continue
			}
			newHypotheses = append(newHypotheses, &Hypothesis{
				ID:                      stableWisDevID("sub-hyp", hypothesis.ID, claim),
				ParentID:                hypothesis.ID,
				Query:                   hypothesis.Query,
				Claim:                   claim,
				FalsifiabilityCondition: "Specific evidence refutes this sub-claim.",
				CreatedAt:               NowMillis(),
				Status:                  "active",
			})
		}
		hypothesis.Status = "branched"
	} else if result.BranchingDecision == "prune" {
		hypothesis.IsTerminated = true
		hypothesis.Status = "pruned"
	}

	switch {
	case result.Score >= 0.8:
		hypothesis.AllocatedQueryBudget = 1
	case result.Score >= 0.55:
		hypothesis.AllocatedQueryBudget = 2
	default:
		hypothesis.AllocatedQueryBudget = 3
	}
	if hypothesis.EvaluationHistory == nil {
		hypothesis.EvaluationHistory = make([]EvaluationResult, 0)
	}
	hypothesis.EvaluationHistory = append(hypothesis.EvaluationHistory, *result)

	return newHypotheses
}

// EvaluateAll evaluates all hypotheses against collected evidence and returns results and any new sub-hypotheses
func (he *HypothesisEvaluator) EvaluateAll(ctx context.Context, hypotheses []*Hypothesis, evidence []EvidenceFinding) ([]*EvaluationResult, []*Hypothesis) {
	results := make([]*EvaluationResult, 0, len(hypotheses))
	var newHypotheses []*Hypothesis

	for _, hypothesis := range hypotheses {
		if hypothesis.IsTerminated {
			continue
		}

		result, err := he.Evaluate(ctx, hypothesis, evidence)
		if err != nil {
			slog.Warn("Failed to evaluate hypothesis", "error", err, "hypothesis", hypothesis.Claim)
			result = he.heuristicEvaluation(hypothesis, evidence)
		}

		// Update hypothesis with evaluation result
		hypothesis.ConfidenceScore = result.Score
		hypothesis.EvaluatedAt = result.EvaluatedAt
		switch result.Verdict {
		case "supported":
			hypothesis.Status = "supported"
			hypothesis.IsTerminated = false
		case "refuted":
			hypothesis.Status = "refuted"
			hypothesis.IsTerminated = true
		default:
			hypothesis.Status = "uncertain"
		}

		// Handle branching decision (R1: Tree of Thoughts)
		if result.BranchingDecision == "branch" && len(result.SubHypotheses) > 0 {
			for _, claim := range result.SubHypotheses {
				if strings.TrimSpace(claim) == "" {
					continue
				}
				sub := &Hypothesis{
					ID:                      stableWisDevID("sub-hyp", hypothesis.ID, claim),
					ParentID:                hypothesis.ID,
					Query:                   hypothesis.Query,
					Claim:                   claim,
					FalsifiabilityCondition: "Specific evidence refutes this sub-claim.",
					CreatedAt:               NowMillis(),
					Status:                  "active",
				}
				newHypotheses = append(newHypotheses, sub)
			}
			// Mark parent as branched (optional, but keep it active for tracking)
			hypothesis.Status = "branched"
		} else if result.BranchingDecision == "prune" {
			hypothesis.IsTerminated = true
			hypothesis.Status = "pruned"
		}

		switch {
		case result.Score >= 0.8:
			hypothesis.AllocatedQueryBudget = 1
		case result.Score >= 0.55:
			hypothesis.AllocatedQueryBudget = 2
		default:
			hypothesis.AllocatedQueryBudget = 3
		}
		if hypothesis.EvaluationHistory == nil {
			hypothesis.EvaluationHistory = make([]EvaluationResult, 0)
		}
		hypothesis.EvaluationHistory = append(hypothesis.EvaluationHistory, *result)

		results = append(results, result)
	}

	return results, newHypotheses
}

// buildEvidenceSummary creates a concise summary of evidence for the evaluation prompt
func (he *HypothesisEvaluator) buildEvidenceSummary(hypothesis *Hypothesis, evidence []EvidenceFinding) string {
	// Filter evidence relevant to this hypothesis
	relevantEvidence := make([]EvidenceFinding, 0)
	if hypothesis.Evidence != nil && len(hypothesis.Evidence) > 0 {
		// Use hypothesis-linked evidence if available
		for _, ev := range hypothesis.Evidence {
			if ev != nil {
				relevantEvidence = append(relevantEvidence, *ev)
			}
		}
	} else {
		// Otherwise use all evidence (might be relevant)
		relevantEvidence = evidence
	}

	if len(relevantEvidence) == 0 {
		return "No evidence collected yet."
	}

	// Limit to top 10 pieces of evidence to avoid prompt bloat
	limit := 10
	if len(relevantEvidence) > limit {
		relevantEvidence = relevantEvidence[:limit]
	}

	var sb strings.Builder
	for idx, ev := range relevantEvidence {
		sb.WriteString(fmt.Sprintf("%d. [Confidence: %.2f] %s\n   Source: %s\n",
			idx+1, ev.Confidence, ev.Claim, ev.PaperTitle))
	}

	return sb.String()
}

// parseEvaluationResponse parses the LLM JSON response into an EvaluationResult
func (he *HypothesisEvaluator) parseEvaluationResponse(response string) (*EvaluationResult, error) {
	response = strings.TrimSpace(response)

	var result EvaluationResult
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("failed to parse evaluation JSON: %w", err)
	}

	// Validate verdict
	if result.Verdict != "supported" && result.Verdict != "uncertain" && result.Verdict != "refuted" {
		result.Verdict = "uncertain"
	}

	// Clamp score
	if result.Score < 0 {
		result.Score = 0
	}
	if result.Score > 1 {
		result.Score = 1
	}

	return &result, nil
}

// heuristicEvaluation provides a fallback evaluation when LLM is unavailable
func (he *HypothesisEvaluator) heuristicEvaluation(hypothesis *Hypothesis, evidence []EvidenceFinding) *EvaluationResult {
	relevantEvidence := evidence
	if hypothesis.Evidence != nil && len(hypothesis.Evidence) > 0 {
		relevantEvidence = make([]EvidenceFinding, 0, len(hypothesis.Evidence))
		for _, ev := range hypothesis.Evidence {
			if ev != nil {
				relevantEvidence = append(relevantEvidence, *ev)
			}
		}
	}

	if len(relevantEvidence) == 0 {
		return &EvaluationResult{
			Score:            0.0,
			Verdict:          "uncertain",
			Reasoning:        "No evidence available for evaluation",
			MissingEvidence:  []string{"Any evidence supporting or refuting this hypothesis"},
			SuggestedQueries: []string{hypothesis.Claim},
			EvaluatedAt:      NowMillis(),
			HypothesisID:     hypothesis.ID,
		}
	}

	// Compute mean confidence
	totalConf := 0.0
	for _, ev := range relevantEvidence {
		totalConf += ev.Confidence
	}
	meanConf := totalConf / float64(len(relevantEvidence))

	// Penalty for contradictions
	contradictionPenalty := float64(hypothesis.ContradictionCount) * 0.15
	if contradictionPenalty > 0.5 {
		contradictionPenalty = 0.5
	}

	score := meanConf - contradictionPenalty

	// Evidence count boost
	countBoost := 0.0
	switch {
	case len(relevantEvidence) >= 10:
		countBoost = 0.10
	case len(relevantEvidence) >= 5:
		countBoost = 0.05
	}
	score += countBoost

	// Clamp
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}

	verdict := "uncertain"
	if score >= 0.7 {
		verdict = "supported"
	} else if score < 0.3 {
		verdict = "refuted"
	}

	reasoning := fmt.Sprintf("Heuristic evaluation: %d evidence pieces, mean confidence %.2f, %d contradictions",
		len(relevantEvidence), meanConf, hypothesis.ContradictionCount)

	return &EvaluationResult{
		Score:        score,
		Verdict:      verdict,
		Reasoning:    reasoning,
		EvaluatedAt:  NowMillis(),
		HypothesisID: hypothesis.ID,
	}
}

// PruneHypothesesByScore removes hypotheses below a confidence threshold
func (he *HypothesisEvaluator) PruneHypothesesByScore(hypotheses []*Hypothesis, threshold float64) []*Hypothesis {
	pruned := make([]*Hypothesis, 0, len(hypotheses))
	for _, h := range hypotheses {
		if h.ConfidenceScore >= threshold {
			pruned = append(pruned, h)
		} else {
			slog.Debug("Pruning low-confidence hypothesis",
				"claim", h.Claim,
				"score", h.ConfidenceScore,
				"threshold", threshold)
		}
	}
	return pruned
}
