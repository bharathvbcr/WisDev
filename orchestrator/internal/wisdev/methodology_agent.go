package wisdev

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// MethodologyAgent provides expert critique of research design and findings.
type MethodologyAgent struct {
	model Model
}

func NewMethodologyAgent(model Model) *MethodologyAgent {
	return &MethodologyAgent{model: model}
}

// Critique evaluates a set of hypotheses and their supporting evidence.
func (a *MethodologyAgent) Critique(ctx context.Context, hypotheses []*Hypothesis) (string, error) {
	if len(hypotheses) == 0 {
		return "No hypotheses provided for critique.", nil
	}

	var findings []string
	for _, h := range hypotheses {
		finding := fmt.Sprintf("Hypothesis: %s (Confidence: %.2f)\n", h.Text, h.ConfidenceScore)
		for _, e := range h.Evidence {
			finding += fmt.Sprintf("- Evidence from %s: %s (Overlap: %.2f)\n", e.PaperTitle, e.Claim, e.OverlapRatio)
		}
		findings = append(findings, finding)
	}

	fallback := formatMethodologyFallbackCritique(heuristicMethodologyWeaknesses(hypotheses))
	if a.model == nil {
		return fallback, nil
	}

	critique, err := a.model.CritiqueFindings(ctx, findings)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		slog.Warn("methodology critique degraded to heuristic fallback",
			"component", "wisdev.methodology",
			"operation", "critique",
			"error", err.Error(),
			"hypothesisCount", len(hypotheses),
		)
		return fallback, nil
	}
	trimmed := strings.TrimSpace(critique)
	if trimmed == "" {
		return fallback, nil
	}
	return trimmed, nil
}

// IdentifyWeaknesses specifically looks for methodological gaps.
func (a *MethodologyAgent) IdentifyWeaknesses(ctx context.Context, hypotheses []*Hypothesis) ([]string, error) {
	if len(hypotheses) == 0 {
		return []string{"No hypotheses provided for critique."}, nil
	}
	critique, err := a.Critique(ctx, hypotheses)
	if err != nil {
		return nil, err
	}
	if weaknesses := extractMethodologyWeaknesses(critique); len(weaknesses) > 0 {
		return weaknesses, nil
	}
	return heuristicMethodologyWeaknesses(hypotheses), nil
}

func heuristicMethodologyWeaknesses(hypotheses []*Hypothesis) []string {
	if len(hypotheses) == 0 {
		return []string{"No hypotheses provided for critique."}
	}

	var weaknesses []string
	for _, hypothesis := range hypotheses {
		label := firstNonEmpty(hypothesis.Text, hypothesis.Claim, "Unnamed hypothesis")
		if strings.TrimSpace(hypothesis.Text) == "" && strings.TrimSpace(hypothesis.Claim) == "" {
			weaknesses = append(weaknesses, "A hypothesis is missing a clear claim statement.")
		}

		evidenceCount := hypothesis.EvidenceCount
		if evidenceCount <= 0 {
			evidenceCount = len(hypothesis.Evidence)
		}
		if evidenceCount == 0 {
			weaknesses = append(weaknesses, fmt.Sprintf("%s lacks supporting evidence and should trigger more retrieval.", label))
		} else if evidenceCount == 1 {
			weaknesses = append(weaknesses, fmt.Sprintf("%s relies on a single supporting source, so source diversity and replication are weak.", label))
		}

		if hypothesis.ConfidenceScore > 0 && hypothesis.ConfidenceScore < 0.5 {
			weaknesses = append(weaknesses, fmt.Sprintf("%s remains low-confidence (%.2f) and needs stronger support or narrower scope.", label, hypothesis.ConfidenceScore))
		}
		if hypothesis.ConfidenceThreshold > 0 && hypothesis.ConfidenceScore > 0 && hypothesis.ConfidenceScore < hypothesis.ConfidenceThreshold {
			weaknesses = append(weaknesses, fmt.Sprintf("%s is below its confidence threshold (%.2f < %.2f).", label, hypothesis.ConfidenceScore, hypothesis.ConfidenceThreshold))
		}

		contradictionCount := hypothesis.ContradictionCount
		if contradictionCount <= 0 {
			contradictionCount = len(hypothesis.Contradictions)
		}
		if contradictionCount > 0 {
			weaknesses = append(weaknesses, fmt.Sprintf("%s has %d contradictory evidence item(s) that are not yet resolved.", label, contradictionCount))
		}
		if hypothesis.IsTerminated {
			weaknesses = append(weaknesses, fmt.Sprintf("%s was terminated before methodological gaps were resolved.", label))
		}

		sourceKeys := make(map[string]struct{}, len(hypothesis.Evidence))
		weakEvidenceSignals := 0
		provenanceGaps := 0
		for _, evidence := range hypothesis.Evidence {
			if evidence == nil {
				weakEvidenceSignals++
				provenanceGaps++
				continue
			}
			key := firstNonEmpty(evidence.SourceID, evidence.PaperTitle, evidence.ID)
			if key != "" {
				sourceKeys[key] = struct{}{}
			}
			if evidence.Confidence > 0 && evidence.Confidence < 0.55 {
				weakEvidenceSignals++
			}
			if evidence.OverlapRatio > 0 && evidence.OverlapRatio < 0.4 {
				weakEvidenceSignals++
			}
			if firstNonEmpty(evidence.SourceID, evidence.PaperTitle) == "" {
				provenanceGaps++
			}
		}
		if evidenceCount > 1 && len(sourceKeys) <= 1 {
			weaknesses = append(weaknesses, fmt.Sprintf("%s is supported by limited source diversity and needs independent corroboration.", label))
		}
		if weakEvidenceSignals >= maxInt(1, evidenceCount) && evidenceCount > 0 {
			weaknesses = append(weaknesses, fmt.Sprintf("%s is supported by weak or only loosely overlapping evidence.", label))
		}
		if provenanceGaps > 0 {
			weaknesses = append(weaknesses, fmt.Sprintf("%s has %d evidence item(s) with weak provenance metadata.", label, provenanceGaps))
		}
	}

	if len(weaknesses) == 0 {
		return []string{"Current hypotheses are plausible, but methods should still be stress-tested for replication, provenance quality, and unresolved confounders."}
	}
	return dedupeTrimmedStrings(weaknesses)
}

func formatMethodologyFallbackCritique(weaknesses []string) string {
	clean := dedupeTrimmedStrings(weaknesses)
	if len(clean) == 0 {
		clean = []string{"Current hypotheses are plausible, but methods should still be stress-tested for replication, provenance quality, and unresolved confounders."}
	}

	var builder strings.Builder
	builder.WriteString("Methodology critique (heuristic fallback):")
	for _, weakness := range clean {
		builder.WriteString("\n- ")
		builder.WriteString(weakness)
	}
	return builder.String()
}

func extractMethodologyWeaknesses(critique string) []string {
	trimmed := strings.TrimSpace(critique)
	if trimmed == "" {
		return nil
	}

	lines := strings.Split(trimmed, "\n")
	weaknesses := make([]string, 0, len(lines))
	for _, line := range lines {
		clean := strings.TrimSpace(line)
		if clean == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(clean), "methodology critique") {
			continue
		}
		clean = strings.TrimLeft(clean, "-*• \t")
		clean = strings.TrimLeft(clean, "0123456789.) ")
		clean = strings.TrimSpace(clean)
		if clean == "" {
			continue
		}
		weaknesses = append(weaknesses, clean)
	}

	if len(weaknesses) == 0 {
		return []string{trimmed}
	}
	return dedupeTrimmedStrings(weaknesses)
}
