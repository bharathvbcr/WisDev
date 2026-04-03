package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/agent/remoteagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/plugin"
	"google.golang.org/adk/plugin/loggingplugin"
	"google.golang.org/adk/plugin/retryandreflect"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
)

type ADKRuntimeConfig struct {
	Runtime   ADKRuntimeDescriptor `json:"runtime" yaml:"runtime"`
	Telemetry ADKTelemetryConfig   `json:"telemetry" yaml:"telemetry"`
	HITL      ADKHITLConfig        `json:"hitl" yaml:"hitl"`
	A2A       ADKA2AConfig         `json:"a2a" yaml:"a2a"`
	Plugins   []ADKPluginConfig    `json:"plugins" yaml:"plugins"`
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

type ADKRuntime struct {
	Config     ADKRuntimeConfig
	ConfigPath string
	toolToPlug map[string]ADKPluginConfig

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

func DefaultADKRuntimeConfig() ADKRuntimeConfig {
	return ADKRuntimeConfig{
		Runtime: ADKRuntimeDescriptor{
			Name:       "WisDev",
			Version:    "1.0",
			Framework:  "google-adk-go",
			AgentID:    "wisdev-orchestrator",
			ConfigMode: "yaml",
		},
		Telemetry: ADKTelemetryConfig{
			OpenTelemetry: true,
			ServiceName:   "wisdev-orchestrator-orchestrator",
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
			trimmed := strings.TrimSpace(toolName)
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
	pluginCfg, ok := r.toolToPlug[strings.TrimSpace(action)]
	return pluginCfg, ok
}

func (r *ADKRuntime) Bind(ctx context.Context, gateway *AgentGateway) {
	if r == nil || gateway == nil || gateway.Registry == nil {
		return
	}

	apiKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	if apiKey == "" {
		r.InitError = "GOOGLE_API_KEY is not set; ADK runner not initialized"
		return
	}

	// 2. Initialize Model
	adkModel, err := gemini.NewModel(ctx, llm.ResolveStandardModel(), &genai.ClientConfig{
		APIKey: apiKey,
	})
	if err != nil {
		r.InitError = fmt.Sprintf("gemini model init failed: %v", err)
		return
	}

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

	// 4. Create Sub-agents (LLM reasoning and Python remote)
	subAgents := []agent.Agent{}
	if r.Config.A2A.Enabled {
		pythonA2A, err := remoteagent.NewA2A(remoteagent.A2AConfig{
			Name:            "python-researcher",
			Description:     "Specialized Python-based research agent for deep academic search and data processing.",
			AgentCardSource: fmt.Sprintf("%s/v2/agent/card", ResolvePythonBase()),
		})
		if err == nil {
			subAgents = append(subAgents, pythonA2A)
		} else {
			slog.Warn("Failed to initialize Python A2A agent", "error", err)
		}
	}

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
	rootAgent, err := NewWisDevWorkflowAgent(gateway, planExecutor, subAgents)
	if err != nil {
		r.InitError = fmt.Sprintf("workflow agent init failed: %v", err)
		return
	}

	// 6. Initialize Standard ADK Plugins
	logPlugin, _ := loggingplugin.New("wisdev-adk")
	retryPlugin, _ := retryandreflect.New()

	// 7. Create Runner
	adkRunner, err := runner.New(runner.Config{
		AppName:        r.Config.Runtime.Name,
		Agent:          rootAgent,
		SessionService: session.InMemoryService(),
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
	}, func(_ adktool.Context, input adkToolInput) (adkToolResult, error) {
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
		sessionState := gateway.ensureADKSession(input.SessionID, input.Query, input.Domain)
		result, err := gateway.ExecuteADKAction(context.Background(), toolDef, payload, sessionState)
		if err != nil {
			return adkToolResult{Action: toolDef.Name, Success: false, Message: err.Error()}, err
		}
		return adkToolResult{Action: toolDef.Name, Success: true, Data: result, Message: "ok"}, nil
	})
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
	return map[string]any{
		"agentId":      r.Config.Runtime.AgentID,
		"name":         r.Config.Runtime.Name,
		"version":      r.Config.Runtime.Version,
		"protocol":     r.Config.A2A.ProtocolVersion,
		"capabilities": len(r.toolToPlug),
	}
}

func (r *ADKRuntime) BuildHITLRequest(token string, step PlanStep, rationale string) map[string]any {
	if r == nil {
		return map[string]any{}
	}
	pluginCfg, _ := r.PluginForAction(step.Action)
	return map[string]any{
		"framework":        r.Config.Runtime.Framework,
		"confirmation":     "required",
		"approvalToken":    token,
		"action":           step.Action,
		"plugin":           pluginCfg.Name,
		"risk":             string(step.Risk),
		"rationale":        strings.TrimSpace(rationale),
		"allowedActions":   append([]string(nil), r.Config.HITL.AllowedActions...),
		"expiresInMinutes": r.Config.HITL.ConfirmationWindowMinutes,
	}
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
		}
	}
	meta := map[string]any{
		"enabled":          true,
		"framework":        r.Config.Runtime.Framework,
		"runtimeName":      r.Config.Runtime.Name,
		"runtimeVersion":   r.Config.Runtime.Version,
		"agentId":          r.Config.Runtime.AgentID,
		"configMode":       r.Config.Runtime.ConfigMode,
		"configPath":       r.ConfigPath,
		"pluginCount":      len(r.toolToPlug),
		"runnerReady":      r.Runner != nil,
		"agentReady":       r.Agent != nil,
		"subAgents":        r.subAgentNames(),
		"toolCount":        r.ToolCount,
		"initError":        r.InitError,
		"openTelemetry":    r.Config.Telemetry.OpenTelemetry,
		"hitlEnabled":      r.Config.HITL.Enabled,
		"a2aEnabled":       r.Config.A2A.Enabled,
		"a2aProtocol":      r.Config.A2A.ProtocolVersion,
		"agentCardExposed": r.Config.A2A.ExposeAgentCard,
	}
	return meta
}
