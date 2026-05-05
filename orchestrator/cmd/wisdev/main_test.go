package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunPrintsHelp(t *testing.T) {
	var stdout bytes.Buffer
	if err := run([]string{"--help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "wisdev yolo --local") {
		t.Fatalf("expected local YOLO usage, got %q", stdout.String())
	}
}

func TestRunYOLOLocalRequiresTask(t *testing.T) {
	err := run([]string{"yolo", "--local"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected missing task error")
	}
	if !strings.Contains(err.Error(), "missing YOLO task") {
		t.Fatalf("expected missing task error, got %v", err)
	}
}

func TestRunYOLOLocalOfflineJSON(t *testing.T) {
	var stdout bytes.Buffer
	err := run([]string{
		"yolo",
		"--local",
		"--offline",
		"--json",
		"--max-iterations", "1",
		"--max-search-terms", "1",
		"--hits-per-search", "1",
		"--disable-planning",
		"--disable-hypotheses",
		"map evidence for open source research agents",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	output := stdout.String()
	for _, fragment := range []string{`"iterations": 1`, `"papersFound": 0`} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("expected output to contain %s, got %q", fragment, output)
		}
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" openalex, arxiv ,,pubmed ")
	want := []string{"openalex", "arxiv", "pubmed"}
	if len(got) != len(want) {
		t.Fatalf("expected %d providers, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("provider %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}
