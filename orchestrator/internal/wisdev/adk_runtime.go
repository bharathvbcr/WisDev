package wisdev

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/agent/remoteagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/plugin"
	"google.golang.org/adk/plugin/loggingplugin"
	"google.golang.org/adk/plugin/retryandreflect"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
)

type ADKRuntimeConfig struct {
	Runtime   ADKRuntimeDescriptor `json:"runtime" yaml:"runtime"`
	Telemetry ADKTelemetryConfig   `json:"telemetry" yaml:"telemetry"`
	HITL      ADKHITLConfig        `json:"hitl" yaml:"hitl"`
	A2A       ADKA2AConfig         `json:"a2a" yaml:"a2a"`
	Plugins   []ADKPluginConfig    `json:"plugins" yaml:"plugins"`
	Policy    *ADKPolicyOverrides  `json:"policy,omitempty" yaml:"policy,omitempty"`
}

type ADKPolicyOverrides struct {
	MaxToolCallsPerSession  *int `json:"maxToolCallsPerSession,omitempty" yaml:"maxToolCallsPerSession,omitempty"`
	MaxScriptRunsPerSession *int `json:"maxScriptRunsPerSession,omitempty" yaml:"maxScriptRunsPerSession,omitempty"`
	MaxCostPerSessionCents  *int `json:"maxCostPerSessionCents,omitempty" yaml:"maxCostPerSessionCents,omitempty"`
}

type ADKRuntimeDescriptor struct {
	Name       string `json:"name" yaml:"name"`
	Version    string `json:"version" yaml:"version"`
	Framework  string `json:"framework" yaml:"framework"`
	AgentID    string `json:"agentId" yaml:"agentId"`
	ConfigMode string `json:"configMode" yaml:"configMode"`
}

type ADKTelemetryConfig struct {
	OpenTelemetry bool   `json:"openTelemetry" yaml:"openTelemetry"`
	ServiceName   string `json:"serviceName" yaml:"serviceName"`
	Namespace     string `json:"namespace" yaml:"namespace"`
}

type ADKHITLConfig struct {
	Enabled                        bool     `json:"enabled" yaml:"enabled"`
	ConfirmationWindowMinutes      int      `json:"confirmationWindowMinutes" yaml:"confirmationWindowMinutes"`
	MediumRiskRequiresConfirmation bool     `json:"mediumRiskRequiresConfirmation" yaml:"mediumRiskRequiresConfirmation"`
	HighRiskRequiresConfirmation   bool     `json:"highRiskRequiresConfirmation" yaml:"highRiskRequiresConfirmation"`
	AllowedActions                 []string `json:"allowedActions" yaml:"allowedActions"`
}

type ADKA2AConfig struct {
	Enabled         bool   `json:"enabled" yaml:"enabled"`
	ProtocolVersion string `json:"protocolVersion" yaml:"protocolVersion"`
	ExposeAgentCard bool   `json:"exposeAgentCard" yaml:"exposeAgentCard"`
}

type ADKPluginConfig struct {
	Name             string   `json:"name" yaml:"name"`
	Description      string   `json:"description" yaml:"description"`
	ExecutionTargets []string `json:"executionTargets,omitempty" yaml:"executionTargets,omitempty"`
	Tools            []string `json:"tools,omitempty" yaml:"tools,omitempty"`
}

type adkDelegationRoute struct {
	Plugin             string
	Owner              string
	SubAgent           string
	OwningComponent    string
	ResultOrigin       string
	ResultFusionIntent string
}

type ADKRuntime struct {
	Config           ADKRuntimeConfig
	ConfigPath       string
	toolToPlug       map[string]ADKPluginConfig
	delegateExecutor func(context.Context, string, map[string]any, *AgentSession) (map[string]any, error)
	CredentialSource string
	ModelBackend     string

	// Official ADK components
	Agent     agent.Agent
	Runner    *runner.Runner
	ToolCount int
	InitError string
}

type adkToolInput struct {
	SessionID string         `json:"sessionId,omitempty"`
	Query     string         `json:"query,omitempty"`
	Domain    string         `json:"domain,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

type adkToolResult struct {
	Action  string         `json:"action"`
	Success bool           `json:"success"`
	Data    map[string]any `json:"data,omitempty"`
	Message string         `json:"message,omitempty"`
}

var (
	newGeminiModel         = gemini.NewModel
	resolveADKProjectID    = resilience.ResolveGoogleCloudProjectIDWithSource
	resolveADKGoogleAPIKey = llm.ResolveGoogleAPIKey
)

const officialADKModule = "google.golang.org/adk"

func DefaultADKRuntimeConfig() ADKRuntimeConfig {
	return ADKRuntimeConfig{
		Runtime: ADKRuntimeDescriptor{
			Name:       "WisDev",
			Version:    "1.0",
			Framework:  "google-adk-go",
			AgentID:    "wisdev-go",
			ConfigMode: "yaml",
		},
		Telemetry: ADKTelemetryConfig{
			OpenTelemetry: true,
			ServiceName:   "wisdev-orchestrator",
			Namespace:     "wisdev",
		},
		HITL: ADKHITLConfig{
			Enabled:                        true,
			ConfirmationWindowMinutes:      10,
			MediumRiskRequiresConfirmation: true,
			HighRiskRequiresConfirmation:   true,
			AllowedActions:                 []string{"approve", "skip", "edit_payload", "reject_replan"},
		},
		A2A: ADKA2AConfig{
			Enabled:         true,
			ProtocolVersion: "refined",
			ExposeAgentCard: true,
		},
	}
}

func ResolveADKConfigPath() string {
	candidates := []string{
		strings.TrimSpace(os.Getenv("WISDEV_ADK_CONFIG")),
		filepath.Join("config", "wisdev-adk.yaml"),
		filepath.Join("backend", "go_orchestrator", "config", "wisdev-adk.yaml"),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func LoadADKRuntime(registry *ToolRegistry) *ADKRuntime {
	runtime, _ := LoadADKRuntimeFromPath(ResolveADKConfigPath(), registry)
	return runtime
}

func LoadADKRuntimeFromPath(path string, registry *ToolRegistry) (*ADKRuntime, error) {
	cfg := DefaultADKRuntimeConfig()
	if strings.TrimSpace(path) != "" {
		payload, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read adk config: %w", err)
		}
		if err := yaml.Unmarshal(payload, &cfg); err != nil {
			return nil, fmt.Errorf("parse adk config: %w", err)
		}
	}
	runtime := &ADKRuntime{
		Config:     cfg,
		ConfigPath: path,
		toolToPlug: make(map[string]ADKPluginConfig),
	}
	runtime.registerPlugins(registry)
	return runtime, nil
}

func (r *ADKRuntime) registerPlugins(registry *ToolRegistry) {
	if r == nil || registry == nil {
		return
	}
	for _, pluginCfg := range r.Config.Plugins {
		for _, toolName := range pluginCfg.Tools {
			trimmed := CanonicalizeWisdevAction(toolName)
			if trimmed == "" {
				continue
			}
			r.toolToPlug[trimmed] = pluginCfg
		}
	}
	for _, toolDef := range registry.List() {
		if _, exists := r.toolToPlug[toolDef.Name]; exists {
			continue
		}
		r.toolToPlug[toolDef.Name] = ADKPluginConfig{
			Name:             pluginNameForTarget(toolDef.ExecutionTarget),
			Description:      fmt.Sprintf("Auto-mapped ADK plugin for %s tools.", strings.ToLower(string(toolDef.ExecutionTarget))),
			ExecutionTargets: []string{string(toolDef.ExecutionTarget)},
			Tools:            []string{toolDef.Name},
		}
	}
}

func pluginNameForTarget(target ExecutionTarget) string {
	switch target {
	case ExecutionTargetGoNative:
		return "go-native-tools"
	case ExecutionTargetPythonSandbox:
		return "python-sandbox-tools"
	default:
		return "python-capability-tools"
	}
}

func (r *ADKRuntime) PluginForAction(action string) (ADKPluginConfig, bool) {
	if r == nil {
		return ADKPluginConfig{}, false
	}
	pluginCfg, ok := r.toolToPlug[CanonicalizeWisdevAction(action)]
	return pluginCfg, ok
}

func adkSubAgentForPlugin(pluginName string) string {
	switch strings.TrimSpace(pluginName) {
	case "go-native-tools":
		return "wisdev-reasoning"
	case "python-capability-tools", "python-sandbox-tools":
		return "python-researcher"
	default:
		return ""
	}
}

func (r *ADKRuntime) hasSubAgent(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || r == nil || r.Agent == nil {
		return false
	}
	for _, subAgent := range r.Agent.SubAgents() {
		if strings.EqualFold(strings.TrimSpace(subAgent.Name()), name) {
			return true
		}
	}
	return false
}

func (r *ADKRuntime) ResolveDelegationRoute(step PlanStep) (adkDelegationRoute, bool) {
	if r == nil {
		return adkDelegationRoute{}, false
	}

	// Prefer routing to specialized research agents for research-specific actions
	role := inferResearchRoleForAction(step.Action)
	if role != "" && r.hasSubAgent(string(role)) {
		return adkDelegationRoute{
			Plugin:             "research-specialist",
			Owner:              string(role),
			SubAgent:           string(role),
			OwningComponent:    "adk_runtime",
			ResultOrigin:       "specialist_delegate",
			ResultFusionIntent: "specialist_result_fusion",
		}, true
	}

	pluginCfg, ok := r.PluginForAction(step.Action)
	if !ok {
		return adkDelegationRoute{}, false
	}

	subAgent := adkSubAgentForPlugin(pluginCfg.Name)
	switch {
	case subAgent != "" && r.hasSubAgent(subAgent):
	case r.hasSubAgent(pluginCfg.Name):
		subAgent = strings.TrimSpace(pluginCfg.Name)
	default:
		return adkDelegationRoute{}, false
	}

	return adkDelegationRoute{
		Plugin:             strings.TrimSpace(pluginCfg.Name),
		Owner:              subAgent,
		SubAgent:           subAgent,
		OwningComponent:    "adk_runtime",
		ResultOrigin:       "adk_delegate",
		ResultFusionIntent: "delegated_result_fusion",
	}, true
}

func inferResearchRoleForAction(action string) ResearchWorkerRole {
	action = strings.ToLower(strings.TrimSpace(action))
	switch {
	case strings.Contains(action, "contradict"):
		return ResearchWorkerContradictionCritic
	case strings.Contains(action, "diversity") || strings.Contains(action, "diversify"):
		return ResearchWorkerSourceDiversifier
	case strings.Contains(action, "citation") || strings.Contains(action, "doi"):
		return ResearchWorkerCitationVerifier
	case strings.Contains(action, "graph") || strings.Contains(action, "network"):
		return ResearchWorkerCitationGraph
	case strings.Contains(action, "verify") || strings.Contains(action, "entail"):
		return ResearchWorkerIndependentVerifier
	case strings.Contains(action, "synthesize") || strings.Contains(action, "draft"):
		return ResearchWorkerSynthesizer
	case strings.Contains(action, "scout") || strings.Contains(action, "plan"):
		return ResearchWorkerScout
	default:
		return ""
	}
}

func (r *ADKRuntime) ExecuteDelegatedAction(
	ctx context.Context,
	route adkDelegationRoute,
	step PlanStep,
	payload map[string]any,
	sessionState *AgentSession,
) (map[string]any, bool, error) {
	if r == nil || r.delegateExecutor == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(route.SubAgent) == "" {
		return nil, false, nil
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["adkDelegatedExecution"] = true
	payload["adkSubAgent"] = route.SubAgent
	payload["adkPlugin"] = route.Plugin
	payload["adkOwningComponent"] = route.OwningComponent
	result, invocationID, eventAuthor, err := r.executeDelegatedActionWithRunner(ctx, route, step, payload, sessionState)
	if result == nil {
		result = map[string]any{}
	}
	result["delegated"] = true
	result["delegatedSubAgent"] = route.SubAgent
	result["delegatedPlugin"] = route.Plugin
	result["resultOrigin"] = route.ResultOrigin
	result["resultFusionIntent"] = route.ResultFusionIntent
	result["adkRunnerExecuted"] = true
	result["adkInvocationId"] = invocationID
	result["adkEventAuthor"] = eventAuthor
	result["adkRuntime"] = "google.golang.org/adk"
	return result, true, err
}

func (r *ADKRuntime) ExecuteProgrammaticAction(
	ctx context.Context,
	action string,
	payload map[string]any,
	sessionState *AgentSession,
) (map[string]any, error) {
	if r == nil || r.delegateExecutor == nil {
		return nil, fmt.Errorf("adk programmatic executor is not configured")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["adkProgrammaticExecution"] = true
	route := adkDelegationRoute{
		Plugin:             "programmatic-loop",
		Owner:              "wisdev-programmatic",
		SubAgent:           "wisdev-programmatic",
		OwningComponent:    "adk_runtime",
		ResultOrigin:       "adk_programmatic_loop",
		ResultFusionIntent: "programmatic_loop_result_fusion",
	}
	step := PlanStep{ID: AsOptionalString(payload["planStepId"]), Action: action}
	result, invocationID, eventAuthor, err := r.executeDelegatedActionWithRunner(ctx, route, step, payload, sessionState)
	if result == nil {
		result = map[string]any{}
	}
	result["adkRunnerExecuted"] = true
	result["adkInvocationId"] = invocationID
	result["adkEventAuthor"] = eventAuthor
	result["adkRuntime"] = "google.golang.org/adk"
	result["resultOrigin"] = route.ResultOrigin
	result["resultFusionIntent"] = route.ResultFusionIntent
	return result, err
}

func (r *ADKRuntime) executeDelegatedActionWithRunner(
	ctx context.Context,
	route adkDelegationRoute,
	step PlanStep,
	payload map[string]any,
	sessionState *AgentSession,
) (map[string]any, string, string, error) {
	actionAgentName := strings.TrimSpace(route.SubAgent)
	if actionAgentName == "" {
		actionAgentName = "wisdev-delegate"
	}
	actionAgent, err := agent.New(agent.Config{
		Name:        actionAgentName,
		Description: fmt.Sprintf("ADK delegated executor for %s.", step.Action),
		Run: func(invocation agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				payload["adkRunnerInternal"] = true
				result, err := r.delegateExecutor(invocation, step.Action, payload, sessionState)
				if err != nil {
					yield(nil, err)
					return
				}
				result = ensureExecutionResultMap(result)
				result["adkRunnerInternal"] = payload["adkRunnerInternal"]
				event := session.NewEvent(invocation.InvocationID())
				event.Author = actionAgentName
				event.Content = genai.NewContentFromText("Delegated WisDev action completed.", genai.RoleModel)
				event.Actions.StateDelta["delegatedResult"] = result
				event.Actions.StateDelta["adkRuntime"] = "google.golang.org/adk"
				event.Actions.StateDelta["adkSubAgent"] = actionAgentName
				event.TurnComplete = true
				yield(event, nil)
			}
		},
	})
	if err != nil {
		return nil, "", "", fmt.Errorf("adk delegated action agent init failed: %w", err)
	}

	logPlugin, _ := loggingplugin.New("wisdev-adk-delegate")
	retryPlugin, _ := retryandreflect.New()
	appName := strings.TrimSpace(r.Config.Runtime.Name)
	if appName == "" {
		appName = "wisdev-adk"
	}
	actionRunner, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             actionAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
		PluginConfig: runner.PluginConfig{
			Plugins: []*plugin.Plugin{logPlugin, retryPlugin},
		},
	})
	if err != nil {
		return nil, "", "", fmt.Errorf("adk delegated action runner init failed: %w", err)
	}

	userID := "wisdev"
	sessionID := ""
	query := AsOptionalString(payload["query"])
	if sessionState != nil {
		if strings.TrimSpace(sessionState.UserID) != "" {
			userID = sessionState.UserID
		}
		sessionID = strings.TrimSpace(sessionState.SessionID)
		if strings.TrimSpace(query) == "" {
			query = resolveSessionQuery(sessionState)
		}
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(AsOptionalString(payload["sessionId"]))
	}
	if sessionID == "" {
		sessionID = fmt.Sprintf("wisdev-delegate-%s", strings.TrimSpace(step.ID))
	}
	if strings.TrimSpace(query) == "" {
		query = step.Action
	}

	var (
		result       map[string]any
		invocationID string
		eventAuthor  string
	)
	for event, runErr := range actionRunner.Run(
		ctx,
		userID,
		sessionID,
		genai.NewContentFromText(query, genai.RoleUser),
		agent.RunConfig{},
		runner.WithStateDelta(cloneAnyMap(payload)),
	) {
		if runErr != nil {
			return result, invocationID, eventAuthor, runErr
		}
		if event == nil {
			continue
		}
		if strings.TrimSpace(invocationID) == "" {
			invocationID = event.InvocationID
		}
		if strings.TrimSpace(eventAuthor) == "" {
			eventAuthor = event.Author
		}
		if delegated, ok := event.Actions.StateDelta["delegatedResult"].(map[string]any); ok {
			result = delegated
		}
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, invocationID, eventAuthor, nil
}

func (r *ADKRuntime) ExecuteSession(
	ctx context.Context,
	sessionState *AgentSession,
	emit func(PlanExecutionEvent),
) error {
	if r == nil || r.Runner == nil || r.Agent == nil || sessionState == nil {
		return fmt.Errorf("adk session runner is not configured")
	}
	query := strings.TrimSpace(resolveSessionQuery(sessionState))
	if query == "" {
		return fmt.Errorf("session query is required")
	}
	userID := strings.TrimSpace(sessionState.UserID)
	if userID == "" {
		userID = "wisdev"
	}
	sessionID := strings.TrimSpace(sessionState.SessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId is required")
	}
	slog.Info("wisdev ADK session execution started",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "wisdev.adk_runtime",
		"operation", "execute_session",
		"stage", "runner_started",
		"session_id", sessionID,
		"plan_id", sessionState.PlanID(),
		"user_id", userID,
		"query_length", len(query),
	)
	eventCount := 0
	emittedCount := 0
	for event, runErr := range r.Runner.Run(ctx, userID, sessionID, genai.NewContentFromText(query, genai.RoleUser), agent.RunConfig{}) {
		if runErr != nil {
			slog.Error("wisdev ADK runner event failed",
				"service", "go_orchestrator",
				"runtime", "go",
				"component", "wisdev.adk_runtime",
				"operation", "execute_session",
				"stage", "runner_event_failed",
				"session_id", sessionID,
				"plan_id", sessionState.PlanID(),
				"event_count", eventCount,
				"emitted_count", emittedCount,
				"error", runErr.Error(),
			)
			return runErr
		}
		if event == nil || emit == nil {
			if event == nil {
				slog.Info("wisdev ADK runner yielded nil event",
					"service", "go_orchestrator",
					"runtime", "go",
					"component", "wisdev.adk_runtime",
					"operation", "execute_session",
					"stage", "nil_event_skipped",
					"session_id", sessionID,
					"plan_id", sessionState.PlanID(),
				)
			}
			continue
		}
		eventCount++
		planEvent := planEventFromADKEvent(event, sessionState)
		if planEvent.Type != "" {
			emittedCount++
			slog.Info("wisdev ADK event mapped",
				"service", "go_orchestrator",
				"runtime", "go",
				"component", "wisdev.adk_runtime",
				"operation", "execute_session",
				"stage", "event_mapped",
				"session_id", sessionID,
				"plan_id", sessionState.PlanID(),
				"event_count", eventCount,
				"emitted_count", emittedCount,
				"adk_author", event.Author,
				"event_type", string(planEvent.Type),
				"step_id", planEvent.StepID,
				"turn_complete", event.TurnComplete,
				"state_delta_keys", len(event.Actions.StateDelta),
				"metadata_keys", len(event.CustomMetadata),
			)
			emit(planEvent)
			continue
		}
		slog.Info("wisdev ADK event skipped",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "wisdev.adk_runtime",
			"operation", "execute_session",
			"stage", "event_skipped",
			"session_id", sessionID,
			"plan_id", sessionState.PlanID(),
			"event_count", eventCount,
			"adk_author", event.Author,
			"turn_complete", event.TurnComplete,
			"state_delta_keys", len(event.Actions.StateDelta),
			"metadata_keys", len(event.CustomMetadata),
		)
	}
	slog.Info("wisdev ADK session execution finished",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "wisdev.adk_runtime",
		"operation", "execute_session",
		"stage", "runner_finished",
		"session_id", sessionID,
		"plan_id", sessionState.PlanID(),
		"event_count", eventCount,
		"emitted_count", emittedCount,
	)
	return nil
}

func planEventFromADKEvent(event *session.Event, sessionState *AgentSession) PlanExecutionEvent {
	metadata := cloneAnyMap(event.CustomMetadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	for key, value := range event.Actions.StateDelta {
		if _, exists := metadata[key]; !exists {
			metadata[key] = value
		}
	}
	eventType := PlanExecutionEventType(AsOptionalString(metadata["eventType"]))
	confirmationPayload := adkConfirmationPayload(event)
	if len(confirmationPayload) > 0 {
		for key, value := range confirmationPayload {
			if _, exists := metadata[key]; !exists {
				metadata[key] = value
			}
		}
		if eventType == "" {
			eventType = EventConfirmationNeed
		}
	}
	if eventType == "" {
		if event.TurnComplete {
			eventType = EventCompleted
		} else {
			eventType = EventProgress
		}
	}
	sessionID := AsOptionalString(metadata["sessionId"])
	planID := AsOptionalString(metadata["planId"])
	if sessionState != nil {
		if strings.TrimSpace(sessionID) == "" {
			sessionID = sessionState.SessionID
		}
		if strings.TrimSpace(planID) == "" {
			planID = sessionState.PlanID()
		}
	}
	message := AsOptionalString(metadata["message"])
	if strings.TrimSpace(message) == "" {
		message = adkEventText(event)
	}
	if strings.TrimSpace(message) == "" && eventType == EventConfirmationNeed {
		message = AsOptionalString(metadata["adkConfirmationHint"])
	}
	traceID := AsOptionalString(metadata["traceId"])
	if strings.TrimSpace(traceID) == "" {
		traceID = NewTraceID()
	}
	createdAt := NowMillis()
	if !event.Timestamp.IsZero() {
		createdAt = event.Timestamp.UnixMilli()
	}
	metadata["adkRunnerExecuted"] = true
	metadata["adkInvocationId"] = event.InvocationID
	metadata["adkEventAuthor"] = event.Author
	metadata["adkRuntime"] = "google.golang.org/adk"
	return PlanExecutionEvent{
		Type:               eventType,
		TraceID:            traceID,
		SessionID:          sessionID,
		PlanID:             planID,
		StepID:             AsOptionalString(metadata["stepId"]),
		Message:            message,
		CreatedAt:          createdAt,
		Payload:            metadata,
		Owner:              AsOptionalString(metadata["owner"]),
		SubAgent:           AsOptionalString(metadata["subAgent"]),
		OwningComponent:    AsOptionalString(metadata["owningComponent"]),
		ResultOrigin:       AsOptionalString(metadata["resultOrigin"]),
		ResultConfidence:   AsFloat(metadata["resultConfidence"]),
		ResultFusionIntent: AsOptionalString(metadata["resultFusionIntent"]),
	}
}

func adkConfirmationPayload(event *session.Event) map[string]any {
	if event == nil || len(event.Actions.RequestedToolConfirmations) == 0 {
		return nil
	}
	payload := map[string]any{
		"adkConfirmationRequired": true,
		"adkConfirmationFunction": toolconfirmation.FunctionCallName,
	}
	for functionCallID, confirmation := range event.Actions.RequestedToolConfirmations {
		if strings.TrimSpace(functionCallID) == "" {
			continue
		}
		payload["adkOriginalFunctionCallId"] = functionCallID
		payload["adkConfirmationHint"] = confirmation.Hint
		payload["adkConfirmationPayload"] = confirmation.Payload
		if mapped, ok := confirmation.Payload.(map[string]any); ok {
			for key, value := range mapped {
				if _, exists := payload[key]; !exists {
					payload[key] = value
				}
			}
		}
		break
	}
	if event.Content != nil {
		for _, part := range event.Content.Parts {
			if part == nil || part.FunctionCall == nil || part.FunctionCall.Name != toolconfirmation.FunctionCallName {
				continue
			}
			payload["adkConfirmationRequestId"] = part.FunctionCall.ID
			original, err := toolconfirmation.OriginalCallFrom(part.FunctionCall)
			if err == nil && original != nil {
				payload["adkOriginalFunctionCallId"] = original.ID
				payload["adkOriginalFunctionCallName"] = original.Name
				payload["action"] = original.Name
				payload["adkOriginalFunctionCallArgs"] = cloneAnyMap(original.Args)
				if args := cloneAnyMap(original.Args); args != nil {
					for key, value := range args {
						if _, exists := payload[key]; !exists {
							payload[key] = value
						}
					}
				}
			}
			break
		}
	}
	return payload
}

func adkEventText(event *session.Event) string {
	if event == nil || event.Content == nil {
		return ""
	}
	for _, part := range event.Content.Parts {
		if part != nil && strings.TrimSpace(part.Text) != "" {
			return part.Text
		}
	}
	return ""
}

func (r *ADKRuntime) Bind(ctx context.Context, gateway *AgentGateway) {
	if r == nil || gateway == nil || gateway.Registry == nil {
		return
	}
	r.CredentialSource = ""
	r.ModelBackend = ""

	location := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_LOCATION"))
	if location == "" {
		location = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_REGION"))
	}
	if location == "" {
		location = "us-central1"
	}

	projectID, projectSource := resolveADKProjectID(ctx)

	var (
		adkModel         model.LLM
		err              error
		credentialSource string
		vertexInitErr    error
	)

	if projectID != "" {
		vertexCfg := &genai.ClientConfig{
			Project:  projectID,
			Location: location,
			Backend:  genai.BackendVertexAI,
		}
		adkModel, err = newGeminiModel(ctx, llm.ResolveStandardModel(), vertexCfg)
		if err == nil {
			credentialSource = "vertex_ai:" + projectSource
			r.ModelBackend = "vertex_ai"
		} else {
			vertexInitErr = err
			slog.Warn("adk vertex model init failed; attempting google api key fallback",
				"project_id", projectID,
				"project_source", projectSource,
				"location", location,
				"error", err,
			)
		}
	}

	if adkModel == nil {
		apiKey, apiKeySource, keyErr := resolveADKGoogleAPIKey(ctx, projectID)
		if apiKey == "" {
			if keyErr != nil {
				r.InitError = fmt.Sprintf("google genai credentials unavailable for ADK runtime: %v", keyErr)
				return
			}
			if vertexInitErr != nil {
				r.InitError = fmt.Sprintf("adk model init failed and no GOOGLE_API_KEY/GEMINI_API_KEY secret or env credential was available: %v", vertexInitErr)
				return
			}
			r.InitError = "GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT is not set and no GOOGLE_API_KEY or GEMINI_API_KEY credential was available for ADK runtime"
			return
		}
		adkModel, err = newGeminiModel(ctx, llm.ResolveStandardModel(), &genai.ClientConfig{
			APIKey:  apiKey,
			Backend: genai.BackendGeminiAPI,
		})
		if err != nil {
			r.InitError = fmt.Sprintf("gemini model init failed: %v", err)
			return
		}
		credentialSource = apiKeySource
		r.ModelBackend = "gemini_api"
	}
	r.CredentialSource = credentialSource

	slog.Info("adk model initialized", "credential_source", credentialSource, "project_id", projectID, "location", location)

	// 3. Wrap Registry Tools as ADK FunctionTools
	tools := make([]adktool.Tool, 0, len(gateway.Registry.List()))
	for _, toolDef := range gateway.Registry.List() {
		wrapped, wrapErr := r.wrapGatewayTool(gateway, toolDef)
		if wrapErr != nil {
			r.InitError = fmt.Sprintf("tool wrap failed for %s: %v", toolDef.Name, wrapErr)
			return
		}
		tools = append(tools, wrapped)
	}
	r.ToolCount = len(tools)

	// 4. Create Specialist Sub-agents (Swarm Orchestration)
	subAgents := []agent.Agent{}
	if r.Config.A2A.Enabled {
		pythonA2A, err := remoteagent.NewA2A(remoteagent.A2AConfig{
			Name:            "python-researcher",
			Description:     "Specialized Python-based research agent for deep academic search and data processing.",
			AgentCardSource: fmt.Sprintf("%s/agent/card", ResolvePythonBase()),
		})
		if err == nil {
			subAgents = append(subAgents, pythonA2A)
		} else {
			slog.Warn("Failed to initialize Python A2A agent", "error", err)
		}
	}

	// Register specialized research roles as true ADK sub-agents
	specialistRoles := []ResearchWorkerRole{
		ResearchWorkerScout,
		ResearchWorkerSourceDiversifier,
		ResearchWorkerCitationVerifier,
		ResearchWorkerCitationGraph,
		ResearchWorkerContradictionCritic,
		ResearchWorkerIndependentVerifier,
		ResearchWorkerSynthesizer,
	}

	for _, role := range specialistRoles {
		contract := buildResearchWorkerContract(role)
		specialist, err := llmagent.New(llmagent.Config{
			Name:        string(role),
			Description: contract.Objective,
			Model:       adkModel,
			Tools:       tools,
		})
		if err == nil {
			subAgents = append(subAgents, specialist)
			slog.Debug("Registered ADK specialist agent", "role", role)
		} else {
			slog.Warn("Failed to initialize specialist agent", "role", role, "error", err)
		}
	}

	// Generic reasoning agent for fallback.
	reasoningAgent, err := llmagent.New(llmagent.Config{
		Name:        "wisdev-reasoning",
		Description: "LLM-based reasoning engine for dynamic research tasks.",
		Model:       adkModel,
		Tools:       tools,
	})
	if err == nil {
		subAgents = append(subAgents, reasoningAgent)
	}

	// 5. Create Workflow Agent (The Core Orchestrator)
	planExecutor, ok := gateway.Executor.(*PlanExecutor)
	if !ok || planExecutor == nil {
		r.InitError = "ADKRuntime requires a *PlanExecutor; gateway.Executor is nil or wrong type"
		return
	}
	rootAgentValue, err := NewWisDevWorkflowAgent(gateway, planExecutor, subAgents)
	if err != nil {
		r.InitError = fmt.Sprintf("workflow agent init failed: %v", err)
		return
	}
	rootAgent, ok := rootAgentValue.(agent.Agent)
	if !ok || rootAgent == nil {
		rootAgent, err = agent.New(agent.Config{
			Name:        "wisdev-root",
			Description: "Top-level WisDev workflow coordinator.",
			SubAgents:   subAgents,
		})
		if err != nil {
			r.InitError = fmt.Sprintf("workflow agent fallback init failed: %v", err)
			return
		}
	}

	// 6. Initialize Standard ADK Plugins
	logPlugin, _ := loggingplugin.New("wisdev-adk")
	retryPlugin, _ := retryandreflect.New()

	// 7. Create Runner
	adkRunner, err := runner.New(runner.Config{
		AppName:           r.Config.Runtime.Name,
		Agent:             rootAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
		PluginConfig: runner.PluginConfig{
			Plugins: []*plugin.Plugin{logPlugin, retryPlugin},
		},
	})
	if err != nil {
		r.InitError = fmt.Sprintf("runner init failed: %v", err)
		return
	}

	r.Agent = rootAgent
	r.Runner = adkRunner
	r.delegateExecutor = gateway.ProgrammaticLoopExecutor()
	r.InitError = ""
}

func (r *ADKRuntime) wrapGatewayTool(gateway *AgentGateway, toolDef ToolDefinition) (adktool.Tool, error) {
	var confirmationProvider any
	if r.Config.HITL.Enabled {
		switch toolDef.Risk {
		case RiskLevelHigh:
			if r.Config.HITL.HighRiskRequiresConfirmation {
				confirmationProvider = func(input adkToolInput) bool { return true }
			}
		case RiskLevelMedium:
			if r.Config.HITL.MediumRiskRequiresConfirmation {
				confirmationProvider = func(input adkToolInput) bool { return true }
			}
		}
	}

	return functiontool.New(functiontool.Config{
		Name:                        toolDef.Name,
		Description:                 toolDef.Description,
		RequireConfirmationProvider: confirmationProvider,
	}, func(toolCtx adktool.Context, input adkToolInput) (adkToolResult, error) {
		payload := map[string]any{}
		for key, value := range input.Payload {
			payload[key] = value
		}
		if strings.TrimSpace(input.Query) != "" {
			payload["query"] = input.Query
		}
		if strings.TrimSpace(input.Domain) != "" {
			payload["domain"] = input.Domain
		}
		execCtx := adkToolExecutionContext(toolCtx)
		sessionState := gateway.ensureADKSessionWithContext(execCtx, input.SessionID, input.Query, input.Domain)
		result, err := gateway.ExecuteADKAction(execCtx, toolDef, payload, sessionState)
		if err != nil {
			return adkToolResult{Action: toolDef.Name, Success: false, Message: err.Error()}, err
		}
		return adkToolResult{Action: toolDef.Name, Success: true, Data: result, Message: "ok"}, nil
	})
}

func adkToolExecutionContext(toolCtx adktool.Context) context.Context {
	if toolCtx == nil {
		return context.Background()
	}
	return toolCtx
}

func (r *ADKRuntime) HITLTimeout() time.Duration {
	if r == nil || r.Config.HITL.ConfirmationWindowMinutes <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(r.Config.HITL.ConfirmationWindowMinutes) * time.Minute
}

func (r *ADKRuntime) BuildA2ACard() map[string]any {
	if r == nil || !r.Config.A2A.Enabled || !r.Config.A2A.ExposeAgentCard {
		return nil
	}
	configured, runnerReady, agentReady, ready, status, initError := r.readinessState()
	toolNames := make([]string, 0, len(r.toolToPlug))
	pluginNames := make([]string, 0, len(r.Config.Plugins))
	for toolName := range r.toolToPlug {
		toolNames = append(toolNames, toolName)
	}
	for _, pluginCfg := range r.Config.Plugins {
		if name := strings.TrimSpace(pluginCfg.Name); name != "" {
			pluginNames = append(pluginNames, name)
		}
	}
	sort.Strings(toolNames)
	sort.Strings(pluginNames)
	return map[string]any{
		"agentId":      r.Config.Runtime.AgentID,
		"name":         r.Config.Runtime.Name,
		"version":      r.Config.Runtime.Version,
		"framework":    r.Config.Runtime.Framework,
		"protocol":     r.Config.A2A.ProtocolVersion,
		"capabilities": len(r.toolToPlug),
		"configured":   configured,
		"ready":        ready,
		"status":       status,
		"runnerReady":  runnerReady,
		"agentReady":   agentReady,
		"initError":    initError,
		"description":  "WisDev orchestration agent for grounded academic research, verification, and manuscript workflows.",
		"toolNames":    toolNames,
		"plugins":      pluginNames,
		"hitlEnabled":  r.Config.HITL.Enabled,
		"endpoints": []map[string]any{
			{"path": "/agent/card", "kind": "agent_card"},
			{"path": "/.well-known/agent-card.json", "kind": "agent_card"},
			{"path": "/agent/tools", "kind": "tool_catalog"},
			{"path": "/agent/sessions", "kind": "session_api"},
		},
	}
}

func (r *ADKRuntime) BuildHITLRequest(token string, step PlanStep, rationale string) map[string]any {
	if r == nil {
		return map[string]any{}
	}
	pluginCfg, _ := r.PluginForAction(step.Action)
	originalCallID := firstNonEmpty(strings.TrimSpace(step.ID), strings.TrimSpace(step.Action), "wisdev_confirmation")
	toolPayload := map[string]any{
		"approvalToken": token,
		"action":        step.Action,
		"risk":          string(step.Risk),
		"rationale":     strings.TrimSpace(rationale),
	}
	if strings.TrimSpace(step.ID) != "" {
		toolPayload["stepId"] = step.ID
	}
	toolConfirmation := toolconfirmation.ToolConfirmation{
		Hint:    strings.TrimSpace(rationale),
		Payload: cloneAnyMap(toolPayload),
	}
	request := map[string]any{
		"framework":        r.Config.Runtime.Framework,
		"confirmation":     "required",
		"approvalToken":    token,
		"action":           step.Action,
		"plugin":           pluginCfg.Name,
		"risk":             string(step.Risk),
		"rationale":        strings.TrimSpace(rationale),
		"allowedActions":   append([]string(nil), r.Config.HITL.AllowedActions...),
		"expiresInMinutes": r.Config.HITL.ConfirmationWindowMinutes,
		"adkConfirmation": map[string]any{
			"function": toolconfirmation.FunctionCallName,
			"originalFunctionCall": map[string]any{
				"id":   originalCallID,
				"name": step.Action,
				"args": cloneAnyMap(toolPayload),
			},
			"toolConfirmation": toolConfirmation,
		},
		"adkConfirmationFunction":       toolconfirmation.FunctionCallName,
		"adkConfirmationPayload":        cloneAnyMap(toolPayload),
		"adkOriginalFunctionCallId":     originalCallID,
		"adkOriginalFunctionCallName":   step.Action,
		"adkOriginalFunctionCallArgs":   cloneAnyMap(toolPayload),
		"adkRequestedToolConfirmation":  toolConfirmation,
		"adkRequestedConfirmationShape": "toolconfirmation.ToolConfirmation",
	}
	if route, ok := r.ResolveDelegationRoute(step); ok {
		request["subAgent"] = route.SubAgent
		request["owningComponent"] = route.OwningComponent
	}
	return request
}

func (r *ADKRuntime) subAgentNames() []string {
	if r == nil || r.Agent == nil {
		return []string{}
	}
	names := []string{}
	for _, sa := range r.Agent.SubAgents() {
		names = append(names, sa.Name())
	}
	return names
}

func (r *ADKRuntime) Metadata() map[string]any {
	if r == nil {
		return map[string]any{
			"enabled": false,
			"ready":   false,
		}
	}
	configured, runnerReady, agentReady, ready, status, initError := r.readinessState()
	enabled := configured && r.Config.A2A.Enabled
	meta := map[string]any{
		"enabled":          enabled,
		"configured":       configured,
		"ready":            ready,
		"status":           status,
		"framework":        r.Config.Runtime.Framework,
		"officialModule":   officialADKModule,
		"canonicalWrapper": officialADKModule,
		"executionPolicy":  "adk_primary_direct_executor_fallback_only",
		"directExecutor": map[string]any{
			"fallbackOnly": true,
			"allowedWhen":  []string{"adk_unavailable", "focused_unit_test", "targeted_step_executor_with_adk_metadata"},
		},
		"moduleVersion":    officialADKModuleVersion(),
		"modelBackend":     strings.TrimSpace(r.ModelBackend),
		"credentialSource": strings.TrimSpace(r.CredentialSource),
		"runtimeName":      r.Config.Runtime.Name,
		"runtimeVersion":   r.Config.Runtime.Version,
		"agentId":          r.Config.Runtime.AgentID,
		"configMode":       r.Config.Runtime.ConfigMode,
		"configPath":       r.ConfigPath,
		"pluginCount":      len(r.toolToPlug),
		"runnerReady":      runnerReady,
		"agentReady":       agentReady,
		"subAgents":        r.subAgentNames(),
		"toolCount":        r.ToolCount,
		"initError":        initError,
		"openTelemetry":    r.Config.Telemetry.OpenTelemetry,
		"hitlEnabled":      r.Config.HITL.Enabled,
		"a2aEnabled":       r.Config.A2A.Enabled,
		"a2aProtocol":      r.Config.A2A.ProtocolVersion,
		"agentCardExposed": r.Config.A2A.ExposeAgentCard,
	}
	return meta
}

func officialADKModuleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path != officialADKModule {
			continue
		}
		if dep.Replace != nil {
			return dep.Replace.Version
		}
		return dep.Version
	}
	return ""
}

func (r *ADKRuntime) readinessState() (configured bool, runnerReady bool, agentReady bool, ready bool, status string, initError string) {
	if r == nil {
		return false, false, false, false, "disabled", ""
	}
	configured = true
	runnerReady = r.Runner != nil
	agentReady = r.Agent != nil
	initError = strings.TrimSpace(r.InitError)
	ready = runnerReady && agentReady && initError == ""
	switch {
	case !r.Config.A2A.Enabled:
		status = "disabled"
	case ready:
		status = "ready"
	case initError != "":
		status = "init_error"
	case runnerReady || agentReady:
		status = "partial"
	default:
		status = "initializing"
	}
	return configured, runnerReady, agentReady, ready, status, initError
}
