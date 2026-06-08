package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// SynthesisRefiner handles iterative refinement of research drafts.
type SynthesisRefiner struct {
	model Model
}

func NewSynthesisRefiner(model Model) *SynthesisRefiner {
	return &SynthesisRefiner{model: model}
}

// InitialSynthesis generates the first draft from hypotheses and evidence.
func (r *SynthesisRefiner) InitialSynthesis(ctx context.Context, hypotheses []*Hypothesis) (string, error) {
	hypTexts := make([]string, len(hypotheses))
	for i, h := range hypotheses {
		hypTexts[i] = h.Text
	}

	evidence := make(map[string]interface{})
	for _, h := range hypotheses {
		evidence[h.ID] = h.Evidence
	}

	if r.model == nil {
		return fallbackInitialSynthesis(hypTexts), nil
	}

	summary, err := r.model.SynthesizeFindings(ctx, hypTexts, evidence)
	if err != nil {
		if wisdevLLMCallIsCoolingDown(err) {
			slog.Warn("initial synthesis fell back during provider cooldown",
				"component", "wisdev.synthesis",
				"operation", "initial_synthesis",
				"hypothesisCount", len(hypotheses),
				"error", err.Error(),
			)
			return fallbackInitialSynthesis(hypTexts), nil
		}
		return "", err
	}
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return fallbackInitialSynthesis(hypTexts), nil
	}
	return trimmed, nil
}

// RefineDraft improves a draft based on critique and new findings.
func (r *SynthesisRefiner) RefineDraft(ctx context.Context, draft string, critique string, newEvidence []*EvidenceFinding) (string, error) {
	prompt := fmt.Sprintf(`Refine the following research draft based on the provided critique and new evidence.

Original Draft:
"""
%s
"""

Critique:
"""
%s
"""

New Evidence:
%v

Please produce an updated draft that addresses the critique and incorporates the new evidence while maintaining scientific rigor.`, draft, critique, newEvidence)

	// Model.Generate is the general-purpose text generation method; we pass the
	// full refinement prompt directly and return the model's response.
	if r.model == nil {
		return strings.TrimSpace(draft), nil
	}

	refined, err := r.model.Generate(ctx, prompt)
	if err != nil {
		if wisdevLLMCallIsCoolingDown(err) {
			slog.Warn("draft refinement skipped during provider cooldown; keeping existing draft",
				"component", "wisdev.synthesis",
				"operation", "refine_draft",
				"evidenceCount", len(newEvidence),
				"error", err.Error(),
			)
			return strings.TrimSpace(draft), nil
		}
		return "", err
	}
	trimmed := strings.TrimSpace(refined)
	if trimmed == "" {
		return strings.TrimSpace(draft), nil
	}
	return trimmed, nil
}

func fallbackInitialSynthesis(hypotheses []string) string {
	for _, hypothesis := range hypotheses {
		if trimmed := strings.TrimSpace(hypothesis); trimmed != "" {
			return "Synthesis: " + trimmed
		}
	}
	return "No hypotheses available."
}
