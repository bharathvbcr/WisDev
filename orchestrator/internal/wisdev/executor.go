package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/redis/go-redis/v9"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const (
	defaultMaxParallelLanes = 3
	defaultMaxReplans       = 2
	defaultMaxAttempts      = 2
	executorOwner           = "wisdev-workflow"
	executionOrigin         = "autonomous_loop"
	fusionIntentStepResult  = "result_fusion"
	nativeSubAgent          = "native"
	nativeOwningComponent   = "go_orchestrator"
)

type PlanExecutor struct {
	registry         *ToolRegistry
	policyConfig     policy.PolicyConfig
	llmClient        *llm.Client
	brainCaps        *BrainCapabilities
	rdb              redis.UniversalClient
	searchRegistry   *internalsearch.ProviderRegistry
	pythonExecute    func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error)
	adkRuntime       *ADKRuntime
	maxParallelLanes int
	maxReplans       int
}

type stepExecutionOwnership struct {
	Owner              string
	SubAgent           string
	OwningComponent    string
	ResultOrigin       string
	ResultFusionIntent string
}

func buildStepExecutionOwnership(step PlanStep, adkRuntime *ADKRuntime) stepExecutionOwnership {
	ownership := stepExecutionOwnership{
		Owner:              executorOwner,
		SubAgent:           nativeSubAgent,
		OwningComponent:    nativeOwningComponent,
		ResultOrigin:       executionOrigin,
		ResultFusionIntent: fusionIntentStepResult,
	}
	if adkRuntime != nil {
		if route, ok := adkRuntime.ResolveDelegationRoute(step); ok {
			ownership.Owner = route.Owner
			ownership.SubAgent = route.SubAgent
			ownership.OwningComponent = route.OwningComponent
			ownership.ResultOrigin = route.ResultOrigin
			ownership.ResultFusionIntent = route.ResultFusionIntent
			return ownership
		}
		if plugin, ok := adkRuntime.PluginForAction(step.Action); ok {
			if pluginName := strings.TrimSpace(plugin.Name); pluginName != "" {
				ownership.SubAgent = pluginName
				ownership.OwningComponent = pluginName
			}
		}
	}
	return ownership
}

func resultExecutionOwnership(result StepResult) stepExecutionOwnership {
	ownership := stepExecutionOwnership{
		Owner:              executorOwner,
		SubAgent:           nativeSubAgent,
		OwningComponent:    nativeOwningComponent,
		ResultOrigin:       executionOrigin,
		ResultFusionIntent: fusionIntentStepResult,
	}
	if strings.TrimSpace(result.Owner) != "" {
		ownership.Owner = result.Owner
	}
	if strings.TrimSpace(result.SubAgent) != "" {
		ownership.SubAgent = result.SubAgent
	}
	if strings.TrimSpace(result.OwningComponent) != "" {
		ownership.OwningComponent = result.OwningComponent
	}
	if strings.TrimSpace(result.ResultOrigin) != "" {
		ownership.ResultOrigin = result.ResultOrigin
	}
	if strings.TrimSpace(result.ResultFusionIntent) != "" {
		ownership.ResultFusionIntent = result.ResultFusionIntent
	}
	return ownership
}

func enrichStepExecutionEvent(
	event PlanExecutionEvent,
	ownership stepExecutionOwnership,
	confidence float64,
) PlanExecutionEvent {
	event.Owner = ownership.Owner
	event.SubAgent = ownership.SubAgent
	event.OwningComponent = ownership.OwningComponent
	event.ResultOrigin = ownership.ResultOrigin
	event.ResultConfidence = confidence
	if ownership.ResultFusionIntent != "" {
		event.ResultFusionIntent = ownership.ResultFusionIntent
	}
	return event
}

func NewPlanExecutor(
	registry *ToolRegistry,
	policyConfig policy.PolicyConfig,
	llmClient *llm.Client,
	brainCaps *BrainCapabilities,
	rdb redis.UniversalClient,
	pythonExecute func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error),
	adkRuntime *ADKRuntime,
	searchRegistries ...*internalsearch.ProviderRegistry,
) *PlanExecutor {
	var searchRegistry *internalsearch.ProviderRegistry
	if len(searchRegistries) > 0 {
		searchRegistry = searchRegistries[0]
	}
	return &PlanExecutor{
		registry:         registry,
		policyConfig:     policyConfig,
		llmClient:        llmClient,
		brainCaps:        brainCaps,
		rdb:              rdb,
		searchRegistry:   searchRegistry,
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

func ensurePlanStateMaps(plan *PlanState) {
	if plan == nil {
		return
	}
	if plan.CompletedStepIDs == nil {
		plan.CompletedStepIDs = make(map[string]bool)
	}
	if plan.FailedStepIDs == nil {
		plan.FailedStepIDs = make(map[string]string)
	}
	if plan.StepAttempts == nil {
		plan.StepAttempts = make(map[string]int)
	}
	if plan.StepFailureCount == nil {
		plan.StepFailureCount = make(map[string]int)
	}
	if plan.ApprovedStepIDs == nil {
		plan.ApprovedStepIDs = make(map[string]bool)
	}
	if plan.StepConfidences == nil {
		plan.StepConfidences = make(map[string]float64)
	}
	if plan.StepArtifacts == nil {
		plan.StepArtifacts = make(map[string]StepArtifactSet)
	}
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

func isTerminalAutomaticReplanFailure(failureReason string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(failureReason))
	if normalized == "" {
		return false
	}
	switch normalized {
	case "GUARDRAIL_BLOCKED", "AUTH_FAILED", "TOOL_NOT_FOUND", "BUDGET_EXCEEDED":
		return true
	}
	for _, prefix := range []string{"GUARDRAIL_BLOCKED:", "FATAL:", "UNAUTHORIZED:", "INVALID_INPUT:", "NOT_FOUND:"} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return strings.Contains(normalized, "BUDGET_EXCEEDED")
}

func (e *PlanExecutor) applyAutomaticReplan(session *AgentSession, failedStepID string, failureReason string) PlanStep {
	session.Plan.ReplanCount++
	replanStep := PlanStep{
		ID:                 fmt.Sprintf("%s_replan_%d", failedStepID, session.Plan.ReplanCount),
		Action:             "research.coordinateReplan",
		Reason:             fmt.Sprintf("Automated replan coordination after %s failed: %s", failedStepID, failureReason),
		Risk:               RiskLevelLow,
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
	if session == nil {
		return ""
	}
	return ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery)
}

func resolveModelForTier(tier ModelTier, budget policy.BudgetState) string {
	if tier == "" {
		tier = ModelTierStandard
	}

	// Budget-Aware Downgrading
	if tier == ModelTierHeavy {
		remaining := budget.MaxCostCents - budget.CostCentsUsed
		if remaining < 50 {
			slog.Warn("Insufficient budget for heavy model, downgrading to standard",
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

func cloneStepParams(step PlanStep) map[string]any {
	if len(step.Params) == 0 {
		return map[string]any{}
	}
	payload := cloneAnyMap(step.Params)
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

func extractSourcesFromExecutionResult(result map[string]any) []Source {
	if len(result) == 0 {
		return nil
	}
	if papers := sourcesFromAnyList(firstNonEmptyValue(result["papers"], result["sources"], result["citations"], result["canonicalSources"], result["verifiedRecords"])); len(papers) > 0 {
		return papers
	}
	return nil
}

func ensureExecutionResultMap(result map[string]any) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	return result
}

func executorContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (e *PlanExecutor) executePythonStep(
	ctx context.Context,
	session *AgentSession,
	step PlanStep,
	payload map[string]any,
	degraded bool,
) (map[string]any, []Source, float64, error) {
	if step.ExecutionTarget == ExecutionTargetPythonSandbox {
		validation := policy.ValidateSandboxSnippet(step.Action)
		if !validation.Valid {
			return nil, nil, 0, fmt.Errorf("GUARDRAIL_BLOCKED:%s", validation.Reason)
		}
	}
	if degraded {
		payload["query"] = degradedQuery(AsOptionalString(payload["query"]))
	}
	if e.pythonExecute == nil {
		return nil, nil, 0, fmt.Errorf("python executor not configured")
	}

	result, err := e.pythonExecute(ctx, step.Action, payload, session)
	if err != nil {
		return nil, nil, 0, err
	}
	result = ensureExecutionResultMap(result)

	confidence := 0.90
	if c, ok := result["confidence"].(float64); ok {
		confidence = c
	}
	return result, extractSourcesFromExecutionResult(result), confidence, nil
}

func (e *PlanExecutor) executeDelegatedStep(
	ctx context.Context,
	session *AgentSession,
	step PlanStep,
	payload map[string]any,
	degraded bool,
	route adkDelegationRoute,
) (map[string]any, []Source, float64, error) {
	payload["delegated"] = true
	payload["delegatedPlugin"] = route.Plugin
	payload["delegatedSubAgent"] = route.SubAgent
	payload["delegatedOwner"] = route.Owner
	payload["delegatedOwningComponent"] = route.OwningComponent
	payload["resultOrigin"] = route.ResultOrigin
	payload["resultFusionIntent"] = route.ResultFusionIntent

	if e.adkRuntime != nil {
		result, handled, err := e.adkRuntime.ExecuteDelegatedAction(ctx, route, step, payload, session)
		if handled {
			confidence := firstNonEmptyFloat(ClampFloat(AsFloat(result["confidence"]), 0, 1), 0.88)
			return ensureExecutionResultMap(result), extractSourcesFromExecutionResult(result), confidence, err
		}
	}

	switch step.ExecutionTarget {
	case ExecutionTargetGoNative:
		if _, ok := payload["limit"]; !ok {
			payload["limit"] = 10
		}
		payload["maxAttempts"] = step.MaxAttempts
		if degraded {
			payload["query"] = degradedQuery(AsOptionalString(payload["query"]))
			payload["limit"] = MinInt(intFromAny(payload["limit"]), 5)
		}
		return e.executeGoNativeStep(ctx, session, step.Action, payload, degraded)
	case ExecutionTargetPythonCapability, ExecutionTargetPythonSandbox:
		return e.executePythonStep(ctx, session, step, payload, degraded)
	default:
		return nil, nil, 0, fmt.Errorf("unsupported delegated execution target: %s", step.ExecutionTarget)
	}
}

func (e *PlanExecutor) executeGoNativeStep(
	ctx context.Context,
	session *AgentSession,
	action string,
	payload map[string]any,
	degraded bool,
) (map[string]any, []Source, float64, error) {
	switch CanonicalizeWisdevAction(action) {
	case ActionResearchRetrievePapers, "search":
		query, opts := resolveRetrievePapersSearchOptions(payload, session, degraded)
		opts.Registry = e.searchRegistry
		papers, result, err := runRetrievePapers(ctx, e.rdb, query, opts)
		return ensureExecutionResultMap(result), papers, 0.85, err
	case ActionResearchFullPaperRetrieve:
		result, papers, err := executeFullPaperRetrieveAction(ctx, e.rdb, session, payload, degraded)
		return ensureExecutionResultMap(result), papers, 0.85, err
	case ActionResearchFullPaperGatewayDispatch:
		result, papers, err := executeFullPaperGatewayDispatchAction(ctx, e.rdb, session, payload, degraded)
		return ensureExecutionResultMap(result), papers, 0.85, err
	case ActionResearchVerifyCitations:
		if e.brainCaps == nil {
			return nil, nil, 0, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		papers := sourcesFromAnyList(firstNonEmptyValue(payload["papers"], payload["citations"]))
		result, err := e.brainCaps.VerifyCitationsInteractive(ctx, papers, AsOptionalString(payload["model"]))
		return ensureExecutionResultMap(result), extractSourcesFromExecutionResult(result), 0.9, err
	case ActionResearchResolveCanonicalCitations:
		if e.brainCaps == nil {
			return nil, nil, 0, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		papers := sourcesFromAnyList(firstNonEmptyValue(payload["papers"], payload["citations"], payload["canonicalSources"]))
		result, err := e.brainCaps.ResolveCanonicalCitations(ctx, papers, AsOptionalString(payload["model"]))
		return ensureExecutionResultMap(result), extractSourcesFromExecutionResult(result), 0.9, err
	case ActionResearchVerifyReasoningPaths:
		if e.brainCaps == nil {
			return nil, nil, 0, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		result, err := e.brainCaps.VerifyReasoningPaths(ctx, firstArtifactMaps(payload["branches"]), AsOptionalString(payload["model"]))
		return ensureExecutionResultMap(result), nil, 0.92, err
	case ActionResearchVerifyClaimsBatch:
		if e.brainCaps == nil {
			return nil, nil, 0, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		outputs := firstArtifactMaps(firstNonEmptyValue(payload["candidateOutputs"], payload["outputs"], payload["claims"]))
		papers := sourcesFromAnyList(firstNonEmptyValue(payload["papers"], payload["sources"], payload["evidence"]))
		result, err := e.brainCaps.VerifyClaimsBatchInteractive(ctx, outputs, papers, AsOptionalString(payload["model"]))
		return ensureExecutionResultMap(result), papers, 0.9, err
	case ActionResearchSynthesizeAnswer:
		if e.brainCaps == nil {
			return nil, nil, 0, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		papers := sourcesFromAnyList(firstNonEmptyValue(payload["papers"], payload["sources"], payload["evidence"]))
		answer, err := e.brainCaps.SynthesizeAnswer(ctx, AsOptionalString(payload["query"]), papers, AsOptionalString(payload["model"]))
		if err != nil {
			return nil, nil, 0, err
		}
		return map[string]any{
			"text":             answer.PlainText,
			"structuredAnswer": answer,
		}, papers, 0.9, nil
	default:
		return nil, nil, 0, fmt.Errorf("unsupported Go-native WisDev action: %s", CanonicalizeWisdevAction(action))
	}
}

func (e *PlanExecutor) executeStepOnce(
	ctx context.Context,
	session *AgentSession,
	step PlanStep,
	degraded bool,
) (map[string]any, []Source, float64, error) {
	if session == nil {
		return nil, nil, 0, fmt.Errorf("session is nil")
	}
	ctx = executorContext(ctx)
	ownership := buildStepExecutionOwnership(step, e.adkRuntime)
	delegationRoute, delegated := adkDelegationRoute{}, false
	if e.adkRuntime != nil {
		delegationRoute, delegated = e.adkRuntime.ResolveDelegationRoute(step)
	}
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
		"owner", ownership.Owner,
		"subAgent", ownership.SubAgent,
		"resultOrigin", ownership.ResultOrigin,
		"target", string(step.ExecutionTarget),
		"risk", string(step.Risk),
		"degraded", degraded)

	span.SetAttributes(
		attribute.String("wisdev.step.id", step.ID),
		attribute.String("wisdev.step.action", step.Action),
		attribute.String("wisdev.step.owner", ownership.Owner),
		attribute.String("wisdev.step.sub_agent", ownership.SubAgent),
		attribute.String("wisdev.result_origin", ownership.ResultOrigin),
		attribute.String("wisdev.execution_target", string(step.ExecutionTarget)),
		attribute.String("wisdev.risk", string(step.Risk)),
		attribute.Bool("wisdev.degraded", degraded),
	)
	if delegated {
		span.SetAttributes(
			attribute.Bool("wisdev.adk.delegated", true),
			attribute.String("wisdev.adk.sub_agent", delegationRoute.SubAgent),
			attribute.String("wisdev.result_origin", delegationRoute.ResultOrigin),
		)
	}
	defer span.End()

	if reason, exceeded := stepExecutionBudgetExceeded(step, session.Budget); exceeded {
		err := fmt.Errorf("GUARDRAIL_BLOCKED:%s", reason)
		span.RecordError(err)
		span.SetStatus(codes.Error, reason)
		return nil, nil, 0, err
	}

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
		return nil, nil, 0, err
	}
	approved := false
	if session.Plan != nil && session.Plan.ApprovedStepIDs != nil {
		approved = session.Plan.ApprovedStepIDs[step.ID]
	}
	if guard.RequiresConfirmation && !approved {
		err := fmt.Errorf("CONFIRMATION_REQUIRED:%s", guard.Reason)
		span.RecordError(err)
		span.SetStatus(codes.Error, guard.Reason)
		return nil, nil, 0, err
	}
	if step.RequiresHumanCheckpoint && !approved {
		err := fmt.Errorf("CONFIRMATION_REQUIRED:human checkpoint required")
		span.RecordError(err)
		span.SetStatus(codes.Error, "human checkpoint required")
		return nil, nil, 0, err
	}

	basePayload := cloneStepParams(step)
	basePayload["sessionId"] = session.SessionID
	planID := ""
	if session.Plan != nil {
		planID = session.Plan.PlanID
	}
	basePayload["planId"] = planID
	basePayload["planStepId"] = step.ID
	basePayload["planStepAction"] = step.Action
	basePayload["degraded"] = degraded
	basePayload["ExecutionTarget"] = string(step.ExecutionTarget)
	basePayload["risk"] = string(step.Risk)
	basePayload["requiresHumanCheckpoint"] = step.RequiresHumanCheckpoint
	basePayload["verificationRequired"] = step.RequiresHumanCheckpoint
	basePayload["executionMode"] = string(session.Mode)
	basePayload["operationMode"] = string(session.Mode)
	basePayload["serviceTier"] = string(session.ServiceTier)
	basePayload["modeManifest"] = BuildModeManifestMap(session.Mode, session.ServiceTier)
	basePayload["academicIntegrity"] = map[string]any{
		"requireCanonicalBibliography": true,
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
	injectStepArtifacts(session, step, basePayload)

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

		if complexity == "low" || complexity == "medium" {
			modelName = llm.ResolveStandardModel()
			span.AddEvent("downgraded_to_standard_by_triage")
		}
	}

	basePayload["model"] = modelName
	span.SetAttributes(attribute.String("wisdev.model_resolved", modelName))
	resolvedQuery := resolveSessionQuery(session)
	if strings.TrimSpace(AsOptionalString(basePayload["query"])) == "" {
		basePayload["query"] = resolvedQuery
	}
	telemetry.FromCtx(ctx).InfoContext(ctx, "wisdev agent query handoff",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "wisdev.executor",
		"operation", "plan_step_handoff",
		"stage", "agent_query_handoff",
		"session_id", session.SessionID,
		"step_id", step.ID,
		"action", step.Action,
		"query_preview", QueryPreview(resolvedQuery),
		"query_length", len(resolvedQuery),
		"degraded", degraded,
	)

	if delegated {
		result, papers, confidence, err := e.executeDelegatedStep(ctx, session, step, basePayload, degraded, delegationRoute)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return result, papers, confidence, err
		}
		span.SetAttributes(attribute.Int("wisdev.result_count", len(papers)))
		span.SetStatus(codes.Ok, "completed")
		return result, papers, confidence, nil
	}

	switch step.ExecutionTarget {
	case ExecutionTargetGoNative:
		basePayload["maxAttempts"] = step.MaxAttempts
		if _, ok := basePayload["limit"]; !ok {
			basePayload["limit"] = 10
		}
		if degraded {
			basePayload["query"] = degradedQuery(AsOptionalString(basePayload["query"]))
			basePayload["limit"] = MinInt(intFromAny(basePayload["limit"]), 5)
		}
		result, papers, confidence, err := e.executeGoNativeStep(ctx, session, step.Action, basePayload, degraded)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return result, papers, confidence, err
		}
		span.SetAttributes(attribute.Int("wisdev.result_count", len(papers)))
		span.SetStatus(codes.Ok, "completed")
		return result, papers, confidence, nil
	case ExecutionTargetPythonCapability, ExecutionTargetPythonSandbox:
		result, sources, confidence, err := e.executePythonStep(ctx, session, step, basePayload, degraded)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, nil, 0, err
		}
		span.SetAttributes(attribute.Int("wisdev.result_count", len(sources)))
		span.SetStatus(codes.Ok, "completed")
		return result, sources, confidence, nil
	default:
		err := fmt.Errorf("unsupported execution target: %s", step.ExecutionTarget)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil, 0, err
	}
}

type StepResult struct {
	Step               PlanStep
	Err                error
	Attempt            int
	Degraded           bool
	LaneID             int
	Payload            map[string]any
	Sources            []Source
	Confidence         float64
	Owner              string
	SubAgent           string
	OwningComponent    string
	ResultOrigin       string
	ResultFusionIntent string
}

func stepExecutionBudgetExceeded(step PlanStep, budget policy.BudgetState) (string, bool) {
	if budget.ToolCallsUsed >= budget.MaxToolCalls {
		return "tool_budget_exceeded", true
	}
	if step.ExecutionTarget == ExecutionTargetPythonSandbox && budget.ScriptRunsUsed >= budget.MaxScriptRuns {
		return "script_budget_exceeded", true
	}
	if step.EstimatedCostCents > 0 && budget.CostCentsUsed+step.EstimatedCostCents > budget.MaxCostCents {
		return "cost_budget_exceeded", true
	}
	return "", false
}

func (e *PlanExecutor) RunStepWithRecovery(ctx context.Context, session *AgentSession, step PlanStep, laneID int) StepResult {
	ctx = executorContext(ctx)
	maxAttempts := step.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	ownership := buildStepExecutionOwnership(step, e.adkRuntime)
	lastErr := error(nil)
	lastPayload := map[string]any{}
	lastSources := make([]Source, 0)
	var lastConfidence float64
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		degraded := attempt > 1
		payload, sources, conf, err := func() (payload map[string]any, sources []Source, conf float64, err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error("wisdev executor step panic recovered",
						"component", "wisdev.executor",
						"operation", "run_step",
						"stepId", step.ID,
						"action", step.Action,
						"laneId", laneID,
						"attempt", attempt,
						"degraded", degraded,
						"panic", fmt.Sprint(recovered),
						"stack", string(debug.Stack()),
					)
					err = fmt.Errorf("step panic recovered: %v", recovered)
				}
			}()
			return e.executeStepOnce(ctx, session, step, degraded)
		}()
		lastPayload = payload
		lastSources = sources
		lastConfidence = conf
		if err == nil {
			return StepResult{
				Step:               step,
				Attempt:            attempt,
				Degraded:           degraded,
				LaneID:             laneID,
				Payload:            payload,
				Sources:            sources,
				Confidence:         conf,
				Owner:              ownership.Owner,
				SubAgent:           ownership.SubAgent,
				OwningComponent:    ownership.OwningComponent,
				ResultOrigin:       ownership.ResultOrigin,
				ResultFusionIntent: ownership.ResultFusionIntent,
			}
		}
		if strings.HasPrefix(err.Error(), "CONFIRMATION_REQUIRED:") {
			return StepResult{
				Step:               step,
				Err:                err,
				Attempt:            attempt,
				LaneID:             laneID,
				Payload:            lastPayload,
				Sources:            lastSources,
				Confidence:         conf,
				Owner:              ownership.Owner,
				SubAgent:           ownership.SubAgent,
				OwningComponent:    ownership.OwningComponent,
				ResultOrigin:       ownership.ResultOrigin,
				ResultFusionIntent: ownership.ResultFusionIntent,
			}
		}
		lastErr = err
	}
	return StepResult{
		Step:               step,
		Err:                lastErr,
		Attempt:            maxAttempts,
		Degraded:           true,
		LaneID:             laneID,
		Payload:            lastPayload,
		Sources:            lastSources,
		Confidence:         lastConfidence,
		Owner:              ownership.Owner,
		SubAgent:           ownership.SubAgent,
		OwningComponent:    ownership.OwningComponent,
		ResultOrigin:       ownership.ResultOrigin,
		ResultFusionIntent: ownership.ResultFusionIntent,
	}
}

func (e *PlanExecutor) CoordinateAgentFeedback(ctx context.Context, session *AgentSession, outcomes []PlanOutcome) (string, error) {
	if !requiresAgentFeedbackMediation(outcomes) {
		return "CONTINUE", nil
	}
	if e.llmClient == nil {
		return "CONTINUE", nil
	}
	if remaining := e.llmClient.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("agent feedback mediation skipped during provider cooldown",
			"component", "wisdev.executor",
			"operation", "coordinate_agent_feedback",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
			"outcomeCount", len(outcomes),
		)
		return "CONTINUE", nil
	}

	outcomeJSON, err := json.Marshal(outcomes)
	if err != nil {
		outcomeJSON = []byte(fmt.Sprintf("%v", outcomes))
	}
	prompt := fmt.Sprintf(
		"Mediate between agent outcomes for query %q. Outcomes JSON: %s. Reply with REPLAN only if a failed, conflicting, or clearly insufficient outcome requires a plan pivot; otherwise reply CONTINUE.",
		resolveSessionQuery(session),
		string(outcomeJSON),
	)
	resp, err := e.llmClient.Generate(ctx, applyWisdevStandardGeneratePolicy(&llmv1.GenerateRequest{
		Prompt: prompt,
		Model:  llm.ResolveStandardModel(),
	}))
	if err != nil {
		if wisdevLLMCallIsCoolingDown(err) {
			slog.Warn("agent feedback mediation fell back during provider cooldown",
				"component", "wisdev.executor",
				"operation", "coordinate_agent_feedback",
				"stage", "rate_limit_fallback",
				"error", err.Error(),
				"outcomeCount", len(outcomes),
			)
		}
		return "CONTINUE", nil
	}
	text, err := normalizeWisdevGeneratedText("agent feedback coordination", resp)
	if err != nil {
		return "CONTINUE", nil
	}
	return text, nil
}

func requiresAgentFeedbackMediation(outcomes []PlanOutcome) bool {
	if len(outcomes) == 0 {
		return false
	}
	if len(outcomes) > 1 {
		return true
	}
	outcome := outcomes[0]
	if !outcome.Success || strings.TrimSpace(outcome.Error) != "" {
		return true
	}
	if outcome.Success && outcome.ResultCount == 0 && outcomeRequiresGroundedResults(outcome.Action) {
		return true
	}
	return false
}

func shouldApplyCoordinatorReplan(outcomes []PlanOutcome, decision string) bool {
	if !strings.Contains(strings.ToUpper(decision), "REPLAN") {
		return false
	}
	for _, outcome := range outcomes {
		if !outcome.Success && isTerminalAutomaticReplanFailure(outcome.Error) {
			return false
		}
		if !outcome.Success || strings.TrimSpace(outcome.Error) != "" {
			return true
		}
		if outcome.Success && outcome.ResultCount == 0 && outcomeRequiresGroundedResults(outcome.Action) {
			return true
		}
	}
	return false
}

func outcomeRequiresGroundedResults(action string) bool {
	switch CanonicalizeWisdevAction(action) {
	case ActionResearchRetrievePapers, ActionResearchResolveCanonicalCitations, ActionResearchVerifyCitations:
		return true
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "search", "parallel_search", "retrieve", "retrieve_papers":
		return true
	default:
		return false
	}
}

func buildPlanOutcomeSummary(step PlanStep, result StepResult, artifactSet StepArtifactSet, artifactErr error) string {
	parts := make([]string, 0, 6)
	if reason := strings.TrimSpace(step.Reason); reason != "" {
		parts = append(parts, truncateOutcomeText(reason, 180))
	}
	if summary := firstOutcomeText(result.Payload, "summary", "message", "result", "answer", "raw_output"); summary != "" {
		parts = append(parts, truncateOutcomeText(summary, 240))
	}
	if len(result.Sources) > 0 {
		titles := make([]string, 0, MinInt(len(result.Sources), 3))
		for _, source := range result.Sources {
			if title := strings.TrimSpace(source.Title); title != "" {
				titles = append(titles, truncateOutcomeText(title, 90))
			}
			if len(titles) >= 3 {
				break
			}
		}
		if len(titles) > 0 {
			parts = append(parts, fmt.Sprintf("top_sources=%s", strings.Join(titles, "; ")))
		}
	}
	if len(artifactSet.Artifacts) > 0 {
		parts = append(parts, fmt.Sprintf("artifact_keys=%s", strings.Join(artifactKeys(artifactSet), ",")))
	}
	if artifactErr != nil {
		parts = append(parts, "artifact_normalization_degraded")
	}
	if len(parts) == 0 {
		return "step completed"
	}
	return strings.Join(parts, " | ")
}

func firstOutcomeText(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := strings.TrimSpace(AsOptionalString(payload[key])); text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

func truncateOutcomeText(text string, maxLen int) string {
	text = strings.Join(strings.Fields(text), " ")
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	return strings.TrimSpace(text[:maxLen]) + "..."
}

func (e *PlanExecutor) Execute(ctx context.Context, session *AgentSession, out chan<- PlanExecutionEvent) {
	if out == nil {
		return
	}
	ctx = executorContext(ctx)
	defer close(out)
	defer func() {
		if recovered := recover(); recovered != nil {
			if session != nil {
				_ = transitionSessionStatus(session, SessionFailed)
			}
			out <- PlanExecutionEvent{
				Type:      EventStepFailed,
				TraceID:   NewTraceID(),
				SessionID: safeSessionID(session),
				PlanID:    safePlanID(session),
				Message:   fmt.Sprintf("executor panic recovered: %v", recovered),
				Payload: map[string]any{
					"errorCode": "EXECUTOR_PANIC",
				},
				CreatedAt: NowMillis(),
			}
			slog.Error("wisdev executor panic recovered",
				"component", "wisdev.executor",
				"operation", "execute",
				"sessionId", safeSessionID(session),
				"planId", safePlanID(session),
				"panic", fmt.Sprint(recovered),
				"stack", string(debug.Stack()),
			)
		}
	}()
	if session == nil {
		out <- PlanExecutionEvent{
			Type:      EventStepFailed,
			TraceID:   NewTraceID(),
			Message:   "session is nil",
			CreatedAt: NowMillis(),
		}
		return
	}
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
	ensurePlanStateMaps(session.Plan)
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
			out <- enrichStepExecutionEvent(PlanExecutionEvent{
				Type:      EventStepFailed,
				TraceID:   NewTraceID(),
				SessionID: session.SessionID,
				PlanID:    session.Plan.PlanID,
				Message:   "execution cancelled",
				CreatedAt: NowMillis(),
			}, stepExecutionOwnership{
				Owner:              executorOwner,
				SubAgent:           nativeSubAgent,
				OwningComponent:    nativeOwningComponent,
				ResultOrigin:       executionOrigin,
				ResultFusionIntent: fusionIntentStepResult,
			}, 0)
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
			if failedID != "" && isTerminalAutomaticReplanFailure(failedReason) {
				_ = transitionSessionStatus(session, SessionFailed)
				out <- PlanExecutionEvent{
					Type:      EventStepFailed,
					TraceID:   NewTraceID(),
					SessionID: session.SessionID,
					PlanID:    session.Plan.PlanID,
					StepID:    failedID,
					Message:   fmt.Sprintf("no ready steps after terminal failure: %s", failedReason),
					Payload: map[string]any{
						"failedStepId": failedID,
						"errorCode":    classifyErrorCode(fmt.Errorf("%s", failedReason)),
					},
					CreatedAt: NowMillis(),
				}
				return
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
			ownership := buildStepExecutionOwnership(step, e.adkRuntime)
			out <- enrichStepExecutionEvent(PlanExecutionEvent{
				Type:      EventStepStarted,
				TraceID:   NewTraceID(),
				SessionID: session.SessionID,
				PlanID:    session.Plan.PlanID,
				StepID:    step.ID,
				Message:   step.Reason,
				Payload: map[string]any{
					"laneId":       laneID,
					"action":       step.Action,
					"delegated":    ownership.ResultOrigin != executionOrigin,
					"subAgent":     ownership.SubAgent,
					"resultOrigin": ownership.ResultOrigin,
				},
				CreatedAt: NowMillis(),
			}, ownership, 0)
			wg.Add(1)
			go func(step PlanStep, laneID int) {
				defer wg.Done()
				resultsCh <- e.RunStepWithRecovery(ctx, session, step, laneID)
			}(step, laneID)
		}
		wg.Wait()
		close(resultsCh)

		emitPaperFoundEvents := func(result StepResult) {
			step := result.Step
			ownership := resultExecutionOwnership(result)
			for idx, paper := range result.Sources {
				out <- enrichStepExecutionEvent(PlanExecutionEvent{
					Type:      EventPaperFound,
					TraceID:   NewTraceID(),
					SessionID: session.SessionID,
					PlanID:    session.Plan.PlanID,
					StepID:    step.ID,
					Message:   "paper found",
					Payload: map[string]any{
						"paperId":      paper.ID,
						"paperIndex":   idx + 1,
						"title":        paper.Title,
						"source":       paper.Source,
						"doi":          paper.DOI,
						"link":         paper.Link,
						"score":        paper.Score,
						"laneId":       result.LaneID,
						"attempt":      result.Attempt,
						"degraded":     result.Degraded,
						"delegated":    ownership.ResultOrigin != executionOrigin,
						"fusionIntent": ownership.ResultFusionIntent,
					},
					CreatedAt: NowMillis(),
				}, ownership, result.Confidence)
			}
		}

		outcomes := make([]PlanOutcome, 0)
		if session.Plan.StepAttempts == nil {
			session.Plan.StepAttempts = map[string]int{}
		}
		if session.Plan.StepConfidences == nil {
			session.Plan.StepConfidences = map[string]float64{}
		}
		if session.Plan.CompletedStepIDs == nil {
			session.Plan.CompletedStepIDs = map[string]bool{}
		}
		if session.Plan.FailedStepIDs == nil {
			session.Plan.FailedStepIDs = map[string]string{}
		}
		if session.Plan.StepFailureCount == nil {
			session.Plan.StepFailureCount = map[string]int{}
		}
		if session.FailureMemory == nil {
			session.FailureMemory = map[string]int{}
		}

		for result := range resultsCh {
			step := result.Step
			session.Plan.StepAttempts[step.ID] += result.Attempt

			session.Plan.StepConfidences[step.ID] = result.Confidence

			if result.Err == nil {
				emitPaperFoundEvents(result)
				policy.ApplyBudgetUsage(&session.Budget, step.ExecutionTarget == ExecutionTargetPythonSandbox, step.EstimatedCostCents)
				session.Plan.CompletedStepIDs[step.ID] = true
				artifactSet, artifactErr := normalizeStepArtifacts(step, result.Payload, result.Sources)
				if artifactErr != nil {
					slog.Warn("wisdev artifact normalization degraded",
						"component", "wisdev.executor",
						"operation", "normalize_step_artifacts",
						"stage", AsOptionalString(artifactSet.Artifacts["artifactNormalizationStage"]),
						"stepId", step.ID,
						"action", step.Action,
						"error", artifactErr.Error(),
					)
				}
				persistStepArtifacts(session, step, artifactSet)
				RefreshSessionArchitectureFromArtifacts(session, artifactSet, result.Sources)
				eventPayload := map[string]any{
					"laneId":          result.LaneID,
					"attempts":        result.Attempt,
					"degraded":        result.Degraded || artifactErr != nil,
					"resultCount":     len(result.Sources),
					"artifactKeys":    stringSliceToAny(artifactKeys(artifactSet)),
					"delegated":       result.ResultOrigin != executionOrigin,
					"resultOrigin":    result.ResultOrigin,
					"fusionIntent":    result.ResultFusionIntent,
					"owningComponent": result.OwningComponent,
				}
				if artifactErr != nil {
					eventPayload["artifactNormalizationError"] = artifactErr.Error()
					if stage := AsOptionalString(artifactSet.Artifacts["artifactNormalizationStage"]); stage != "" {
						eventPayload["artifactNormalizationStage"] = stage
					}
					if code := AsOptionalString(artifactSet.Artifacts["artifactNormalizationErrorCode"]); code != "" {
						eventPayload["artifactNormalizationErrorCode"] = code
					}
				}
				slog.Info("wisdev step completed",
					"service", "go_orchestrator",
					"runtime", "go",
					"component", "wisdev.executor",
					"operation", "execute",
					"stage", "step_completed",
					"session_id", session.SessionID,
					"plan_id", session.Plan.PlanID,
					"step_id", step.ID,
					"action", step.Action,
					"lane_id", result.LaneID,
					"attempts", result.Attempt,
					"result_count", len(result.Sources),
					"degraded", result.Degraded || artifactErr != nil,
					"delegated", result.ResultOrigin != executionOrigin,
					"result_origin", result.ResultOrigin,
					"fusion_intent", result.ResultFusionIntent,
					"owning_component", result.OwningComponent,
					"confidence", result.Confidence,
				)
				out <- enrichStepExecutionEvent(PlanExecutionEvent{
					Type:      EventStepCompleted,
					TraceID:   NewTraceID(),
					SessionID: session.SessionID,
					PlanID:    session.Plan.PlanID,
					StepID:    step.ID,
					Message:   "step completed",
					Payload:   eventPayload,
					CreatedAt: NowMillis(),
				}, resultExecutionOwnership(result), result.Confidence)
				outcomes = append(outcomes, PlanOutcome{
					StepID:       step.ID,
					Action:       step.Action,
					Success:      true,
					Summary:      buildPlanOutcomeSummary(step, result, artifactSet, artifactErr),
					ResultCount:  len(result.Sources),
					ArtifactKeys: artifactKeys(artifactSet),
					Confidence:   result.Confidence,
					Degraded:     result.Degraded || artifactErr != nil,
					ResultOrigin: result.ResultOrigin,
					SubAgent:     result.SubAgent,
				})
				continue
			}

			errMsg := result.Err.Error()
			if strings.HasPrefix(errMsg, "CONFIRMATION_REQUIRED:") {
				token, tokenHash, tokenErr := NewApprovalToken()
				if tokenErr != nil {
					token = fmt.Sprintf("approve_%s_%d", step.ID, time.Now().UnixMilli())
					tokenHash = HashApprovalToken(token)
				}
				session.Plan.PendingApprovalID = NewTraceID()
				session.Plan.PendingApprovalTokenHash = tokenHash
				session.Plan.PendingApprovalStepID = step.ID

				timeout := 10 * time.Minute
				if e.adkRuntime != nil {
					timeout = e.adkRuntime.HITLTimeout()
				}
				session.Plan.PendingApprovalExpiresAt = time.Now().Add(timeout).UnixMilli()

				hitlPayload := map[string]any{
					"approvalId":     session.Plan.PendingApprovalID,
					"approvalToken":  token,
					"stepId":         step.ID,
					"expiresAt":      session.Plan.PendingApprovalExpiresAt,
					"action":         step.Action,
					"allowedActions": []string{"approve", "skip", "edit_payload", "reject_replan"},
				}
				if e.adkRuntime != nil {
					hitlPayload = e.adkRuntime.BuildHITLRequest(token, step, strings.TrimPrefix(errMsg, "CONFIRMATION_REQUIRED:"))
					hitlPayload["approvalId"] = session.Plan.PendingApprovalID
					hitlPayload["stepId"] = step.ID
					hitlPayload["expiresAt"] = session.Plan.PendingApprovalExpiresAt
				}
				out <- enrichStepExecutionEvent(PlanExecutionEvent{
					Type:      EventConfirmationNeed,
					TraceID:   NewTraceID(),
					SessionID: session.SessionID,
					PlanID:    session.Plan.PlanID,
					StepID:    step.ID,
					Message:   strings.TrimPrefix(errMsg, "CONFIRMATION_REQUIRED:"),
					Payload:   hitlPayload,
					CreatedAt: NowMillis(),
				}, resultExecutionOwnership(result), result.Confidence)
				_ = transitionSessionStatus(session, SessionPaused)
				return
			}

			session.Plan.FailedStepIDs[step.ID] = errMsg
			session.Plan.StepFailureCount[step.ID]++
			session.FailureMemory[step.Action]++
			errorCode := classifyErrorCode(result.Err)
			if len(result.Sources) > 0 {
				emitPaperFoundEvents(result)
			}
			slog.Warn("wisdev step failed",
				"service", "go_orchestrator",
				"runtime", "go",
				"component", "wisdev.executor",
				"operation", "execute",
				"stage", "step_failed",
				"session_id", session.SessionID,
				"plan_id", session.Plan.PlanID,
				"step_id", step.ID,
				"action", step.Action,
				"lane_id", result.LaneID,
				"attempts", result.Attempt,
				"result_count", len(result.Sources),
				"degraded", result.Degraded,
				"error_code", errorCode,
				"error", errMsg,
				"delegated", result.ResultOrigin != executionOrigin,
				"result_origin", result.ResultOrigin,
				"fusion_intent", result.ResultFusionIntent,
				"owning_component", result.OwningComponent,
				"failure_count", session.Plan.StepFailureCount[step.ID],
			)
			out <- enrichStepExecutionEvent(PlanExecutionEvent{
				Type:      EventStepFailed,
				TraceID:   NewTraceID(),
				SessionID: session.SessionID,
				PlanID:    session.Plan.PlanID,
				StepID:    step.ID,
				Message:   errMsg,
				Payload: map[string]any{
					"laneId":          result.LaneID,
					"attempts":        result.Attempt,
					"degraded":        result.Degraded,
					"failureCount":    session.Plan.StepFailureCount[step.ID],
					"errorCode":       errorCode,
					"resultCount":     len(result.Sources),
					"delegated":       result.ResultOrigin != executionOrigin,
					"resultOrigin":    result.ResultOrigin,
					"fusionIntent":    result.ResultFusionIntent,
					"owningComponent": result.OwningComponent,
				},
				CreatedAt: NowMillis(),
			}, resultExecutionOwnership(result), result.Confidence)
			outcomes = append(outcomes, PlanOutcome{
				StepID:       step.ID,
				Action:       step.Action,
				Success:      false,
				Error:        errorCode,
				Summary:      truncateOutcomeText(errMsg, 240),
				ResultCount:  len(result.Sources),
				Confidence:   result.Confidence,
				Degraded:     result.Degraded,
				ResultOrigin: result.ResultOrigin,
				SubAgent:     result.SubAgent,
			})
		}

		// Phase 3: Inter-agent mediation (standard model)
		if len(outcomes) > 0 {
			slog.Info("wisdev coordinator evaluation",
				"service", "go_orchestrator",
				"runtime", "go",
				"component", "wisdev.executor",
				"operation", "coordinate_feedback",
				"stage", "evaluation_started",
				"session_id", session.SessionID,
				"plan_id", session.Plan.PlanID,
				"outcome_count", len(outcomes),
				"failed_count", len(session.Plan.FailedStepIDs),
				"completed_count", len(session.Plan.CompletedStepIDs),
			)
			decision, err := e.CoordinateAgentFeedback(ctx, session, outcomes)
			if err == nil {
				slog.Info("wisdev coordinator decision",
					"service", "go_orchestrator",
					"runtime", "go",
					"component", "wisdev.executor",
					"operation", "coordinate_feedback",
					"stage", "decision_ready",
					"session_id", session.SessionID,
					"plan_id", session.Plan.PlanID,
					"decision", decision,
				)
				if shouldApplyCoordinatorReplan(outcomes, decision) && session.Plan.ReplanCount < e.maxReplans {
					replanned := e.applyAutomaticReplan(session, "coordinator", "Dynamic pivot requested by standard coordinator")
					slog.Info("wisdev coordinator replan applied",
						"service", "go_orchestrator",
						"runtime", "go",
						"component", "wisdev.executor",
						"operation", "coordinate_feedback",
						"stage", "replan_applied",
						"session_id", session.SessionID,
						"plan_id", session.Plan.PlanID,
						"replan_step_id", replanned.ID,
						"replan_count", session.Plan.ReplanCount,
					)
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
			} else {
				slog.Warn("wisdev coordinator evaluation failed",
					"service", "go_orchestrator",
					"runtime", "go",
					"component", "wisdev.executor",
					"operation", "coordinate_feedback",
					"stage", "decision_failed",
					"session_id", session.SessionID,
					"plan_id", session.Plan.PlanID,
					"error", err.Error(),
				)
			}
		}

		out <- PlanExecutionEvent{
			Type:      EventProgress,
			TraceID:   NewTraceID(),
			SessionID: session.SessionID,
			PlanID:    session.Plan.PlanID,
			Message:   "progress update",
			Payload: map[string]any{
				"completed":      len(session.Plan.CompletedStepIDs),
				"failed":         len(session.Plan.FailedStepIDs),
				"total":          len(session.Plan.Steps),
				"mode":           session.Mode,
				"serviceTier":    session.ServiceTier,
				"reasoningGraph": session.ReasoningGraph,
				"memoryTiers":    session.MemoryTiers,
			},
			CreatedAt: NowMillis(),
		}
	}
}

func safeSessionID(session *AgentSession) string {
	if session == nil {
		return ""
	}
	return session.SessionID
}

func safePlanID(session *AgentSession) string {
	if session == nil || session.Plan == nil {
		return ""
	}
	return session.Plan.PlanID
}
