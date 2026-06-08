package wisdev

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestToolRegistry_YoloDefaultPlanActionsAreExecutable(t *testing.T) {
	registry := NewToolRegistry()
	capabilities := BuildDeepAgentsCapabilities(registry)
	session := &AgentSession{
		SessionID:     "session-yolo-actions",
		OriginalQuery: "test autonomous research",
		Mode:          WisDevModeYOLO,
	}
	plan := BuildDefaultPlan(session)
	require.NotNil(t, plan)

	for _, step := range plan.Steps {
		tool, err := registry.Get(step.Action)
		require.NoError(t, err, "YOLO plan action must be registered: %s", step.Action)
		require.Equal(t, step.ExecutionTarget, tool.ExecutionTarget, "registered execution target drift for %s", step.Action)
		require.Contains(t, capabilities.WisdevActions, step.Action, "YOLO plan action must be capability-allowlisted: %s", step.Action)
	}
}

func TestADKConfig_YoloDefaultPlanActionsAreExplicitlyAllowlisted(t *testing.T) {
	registry := NewToolRegistry()
	runtime, err := LoadADKRuntimeFromPath(filepath.Join("..", "..", "config", "wisdev-adk.yaml"), registry)
	require.NoError(t, err)
	require.NotNil(t, runtime)

	configuredTools := map[string]ADKPluginConfig{}
	for _, plugin := range runtime.Config.Plugins {
		for _, tool := range plugin.Tools {
			configuredTools[CanonicalizeWisdevAction(tool)] = plugin
		}
	}

	session := &AgentSession{
		SessionID:     "session-yolo-actions",
		OriginalQuery: "test autonomous research",
		Mode:          WisDevModeYOLO,
	}
	plan := BuildDefaultPlan(session)
	require.NotNil(t, plan)

	for _, step := range plan.Steps {
		plugin, ok := configuredTools[step.Action]
		require.True(t, ok, "YOLO plan action must be explicitly listed in wisdev-adk.yaml: %s", step.Action)
		require.Contains(t, plugin.ExecutionTargets, string(step.ExecutionTarget), "ADK plugin execution target drift for %s", step.Action)
	}
}

func TestAgentGateway_ProgrammaticLoopExecutorRoutesYoloGoNativeActionThroughRegistry(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gw := NewAgentGateway(nil, nil, nil)
	gw.ADKRuntime = nil
	gw.Brain = NewBrainCapabilities(client)
	gw.LLMClient = client
	gw.Registry = NewToolRegistry()

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.Model != "" &&
			strings.Contains(req.Prompt, "Resolve canonical citations") &&
			strings.Contains(req.Prompt, "Graph Neural Scaling Laws")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"resolved":[{"id":"paper-1","canonicalId":"10.1234/gnn"}]}`}, nil).Once()

	result, err := gw.ProgrammaticLoopExecutor()(context.Background(), ActionResearchResolveCanonicalCitations, map[string]any{
		"papers": []any{
			map[string]any{"id": "paper-1", "title": "Graph Neural Scaling Laws", "doi": "10.1234/gnn"},
		},
	}, &AgentSession{SessionID: "session-yolo-actions"})

	require.NoError(t, err)
	require.NotEmpty(t, result["resolved"])
	mockLLM.AssertExpectations(t)
}

func TestAgentGateway_DefaultPythonExecutorHandlesYoloGoNativeActionsDefensively(t *testing.T) {
	mockLLM := new(mockLLMServiceClient)
	client := llm.NewClient()
	client.SetClient(mockLLM)

	gw := &AgentGateway{
		Brain:     NewBrainCapabilities(client),
		LLMClient: client,
	}

	mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return req != nil &&
			req.Model != "" &&
			strings.Contains(req.Prompt, "Resolve canonical citations") &&
			strings.Contains(req.Prompt, "Graph Neural Scaling Laws")
	})).Return(&llmv1.StructuredResponse{JsonResult: `{"resolved":[{"id":"paper-1","canonicalId":"10.1234/gnn"}]}`}, nil).Once()

	citations, err := gw.defaultPythonExecutor(context.Background(), ActionResearchResolveCanonicalCitations, map[string]any{
		"papers": []any{
			map[string]any{"id": "paper-1", "title": "Graph Neural Scaling Laws", "doi": "10.1234/gnn"},
		},
	}, &AgentSession{SessionID: "session-yolo-actions"})
	require.NoError(t, err)
	require.NotEmpty(t, citations["resolved"])

	reasoningGateway := &AgentGateway{
		Brain: NewBrainCapabilities(nil),
	}
	reasoning, err := reasoningGateway.defaultPythonExecutor(context.Background(), ActionResearchVerifyReasoningPaths, map[string]any{
		"branches": []any{
			map[string]any{
				"id":            "branch-1",
				"evidenceCount": 2,
				"findings": []any{
					map[string]any{"sourceId": "paper-1"},
					map[string]any{"sourceId": "paper-2"},
				},
			},
		},
	}, &AgentSession{SessionID: "session-yolo-actions"})
	require.NoError(t, err)
	require.Equal(t, true, reasoning["readyForSynthesis"])
	mockLLM.AssertExpectations(t)
}

func TestAgentGateway_ProgrammaticLoopExecutorRoutesYoloReasoningGateThroughRegistry(t *testing.T) {
	gw := NewAgentGateway(nil, nil, nil)
	gw.ADKRuntime = nil
	gw.Brain = NewBrainCapabilities(nil)
	gw.Registry = NewToolRegistry()

	result, err := gw.ProgrammaticLoopExecutor()(context.Background(), ActionResearchVerifyReasoningPaths, map[string]any{
		"branches": []any{
			map[string]any{
				"id":                 "branch-1",
				"claim":              "architecture advances require retrieval grounding",
				"evidenceCount":      2,
				"contradictionCount": 0,
				"findings": []any{
					map[string]any{"sourceId": "paper-1", "snippet": "supported"},
					map[string]any{"sourceId": "paper-2", "snippet": "replicated"},
				},
			},
		},
	}, &AgentSession{SessionID: "session-yolo-actions"})

	require.NoError(t, err)
	require.Equal(t, true, result["readyForSynthesis"])
}
