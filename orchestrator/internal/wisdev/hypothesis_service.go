package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type HypothesisService struct {
	llmClient *llm.Client
}

func NewHypothesisService(client *llm.Client) *HypothesisService {
	return &HypothesisService{llmClient: client}
}

func (s *HypothesisService) ProposeHypotheses(ctx context.Context, query string, intent string, model string) ([]Hypothesis, error) {
	if s == nil || s.llmClient == nil {
		return nil, fmt.Errorf("llm client is not configured")
	}
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := s.llmClient.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("hypothesis service proposal using cooldown fallback",
			"component", "wisdev.hypothesis",
			"operation", "propose_hypotheses",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackBrainHypotheses(query, intent), nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Propose 3 research hypotheses for the query: %s. Intent: %s. Include claim, falsifiabilityCondition, and confidenceThreshold.", query, intent))
	resp, err := s.llmClient.StructuredOutput(ctx, applyWisdevStandardStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "array", "items": {"type": "object", "properties": {"claim": {"type": "string"}, "falsifiabilityCondition": {"type": "string"}, "confidenceThreshold": {"type": "number"}}}}`,
	}))
	if err != nil {
		if wisdevLLMCallIsCoolingDown(err) {
			slog.Warn("hypothesis service proposal using provider cooldown fallback",
				"component", "wisdev.hypothesis",
				"operation", "propose_hypotheses",
				"stage", "rate_limit_fallback",
				"error", err.Error(),
			)
			return fallbackBrainHypotheses(query, intent), nil
		}
		return nil, err
	}
	var hypotheses []Hypothesis
	if err := json.Unmarshal([]byte(resp.JsonResult), &hypotheses); err != nil {
		return nil, err
	}
	return hypotheses, nil
}
