package wisdev

import (
	"log/slog"
	"strings"
)

// Belief↔hypothesis consistency. Beliefs are derived from hypotheses
// (BuildBeliefsFromHypotheses) but until now a hypothesis that was later
// refuted, pruned, or merged away left its belief active — convergence checks
// and rebuttal-query injection kept treating a dead claim as live. This sync
// retires those beliefs, keeping the hypothesis ledger the single source of
// truth for which claims are still under investigation.

// RetireBeliefsForInactiveHypotheses marks beliefs whose source hypotheses are
// terminated as refuted (refuted/pruned hypotheses) or revised (merged
// hypotheses — the surviving duplicate carries the claim forward). Beliefs are
// matched by normalized claim text. Returns the number of beliefs retired.
func (bsm *BeliefStateManager) RetireBeliefsForInactiveHypotheses(hypotheses []Hypothesis) int {
	if bsm == nil {
		return 0
	}
	state := bsm.GetState()
	if state == nil || len(state.Beliefs) == 0 {
		return 0
	}

	refutedClaims := make(map[string]struct{})
	mergedClaims := make(map[string]struct{})
	for i := range hypotheses {
		h := &hypotheses[i]
		key := normalizedBeliefClaimKey(h.Claim)
		if key == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(h.Status))
		switch {
		case status == "merged":
			mergedClaims[key] = struct{}{}
		case h.IsTerminated || status == "refuted" || status == "pruned":
			refutedClaims[key] = struct{}{}
		}
	}
	if len(refutedClaims) == 0 && len(mergedClaims) == 0 {
		return 0
	}

	retired := 0
	for id, belief := range state.Beliefs {
		if belief == nil || belief.Status != BeliefStatusActive {
			continue
		}
		key := normalizedBeliefClaimKey(belief.Claim)
		if key == "" {
			continue
		}
		if _, refuted := refutedClaims[key]; refuted {
			state.RefuteBelief(id)
			retired++
			continue
		}
		if _, merged := mergedClaims[key]; merged {
			state.UpdateBelief(id, func(b *Belief) {
				b.Status = BeliefStatusRevised
			})
			retired++
		}
	}

	if retired > 0 {
		slog.Info("Retired beliefs for inactive hypotheses",
			"component", "wisdev.beliefs",
			"retired", retired,
			"refutedClaims", len(refutedClaims),
			"mergedClaims", len(mergedClaims))
	}
	return retired
}

func normalizedBeliefClaimKey(claim string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(claim))), " ")
}
