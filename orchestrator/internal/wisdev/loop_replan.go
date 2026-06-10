package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

// borderlineSufficiencyConfidence is the threshold below which a "sufficient"
// verdict is considered borderline and must be confirmed by a second
// independent evaluation before the loop is allowed to converge.
const borderlineSufficiencyConfidence = 0.65

// maxLoopReplanQueries caps how many fresh agenda queries a single mid-loop
// replanning pass may contribute.
const maxLoopReplanQueries = 5

// confirmBorderlineSufficiency re-evaluates a borderline "sufficient" verdict
// with a second independent LLM pass and only confirms convergence when both
// evaluations agree. High-confidence verdicts pass through without extra cost.
func (l *AutonomousLoop) confirmBorderlineSufficiency(
	ctx context.Context,
	req LoopRequest,
	analysis *sufficiencyAnalysis,
	papers []search.Paper,
) bool {
	if analysis == nil || !analysis.Sufficient {
		return false
	}
	if analysis.Confidence >= borderlineSufficiencyConfidence {
		return true
	}
	if l.llmClient == nil || autonomousLLMCooldownRemaining(l) > 0 {
		// No second opinion available — accept the verdict rather than burning
		// budget looping on a decision we cannot re-check.
		return true
	}

	second, err := l.evaluateSufficiency(ctx, req.Query, papers)
	if err != nil || second == nil {
		slog.Warn("Borderline sufficiency confirmation unavailable; accepting first verdict",
			"component", "wisdev.autonomous",
			"operation", "confirm_sufficiency",
			"error", err,
			"confidence", analysis.Confidence,
		)
		return true
	}

	if second.Sufficient {
		// Agreement: merge the (usually richer) second analysis confidence.
		if second.Confidence > analysis.Confidence {
			analysis.Confidence = second.Confidence
		}
		return true
	}

	slog.Info("Borderline sufficiency verdict rejected by second evaluation; continuing research",
		"component", "wisdev.autonomous",
		"operation", "confirm_sufficiency",
		"first_confidence", analysis.Confidence,
		"second_confidence", second.Confidence,
		"second_reasoning", strings.TrimSpace(second.Reasoning),
	)
	// Carry the dissenting evaluation's gaps forward so the next iteration
	// targets them.
	analysis.MissingAspects = dedupeTrimmedStrings(append(analysis.MissingAspects, second.MissingAspects...))
	analysis.NextQueries = dedupeTrimmedStrings(append(analysis.NextQueries, second.NextQueries...))
	return false
}

// regenerateLoopAgenda asks the LLM for a fresh batch of agenda queries
// conditioned on the current gap state and the evidence gathered so far. This
// is the mid-loop replanning step: instead of only consuming the static query
// pool plus heuristic follow-ups, the loop periodically rewrites its agenda
// from what it has learned.
func (l *AutonomousLoop) regenerateLoopAgenda(
	ctx context.Context,
	req LoopRequest,
	analysis *sufficiencyAnalysis,
	gapState *LoopGapState,
	papers []search.Paper,
	executedQueries []string,
) []string {
	if l.llmClient == nil || analysis == nil || analysis.Sufficient || gapState == nil {
		return nil
	}
	if autonomousLLMCooldownRemaining(l) > 0 {
		return nil
	}

	openObligations := make([]string, 0, len(gapState.Ledger))
	for _, entry := range gapState.Ledger {
		if strings.EqualFold(entry.Status, "closed") {
			continue
		}
		title := strings.TrimSpace(firstNonEmpty(entry.Title, entry.Description))
		if title == "" {
			continue
		}
		openObligations = append(openObligations, fmt.Sprintf("- [%s] %s", firstNonEmpty(entry.Category, "gap"), title))
		if len(openObligations) >= 6 {
			break
		}
	}

	topPapers := make([]string, 0, 8)
	for _, p := range papers {
		title := strings.TrimSpace(p.Title)
		if title == "" {
			continue
		}
		topPapers = append(topPapers, "- "+title)
		if len(topPapers) >= 8 {
			break
		}
	}

	executed := executedQueries
	if len(executed) > 12 {
		executed = executed[len(executed)-12:]
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`You are replanning an in-progress literature research loop. The evidence so far is NOT sufficient.

Research query: %s

Identified gaps:
- Missing aspects: %s
- Missing source types: %s
- Unresolved contradictions: %s

Open coverage obligations:
%s

Papers already retrieved (titles):
%s

Queries already executed (do NOT repeat or trivially rephrase these):
%s

Generate up to %d NEW search queries that directly target the gaps above. Each query must:
1. Target a specific missing aspect, source type, or contradiction — not the broad topic again.
2. Use precise academic terminology suitable for scholarly search APIs.
3. Be meaningfully different from every executed query.`,
		strings.TrimSpace(req.Query),
		strings.Join(analysis.MissingAspects, "; "),
		strings.Join(analysis.MissingSourceTypes, "; "),
		strings.Join(analysis.Contradictions, "; "),
		strings.Join(openObligations, "\n"),
		strings.Join(topPapers, "\n"),
		strings.Join(executed, " | "),
		maxLoopReplanQueries,
	))

	reqCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	resp, err := l.llmClient.StructuredOutput(reqCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveStandardModel(),
		JsonSchema: `{"type":"object","required":["queries"],"properties":{"queries":{"type":"array","items":{"type":"string"},"maxItems":5},"reasoning":{"type":"string"}}}`,
	}))
	if err != nil {
		slog.Warn("Mid-loop agenda regeneration failed; continuing with existing queue",
			"component", "wisdev.autonomous",
			"operation", "regenerate_agenda",
			"error", err,
		)
		return nil
	}

	var parsed struct {
		Queries   []string `json:"queries"`
		Reasoning string   `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.JsonResult)), &parsed); err != nil {
		slog.Warn("Mid-loop agenda regeneration returned unparseable output",
			"component", "wisdev.autonomous",
			"operation", "regenerate_agenda",
			"error", err,
		)
		return nil
	}

	queries := normalizeLoopQueries("", parsed.Queries)
	if len(queries) > maxLoopReplanQueries {
		queries = queries[:maxLoopReplanQueries]
	}
	if len(queries) > 0 {
		slog.Info("Mid-loop replanning regenerated agenda queries",
			"component", "wisdev.autonomous",
			"operation", "regenerate_agenda",
			"queryCount", len(queries),
			"reasoning", strings.TrimSpace(parsed.Reasoning),
		)
	}
	return queries
}
