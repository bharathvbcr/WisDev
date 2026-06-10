package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

func TestFormatYOLOResultMarkdown(t *testing.T) {
	doc := formatYOLOResultMarkdown("RAG papers", &agent.YOLOResult{
		FinalAnswer:     "Evidence supports retrieval.",
		Iterations:      2,
		PapersFound:     3,
		StopReason:      "coverage_satisfied",
		ExecutedQueries: []string{"rag scientific literature"},
	}, 4*time.Second, nil, nil)

	for _, want := range []string{
		"# WisDev Research Result",
		"**Question:** RAG papers",
		"## Final answer",
		"Evidence supports retrieval.",
		"rag scientific literature",
		"**Elapsed:** 4.0s",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("expected markdown to contain %q:\n%s", want, doc)
		}
	}
}

func TestSaveTUIResultWritesFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "result.md")
	saved, err := saveTUIResult(target, "test query", &agent.YOLOResult{
		FinalAnswer: "done",
	}, time.Second, nil, []tuiLogEntry{{msg: "[loop_started] autonomous loop started", tag: "I"}})
	if err != nil {
		t.Fatalf("saveTUIResult() error: %v", err)
	}
	if saved == "" {
		t.Fatal("expected non-empty saved path")
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if !strings.Contains(string(raw), "done") {
		t.Fatalf("unexpected file contents: %s", string(raw))
	}
	if !strings.Contains(string(raw), "## Stage log") {
		t.Fatalf("expected stage log section in export: %s", string(raw))
	}
}

func TestParseMouseScrollDelta(t *testing.T) {
	if got := parseMouseScrollDelta([]byte("\033[<64;8;12M")); got != -3 {
		t.Fatalf("wheel up = %d; want -3", got)
	}
	if got := parseMouseScrollDelta([]byte("\033[<65;8;12M")); got != 3 {
		t.Fatalf("wheel down = %d; want 3", got)
	}
	if got := parseMouseScrollDelta([]byte("\033[A")); got != 0 {
		t.Fatalf("arrow = %d; want 0", got)
	}
}

func TestPaperSourceURLPrefersDOI(t *testing.T) {
	link := paperSourceURL(agent.Paper{
		Title: "Example",
		DOI:   "10.1038/nature12345",
	})
	if !strings.Contains(link, "doi.org/10.1038") {
		t.Fatalf("unexpected paper link: %s", link)
	}
}

func TestBuildTUIResultLinesFiltersByPane(t *testing.T) {
	state := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer:     "Answer only",
			ExecutedQueries: []string{"query one"},
			Papers:          []agent.Paper{{Title: "Paper A", Year: 2024}},
		},
	}
	answerLines := buildTUIResultLines(state, 100, resultPaneAnswer)
	joined := strings.Join(answerLines, "\n")
	if !strings.Contains(joined, "Answer only") {
		t.Fatalf("expected answer pane content: %s", joined)
	}
	if !strings.Contains(joined, "Top references") {
		t.Fatalf("expected answer pane to surface citation context: %s", joined)
	}
	if strings.Contains(joined, "query one") {
		t.Fatalf("answer pane should not include queries: %s", joined)
	}

	sourceLines := buildTUIResultLines(state, 100, resultPaneSources)
	joinedSources := strings.Join(sourceLines, "\n")
	if !strings.Contains(joinedSources, "Paper A") {
		t.Fatal("expected sources pane to include paper title")
	}
	if !strings.Contains(joinedSources, "Citations:") {
		t.Fatal("expected sources pane to include citation metadata")
	}
}

func TestFormatYOLOResultMarkdownIncludesReferences(t *testing.T) {
	doc := formatYOLOResultMarkdown("meniscus scaffolds", &agent.YOLOResult{
		Papers: []agent.Paper{{
			Title:         "Meniscus scaffold repair",
			Authors:       []string{"Ada Lovelace"},
			Year:          2024,
			Venue:         "Journal of Knee Surgery",
			CitationCount: 42,
			DOI:           "10.1000/meniscus.2024",
		}},
	}, 0, nil, nil)
	for _, want := range []string{
		"## References",
		"Ada Lovelace (2024)",
		"Citations: 42",
		"doi.org/10.1000/meniscus.2024",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("expected markdown references to contain %q:\n%s", want, doc)
		}
	}
}

func TestInferRunPhaseFromLogsUsesStageTags(t *testing.T) {
	if got := inferRunPhaseFromLogs([]tuiLogEntry{{msg: "[synthesis_started] Synthesizing draft"}}, 1); got != "Synthesizing" {
		t.Fatalf("got %q want Synthesizing", got)
	}
	if got := inferRunPhaseFromLogs([]tuiLogEntry{{msg: "[search_batch_started] Searching 2 queries"}}, 1); got != "Searching" {
		t.Fatalf("got %q want Searching", got)
	}
	if got := inferRunPhase([]tuiLogEntry{{msg: "starting hypothesis generation"}}, 0); got != "Planning" {
		t.Fatalf("got %q want Planning", got)
	}
}

func TestSaveTUIResultJSONWritesFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "result.json")
	saved, err := saveTUIResultJSON(target, "q", &agent.YOLOResult{FinalAnswer: "ok"}, time.Second, nil)
	if err != nil {
		t.Fatalf("saveTUIResultJSON() error: %v", err)
	}
	if saved == "" {
		t.Fatal("expected saved path")
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	if !strings.Contains(string(raw), `"finalAnswer": "ok"`) {
		t.Fatalf("unexpected json: %s", string(raw))
	}
}

func TestBuildTUIResultLinesIncludesElapsed(t *testing.T) {
	state := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer: "Answer text",
			Iterations:  2,
			PapersFound: 1,
			StopReason:  "converged",
		},
		completedElapsed: 2 * time.Second,
	}
	lines := buildTUIResultLines(state, 100, resultPaneAll)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "elapsed=2.0s") {
		t.Fatalf("expected elapsed in result lines: %s", joined)
	}
	if !strings.Contains(joined, "Answer text") {
		t.Fatalf("expected answer in result lines: %s", joined)
	}
}
