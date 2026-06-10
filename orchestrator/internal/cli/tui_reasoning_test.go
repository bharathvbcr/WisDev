package cli

import (
	"strings"
	"testing"

	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

func sampleReasoningResult() *agent.YOLOResult {
	return &agent.YOLOResult{
		FinalAnswer: "Answer",
		Iterations:  2,
		ReasoningTrace: []agent.ReasoningStep{
			{
				Timestamp: 1717000000000,
				Phase:     "planning",
				Decision:  "cot_plan_summary",
				Reasoning: "Decomposed the question into three retrieval branches.",
			},
			{
				Timestamp: 1717000001000,
				Phase:     "planning",
				Decision:  "pre_retrieval_hypotheses",
				Reasoning: "Generated two falsifiable hypotheses before retrieval.",
			},
			{
				Timestamp:    1717000002000,
				Phase:        "retrieval",
				Decision:     "react_action_retrieve",
				Reasoning:    "Executed the branch queries against the enabled providers.",
				Alternatives: []string{"alt-a", "alt-b", "alt-c", "alt-d", "alt-e", "alt-f"},
			},
			{
				Timestamp: 1717000003000,
				Phase:     "evaluation",
				Decision:  "react_reflect_sufficiency",
				Reasoning: "Coverage still open on the replication branch.",
			},
			{
				Timestamp: 1717000004000,
				Phase:     "synthesis",
				Decision:  "draft",
				Reasoning: "Drafted the synthesis from admitted evidence.",
			},
		},
	}
}

func TestBuildReasoningTraceLinesGroupsAndLabels(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("WISDEV_PLAIN", "")
	state := &tuiState{result: sampleReasoningResult()}
	lines := buildTUIResultLines(state, 100, resultPaneReasoning)
	joined := removeEscapeSequences(strings.Join(lines, "\n"))

	if !strings.Contains(joined, "Reasoning trace (5 steps") {
		t.Fatalf("expected trace header with step count: %s", joined)
	}
	for _, chip := range []string{"◆ Planning", "◆ Retrieval", "◆ Evaluation", "◆ Synthesis"} {
		if !strings.Contains(joined, chip) {
			t.Fatalf("expected phase chip %q: %s", chip, joined)
		}
	}
	// Consecutive same-phase entries share one chip: "◆ Planning" appears once.
	if strings.Count(joined, "◆ Planning") != 1 {
		t.Fatalf("expected planning chip once for consecutive planning steps: %s", joined)
	}
	for _, label := range []string{
		"plan: chain-of-thought summary",
		"plan: pre-retrieval hypotheses",
		"act: retrieve",
		"reflect: sufficiency",
		"synthesize: draft",
	} {
		if !strings.Contains(joined, label) {
			t.Fatalf("expected humanized decision label %q: %s", label, joined)
		}
	}
	if !strings.Contains(joined, "Decomposed the question into three retrieval branches.") {
		t.Fatalf("expected reasoning body text: %s", joined)
	}
}

func TestBuildReasoningTraceLinesCapsAlternatives(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("WISDEV_PLAIN", "")
	state := &tuiState{result: sampleReasoningResult()}
	lines := buildTUIResultLines(state, 100, resultPaneReasoning)
	joined := removeEscapeSequences(strings.Join(lines, "\n"))

	for _, alt := range []string{"· alt: alt-a", "· alt: alt-d"} {
		if !strings.Contains(joined, alt) {
			t.Fatalf("expected alternative bullet %q: %s", alt, joined)
		}
	}
	if strings.Contains(joined, "alt-e") || strings.Contains(joined, "alt-f") {
		t.Fatalf("expected alternatives capped at %d: %s", maxReasoningAlternatives, joined)
	}
	if !strings.Contains(joined, "· +2 more alternatives") {
		t.Fatalf("expected overflow marker for capped alternatives: %s", joined)
	}
}

func TestBuildReasoningTraceLinesEmptyTrace(t *testing.T) {
	state := &tuiState{result: &agent.YOLOResult{FinalAnswer: "Answer"}}
	lines := buildTUIResultLines(state, 100, resultPaneReasoning)
	joined := removeEscapeSequences(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "No reasoning trace recorded") {
		t.Fatalf("expected empty-trace placeholder: %s", joined)
	}
}

func TestReasoningDecisionLabelFallback(t *testing.T) {
	if got := reasoningDecisionLabel("critique_replan"); got != "replan: critique" {
		t.Fatalf("critique_replan label = %q", got)
	}
	if got := reasoningDecisionLabel("custom_decision_id"); got != "custom decision id" {
		t.Fatalf("fallback label = %q", got)
	}
}

func TestFormatYOLOResultMarkdownIncludesReasoningTrace(t *testing.T) {
	doc := formatYOLOResultMarkdown("q", sampleReasoningResult(), 0, nil, nil)
	if !strings.Contains(doc, "## Reasoning trace") {
		t.Fatalf("expected reasoning trace section in markdown: %s", doc)
	}
	if !strings.Contains(doc, "- `[planning]` plan: chain-of-thought summary — Decomposed the question into three retrieval branches.") {
		t.Fatalf("expected one-line trace entry in markdown: %s", doc)
	}
	if !strings.Contains(doc, "(6 alternative(s) considered)") {
		t.Fatalf("expected alternatives count annotation: %s", doc)
	}
	if strings.Contains(doc, "\033]8;;") {
		t.Fatalf("markdown export must not contain OSC-8 sequences: %s", doc)
	}
}

func TestSourcesPaneHyperlinksTitles(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("WISDEV_PLAIN", "")
	state := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer: "Answer",
			Papers: []agent.Paper{{
				ID:    "p1",
				Title: "Tunneling in Quantum Dots",
				Link:  "https://example.org/qd",
			}},
		},
	}
	joined := strings.Join(buildTUIResultLines(state, 100, resultPaneSources), "\n")
	if !strings.Contains(joined, "\033]8;;https://example.org/qd\007Tunneling in Quantum Dots\033]8;;\007") {
		t.Fatalf("expected OSC-8 hyperlinked title in styled sources pane: %q", joined)
	}

	// Answer pane top references should also be linked in styled mode.
	joined = strings.Join(buildTUIResultLines(state, 100, resultPaneAnswer), "\n")
	if !strings.Contains(joined, "\033]8;;https://example.org/qd\007") {
		t.Fatalf("expected OSC-8 hyperlinked title in answer pane top references: %q", joined)
	}
}

func TestSourcesPanePlainModeShowsRawURL(t *testing.T) {
	t.Setenv("WISDEV_PLAIN", "1")
	state := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer: "Answer",
			Papers: []agent.Paper{{
				ID:    "p1",
				Title: "Tunneling in Quantum Dots",
				Link:  "https://example.org/qd",
			}},
		},
	}
	joined := strings.Join(buildTUIResultLines(state, 100, resultPaneSources), "\n")
	if strings.Contains(joined, "\033]8;;") {
		t.Fatalf("plain mode must not emit OSC-8 sequences: %q", joined)
	}
	if !strings.Contains(joined, "Tunneling in Quantum Dots") || !strings.Contains(joined, "https://example.org/qd") {
		t.Fatalf("plain mode should show title and raw URL: %q", joined)
	}

	// Answer pane in plain mode shows the raw URL under the title.
	joined = strings.Join(buildTUIResultLines(state, 100, resultPaneAnswer), "\n")
	if strings.Contains(joined, "\033]8;;") {
		t.Fatalf("plain mode answer pane must not emit OSC-8: %q", joined)
	}
	if !strings.Contains(joined, "https://example.org/qd") {
		t.Fatalf("plain mode answer pane should list the raw URL: %q", joined)
	}
}

func TestUpdateRunCountersAggregatesProviderCounts(t *testing.T) {
	s := &tuiState{eventCh: make(chan tuiEvent, 8)}
	s.handleProgressEvent(agent.ProgressEvent{
		Stage:   "query_completed",
		Message: "Query completed: a (5 papers)",
		Payload: map[string]any{
			"provider_result_counts": map[string]int{"openalex": 5, "arxiv": 3},
		},
	})
	s.handleProgressEvent(agent.ProgressEvent{
		Stage:   "query_completed",
		Message: "Query completed: b (8 papers)",
		Payload: map[string]any{
			"provider_result_counts": map[string]any{"openalex": float64(7), "pubmed": 3},
		},
	})
	// "providers" key (admission events) must not double-count.
	s.handleProgressEvent(agent.ProgressEvent{
		Stage:   "search_result_admitted",
		Message: "Admitted 4 papers",
		Payload: map[string]any{
			"providers": map[string]int{"openalex": 99},
		},
	})
	if got := s.providerCounts["openalex"]; got != 12 {
		t.Fatalf("openalex count = %d; want 12", got)
	}
	if got := s.providerCounts["arxiv"]; got != 3 {
		t.Fatalf("arxiv count = %d; want 3", got)
	}
	if got := s.providerCounts["pubmed"]; got != 3 {
		t.Fatalf("pubmed count = %d; want 3", got)
	}
}

func TestFormatProviderCountsSortsAndCaps(t *testing.T) {
	counts := map[string]int{"pubmed": 3, "openalex": 12, "arxiv": 7, "crossref": 3}
	got := formatProviderCounts(counts, 0)
	want := "openalex 12 · arxiv 7 · crossref 3 · pubmed 3"
	if got != want {
		t.Fatalf("formatProviderCounts = %q; want %q", got, want)
	}
	capped := formatProviderCounts(counts, 22)
	if !strings.HasPrefix(capped, "openalex 12 · arxiv 7") || !strings.Contains(capped, "+2") {
		t.Fatalf("expected width-capped output with overflow count, got %q", capped)
	}
	if formatProviderCounts(nil, 40) != "" {
		t.Fatal("expected empty string for nil counts")
	}
}

func TestFormatAnswerLinesDimsMetaSections(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("WISDEV_PLAIN", "")
	t.Setenv("WISDEV_THEME", "scholarlm")
	theme := activeTheme()
	answer := "Main finding paragraph.\n\n## Grounding audit\n\n- claim 1 grounded\n\n## Loop critique gaps\n\ngap detail\n\n## Conclusion\n\nFinal text."
	lines := formatAnswerLinesForTUI(answer, 100, theme)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, theme.DimText+"Grounding audit"+ansiReset) {
		t.Fatalf("expected dimmed grounding audit heading: %q", joined)
	}
	if !strings.Contains(joined, theme.DimText+"- claim 1 grounded"+ansiReset) {
		t.Fatalf("expected dimmed grounding audit body: %q", joined)
	}
	if !strings.Contains(joined, theme.DimText+"gap detail"+ansiReset) {
		t.Fatalf("expected dimmed loop critique body: %q", joined)
	}
	if strings.Contains(joined, theme.DimText+"Conclusion"+ansiReset) {
		t.Fatalf("conclusion heading must not be dimmed: %q", joined)
	}
	if !strings.Contains(joined, themeHeading(theme, "Conclusion")) {
		t.Fatalf("expected normal heading style after meta section: %q", joined)
	}
}

func TestAvailableResultPanesIncludesReasoning(t *testing.T) {
	state := &tuiState{result: sampleReasoningResult()}
	panes := state.availableResultPanes()
	found := false
	for _, pane := range panes {
		if pane == resultPaneReasoning {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reasoning pane in available panes: %#v", panes)
	}
	if resultPaneReasoning.label() != "Reasoning" {
		t.Fatalf("reasoning pane label = %q", resultPaneReasoning.label())
	}
}
