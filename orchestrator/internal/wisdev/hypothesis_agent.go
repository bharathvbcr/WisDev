package wisdev

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// HypothesisAgent generates and normalizes hypotheses from a user query.
type HypothesisAgent struct {
	model Model
}

func NewHypothesisAgent(model Model) *HypothesisAgent {
	return &HypothesisAgent{model: model}
}

func (a *HypothesisAgent) Generate(ctx context.Context, query string, count int) ([]*Hypothesis, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if count <= 0 {
		count = 5
	}

	texts := make([]string, 0, count)
	if a.model != nil {
		generated, err := a.model.GenerateHypotheses(ctx, query)
		if err == nil && len(generated) > 0 {
			texts = generated
		}
	}

	if len(texts) == 0 {
		// Deterministic fallback ensures the pipeline still runs under model outages.
		for i := 0; i < count; i++ {
			texts = append(texts, fmt.Sprintf("Hypothesis %d: %s", i+1, query))
		}
	}

	now := time.Now()
	out := make([]*Hypothesis, 0, len(texts))
	for i, text := range texts {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, &Hypothesis{
			ID:        fmt.Sprintf("hyp_%d", i+1),
			Query:     query,
			Text:      text,
			Category:  "empirical",
			Status:    "generating",
			CreatedAt: now.UnixMilli(),
			UpdatedAt: now.UnixMilli(),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no hypotheses generated")
	}
	return out, nil
}
