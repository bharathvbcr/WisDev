package cli

import (
	"bytes"
	"strings"
	"testing"

	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

func TestPrintYOLOResultQuiet(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := printYOLOResult(&stdout, &stderr, &agent.YOLOResult{
		FinalAnswer: "Evidence supports RAG.",
		Iterations:  2,
		PapersFound: 3,
		Converged:   true,
	}, true, false)
	if err != nil {
		t.Fatalf("printYOLOResult: %v", err)
	}
	if stdout.String() != "Evidence supports RAG.\n" {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestPrintYOLOResultVerbose(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := printYOLOResult(&stdout, &stderr, &agent.YOLOResult{
		FinalAnswer:     "Done.",
		Iterations:      1,
		PapersFound:     1,
		ExecutedQueries: []string{"rag scientific literature"},
		Papers:          []agent.Paper{{Title: "Test Paper", Year: 2024}},
	}, false, true)
	if err != nil {
		t.Fatalf("printYOLOResult: %v", err)
	}
	if !strings.Contains(stderr.String(), "Executed queries") {
		t.Fatalf("expected verbose stderr, got %q", stderr.String())
	}
}
