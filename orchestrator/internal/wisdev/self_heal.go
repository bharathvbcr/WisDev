package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

// PlanStepRunner is the interface SelfHealer uses to execute plan steps.
// PlanExecutor satisfies this interface via RunStepWithRecovery.
type PlanStepRunner interface {
	RunStepWithRecovery(ctx context.Context, session *AgentSession, step PlanStep, laneID int) StepResult
}

// SelfHealer wraps plan step execution with LLM-backed retry logic.
type SelfHealer struct {
	LLM        LLMRequester
	Executor   PlanStepRunner
	MaxRetries int
}

func NewSelfHealer(llm LLMRequester, executor PlanStepRunner) *SelfHealer {
	return &SelfHealer{
		LLM:        llm,
		Executor:   executor,
		MaxRetries: 3,
	}
}

// Execute runs a plan step and attempts to self-heal on retryable errors.
func (sh *SelfHealer) Execute(ctx context.Context, sessionID string, step PlanStep) (map[string]any, error) {
	var lastErr error
	var errorHistory []string
	currentStep := step

	session := &AgentSession{
		SessionID: sessionID,
		Plan:      &PlanState{PlanID: "temp"},
	}

	for attempt := 1; attempt <= sh.MaxRetries; attempt++ {
		if attempt > 1 {
			slog.Info("self-healing: retrying step", "step", currentStep.ID, "attempt", attempt)
		}

		result := sh.Executor.RunStepWithRecovery(ctx, session, currentStep, 1)
		if result.Err == nil {
			return map[string]any{"sources": result.Sources}, nil
		}

		lastErr = result.Err
		if !sh.isRetryable(result.Err) {
			slog.Warn("self-healing: fatal error, stopping", "step", currentStep.ID, "error", result.Err)
			return nil, result.Err
		}

		// Detect oscillating errors
		errMsg := result.Err.Error()
		for _, prev := range errorHistory {
			if prev == errMsg {
				slog.Error("self-healing: error oscillation detected, aborting step", "step", currentStep.ID, "error", errMsg)
				return nil, fmt.Errorf("infinite recovery loop detected: %w", result.Err)
			}
		}
		errorHistory = append(errorHistory, errMsg)

		// Self-heal: ask LLM to revise the step
		revisedStep, healErr := sh.replanStep(ctx, currentStep, lastErr, attempt)
		if healErr != nil {
			slog.Error("self-healing: replan failed", "error", healErr)
			return nil, lastErr
		}
		currentStep = *revisedStep
	}

	return nil, fmt.Errorf("max retries reached for step %s: %w", step.ID, lastErr)
}

func (sh *SelfHealer) isRetryable(err error) bool {
	upper := strings.ToUpper(err.Error())
	for _, fatal := range []string{"FATAL:", "UNAUTHORIZED:", "INVALID_INPUT:", "NOT_FOUND:", "GUARDRAIL_BLOCKED:"} {
		if strings.Contains(upper, fatal) {
			return false
		}
	}
	return true
}

func (sh *SelfHealer) replanStep(ctx context.Context, step PlanStep, err error, attempt int) (*PlanStep, error) {
	if remaining := wisdevLLMCooldownRemaining(sh.LLM); remaining > 0 {
		slog.Warn("self-heal replan skipped during provider cooldown; keeping original step",
			"component", "wisdev.self_heal",
			"operation", "replan_step",
			"step_id", step.ID,
			"retry_after_ms", remaining.Milliseconds(),
		)
		return &step, nil
	}
	stepJSON, marshalErr := json.Marshal(step)
	if marshalErr != nil {
		return nil, fmt.Errorf("replan: could not serialize step: %w", marshalErr)
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`You are an autonomous AI Scientist. A research step failed and needs to be revised.

Current step (JSON): %s
Error: %s
Attempt: %d

Revise the step to avoid this error.
Do NOT change the step ID. Only adjust params or strategy while preserving the step schema.`,
		string(stepJSON), err.Error(), attempt))

	replanCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	resp, llmErr := sh.LLM.StructuredOutput(replanCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		JsonSchema: `{"type":"object","properties":{"id":{"type":"string"},"action":{"type":"string"},"params":{"type":"object"},"risk":{"type":"string"},"dependsOnStepIds":{"type":"array","items":{"type":"string"}}},"required":["id","action"]}`,
		Model:      llm.ResolveStandardModel(),
	}))
	if llmErr != nil {
		if wisdevLLMCallIsCoolingDown(llmErr) {
			slog.Warn("self-heal replan fell back during provider cooldown; keeping original step",
				"component", "wisdev.self_heal",
				"operation", "replan_step",
				"step_id", step.ID,
				"error", llmErr.Error(),
			)
			return &step, nil
		}
		return nil, fmt.Errorf("replan LLM call failed: %w", llmErr)
	}

	var revised PlanStep
	if jsonErr := json.Unmarshal([]byte(resp.JsonResult), &revised); jsonErr != nil {
		slog.Warn("replan: could not parse LLM response, keeping original step", "error", jsonErr)
		return &step, nil
	}
	revised.ID = step.ID // Preserve DAG integrity
	return &revised, nil
}
