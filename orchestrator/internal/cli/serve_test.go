package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindOrchestratorRootFromEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "wisdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "server", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "wisdev", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WISDEV_ORCHESTRATOR_ROOT", root)
	got, err := findOrchestratorRoot()
	if err != nil {
		t.Fatalf("findOrchestratorRoot: %v", err)
	}
	if got != root {
		t.Fatalf("expected %q, got %q", root, got)
	}
}

func TestRunServeStartsProcessWhenRootFound(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "wisdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "server", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "wisdev", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WISDEV_ORCHESTRATOR_ROOT", root)
	var seenRoot string
	original := runServeProcess
	runServeProcess = func(foundRoot string, stdout, stderr io.Writer) error {
		seenRoot = foundRoot
		return nil
	}
	t.Cleanup(func() {
		runServeProcess = original
	})

	if err := runServe(&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runServe: %v", err)
	}
	if seenRoot != root {
		t.Fatalf("expected serve root %q, got %q", root, seenRoot)
	}
}

func TestRunMCPConfigWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "wisdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "server", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "wisdev", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WISDEV_ORCHESTRATOR_ROOT", root)
	outDir := t.TempDir()
	target := filepath.Join(outDir, ".cursor", "mcp.json")
	var stdout bytes.Buffer
	if err := runMCPConfig([]string{"--write", target}, &stdout); err != nil {
		t.Fatalf("runMCPConfig: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(body)
	if !strings.Contains(content, `"wisdev"`) || !strings.Contains(content, filepath.ToSlash(root)) {
		t.Fatalf("unexpected config: %s", content)
	}
}

func TestPrintServeInstructions(t *testing.T) {
	var stdout bytes.Buffer
	err := printServeInstructions(&stdout, errors.New("missing root"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stdout.String(), "make serve") {
		t.Fatalf("expected instructions, got %q", stdout.String())
	}
}
