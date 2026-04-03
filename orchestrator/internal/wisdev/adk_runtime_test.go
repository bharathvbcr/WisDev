package wisdev

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadADKRuntimeFromPathMergesYAMLAndRegistry(t *testing.T) {
	t.Helper()

	registry := NewToolRegistry()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "wisdev-adk.yaml")
	payload := []byte(`
runtime:
  name: TestWisDev
  version: '1.0'
  framework: google-adk-go
hitl:
  confirmationWindowMinutes: 7
plugins:
  - name: custom-go
    tools:
      - research.retrievePapers
`)
	if err := os.WriteFile(configPath, payload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadADKRuntimeFromPath(configPath, registry)
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	if runtime.Config.Runtime.Name != "TestWisDev" {
		t.Fatalf("expected runtime name override, got %q", runtime.Config.Runtime.Name)
	}
	if runtime.Config.HITL.ConfirmationWindowMinutes != 7 {
		t.Fatalf("expected confirmation window 7, got %d", runtime.Config.HITL.ConfirmationWindowMinutes)
	}
	plugin, ok := runtime.PluginForAction("research.retrievePapers")
	if !ok {
		t.Fatalf("expected plugin for retrieve papers")
	}
	if plugin.Name != "custom-go" {
		t.Fatalf("expected custom-go plugin, got %q", plugin.Name)
	}
}

func TestBuildHITLRequestUsesConfiguredActions(t *testing.T) {
	runtime := &ADKRuntime{
		Config: DefaultADKRuntimeConfig(),
		toolToPlug: map[string]ADKPluginConfig{
			"research.coordinateReplan": {
				Name: "planner-plugin",
			},
		},
	}
	step := PlanStep{
		ID:      "step-1",
		Action:  "research.coordinateReplan",
		Risk:    RiskLevelMedium,
	}
	request := runtime.BuildHITLRequest("token-1", step, "review replanning step")
	if request["plugin"] != "planner-plugin" {
		t.Fatalf("expected planner-plugin, got %#v", request["plugin"])
	}
	allowed, ok := request["allowedActions"].([]string)
	if !ok {
		t.Fatalf("expected []string allowedActions, got %T", request["allowedActions"])
	}
	if len(allowed) == 0 || allowed[0] != "approve" {
		t.Fatalf("unexpected allowed actions %#v", allowed)
	}
}

func TestHITLTimeout(t *testing.T) {
	runtime := &ADKRuntime{
		Config: ADKRuntimeConfig{
			HITL: ADKHITLConfig{
				ConfirmationWindowMinutes: 15,
			},
		},
	}
	if runtime.HITLTimeout() != 15*time.Minute {
		t.Fatalf("expected 15m, got %v", runtime.HITLTimeout())
	}
	
	runtime = nil
	if runtime.HITLTimeout() != 10*time.Minute {
		t.Fatalf("expected default 10m for nil, got %v", runtime.HITLTimeout())
	}
}

func TestBuildA2ACard(t *testing.T) {
	runtime := &ADKRuntime{
		Config: ADKRuntimeConfig{
			Runtime: ADKRuntimeDescriptor{
				Name:    "WisDev",
				Version: "1.0",
				AgentID: "wisdev-orchestrator",
			},
			A2A: ADKA2AConfig{
				Enabled:         true,
				ProtocolVersion: "refined",
				ExposeAgentCard: true,
			},
		},
		toolToPlug: map[string]ADKPluginConfig{"t1": {}, "t2": {}},
	}
	card := runtime.BuildA2ACard()
	if card["agentId"] != "wisdev-orchestrator" || card["protocol"] != "refined" {
		t.Fatalf("unexpected card: %+v", card)
	}
	if card["capabilities"] != 2 {
		t.Fatalf("expected 2 capabilities, got %v", card["capabilities"])
	}

	runtime.Config.A2A.ExposeAgentCard = false
	if runtime.BuildA2ACard() != nil {
		t.Fatalf("expected nil card when exposed=false")
	}
}

func TestMetadataNilSafety(t *testing.T) {
	var runtime *ADKRuntime
	meta := runtime.Metadata()
	if meta["enabled"] != false {
		t.Fatalf("expected enabled=false for nil runtime")
	}
}
