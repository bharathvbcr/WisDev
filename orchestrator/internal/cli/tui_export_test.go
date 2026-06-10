package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
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

func TestSnakeCaseQuerySlug(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"map open source research agent evidence", "map_open_source_research_agent_evidence"},
		{"  What evidence supports RAG?  ", "what_evidence_supports_rag"},
		{"CRISPR-Cas9 off-target effects (2024)", "crispr_cas9_off_target_effects_2024"},
		{"___", "wisdev_result"},
		{"", "wisdev_result"},
		{strings.Repeat("verylongword ", 10), "verylongword_verylongword_verylongword_verylongword_verylong"},
	}
	for _, tc := range cases {
		if got := snakeCaseQuerySlug(tc.query); got != tc.want {
			t.Errorf("snakeCaseQuerySlug(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
	if slug := snakeCaseQuerySlug(strings.Repeat("a b ", 40)); len(slug) > 60 {
		t.Errorf("slug exceeds max length: %d", len(slug))
	}
}

func TestDefaultTUIResultFile(t *testing.T) {
	path := defaultTUIResultFile("map open source evidence", "md")
	if dir := filepath.Dir(path); dir != defaultResultsDirName {
		t.Errorf("expected results to land in %q, got dir %q", defaultResultsDirName, dir)
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "map_open_source_evidence_") {
		t.Errorf("expected snake_case query prefix, got %q", base)
	}
	if !strings.HasSuffix(base, ".md") {
		t.Errorf("expected .md extension, got %q", base)
	}
}

func TestSaveTUIResultDefaultsToResultsFolder(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	saved, err := saveTUIResult("", "graphene battery anodes", &agent.YOLOResult{FinalAnswer: "ok"}, time.Second, nil, nil)
	if err != nil {
		t.Fatalf("saveTUIResult() error: %v", err)
	}
	rel, err := filepath.Rel(dir, saved)
	if err != nil {
		t.Fatalf("saved path %q not under temp dir: %v", saved, err)
	}
	if filepath.Dir(rel) != defaultResultsDirName {
		t.Errorf("expected save under %s/, got %q", defaultResultsDirName, rel)
	}
	if base := filepath.Base(rel); !strings.HasPrefix(base, "graphene_battery_anodes_") {
		t.Errorf("expected snake_case filename, got %q", base)
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
