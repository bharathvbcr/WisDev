package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunPrintsHelp(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run([]string{"--help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, fragment := range []string{"wisdev search", "wisdev run search:", "wisdev yolo --local"} {
		if !strings.Contains(stdout.String(), fragment) {
			t.Fatalf("expected usage to contain %q, got %q", fragment, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "wisde run") {
		t.Fatalf("expected help to expose only wisdev commands, got %q", stdout.String())
	}
}

func TestSearchShortcutRequiresTask(t *testing.T) {
	err := Run([]string{"search"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected missing task error")
	}
	if !strings.Contains(err.Error(), "missing YOLO task") {
		t.Fatalf("expected missing task error, got %v", err)
	}
}

func TestSearchShortcutOfflineJSON(t *testing.T) {
	var stdout bytes.Buffer
	err := Run([]string{
		"search",
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

func TestRunShortcutRequiresTask(t *testing.T) {
	err := Run([]string{"run", "search:"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected missing task error")
	}
	if !strings.Contains(err.Error(), "missing YOLO task") {
		t.Fatalf("expected missing task error, got %v", err)
	}
}

func TestRunShortcutSearchOfflineJSON(t *testing.T) {
	var stdout bytes.Buffer
	err := Run([]string{
		"run",
		"--offline",
		"--json",
		"--max-iterations", "1",
		"--max-search-terms", "1",
		"--hits-per-search", "1",
		"--disable-planning",
		"--disable-hypotheses",
		"search:",
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

func TestNormalizeRunArgsStripsSearchPrefix(t *testing.T) {
	got := normalizeRunArgs([]string{"--max-iterations", "1", "--offline", "search:map", "agent", "evidence"})
	want := []string{"--max-iterations", "1", "--offline", "map", "agent", "evidence"}
	if len(got) != len(want) {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestRunYOLOLocalRequiresTask(t *testing.T) {
	err := Run([]string{"yolo", "--local"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected missing task error")
	}
	if !strings.Contains(err.Error(), "missing YOLO task") {
		t.Fatalf("expected missing task error, got %v", err)
	}
}

func TestRunYOLOLocalOfflineJSON(t *testing.T) {
	var stdout bytes.Buffer
	err := Run([]string{
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
