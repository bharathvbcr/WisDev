package wisdev

import (
	"log/slog"
	"strings"
)

// hypothesisMergeSimilarity is the claim-similarity threshold (Jaccard over
// meaningful tokens) above which two active hypotheses are treated as
// duplicates and merged.
const hypothesisMergeSimilarity = 0.75

// MergeSimilarHypotheses collapses near-duplicate hypotheses so long research
// runs don't accumulate overlapping branches. For each duplicate pair the
// higher-confidence hypothesis survives; the other's evidence is folded in
// (deduplicated by finding ID) and it is terminated with status "merged".
// Terminated hypotheses are left untouched. The input slice is returned with
// merged-away entries removed.
func MergeSimilarHypotheses(hypotheses []Hypothesis) []Hypothesis {
	if len(hypotheses) < 2 {
		return hypotheses
	}

	tokens := make([]map[string]struct{}, len(hypotheses))
	for i := range hypotheses {
		tokens[i] = hypothesisClaimTokens(&hypotheses[i])
	}

	mergedAway := make([]bool, len(hypotheses))
	for i := 0; i < len(hypotheses); i++ {
		if mergedAway[i] || hypotheses[i].IsTerminated {
			continue
		}
		for j := i + 1; j < len(hypotheses); j++ {
			if mergedAway[j] || hypotheses[j].IsTerminated {
				continue
			}
			if tokenJaccard(tokens[i], tokens[j]) < hypothesisMergeSimilarity {
				continue
			}
			winner, loser := i, j
			if hypotheses[j].ConfidenceScore > hypotheses[i].ConfidenceScore {
				winner, loser = j, i
			}
			absorbHypothesisEvidence(&hypotheses[winner], &hypotheses[loser])
			mergedAway[loser] = true
			slog.Info("Merged near-duplicate hypotheses",
				"component", "wisdev.autonomous",
				"operation", "hypothesis_merge",
				"survivorID", hypotheses[winner].ID,
				"mergedID", hypotheses[loser].ID,
				"survivorClaim", hypotheses[winner].Claim,
				"mergedClaim", hypotheses[loser].Claim,
			)
			if loser == i {
				break // i was merged away; stop comparing it against later entries
			}
		}
	}

	result := make([]Hypothesis, 0, len(hypotheses))
	for i := range hypotheses {
		if mergedAway[i] {
			continue
		}
		result = append(result, hypotheses[i])
	}
	return result
}

// absorbHypothesisEvidence folds the loser's evidence and counters into the
// winner, deduplicating findings by ID, and marks the loser as merged.
func absorbHypothesisEvidence(winner, loser *Hypothesis) {
	seen := make(map[string]struct{}, len(winner.Evidence))
	for _, ev := range winner.Evidence {
		if ev != nil && ev.ID != "" {
			seen[ev.ID] = struct{}{}
		}
	}
	for _, ev := range loser.Evidence {
		if ev == nil {
			continue
		}
		if ev.ID != "" {
			if _, dup := seen[ev.ID]; dup {
				continue
			}
			seen[ev.ID] = struct{}{}
		}
		winner.Evidence = append(winner.Evidence, ev)
	}
	winner.EvidenceCount = len(winner.Evidence)
	winner.ContradictionCount += loser.ContradictionCount
	winner.Contradictions = append(winner.Contradictions, loser.Contradictions...)
	winner.UpdatedAt = NowMillis()

	loser.IsTerminated = true
	loser.Status = "merged"
	loser.ParentID = firstNonEmpty(loser.ParentID, winner.ID)
	loser.UpdatedAt = NowMillis()
}

// hypothesisClaimTokens builds the meaningful-token set of a hypothesis claim.
func hypothesisClaimTokens(h *Hypothesis) map[string]struct{} {
	text := strings.TrimSpace(firstNonEmpty(h.Claim, h.Text))
	tokens := make(map[string]struct{})
	for _, token := range strings.Fields(strings.ToLower(text)) {
		token = strings.Trim(token, ".,;:!?()[]\"'")
		if len(token) >= 4 {
			tokens[token] = struct{}{}
		}
	}
	return tokens
}

// tokenJaccard computes the Jaccard similarity of two token sets.
func tokenJaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	intersection := 0
	for token := range small {
		if _, ok := large[token]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
