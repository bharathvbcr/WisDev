package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

// Semantic contradiction detection. The keyword/polarity heuristic in
// contradictionPairsFor is a cheap prescreen; this judge runs ONE batched LLM
// call over the candidate pairs (plus a few topically-overlapping latent
// candidates the heuristic cannot see) and keeps only pairs that genuinely
// contradict. On any failure the heuristic candidates pass through unchanged.

const (
	maxContradictionPairsJudged = 6
	maxLatentCandidatePairs     = 3
	contradictionExcerptLength  = 220
)

type contradictionVerdict struct {
	PairIndex   int    `json:"pairIndex"`
	Contradicts bool   `json:"contradicts"`
	Severity    string `json:"severity"`
	Explanation string `json:"explanation"`
}

// judgeContradictionPairsWithLLM verifies candidate contradiction pairs
// semantically. Returns the confirmed pairs (with judge-assigned severity and
// explanations); falls back to the input candidates when the judge can't run.
func judgeContradictionPairsWithLLM(ctx context.Context, h *Hypothesis, candidates []ContradictionPair) []ContradictionPair {
	client := GlobalLLMClient
	if client == nil || ctx == nil || h == nil {
		return candidates
	}
	if remaining := client.ProviderCooldownRemaining(); remaining > 0 {
		return candidates
	}

	// Add latent candidates: topically-overlapping evidence pairs without
	// polarity keywords — disagreements the heuristic is blind to.
	pairs := append([]ContradictionPair(nil), candidates...)
	pairs = append(pairs, latentContradictionCandidates(h, pairs, maxLatentCandidatePairs)...)
	if len(pairs) == 0 {
		return candidates
	}
	if len(pairs) > maxContradictionPairsJudged {
		// Candidates past the cap are NOT judged. This function returns only the
		// pairs it CONFIRMED, so a caller that treats "absent from the result" as
		// "checked and cleared" would silently clear contradictions nobody read —
		// the defect fixed upstream in ScholarLM, where the callers that make it
		// exploitable live. No such caller exists here yet; log it so adding one
		// does not reintroduce the bug invisibly.
		slog.Warn("contradiction judge batch truncated; candidates past the cap were not examined",
			"component", "wisdev.contradictions", "operation", "adjudicate",
			"cap", maxContradictionPairsJudged, "dropped", len(pairs)-maxContradictionPairsJudged,
			"result", "partial", "error_code", "CONTRADICTION_ADJUDICATION_TRUNCATED")
		pairs = pairs[:maxContradictionPairsJudged]
	}

	var sb strings.Builder
	for i, pair := range pairs {
		sb.WriteString(fmt.Sprintf("Pair %d:\n  A) %s\n  B) %s\n",
			i,
			contradictionExcerpt(pair.FindingA),
			contradictionExcerpt(pair.FindingB)))
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`You are verifying whether pairs of research findings genuinely CONTRADICT each other with respect to a hypothesis.

Hypothesis under investigation: %s

Candidate pairs:
%s

For each pair decide:
- contradicts: true ONLY if the two findings make incompatible claims about the same phenomenon (opposite effect direction, presence vs absence of an effect, incompatible mechanisms). Different populations, methods, or sub-topics are NOT contradictions.
- severity: "high" (direct opposite conclusions), "medium" (partially incompatible), or "low" (weak tension).
- explanation: one sentence.

Return one verdict per pair, in order, using pairIndex.`,
		strings.TrimSpace(firstNonEmpty(h.Claim, h.Text)),
		sb.String(),
	))

	reqCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	resp, err := client.StructuredOutput(reqCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveLightModel(),
		JsonSchema: `{"type":"object","required":["verdicts"],"properties":{"verdicts":{"type":"array","items":{"type":"object","required":["pairIndex","contradicts","severity","explanation"],"properties":{"pairIndex":{"type":"integer"},"contradicts":{"type":"boolean"},"severity":{"type":"string"},"explanation":{"type":"string"}}}}}}`,
	}))
	if err != nil {
		slog.Debug("Contradiction judge unavailable; keeping heuristic pairs",
			"component", "wisdev.contradictions",
			"error", err)
		return candidates
	}

	var parsed struct {
		Verdicts []contradictionVerdict `json:"verdicts"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.JsonResult)), &parsed); err != nil {
		slog.Debug("Contradiction judge returned unparseable output; keeping heuristic pairs",
			"component", "wisdev.contradictions",
			"error", err)
		return candidates
	}

	confirmed := make([]ContradictionPair, 0, len(pairs))
	for _, verdict := range parsed.Verdicts {
		if !verdict.Contradicts || verdict.PairIndex < 0 || verdict.PairIndex >= len(pairs) {
			continue
		}
		pair := pairs[verdict.PairIndex]
		switch strings.ToLower(strings.TrimSpace(verdict.Severity)) {
		case "high":
			pair.Severity = ContradictionHigh
		case "low":
			pair.Severity = ContradictionLow
		default:
			pair.Severity = ContradictionMedium
		}
		if explanation := strings.TrimSpace(verdict.Explanation); explanation != "" {
			pair.Explanation = explanation
		}
		confirmed = append(confirmed, pair)
	}

	slog.Debug("Contradiction judge filtered candidate pairs",
		"component", "wisdev.contradictions",
		"candidates", len(pairs),
		"confirmed", len(confirmed))
	return confirmed
}

// latentContradictionCandidates pairs topically-overlapping evidence that the
// polarity heuristic skipped, so the judge can catch disagreements that don't
// use explicit negation language. Marked low severity until judged.
func latentContradictionCandidates(h *Hypothesis, existing []ContradictionPair, max int) []ContradictionPair {
	if max <= 0 || len(h.Evidence) < 2 {
		return nil
	}
	inExisting := make(map[string]bool, len(existing)*2)
	for _, pair := range existing {
		inExisting[pair.FindingA.ID+"|"+pair.FindingB.ID] = true
		inExisting[pair.FindingB.ID+"|"+pair.FindingA.ID] = true
	}

	hypKeywords := claimKeywords(firstNonEmpty(h.Text, h.Claim))
	overlapsHypothesis := func(ev *EvidenceFinding) bool {
		if ev == nil {
			return false
		}
		return spanMatchScore(hypKeywords, ev.Claim+" "+ev.Snippet) >= 0.3
	}

	var candidates []ContradictionPair
	for i := 0; i < len(h.Evidence) && len(candidates) < max; i++ {
		for j := i + 1; j < len(h.Evidence) && len(candidates) < max; j++ {
			a, b := h.Evidence[i], h.Evidence[j]
			if a == nil || b == nil || inExisting[a.ID+"|"+b.ID] {
				continue
			}
			if !overlapsHypothesis(a) || !overlapsHypothesis(b) {
				continue
			}
			candidates = append(candidates, ContradictionPair{
				FindingA:    *a,
				FindingB:    *b,
				Severity:    ContradictionLow,
				Explanation: "Latent candidate: both findings address the hypothesis subject.",
			})
		}
	}
	return candidates
}

func contradictionExcerpt(ev EvidenceFinding) string {
	text := strings.TrimSpace(firstNonEmpty(ev.Claim, ev.PaperTitle))
	snippet := strings.TrimSpace(ev.Snippet)
	if snippet != "" {
		text = text + ": " + snippet
	}
	if len(text) > contradictionExcerptLength {
		text = text[:contradictionExcerptLength] + "…"
	}
	return text
}
