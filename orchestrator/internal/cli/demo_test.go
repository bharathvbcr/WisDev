package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunDemoOfflineSmoke(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run([]string{
		"demo",
		"--offline",
		"--query",
		"map open source research agent evidence",
		"--skip-doctor",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !strings.Contains(stderr.String(), "Scene 3") {
		t.Fatalf("expected demo scenes on stderr, got %q", stderr.String())
	}
}

func TestRunDemoRequiresQueryWhenEmpty(t *testing.T) {
	err := Run([]string{"demo", "--query", "   "}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected empty query error")
	}
}
