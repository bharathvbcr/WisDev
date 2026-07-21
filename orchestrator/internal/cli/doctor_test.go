package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunDoctorJSON(t *testing.T) {
	var stdout bytes.Buffer
	if err := runDoctor([]string{"--json"}, &stdout); err != nil {
		t.Fatalf("runDoctor: %v", err)
	}
	output := stdout.String()
	for _, fragment := range []string{`"id": "search-providers"`, `"id": "mcp-tools"`, `"id": "orchestrator"`} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("expected %s in output: %s", fragment, output)
		}
	}
}

func TestRunProvidersListsNames(t *testing.T) {
	var stdout bytes.Buffer
	if err := runProviders(nil, &stdout); err != nil {
		t.Fatalf("runProviders: %v", err)
	}
	if !strings.Contains(stdout.String(), "openalex") || !strings.Contains(stdout.String(), "arxiv") {
		t.Fatalf("expected provider names, got %q", stdout.String())
	}
}

func TestRunVersion(t *testing.T) {
	Version = "1.2.3-test"
	var stdout bytes.Buffer
	if err := runVersion(&stdout); err != nil {
		t.Fatalf("runVersion: %v", err)
	}
	if !strings.Contains(stdout.String(), "wisdev 1.2.3-test") {
		t.Fatalf("unexpected version output: %q", stdout.String())
	}
}

func TestRunHelpMCP(t *testing.T) {
	var stdout bytes.Buffer
	if err := runHelp([]string{"mcp"}, &stdout); err != nil {
		t.Fatalf("runHelp: %v", err)
	}
	if !strings.Contains(stdout.String(), "wisdev mcp") {
		t.Fatalf("expected mcp help, got %q", stdout.String())
	}
}

func TestRunMCPWithIOOffline(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	var stdout bytes.Buffer
	if err := runMCPWithIO([]string{"--offline"}, strings.NewReader(input), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runMCPWithIO: %v", err)
	}
	if !strings.Contains(stdout.String(), "wisdevSearchPapers") {
		t.Fatalf("expected tools/list response, got %q", stdout.String())
	}
}
