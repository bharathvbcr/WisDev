package wisdev

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
	"google.golang.org/adk/agent"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
)

func TestWisDevWorkflowAgent_Run(t *testing.T) {
	registry := NewToolRegistry()
	policyCfg := policy.DefaultPolicyConfig()
	var llmClient *llm.Client
	var rdb redis.UniversalClient
	pythonExecute := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		return map[string]any{"success": true}, nil
	}
	adkRuntime := &ADKRuntime{}
	executor := NewPlanExecutor(registry, policyCfg, llmClient, nil, rdb, pythonExecute, adkRuntime)


	gateway := &AgentGateway{
		Registry: registry,
		Executor: executor,
		Store:    NewInMemorySessionStore(),
	}

	subAgent, err := agent.New(agent.Config{
		Name:        "delegate-agent",
		Description: "test delegate",
	})
	if err != nil {
		t.Fatalf("failed to create sub-agent: %v", err)
	}

	wa, err := NewWisDevWorkflowAgent(gateway, executor, []agent.Agent{subAgent})
	if err != nil {
		t.Fatalf("failed to create workflow agent: %v", err)
	}

	if wa.Name() != "wisdev-workflow" {
		t.Errorf("expected name wisdev-workflow, got %s", wa.Name())
	}
	if len(wa.SubAgents()) != 1 || wa.SubAgents()[0].Name() != "delegate-agent" {
		t.Fatalf("expected workflow agent to retain configured sub-agents, got %#v", wa.SubAgents())
	}
}
