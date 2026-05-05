package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type PlannerService struct {
	llmClient *llm.Client
}

func NewPlannerService(client *llm.Client) *PlannerService {
	return &PlannerService{llmClient: client}
}

func (s *PlannerService) DecomposeTask(ctx context.Context, query string, domain string, model string) ([]ResearchTask, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if s == nil || s.llmClient == nil {
		return nil, fmt.Errorf("llm client is not configured")
	}
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := s.llmClient.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("planner service task decomposition using cooldown fallback",
			"component", "wisdev.planner",
			"operation", "decompose_task",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackBrainResearchTasks(query, domain), nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`Decompose the following research query into a structured multi-step plan.
Query: %s
Domain: %s
Include tasks with 'id', 'name', 'action', and 'dependsOnIds'.`, query, domain))

	req := applyWisdevStandardStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "array", "items": {"type": "object", "properties": {"id": {"type": "string"}, "name": {"type": "string"}, "action": {"type": "string"}, "dependsOnIds": {"type": "array", "items": {"type": "string"}}}}}`,
	})
	resp, err := s.llmClient.StructuredOutput(ctx, req)
	if err != nil {
		if wisdevLLMCallIsCoolingDown(err) {
			slog.Warn("planner service task decomposition using provider cooldown fallback",
				"component", "wisdev.planner",
				"operation", "decompose_task",
				"stage", "rate_limit_fallback",
				"error", err.Error(),
			)
			return fallbackBrainResearchTasks(query, domain), nil
		}
		return nil, err
	}
	var tasks []ResearchTask
	if err := json.Unmarshal([]byte(resp.JsonResult), &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *PlannerService) CoordinateReplan(ctx context.Context, failedID string, reason string, contextData map[string]any, model string) ([]ResearchTask, error) {
	if s == nil || s.llmClient == nil {
		return nil, fmt.Errorf("llm client is not configured")
	}
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := s.llmClient.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("planner service replan using cooldown fallback",
			"component", "wisdev.planner",
			"operation", "coordinate_replan",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackBrainReplanTasks(failedID, reason, contextData), nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("A research step (%s) failed with reason: %s. Context: %v. Propose a recovery plan with replacement research tasks that include id, name, action, and dependsOnIds.", failedID, reason, contextData))
	req := applyWisdevStandardStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "array", "items": {"type": "object", "properties": {"id": {"type": "string"}, "name": {"type": "string"}, "action": {"type": "string"}, "dependsOnIds": {"type": "array", "items": {"type": "string"}}}}}`,
	})
	resp, err := s.llmClient.StructuredOutput(ctx, req)
	if err != nil {
		if wisdevLLMCallIsCoolingDown(err) {
			slog.Warn("planner service replan using provider cooldown fallback",
				"component", "wisdev.planner",
				"operation", "coordinate_replan",
				"stage", "rate_limit_fallback",
				"error", err.Error(),
			)
			return fallbackBrainReplanTasks(failedID, reason, contextData), nil
		}
		return nil, err
	}
	var tasks []ResearchTask
	if err := json.Unmarshal([]byte(resp.JsonResult), &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *PlannerService) AssessResearchComplexity(ctx context.Context, query string) (string, error) {
	if s == nil || s.llmClient == nil {
		return "", fmt.Errorf("llm client is not configured")
	}
	if remaining := s.llmClient.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("planner service complexity assessment using cooldown fallback",
			"component", "wisdev.planner",
			"operation", "assess_research_complexity",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackResearchComplexity(query), nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Assess the complexity of this research query: %s. Classify it as low, medium, or high.", query))
	req := applyWisdevLightStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		JsonSchema: wisdevResearchComplexitySchema,
	})
	resp, err := s.llmClient.StructuredOutput(ctx, req)
	if err != nil {
		if wisdevLLMCallIsCoolingDown(err) {
			slog.Warn("planner service complexity assessment using provider cooldown fallback",
				"component", "wisdev.planner",
				"operation", "assess_research_complexity",
				"stage", "rate_limit_fallback",
				"error", err.Error(),
			)
			return fallbackResearchComplexity(query), nil
		}
		return "", err
	}
	return parseResearchComplexity(resp.JsonResult)
}

func (s *PlannerService) AskFollowUpIfAmbiguous(ctx context.Context, query string, model string) (map[string]any, error) {
	if s == nil || s.llmClient == nil {
		return nil, fmt.Errorf("llm client is not configured")
	}
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := s.llmClient.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("planner service ambiguity check using cooldown fallback",
			"component", "wisdev.planner",
			"operation", "ask_follow_up_if_ambiguous",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackAmbiguityResponse(), nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Analyze this query: %s. If it is ambiguous, set isAmbiguous to true and provide a clarifying question. Otherwise set isAmbiguous to false and leave question empty.", query))
	req := applyWisdevStandardStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"isAmbiguous": {"type": "boolean"}, "question": {"type": "string"}}}`,
	})
	resp, err := s.llmClient.StructuredOutput(ctx, req)
	if err != nil {
		if wisdevLLMCallIsCoolingDown(err) {
			slog.Warn("planner service ambiguity check using provider cooldown fallback",
				"component", "wisdev.planner",
				"operation", "ask_follow_up_if_ambiguous",
				"stage", "rate_limit_fallback",
				"error", err.Error(),
			)
			return fallbackAmbiguityResponse(), nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func fallbackAmbiguityResponse() map[string]any {
	return map[string]any{
		"isAmbiguous": false,
		"question":    "",
		"degraded":    true,
	}
}
