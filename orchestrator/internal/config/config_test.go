package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wisdev.yaml")
	content := `
agent:
  mode: autonomous
  max_steps: 20
  token_budget: 50000
llm:
  provider: openai
  model: gpt-4o
  api_key: sk-test123
storage:
  type: memory
observability:
  enable_otel: false
server:
  http_port: 9090
  grpc_port: 50053
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Agent.Mode != "autonomous" {
		t.Errorf("expected mode autonomous, got %s", cfg.Agent.Mode)
	}
	if cfg.Agent.MaxSteps != 20 {
		t.Errorf("expected max_steps 20, got %d", cfg.Agent.MaxSteps)
	}
	if cfg.Agent.TokenBudget != 50000 {
		t.Errorf("expected token_budget 50000, got %d", cfg.Agent.TokenBudget)
	}
	if cfg.LLM.Model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", cfg.LLM.Model)
	}
	if cfg.Server.HTTPPort != 9090 {
		t.Errorf("expected http_port 9090, got %d", cfg.Server.HTTPPort)
	}
	if cfg.Server.GRPCPort != 50053 {
		t.Errorf("expected grpc_port 50053, got %d", cfg.Server.GRPCPort)
	}
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wisdev.yaml")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Agent.Mode != "autonomous" {
		t.Errorf("expected default mode autonomous, got %s", cfg.Agent.Mode)
	}
	if cfg.Agent.MaxSteps != 15 {
		t.Errorf("expected default max_steps 15, got %d", cfg.Agent.MaxSteps)
	}
	if cfg.Server.HTTPPort != 8081 {
		t.Errorf("expected default http_port 8081, got %d", cfg.Server.HTTPPort)
	}
	if cfg.Storage.Type != "memory" {
		t.Errorf("expected default storage type memory, got %s", cfg.Storage.Type)
	}
}

func TestLoadEnvVarResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wisdev.yaml")
	os.Setenv("TEST_API_KEY", "resolved-key")
	defer os.Unsetenv("TEST_API_KEY")

	content := `
llm:
  api_key: ${TEST_API_KEY}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.LLM.APIKey != "resolved-key" {
		t.Errorf("expected resolved api key, got %s", cfg.LLM.APIKey)
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/wisdev.yaml")
	if err == nil {
		t.Error("expected error for missing config file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wisdev.yaml")
	if err := os.WriteFile(path, []byte(`{{{invalid`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadOrGet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wisdev.yaml")
	content := `
agent:
  mode: guided
llm:
  model: claude-3
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadOrGet(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Agent.Mode != "guided" {
		t.Errorf("expected mode guided, got %s", cfg.Agent.Mode)
	}
}
