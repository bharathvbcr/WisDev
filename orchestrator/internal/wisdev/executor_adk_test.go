package wisdev

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"google.golang.org/adk/agent"
)

func TestPlanExecutor_ADKIntegration(t *testing.T) {
	t.Setenv(allowGoCitationFallbackEnv, "true")

	// Setup a minimal ADK runtime
	runtime := &ADKRuntime{
		Config: ADKRuntimeConfig{
			Telemetry: ADKTelemetryConfig{Namespace: "test-ns"},
			Runtime:   ADKRuntimeDescriptor{Framework: "test-fw"},
		},
	}

	e := &PlanExecutor{
		adkRuntime:       runtime,
		brainCaps:        NewBrainCapabilities(nil),
		maxParallelLanes: 1,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun: true,
		},
	}

	session := &AgentSession{
		SessionID:     "s1",
		OriginalQuery: "test query",
		Status:        SessionGeneratingTree,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID: "p1",
			Steps: []PlanStep{
				{
					ID:              "s1",
					Action:          ActionResearchVerifyReasoningPaths,
					ExecutionTarget: ExecutionTargetGoNative,
					Risk:            RiskLevelLow,
					Params: map[string]any{
						"branches": []any{
							map[string]any{
								"id":                 "branch-1",
								"claim":              "supported branch",
								"supportScore":       0.91,
								"evidenceCount":      2,
								"contradictionCount": 0,
								"findings": []any{
									map[string]any{"claim": "supported branch", "source_id": "paper-1", "snippet": "primary support"},
									map[string]any{"claim": "supported branch", "source_id": "paper-2", "snippet": "replication support"},
								},
							},
						},
					},
				},
			},
			ApprovedStepIDs: make(map[string]bool),
		},
	}

	out := make(chan PlanExecutionEvent, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go e.Execute(ctx, session, out)

	foundStarted := false
	foundCompleted := false
	for ev := range out {
		if ev.Type == EventStepStarted {
			foundStarted = true
		}
		if ev.Type == EventCompleted {
			foundCompleted = true
			cancel()
		}
		if ev.Type == EventStepFailed {
			// Fail early if we hit unexpected failure
			t.Logf("Unexpected step failure: %s", ev.Message)
		}
	}
	assert.True(t, foundStarted)
	assert.True(t, foundCompleted)
	assert.Equal(t, SessionComplete, session.Status)
}

func TestPlanExecutor_ADK_HITL(t *testing.T) {
	t.Setenv(allowGoCitationFallbackEnv, "true")

	// Setup a minimal ADK runtime
	runtime := &ADKRuntime{
		Config: ADKRuntimeConfig{
			Telemetry: ADKTelemetryConfig{Namespace: "test-ns"},
			HITL: ADKHITLConfig{
				Enabled:                   true,
				ConfirmationWindowMinutes: 5,
				AllowedActions:            []string{"approve"},
			},
		},
	}

	e := &PlanExecutor{
		adkRuntime:       runtime,
		maxParallelLanes: 1,
		policyConfig: policy.PolicyConfig{
			AlwaysConfirmHighRisk: true,
		},
	}

	session := &AgentSession{
		SessionID:     "s1",
		OriginalQuery: "test query",
		Status:        SessionGeneratingTree,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID: "p1",
			Steps: []PlanStep{
				{ID: "s1", Action: "risky", Risk: RiskLevelHigh, ExecutionTarget: ExecutionTargetPythonCapability},
			},
			ApprovedStepIDs: make(map[string]bool),
		},
	}

	out := make(chan PlanExecutionEvent, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go e.Execute(ctx, session, out)

	foundConfirmation := false
	for ev := range out {
		if ev.Type == EventConfirmationNeed {
			foundConfirmation = true
			assert.Equal(t, "required", ev.Payload["confirmation"])
			assert.Equal(t, 5, ev.Payload["expiresInMinutes"])
			cancel()
		}
	}
	assert.True(t, foundConfirmation)
	assert.Equal(t, SessionPaused, session.Status)
}

func TestPlanExecutor_ADKDelegatedPythonStepCarriesOwnershipAndFusionMetadata(t *testing.T) {

	root := new(mockADKAgent)
	pythonResearcher := new(mockADKAgent)
	root.On("SubAgents").Return([]agent.Agent{pythonResearcher})
	pythonResearcher.On("Name").Return("python-researcher")

	runtime := &ADKRuntime{
		Agent: root,
		toolToPlug: map[string]ADKPluginConfig{
			"research.search": {Name: "python-capability-tools", Tools: []string{"research.search"}},
		},
	}

	var capturedPayload map[string]any
	e := &PlanExecutor{
		adkRuntime: runtime,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun: true,
		},
		pythonExecute: func(_ context.Context, action string, payload map[string]any, _ *AgentSession) (map[string]any, error) {
			require.Equal(t, "research.search", action)
			capturedPayload = cloneAnyMap(payload)
			return map[string]any{
				"confidence": 0.77,
				"sources": []any{
					map[string]any{"id": "paper-1", "title": "Delegated Paper", "source": "crossref"},
				},
			}, nil
		},
	}

	result := e.RunStepWithRecovery(context.Background(), &AgentSession{
		SessionID:     "delegated-session",
		OriginalQuery: "delegated query",
		Budget:        policy.BudgetState{MaxToolCalls: 10, MaxCostCents: 1000},
	}, PlanStep{
		ID:              "delegated-step",
		Action:          "research.search",
		ExecutionTarget: ExecutionTargetPythonCapability,
		Risk:            RiskLevelLow,
		Params:          map[string]any{"query": "delegated query"},
	}, 1)

	require.NoError(t, result.Err)
	require.NotNil(t, capturedPayload)
	assert.Equal(t, true, capturedPayload["delegated"])
	assert.Equal(t, "python-capability-tools", capturedPayload["delegatedPlugin"])
	assert.Equal(t, "python-researcher", capturedPayload["delegatedSubAgent"])
	assert.Equal(t, "adk_delegate", capturedPayload["resultOrigin"])
	assert.Equal(t, "delegated_result_fusion", capturedPayload["resultFusionIntent"])
	assert.Equal(t, "python-researcher", result.Owner)
	assert.Equal(t, "python-researcher", result.SubAgent)
	assert.Equal(t, "adk_runtime", result.OwningComponent)
	assert.Equal(t, "adk_delegate", result.ResultOrigin)
	assert.Equal(t, "delegated_result_fusion", result.ResultFusionIntent)
	assert.Len(t, result.Sources, 1)
	assert.InDelta(t, 0.77, result.Confidence, 0.001)
}

func TestPlanExecutor_ADKDelegatedStepUsesRuntimeDispatcherWhenBound(t *testing.T) {

	root := new(mockADKAgent)
	pythonResearcher := new(mockADKAgent)
	root.On("SubAgents").Return([]agent.Agent{pythonResearcher})
	pythonResearcher.On("Name").Return("python-researcher")

	dispatchCalled := false
	runtime := &ADKRuntime{
		Agent: root,
		toolToPlug: map[string]ADKPluginConfig{
			"research.search": {Name: "python-capability-tools", Tools: []string{"research.search"}},
		},
		delegateExecutor: func(_ context.Context, action string, payload map[string]any, _ *AgentSession) (map[string]any, error) {
			dispatchCalled = true
			require.Equal(t, "research.search", action)
			require.Equal(t, true, payload["adkDelegatedExecution"])
			require.Equal(t, "python-researcher", payload["adkSubAgent"])
			return map[string]any{
				"confidence": 0.82,
				"sources": []any{
					map[string]any{"id": "paper-runtime", "title": "Runtime Delegated Paper", "source": "adk"},
				},
			}, nil
		},
	}

	e := &PlanExecutor{
		adkRuntime: runtime,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun: true,
		},
		pythonExecute: func(context.Context, string, map[string]any, *AgentSession) (map[string]any, error) {
			t.Fatal("pythonExecute should not be called when ADK runtime dispatcher is bound")
			return nil, nil
		},
	}

	result := e.RunStepWithRecovery(context.Background(), &AgentSession{
		SessionID:     "delegated-runtime-session",
		OriginalQuery: "delegated query",
		Budget:        policy.BudgetState{MaxToolCalls: 10, MaxCostCents: 1000},
	}, PlanStep{
		ID:              "delegated-runtime-step",
		Action:          "research.search",
		ExecutionTarget: ExecutionTargetPythonCapability,
		Risk:            RiskLevelLow,
		Params:          map[string]any{"query": "delegated query"},
	}, 1)

	require.NoError(t, result.Err)
	assert.True(t, dispatchCalled)
	assert.Equal(t, "adk_delegate", result.ResultOrigin)
	assert.Len(t, result.Sources, 1)
	assert.InDelta(t, 0.82, result.Confidence, 0.001)
}
