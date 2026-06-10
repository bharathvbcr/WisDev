package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

func writeRunFile(t *testing.T, dir, name, query string, mtime time.Time) string {
	t.Helper()
	payload := tuiResultExport{
		Query:      query,
		ElapsedSec: 2.5,
		Result:     &agent.YOLOResult{FinalAnswer: "answer for " + query, Iterations: 1, PapersFound: 0},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal run file: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write run file: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

func TestListRecentRunFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 7; i++ {
		writeRunFile(t, dir, fmt.Sprintf("wisdev-result-2026010%d-010101.json", i+1),
			"query "+string(rune('a'+i)), base.Add(time.Duration(i)*time.Minute))
	}
	// Distractors: invalid JSON and non-matching names must be skipped/ignored.
	if err := os.WriteFile(filepath.Join(dir, "wisdev-result-bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := listRecentRunFiles(dir, maxRecentRuns)
	if len(entries) != maxRecentRuns {
		t.Fatalf("expected %d entries, got %d", maxRecentRuns, len(entries))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].ModTime.After(entries[i-1].ModTime) {
			t.Fatalf("entries not sorted most-recent-first: %v", entries)
		}
	}
	if entries[0].Query != "query g" {
		t.Fatalf("expected newest run first with parsed query, got %q", entries[0].Query)
	}
}

func TestListRecentRunFilesEmptyDir(t *testing.T) {
	if entries := listRecentRunFiles(t.TempDir(), 5); entries != nil {
		t.Fatalf("expected nil for empty dir, got %v", entries)
	}
}

func TestParseRecentRunFileRejectsMissingResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wisdev-result-empty.json")
	if err := os.WriteFile(path, []byte(`{"query":"q"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := parseRecentRunFile(path); err == nil {
		t.Fatal("expected error for export without result payload")
	}
}

func TestOpenRecentRunLoadsResultsView(t *testing.T) {
	dir := t.TempDir()
	path := writeRunFile(t, dir, "wisdev-result-20260101-010101.json", "saved question", time.Now())

	s := &tuiState{mode: modeInput, resultPane: resultPaneSources, scrollOffset: 9}
	s.openRecentRun(recentRunEntry{Path: path, Query: "saved question", ModTime: time.Now()})

	if s.mode != modeResults {
		t.Fatalf("expected results mode, got %v", s.mode)
	}
	if s.result == nil || s.result.FinalAnswer != "answer for saved question" {
		t.Fatalf("unexpected loaded result: %+v", s.result)
	}
	if s.runningTask != "saved question" {
		t.Fatalf("runningTask = %q", s.runningTask)
	}
	if s.completedElapsed != 2500*time.Millisecond {
		t.Fatalf("completedElapsed = %v", s.completedElapsed)
	}
	if s.resultPane != resultPaneAll || s.scrollOffset != 0 {
		t.Fatalf("expected reset pane/scroll, got pane=%v offset=%d", s.resultPane, s.scrollOffset)
	}
}

func TestOpenRecentRunBadFileKeepsInputMode(t *testing.T) {
	s := &tuiState{mode: modeInput}
	s.openRecentRun(recentRunEntry{Path: filepath.Join(t.TempDir(), "missing.json")})
	if s.mode != modeInput {
		t.Fatalf("expected to stay in input mode, got %v", s.mode)
	}
	if s.saveMsg == "" {
		t.Fatal("expected an error message")
	}
}

func TestRecentRunLabel(t *testing.T) {
	entry := recentRunEntry{
		Path:    filepath.Join("x", "wisdev-result-20260101-010101.json"),
		Query:   "meniscus repair strategies",
		ModTime: time.Date(2026, 6, 9, 14, 30, 0, 0, time.UTC),
	}
	label := recentRunLabel(entry, 80)
	if label != "[2026-06-09 14:30] meniscus repair strategies" {
		t.Fatalf("label = %q", label)
	}
	entry.Query = ""
	if label := recentRunLabel(entry, 80); label != "[2026-06-09 14:30] wisdev-result-20260101-010101.json" {
		t.Fatalf("fallback label = %q", label)
	}
}
