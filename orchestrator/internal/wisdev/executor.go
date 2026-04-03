package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/telemetry"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const (
	defaultMaxParallelLanes = 3
	defaultMaxReplans       = 2
	defaultMaxAttempts      = 2
)

type PlanExecutor struct {
	registry         *ToolRegistry
	policyConfig     policy.PolicyConfig
	llmClient        *llm.Client
	brainCaps        *BrainCapabilities
	rdb              redis.UniversalClient
	pythonExecute    func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error)
	adkRuntime       *ADKRuntime
	maxParallelLanes int
	maxReplans       int
}

func NewPlanExecutor(
	registry *ToolRegistry,
	policyConfig policy.PolicyConfig,
	llmClient *llm.Client,
	brainCaps *BrainCapabilities,
	rdb redis.UniversalClient,
	pythonExecute func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error),
	adkRuntime *ADKRuntime,
) *PlanExecutor {
	return &PlanExecutor{
		registry:         registry,
		policyConfig:     policyConfig,
		llmClient:        llmClient,
		brainCaps:        brainCaps,
		rdb:              rdb,
		pythonExecute:    pythonExecute,
		adkRuntime:       adkRuntime,
		maxParallelLanes: defaultMaxParallelLanes,
		maxReplans:       defaultMaxReplans,
	}
}

func dependenciesSatisfied(step PlanStep, completed map[string]bool) bool {
	for _, dependency := range step.DependsOnStepIDs {
		if !completed[dependency] {
			return false
		}
	}
	return true
}

func isStepTerminal(Plan *PlanState, stepID string) bool {
	return Plan.CompletedStepIDs[stepID] || Plan.FailedStepIDs[stepID] != ""
}

func collectReadySteps(Plan *PlanState) []PlanStep {
	ready := make([]PlanStep, 0)
	for _, step := range Plan.Steps {
		if isStepTerminal(Plan, step.ID) {
			continue
		}
		if dependenciesSatisfied(step, Plan.CompletedStepIDs) {
			ready = append(ready, step)
		}
	}
	return ready
}

func allStepsTerminal(Plan *PlanState) bool {
	for _, step := range Plan.Steps {
		if !isStepTerminal(Plan, step.ID) {
			return false
		}
	}
	return true
}

func selectRunnableSteps(ready []PlanStep, laneBudget int) []PlanStep {
	if len(ready) == 0 || laneBudget <= 0 {
		return nil
	}
	parallel := make([]PlanStep, 0, laneBudget)
	sequential := make([]PlanStep, 0, 1)
	for _, step := range ready {
		if step.Parallelizable {
			parallel = append(parallel, step)
			if len(parallel) >= laneBudget {
				break
			}
			continue
		}
		if len(sequential) == 0 {
			sequential = append(sequential, step)
		}
	}
	if len(parallel) > 0 {
		return parallel
	}
	return sequential
}

func degradedQuery(query string) string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return trimmed
	}
	parts := strings.Fields(trimmed)
	if len(parts) <= 6 {
		return trimmed
	}
	return strings.Join(parts[:6], " ")
}

func classifyErrorCode(err error) string {
	if err == nil {
		return ""
	}
	raw := strings.ToLower(err.Error())
	switch {
	case strings.Contains(raw, "timeout") || strings.Contains(raw, "deadline exceeded"):
		return "TOOL_TIMEOUT"
	case strings.Contains(raw, "rate limit") || strings.Contains(raw, "rate_limit") || strings.Contains(raw, "too many requests") || strings.Contains(raw, "429"):
		return "TOOL_RATE_LIMIT"
	case strings.Contains(raw, "invalid argument") || strings.Contains(raw, "schema") || strings.Contains(raw, "validation"):
		return "TOOL_SCHEMA_INVALID"
	case strings.Contains(raw, "guardrail") || strings.Contains(raw, "blocked") || strings.Contains(raw, "policy"):
		return "GUARDRAIL_BLOCKED"
	case strings.Contains(raw, "confirmation") || strings.Contains(raw, "approval"):
		return "CONFIRMATION_REQUIRED"
	case strings.Contains(raw, "not found") || strings.Contains(raw, "unknown action"):
		return "TOOL_NOT_FOUND"
	case strings.Contains(raw, "budget") || strings.Contains(raw, "cost exceeded"):
		return "BUDGET_EXCEEDED"
	case strings.Contains(raw, "unauthorized") || strings.Contains(raw, "forbidden") || strings.Contains(raw, "401") || strings.Contains(raw, "403"):
		return "AUTH_FAILED"
	default:
		return "TOOL_EXEC_FAILED"
	}
}

func (e *PlanExecutor) applyAutomaticReplan(session *AgentSession, failedStepID string, failureReason string) PlanStep {
	session.Plan.ReplanCount++
	replanStep := PlanStep{
		ID:                 fmt.Sprintf("%s_replan_%d", failedStepID, session.Plan.ReplanCount),
		Action:             "research.coordinateReplan",
		Reason:             fmt.Sprintf("Automated replan coordination after %s failed: %s", failedStepID, failureReason),
		Risk:               RiskLevelMedium,
		ModelTier:          ModelTierStandard,
		ExecutionTarget:    ExecutionTargetPythonCapability,
		Parallelizable:     false,
		EstimatedCostCents: 2,
		MaxAttempts:        1,
		Params: map[string]any{
			"failedStepId": failedStepID,
			"reason":       failureReason,
		},
	}
	session.Plan.Steps = append(session.Plan.Steps, replanStep)
	return replanStep
}

func resolveSessionQuery(session *AgentSession) string {
	query := session.CorrectedQuery
	if query == "" {
		query = session.OriginalQuery
	}
	return query
}

func resolveModelForTier(tier ModelTier, budget policy.BudgetState) string {
	if tier == "" {
		tier = ModelTierStandard
	}

	// Budget-Aware Downgrading
	if tier == ModelTierHeavy {
		remaining := budget.MaxCostCents - budget.CostCentsUsed
		if remaining < 50 {
			slog.Warn("Insufficient budget for Heavy model, downgrading to Balanced",
				"remainingCents", remaining,
				"threshold", 50)
			tier = ModelTierStandard
		}
	}

	switch tier {
	case ModelTierHeavy:
		return llm.ResolveHeavyModel()
	case ModelTierStandard:
		return llm.ResolveStandardModel()
	case ModelTierLight:
		return llm.ResolveLightModel()
	default:
		return llm.ResolveStandardModel()
	}
}

func (e *PlanExecutor) executeStepOnce(
	ctx context.Context,
	session *AgentSession,
	step PlanStep,
	degraded bool,
) ([]Source, float64, error) {
	namespace := "wisdev"
	if e.adkRuntime != nil && e.adkRuntime.Config.Telemetry.Namespace != "" {
		namespace = e.adkRuntime.Config.Telemetry.Namespace
	}
	tracer := telemetry.Tracer(fmt.Sprintf("wisdev.%s.executor", namespace))
	ctx, span := tracer.Start(ctx, "wisdev.plan.execute_step")
	
	pluginName := "native"
	if e.adkRuntime != nil {
		if plugin, ok := e.adkRuntime.PluginForAction(step.Action); ok {
			pluginName = plugin.Name
			span.SetAttributes(
				attribute.String("wisdev.adk.framework", e.adkRuntime.Config.Runtime.Framework),
				attribute.String("wisdev.adk.plugin", plugin.Name),
			)
		}
	}

	slog.Info("Executing plan step",
		"stepId", step.ID,
		"action", step.Action,
		"plugin", pluginName,
		"target", string(step.ExecutionTarget),
		"risk", string(step.Risk),
		"degraded", degraded)

	span.SetAttributes(
		attribute.String("wisdev.step.id", step.ID),
		attribute.String("wisdev.step.action", step.Action),
		attribute.String("wisdev.execution_target", string(step.ExecutionTarget)),
		attribute.String("wisdev.risk", string(step.Risk)),
		attribute.Bool("wisdev.degraded", degraded),
	)
	defer span.End()

	guard := policy.EvaluateGuardrail(
		e.policyConfig,
		session.Budget,
		ToPolicyRisk(step.Risk),
		step.ExecutionTarget == ExecutionTargetPythonSandbox,
		step.EstimatedCostCents,
	)
	if !guard.Allowed {
		err := fmt.Errorf("GUARDRAIL_BLOCKED:%s", guard.Reason)
		span.RecordError(err)
		span.SetStatus(codes.Error, guard.Reason)
		return nil, 0, err
	}
	if guard.RequiresConfirmation && !session.Plan.ApprovedStepIDs[step.ID] {
		err := fmt.Errorf("CONFIRMATION_REQUIRED:%s", guard.Reason)
		span.RecordError(err)
		span.SetStatus(codes.Error, guard.Reason)
		return nil, 0, err
	}

	basePayload := map[string]any{
		"sessionId":       session.SessionID,
		"planId":          session.Plan.PlanID,
		"planStepId":      step.ID,
		"planStepAction":  step.Action,
		"degraded":        degraded,
		"ExecutionTarget": string(step.ExecutionTarget),
		"risk":            string(step.Risk),
	}
	if len(step.DependsOnStepIDs) > 0 {
		basePayload["dependsOnStepIds"] = step.DependsOnStepIDs
		if session.Plan != nil && session.Plan.StepConfidences != nil {
			prevConfidences := make(map[string]float64)
			for _, depID := range step.DependsOnStepIDs {
				if conf, ok := session.Plan.StepConfidences[depID]; ok {
					prevConfidences[depID] = conf
				}
			}
			if len(prevConfidences) > 0 {
				basePayload["previous_step_confidences"] = prevConfidences
			}
		}
	}
	if step.ParallelGroup != "" {
		basePayload["parallelGroup"] = step.ParallelGroup
	}

	// Actually resolve the model name based on requested tier and remaining budget.
	modelName := resolveModelForTier(step.ModelTier, session.Budget)

	// Robustness: If heavy model is requested, perform a quick triage with standard model first
	// to decide if heavy is truly needed. This optimizes quota.
	if step.ModelTier == ModelTierHeavy && modelName == llm.ResolveHeavyModel() {
		// Optimization: Cache triage result in session to avoid redundant ~400ms calls
		complexity := session.AssessedComplexity
		if complexity == "" {
			triageCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			var err error
			complexity, err = e.brainCaps.AssessResearchComplexity(triageCtx, resolveSessionQuery(session))
			cancel()
			if err == nil {
				session.AssessedComplexity = complexity
				// We don't eagerly persist here to avoid DB lock contention, 
				// it will be saved with the session at the end of the loop iteration.
			}
		}

		if complexity == "standard" {
			modelName = llm.ResolveStandardModel()
			span.AddEvent("downgraded_to_standard_by_triage")
		}
	}

	basePayload["model"] = modelName
	span.SetAttributes(attribute.String("wisdev.model_resolved", modelName))

	switch step.ExecutionTarget {
	case ExecutionTargetGoNative:
		query := resolveSessionQuery(session)
		basePayload["query"] = query
		basePayload["maxAttempts"] = step.MaxAttempts
		limit := 10
		if degraded {
			query = degradedQuery(query)
			limit = 5
		}
		basePayload["limit"] = limit
		papers, err := FastParallelSearch(ctx, e.rdb, query, limit)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return papers, 0.85, err
		}
		span.SetStatus(codes.Ok, "completed")
		return papers, 0.85, err
	case ExecutionTargetPythonCapability, ExecutionTargetPythonSandbox:
		if step.ExecutionTarget == ExecutionTargetPythonSandbox {
			validation := policy.ValidateSandboxSnippet(step.Action)
			if !validation.Valid {
				err := fmt.Errorf("GUARDRAIL_BLOCKED:%s", validation.Reason)
				span.RecordError(err)
				span.SetStatus(codes.Error, validation.Reason)
				return nil, 0, err
			}
		}
		query := resolveSessionQuery(session)
		if degraded {
			query = degradedQuery(query)
		}
		basePayload["query"] = query

		// Call specialized Python logic via the gateway's executor
		if e.pythonExecute == nil {
			return nil, 0, fmt.Errorf("python executor not configured")
		}

		result, err := e.pythonExecute(ctx, step.Action, basePayload, session)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, 0, err
		}

		// Handle outcome and confidence if returned by Python
		confidence := 0.90
		if c, ok := result["confidence"].(float64); ok {
			confidence = c
		}
		
		// If the result contains papers/sources, extract them
		var sources []Source
		if papers, ok := result["papers"].([]any); ok {
			for _, p := range papers {
				if paperMap, ok := p.(map[string]any); ok {
					sources = append(sources, Source{
						ID:      AsOptionalString(paperMap["id"]),
						Title:   AsOptionalString(paperMap["title"]),
						Summary: AsOptionalString(paperMap["abstract"]),
						Link:    AsOptionalString(paperMap["link"]),
						Source:  AsOptionalString(paperMap["source"]),
					})
				}
			}
		}

		span.SetAttributes(attribute.Int("wisdev.result_count", len(sources)))
		span.SetStatus(codes.Ok, "completed")
		return sources, confidence, nil
	default:
		err := fmt.Errorf("unsupported execution target: %s", step.ExecutionTarget)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, 0, err
	}
}

type StepResult struct {
	Step       PlanStep
	Err        error
	Attempt    int
	Degraded   bool
	LaneID     int
	Sources    []Source
	Confidence float64
}

func (e *PlanExecutor) RunStepWithRecovery(ctx context.Context, session *AgentSession, step PlanStep, laneID int) StepResult {
	maxAttempts := step.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	lastErr := error(nil)
	lastSources := make([]Source, 0)
	var lastConfidence float64
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		degraded := attempt > 1
		sources, conf, err := e.executeStepOnce(ctx, session, step, degraded)
		lastSources = sources
		lastConfidence = conf
		if err == nil {
			return StepResult{Step: step, Attempt: attempt, Degraded: degraded, LaneID: laneID, Sources: sources, Confidence: conf}
		}
		if strings.HasPrefix(err.Error(), "CONFIRMATION_REQUIRED:") {
			return StepResult{Step: step, Err: err, Attempt: attempt, LaneID: laneID, Sources: lastSources, Confidence: conf}
		}
		lastErr = err
	}
	return StepResult{Step: step, Err: lastErr, Attempt: maxAttempts, Degraded: true, LaneID: laneID, Sources: lastSources, Confidence: lastConfidence}
}

func (e *PlanExecutor) CoordinateAgentFeedback(ctx context.Context, session *AgentSession, outcomes []PlanOutcome) (string, error) {
	if e.llmClient == nil {
		return "CONTINUE", nil
	}
	
	prompt := fmt.Sprintf("Mediate between agent outcomes: %v for query %q. Reply with REPLAN if a pivot is needed, else CONTINUE.", outcomes, session.OriginalQuery)
	resp, err := e.llmClient.Generate(ctx, &llmv1.GenerateRequest{
		Prompt: prompt,
		Model:  llm.ResolveStandardModel(),
	})
	if err != nil {
		return "CONTINUE", nil
	}
	return resp.Text, nil
}

func (e *PlanExecutor) Execute(ctx context.Context, session *AgentSession, out chan<- PlanExecutionEvent) {
	defer close(out)
	if session.Plan == nil {
		out <- PlanExecutionEvent{
			Type:      EventStepFailed,
			TraceID:   NewTraceID(),
			SessionID: session.SessionID,
			Message:   "session has no Plan",
			CreatedAt: NowMillis(),
		}
		return
	}
	if session.Plan.StepAttempts == nil {
		session.Plan.StepAttempts = make(map[string]int)
	}
	if session.Plan.StepFailureCount == nil {
		session.Plan.StepFailureCount = make(map[string]int)
	}
	if session.FailureMemory == nil {
		session.FailureMemory = make(map[string]int)
	}

	if err := transitionSessionStatus(session, SessionExecutingPlan); err != nil {
		out <- PlanExecutionEvent{
			Type:      EventStepFailed,
			TraceID:   NewTraceID(),
			SessionID: session.SessionID,
			Message:   err.Error(),
			CreatedAt: NowMillis(),
		}
		return
	}

	for {
		if ctx.Err() != nil {
			out <- PlanExecutionEvent{
				Type:      EventStepFailed,
				TraceID:   NewTraceID(),
				SessionID: session.SessionID,
				PlanID:    session.Plan.PlanID,
				Message:   "execution cancelled",
				CreatedAt: NowMillis(),
			}
			return
		}

		if allStepsTerminal(session.Plan) {
			if len(session.Plan.FailedStepIDs) == 0 {
				_ = transitionSessionStatus(session, SessionComplete)
				out <- PlanExecutionEvent{
					Type:      EventCompleted,
					TraceID:   NewTraceID(),
					SessionID: session.SessionID,
					PlanID:    session.Plan.PlanID,
					Message:   "Plan completed",
					CreatedAt: NowMillis(),
				}
			} else {
				_ = transitionSessionStatus(session, SessionFailed)
				out <- PlanExecutionEvent{
					Type:      EventStepFailed,
					TraceID:   NewTraceID(),
					SessionID: session.SessionID,
					PlanID:    session.Plan.PlanID,
					Message:   "Plan ended with failed steps",
					Payload: map[string]any{
						"failedCount": len(session.Plan.FailedStepIDs),
					},
					CreatedAt: NowMillis(),
				}
			}
			return
		}

		ready := collectReadySteps(session.Plan)
		if len(ready) == 0 {
			if session.Plan.ReplanCount >= e.maxReplans {
				_ = transitionSessionStatus(session, SessionFailed)
				out <- PlanExecutionEvent{
					Type:      EventStepFailed,
					TraceID:   NewTraceID(),
					SessionID: session.SessionID,
					PlanID:    session.Plan.PlanID,
					Message:   "no ready steps and replan budget exhausted",
					CreatedAt: NowMillis(),
				}
				return
			}
			failedID := ""
			failedReason := "dependency_deadlock"
			for _, step := range session.Plan.Steps {
				if reason := session.Plan.FailedStepIDs[step.ID]; reason != "" {
					failedID = step.ID
					failedReason = reason
					break
				}
			}
			if failedID == "" {
				failedID = fmt.Sprintf("deadlock_%d", session.Plan.ReplanCount+1)
			}
			replanned := e.applyAutomaticReplan(session, failedID, failedReason)
			out <- PlanExecutionEvent{
				Type:      EventPlanRevised,
				TraceID:   NewTraceID(),
				SessionID: session.SessionID,
				PlanID:    session.Plan.PlanID,
				StepID:    replanned.ID,
				Message:   replanned.Reason,
				Payload: map[string]any{
					"replanCount": session.Plan.ReplanCount,
					"replanFor":   failedID,
				},
				CreatedAt: NowMillis(),
			}
			continue
		}

		runnable := selectRunnableSteps(ready, e.maxParallelLanes)
		resultsCh := make(chan StepResult, len(runnable))
		var wg sync.WaitGroup
		for idx, step := range runnable {
			laneID := idx + 1
			out <- PlanExecutionEvent{
				Type:      EventStepStarted,
				TraceID:   NewTraceID(),
				SessionID: session.SessionID,
				PlanID:    session.Plan.PlanID,
				StepID:    step.ID,
				Message:   step.Reason,
				Payload: map[string]any{
					"laneId": laneID,
					"action": step.Action,
				},
				CreatedAt: NowMillis(),
			}
			wg.Add(1)
			go func(step PlanStep, laneID int) {
				defer wg.Done()
				resultsCh <- e.RunStepWithRecovery(ctx, session, step, laneID)
			}(step, laneID)
		}
		wg.Wait()
		close(resultsCh)

		emitPaperFoundEvents := func(step PlanStep, laneID int, sources []Source, attempt int, degraded bool) {
			for idx, paper := range sources {
				out <- PlanExecutionEvent{
					Type:      EventPaperFound,
					TraceID:   NewTraceID(),
					SessionID: session.SessionID,
					PlanID:    session.Plan.PlanID,
					StepID:    step.ID,
					Message:   "paper found",
					Payload: map[string]any{
						"paperId":    paper.ID,
						"paperIndex": idx + 1,
						"title":      paper.Title,
						"source":     paper.Source,
						"doi":        paper.DOI,
						"link":       paper.Link,
						"score":      paper.Score,
						"laneId":     laneID,
						"attempt":    attempt,
						"degraded":   degraded,
					},
					CreatedAt: NowMillis(),
				}
			}
		}

		outcomes := make([]PlanOutcome, 0)
		for result := range resultsCh {
			step := result.Step
			session.Plan.StepAttempts[step.ID] += result.Attempt

			if session.Plan.StepConfidences == nil {
				session.Plan.StepConfidences = make(map[string]float64)
			}
			session.Plan.StepConfidences[step.ID] = result.Confidence

			if result.Err == nil {
				emitPaperFoundEvents(step, result.LaneID, result.Sources, result.Attempt, result.Degraded)
				policy.ApplyBudgetUsage(&session.Budget, step.ExecutionTarget == ExecutionTargetPythonSandbox, step.EstimatedCostCents)
				session.Plan.CompletedStepIDs[step.ID] = true
				out <- PlanExecutionEvent{
					Type:      EventStepCompleted,
					TraceID:   NewTraceID(),
					SessionID: session.SessionID,
					PlanID:    session.Plan.PlanID,
					StepID:    step.ID,
					Message:   "step completed",
					Payload: map[string]any{
						"laneId":      result.LaneID,
						"attempts":    result.Attempt,
						"degraded":    result.Degraded,
						"resultCount": len(result.Sources),
					},
					CreatedAt: NowMillis(),
				}
				outcomes = append(outcomes, PlanOutcome{StepID: step.ID, Success: true})
				continue
			}

			errMsg := result.Err.Error()
			if strings.HasPrefix(errMsg, "CONFIRMATION_REQUIRED:") {
				token := fmt.Sprintf("approve_%s_%d", step.ID, time.Now().UnixMilli())
				session.Plan.PendingApprovalID = token
				session.Plan.PendingApprovalStepID = step.ID
				
				timeout := 10 * time.Minute
				if e.adkRuntime != nil {
					timeout = e.adkRuntime.HITLTimeout()
				}
				session.Plan.PendingApprovalExpiresAt = time.Now().Add(timeout).UnixMilli()

				hitlPayload := map[string]any{
					"approvalToken":  token,
					"stepId":         step.ID,
					"expiresAt":      session.Plan.PendingApprovalExpiresAt,
					"action":         step.Action,
					"allowedActions": []string{"approve", "skip", "edit_payload", "reject_replan"},
				}
				if e.adkRuntime != nil {
					hitlPayload = e.adkRuntime.BuildHITLRequest(token, step, strings.TrimPrefix(errMsg, "CONFIRMATION_REQUIRED:"))
					hitlPayload["stepId"] = step.ID
					hitlPayload["expiresAt"] = session.Plan.PendingApprovalExpiresAt
				}
				out <- PlanExecutionEvent{
					Type:      EventConfirmationNeed,
					TraceID:   NewTraceID(),
					SessionID: session.SessionID,
					PlanID:    session.Plan.PlanID,
					StepID:    step.ID,
					Message:   strings.TrimPrefix(errMsg, "CONFIRMATION_REQUIRED:"),
					Payload:   hitlPayload,
					CreatedAt: NowMillis(),
				}
				_ = transitionSessionStatus(session, SessionPaused)
				return
			}

			session.Plan.FailedStepIDs[step.ID] = errMsg
			session.Plan.StepFailureCount[step.ID]++
			session.FailureMemory[step.Action]++
			errorCode := classifyErrorCode(result.Err)
			if len(result.Sources) > 0 {
				emitPaperFoundEvents(step, result.LaneID, result.Sources, result.Attempt, result.Degraded)
			}
			out <- PlanExecutionEvent{
				Type:      EventStepFailed,
				TraceID:   NewTraceID(),
				SessionID: session.SessionID,
				PlanID:    session.Plan.PlanID,
				StepID:    step.ID,
				Message:   errMsg,
				Payload: map[string]any{
					"laneId":       result.LaneID,
					"attempts":     result.Attempt,
					"degraded":     result.Degraded,
					"failureCount": session.Plan.StepFailureCount[step.ID],
					"errorCode":    errorCode,
					"resultCount":  len(result.Sources),
				},
				CreatedAt: NowMillis(),
			}
			outcomes = append(outcomes, PlanOutcome{StepID: step.ID, Success: false, Error: errorCode})
		}

		// Phase 3: Inter-Agent Mediation (Balanced Model)
		if len(outcomes) > 0 {
			decision, err := e.CoordinateAgentFeedback(ctx, session, outcomes)
			if err == nil {
				slog.Info("Inter-agent coordination", "decision", decision)
				if strings.Contains(strings.ToUpper(decision), "REPLAN") && session.Plan.ReplanCount < e.maxReplans {
					replanned := e.applyAutomaticReplan(session, "coordinator", "Dynamic pivot requested by Balanced coordinator")
					out <- PlanExecutionEvent{
						Type:      EventPlanRevised,
						TraceID:   NewTraceID(),
						SessionID: session.SessionID,
						PlanID:    session.Plan.PlanID,
						StepID:    replanned.ID,
						Message:   replanned.Reason,
						Payload: map[string]any{
							"replanCount": session.Plan.ReplanCount,
							"from":        "coordinator",
						},
						CreatedAt: NowMillis(),
					}
				}
			}
		}

		out <- PlanExecutionEvent{
			Type:      EventProgress,
			TraceID:   NewTraceID(),
			SessionID: session.SessionID,
			PlanID:    session.Plan.PlanID,
			Message:   "progress update",
			Payload: map[string]any{
				"completed": len(session.Plan.CompletedStepIDs),
				"failed":    len(session.Plan.FailedStepIDs),
				"total":     len(session.Plan.Steps),
			},
			CreatedAt: NowMillis(),
		}
	}
}
