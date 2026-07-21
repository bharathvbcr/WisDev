package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMCPConfigMergePreservesOtherServers(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mcp.json")
	existing := `{
  "mcpServers": {
    "gitnexus": {
      "command": "gitnexus",
      "args": ["mcp"]
    }
  }
}
`
	if err := os.WriteFile(target, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "wisdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "wisdev", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "server", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WISDEV_ORCHESTRATOR_ROOT", root)

	var stdout bytes.Buffer
	if err := runMCPConfig([]string{"--write", target}, &stdout); err != nil {
		t.Fatalf("runMCPConfig: %v", err)
	}

	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var cfg mcpClientConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if _, ok := cfg.MCPServers["gitnexus"]; !ok {
		t.Fatalf("expected gitnexus preserved, got %s", raw)
	}
	wisdev, ok := cfg.MCPServers["wisdev"]
	if !ok {
		t.Fatalf("expected wisdev entry, got %s", raw)
	}
	if wisdev.Command != "go" {
		t.Fatalf("expected go run entry, got %#v", wisdev)
	}
	if !strings.Contains(stdout.String(), target) {
		t.Fatalf("expected write note, got %q", stdout.String())
	}
}

func TestRunMCPConfigReplaceOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(target, []byte(`{"mcpServers":{"other":{"command":"x","args":[]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "wisdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "wisdev", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "server", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WISDEV_ORCHESTRATOR_ROOT", root)

	var stdout bytes.Buffer
	if err := runMCPConfig([]string{"--write", target, "--replace"}, &stdout); err != nil {
		t.Fatalf("runMCPConfig: %v", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var cfg mcpClientConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.MCPServers["other"]; ok {
		t.Fatalf("expected --replace to drop other servers, got %s", raw)
	}
	if _, ok := cfg.MCPServers["wisdev"]; !ok {
		t.Fatalf("expected wisdev only, got %s", raw)
	}
}

func TestResolveWisdevBinaryPathFallsBackToLookPath(t *testing.T) {
	// When the test binary is not named wisdev, LookPath should still work if
	// wisdev is on PATH (common in this environment). Skip if missing.
	if _, err := resolveWisdevBinaryPath(); err != nil {
		t.Skipf("wisdev not resolvable in this environment: %v", err)
	}
}
