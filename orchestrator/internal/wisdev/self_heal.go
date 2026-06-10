package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"strings"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
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
	ADKRuntime *ADKRuntime
	MaxRetries int
}

func NewSelfHealer(llm LLMRequester, executor PlanStepRunner) *SelfHealer {
	adkRuntime := (*ADKRuntime)(nil)
	if planExecutor, ok := executor.(*PlanExecutor); ok {
		adkRuntime = planExecutor.adkRuntime
	}
	return &SelfHealer{
		LLM:        llm,
		Executor:   executor,
		ADKRuntime: adkRuntime,
		MaxRetries: 3,
	}
}

func (sh *SelfHealer) WithADKRuntime(runtime *ADKRuntime) *SelfHealer {
	if sh != nil {
		sh.ADKRuntime = runtime
	}
	return sh
}

// Execute runs a plan step and attempts to self-heal on retryable errors.
func (sh *SelfHealer) Execute(ctx context.Context, sessionID string, step PlanStep) (map[string]any, error) {
	if sh != nil && sh.ADKRuntime != nil {
		if result, handled, err := sh.executeWithADKRunner(ctx, sessionID, step); handled {
			return result, err
		}
	}
	return sh.executeDirect(ctx, sessionID, step)
}

func (sh *SelfHealer) executeWithADKRunner(ctx context.Context, sessionID string, step PlanStep) (map[string]any, bool, error) {
	if sh == nil || sh.ADKRuntime == nil {
		return nil, false, nil
	}
	agentName := "wisdev-self-healer"
	selfHealAgent, err := agent.New(agent.Config{
		Name:        agentName,
		Description: "Official ADK wrapper for WisDev self-healing step execution.",
		Run: func(invocation agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				result, runErr := sh.executeDirect(invocation, sessionID, step)
				event := session.NewEvent(invocation.InvocationID())
				event.Author = agentName
				event.Content = genai.NewContentFromText("WisDev self-heal execution completed.", genai.RoleModel)
				event.Actions.StateDelta["selfHealResult"] = ensureExecutionResultMap(result)
				event.Actions.StateDelta["adkRuntime"] = "google.golang.org/adk"
				event.Actions.StateDelta["adkRunnerExecuted"] = true
				event.Actions.StateDelta["adkSubAgent"] = agentName
				if runErr != nil {
					event.Actions.StateDelta["selfHealError"] = runErr.Error()
				}
				event.TurnComplete = true
				yield(event, nil)
			}
		},
	})
	if err != nil {
		slog.Warn("self-heal ADK agent init failed; falling back to direct execution",
			"component", "wisdev.self_heal",
			"operation", "execute",
			"stage", "adk_agent_init_failed",
			"error", err.Error(),
		)
		return nil, false, nil
	}

	appName := strings.TrimSpace(sh.ADKRuntime.Config.Runtime.Name)
	if appName == "" {
		appName = "wisdev-adk"
	}
	selfHealRunner, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             selfHealAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		slog.Warn("self-heal ADK runner init failed; falling back to direct execution",
			"component", "wisdev.self_heal",
			"operation", "execute",
			"stage", "adk_runner_init_failed",
			"error", err.Error(),
		)
		return nil, false, nil
	}

	userID := "wisdev"
	if strings.TrimSpace(sessionID) == "" {
		sessionID = NewTraceID()
	}
	var (
		result       map[string]any
		runErrorText string
		invocationID string
		eventAuthor  string
	)
	for event, runErr := range selfHealRunner.Run(
		executorContext(ctx),
		userID,
		sessionID,
		genai.NewContentFromText(firstNonEmpty(step.Action, step.ID, "self-heal"), genai.RoleUser),
		agent.RunConfig{},
		runner.WithStateDelta(map[string]any{
			"sessionId": sessionID,
			"stepId":    step.ID,
			"action":    step.Action,
		}),
	) {
		if runErr != nil {
			return result, true, runErr
		}
		if event == nil {
			continue
		}
		if invocationID == "" {
			invocationID = event.InvocationID
		}
		if eventAuthor == "" {
			eventAuthor = event.Author
		}
		if mapped, ok := event.Actions.StateDelta["selfHealResult"].(map[string]any); ok {
			result = mapped
		}
		if text := strings.TrimSpace(AsOptionalString(event.Actions.StateDelta["selfHealError"])); text != "" {
			runErrorText = text
		}
	}
	if result == nil {
		result = map[string]any{}
	}
	result["adkRunnerExecuted"] = true
	result["adkInvocationId"] = invocationID
	result["adkEventAuthor"] = eventAuthor
	result["adkRuntime"] = "google.golang.org/adk"
	result["resultOrigin"] = "adk_self_heal"
	result["resultFusionIntent"] = "self_heal_result_fusion"
	if runErrorText != "" {
		return result, true, fmt.Errorf("%s", runErrorText)
	}
	return result, true, nil
}

func (sh *SelfHealer) executeDirect(ctx context.Context, sessionID string, step PlanStep) (map[string]any, error) {
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
