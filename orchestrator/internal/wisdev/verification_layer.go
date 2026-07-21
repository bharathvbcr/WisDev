package wisdev

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Verify runs the four-stage verification pipeline on a hypothesis in place.
// Stage 1: claim linking (lexical overlap between findings and the hypothesis).
// Stage 2: source credibility scoring (impact + overlap + recency).
// Stage 3: cross-pair contradiction detection.
// Stage 4: confidence aggregation with contradiction penalty.
// Each stage is gated by the corresponding VerificationLayerConfig flag.
func (v *VerificationLayer) Verify(ctx context.Context, h *Hypothesis) error {
	if v == nil {
		return fmt.Errorf("verification layer is nil")
	}
	if h == nil {
		return fmt.Errorf("hypothesis is nil")
	}
	if strings.TrimSpace(h.Text) == "" && strings.TrimSpace(h.Claim) == "" {
		return fmt.Errorf("hypothesis text is empty")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	if v.config.EnableClaimLinking {
		hypClaim := strings.TrimSpace(h.Text)
		if hypClaim == "" {
			hypClaim = strings.TrimSpace(h.Claim)
		}
		for _, ev := range h.Evidence {
			if ev == nil {
				continue
			}
			// Anchor the hypothesis to the exact sentences that support it, so
			// downstream confidence is traceable to source text.
			if len(ev.Spans) == 0 {
				ev.Spans = extractEvidenceSpans(hypClaim, ev)
			}
			if ev.OverlapRatio <= 0 {
				ev.OverlapRatio = evidenceHypothesisOverlap(h, ev)
				// A strong sentence-level anchor is better evidence of linkage
				// than diffuse whole-text overlap.
				for _, span := range ev.Spans {
					if span.MatchScore > ev.OverlapRatio {
						ev.OverlapRatio = span.MatchScore
					}
				}
			}
		}
	}

	if v.config.EnableSourceScoring {
		for _, ev := range h.Evidence {
			if ev == nil {
				continue
			}
			ev.Confidence = scoreEvidenceSourceCredibility(ev)
		}
	}

	var pairs []ContradictionPair
	if v.config.EnableContradictionDetection {
		// Keyword/polarity prescreen, then a batched semantic judge that drops
		// false positives and catches disagreements without negation keywords.
		pairs = judgeContradictionPairsWithLLM(ctx, h, contradictionPairsFor(h))
		h.ContradictionCount = len(pairs)
		contradicting := make(map[string]bool, len(pairs)*2)
		for _, p := range pairs {
			contradicting[p.FindingA.ID] = true
			contradicting[p.FindingB.ID] = true
		}
		var contradictions []*EvidenceFinding
		for _, ev := range h.Evidence {
			if ev != nil && contradicting[ev.ID] {
				contradictions = append(contradictions, ev)
			}
		}
		h.Contradictions = contradictions
	}

	if v.config.EnableConfidenceAggregation {
		if !v.config.EnableContradictionDetection {
			pairs = contradictionPairsFor(h)
		}
		h.ConfidenceScore = aggregateHypothesisConfidence(h, pairs)
	}

	h.UpdatedAt = NowMillis()
	return nil
}

// evidenceHypothesisOverlap measures the fraction of hypothesis keywords
// (length >= 4) that appear in the finding's claim, snippet, or title.
func evidenceHypothesisOverlap(h *Hypothesis, ev *EvidenceFinding) float64 {
	hypText := strings.TrimSpace(h.Text)
	if hypText == "" {
		hypText = strings.TrimSpace(h.Claim)
	}
	var keywords []string
	for _, kw := range strings.Fields(strings.ToLower(hypText)) {
		if len(kw) >= 4 {
			keywords = append(keywords, kw)
		}
	}
	if len(keywords) == 0 {
		return 0
	}
	body := strings.ToLower(ev.Claim + " " + ev.Snippet + " " + ev.PaperTitle)
	matched := 0
	for _, kw := range keywords {
		if strings.Contains(body, kw) {
			matched++
		}
	}
	return float64(matched) / float64(len(keywords))
}

// scoreEvidenceSourceCredibility scores a finding as
// Impact (0.4) + Overlap (0.3) + Recency (0.3).
func scoreEvidenceSourceCredibility(ev *EvidenceFinding) float64 {
	// Impact: carry forward any existing confidence signal (0.5 baseline).
	impact := ev.Confidence
	if impact <= 0 {
		impact = 0.5
	}
	// Cap impact at 1.0 so we don't compound self-referentially.
	if impact > 1.0 {
		impact = 1.0
	}

	overlap := ev.OverlapRatio
	if overlap <= 0 {
		overlap = 0.5 // default when not measured
	}

	// Recency: papers from the last 2 years score 1.0, decay to 0.3 over 10 years.
	recency := 0.5 // neutral default when year is unknown
	if ev.Year > 0 {
		currentYear := time.Now().Year()
		age := currentYear - ev.Year
		if age < 0 {
			age = 0
		}
		switch {
		case age <= 2:
			recency = 1.0
		case age <= 5:
			recency = 0.85
		case age <= 10:
			recency = 0.65
		default:
			recency = 0.3
		}
	}

	return (impact * 0.4) + (overlap * 0.3) + (recency * 0.3)
}

// aggregateHypothesisConfidence computes the weighted-mean evidence confidence
// with a severity-scaled contradiction penalty (capped at 40%).
func aggregateHypothesisConfidence(h *Hypothesis, pairs []ContradictionPair) float64 {
	if len(h.Evidence) == 0 {
		return 0.0
	}

	// Weighted mean: specialist-verified evidence (+) / rejected evidence (-) counts double.
	totalWeight := 0.0
	weightedConf := 0.0
	for _, ev := range h.Evidence {
		if ev == nil {
			continue
		}
		weight := 1.0
		switch ev.Specialist.Verification {
		case 1:
			weight = 2.0 // Verified by specialist
		case -1:
			weight = 0.25 // Rejected — down-weight heavily
		}
		weightedConf += ev.Confidence * weight
		totalWeight += weight
	}
	if totalWeight == 0 {
		return 0.0
	}

	baseConf := weightedConf / totalWeight

	// Contradiction penalty: each high-confidence pair reduces score by 0.15,
	// medium by 0.08, low by 0.03 — capped at 40% total reduction.
	penalty := 0.0
	for _, pair := range pairs {
		switch pair.Severity {
		case ContradictionHigh:
			penalty += 0.15
		case ContradictionMedium:
			penalty += 0.08
		case ContradictionLow:
			penalty += 0.03
		}
	}
	if penalty > 0.40 {
		penalty = 0.40
	}
	baseConf -= penalty

	if baseConf < 0 {
		baseConf = 0
	}
	if baseConf > 1.0 {
		baseConf = 1.0
	}

	return baseConf
}
