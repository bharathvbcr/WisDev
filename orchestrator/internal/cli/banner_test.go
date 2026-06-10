package cli

import (
	"strings"
	"testing"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

func TestRenderWisDevBannerArtMode(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("WISDEV_PLAIN", "")

	lines := renderWisDevBanner(80, scholarlmTheme)
	if len(lines) < 5 || len(lines) > 8 {
		t.Fatalf("expected 5-8 art rows, got %d", len(lines))
	}
	plain := removeEscapeSequences(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "WisDev") {
		t.Fatalf("expected wordmark in banner art:\n%s", plain)
	}
	if !strings.Contains(plain, wisdevBannerTagline) {
		t.Fatalf("expected tagline in banner art:\n%s", plain)
	}
	for i, line := range lines {
		if w := visibleWidth(line); w > wisdevBannerWidthLimit {
			t.Errorf("banner row %d visible width %d exceeds budget %d: %q", i, w, wisdevBannerWidthLimit, removeEscapeSequences(line))
		}
	}
	styled := false
	for _, line := range lines {
		if strings.Contains(line, "\033[") {
			styled = true
			break
		}
	}
	if !styled {
		t.Fatal("expected themed ANSI styling in banner art")
	}
}

func TestRenderWisDevBannerPlainMode(t *testing.T) {
	t.Setenv("WISDEV_PLAIN", "1")

	lines := renderWisDevBanner(80, scholarlmTheme)
	if len(lines) != 1 {
		t.Fatalf("expected single-line fallback in plain mode, got %d lines", len(lines))
	}
	if strings.Contains(lines[0], "\033") {
		t.Fatalf("expected no ANSI escapes in plain mode, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "WisDev") {
		t.Fatalf("expected wordmark in plain fallback, got %q", lines[0])
	}
}

func TestRenderWisDevBannerNarrowWidthFallsBack(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("WISDEV_PLAIN", "")

	lines := renderWisDevBanner(30, scholarlmTheme)
	if len(lines) != 1 {
		t.Fatalf("expected single-line fallback for narrow terminal, got %d lines", len(lines))
	}
	if !strings.Contains(removeEscapeSequences(lines[0]), "WisDev") {
		t.Fatalf("expected wordmark in narrow fallback, got %q", lines[0])
	}
}

func TestPrintUsageIncludesBannerArt(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("WISDEV_PLAIN", "")

	var buf strings.Builder
	printUsage(&buf)
	plain := removeEscapeSequences(buf.String())
	if !strings.Contains(plain, wisdevBannerTagline) {
		t.Fatalf("expected banner tagline in usage header:\n%s", plain)
	}
}

func TestFormatProgressEventDegradedPrefix(t *testing.T) {
	msg, tag := formatProgressEvent(agent.ProgressEvent{
		Stage:    "synthesize_with_evidence",
		Message:  "LLM unavailable; using heuristic synthesis",
		Degraded: true,
		Payload:  map[string]any{"degraded": true, "fallback": "heuristic"},
	})
	if tag != "W" {
		t.Fatalf("tag = %q; want W", tag)
	}
	if !strings.HasPrefix(msg, "⚠ degraded: ") {
		t.Fatalf("expected degraded prefix, got %q", msg)
	}
	if extractLogStage(msg) != "synthesize_with_evidence" {
		t.Fatalf("expected stage extraction to survive degraded prefix, got %q", extractLogStage(msg))
	}
}

func TestProgressStageLabelHumanizesNewStages(t *testing.T) {
	cases := map[string]string{
		"pre_retrieval_hypotheses":       "hypothesis planning",
		"hypothesis_probe_budget_capped": "hypothesis probe budget capped",
		"critique_retrieval_started":     "critique retrieval",
		"search_batch_started":           "search_batch_started", // established IDs pass through
	}
	for stage, want := range cases {
		if got := progressStageLabel(stage); got != want {
			t.Errorf("progressStageLabel(%q) = %q; want %q", stage, got, want)
		}
	}
	if got := inferRunPhaseFromLogs([]tuiLogEntry{{msg: "[hypothesis planning] Seeded 3 hypothesis branches"}}, 0); got != "Planning" {
		t.Errorf("expected Planning phase for hypothesis stage label, got %q", got)
	}
	if got := inferRunPhaseFromLogs([]tuiLogEntry{{msg: "[critique retrieval] Reopening retrieval for 2 critique queries"}}, 0); got != "Verifying" {
		t.Errorf("expected Verifying phase for critique retrieval, got %q", got)
	}
}

func TestHandleProgressEventCountsDegradedAndPapers(t *testing.T) {
	s := &tuiState{eventCh: make(chan tuiEvent, 4)}
	s.handleProgressEvent(agent.ProgressEvent{
		Stage:    "evaluate_sufficiency",
		Message:  "LLM unavailable; using heuristic sufficiency",
		Degraded: true,
		Payload:  map[string]any{"degraded": true, "paperCount": 7},
	})
	if s.degradedSteps != 1 {
		t.Fatalf("degradedSteps = %d; want 1", s.degradedSteps)
	}
	if s.papersFound != 7 {
		t.Fatalf("papersFound = %d; want 7", s.papersFound)
	}
}

func TestBuildTUIResultLinesShowsSynthesisModeAndDegraded(t *testing.T) {
	state := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer:   "Answer",
			Iterations:    1,
			SynthesisMode: "heuristic",
		},
		degradedSteps: 3,
		llmBackend:    "ollama",
	}
	joined := strings.Join(buildTUIResultLines(state, 100, resultPaneAll), "\n")
	if !strings.Contains(joined, "mode=heuristic (3 degraded steps)") {
		t.Fatalf("expected degraded step count in result header: %s", joined)
	}

	state.result.SynthesisMode = "llm"
	state.degradedSteps = 0
	joined = strings.Join(buildTUIResultLines(state, 100, resultPaneAll), "\n")
	if !strings.Contains(joined, "mode=llm") {
		t.Fatalf("expected llm synthesis mode in result header: %s", joined)
	}
	if strings.Contains(joined, "degraded steps") {
		t.Fatalf("did not expect degraded annotation for llm synthesis: %s", joined)
	}
}

func TestBuildTUIResultLinesGroupsBranchPlans(t *testing.T) {
	state := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer: "Answer",
			BranchPlans: []agent.BranchPlan{
				{
					ID:                "branch-hyp-1",
					Query:             "probe: sleep consolidates memory",
					Hypothesis:        "Sleep consolidates declarative memory",
					ReasoningStrategy: "pre_retrieval_hypothesis_test",
					Status:            "retrieved",
				},
				{
					ID:                "branch-2",
					Query:             "memory consolidation mechanisms",
					ReasoningStrategy: "evidence_grounded_retrieval",
					Status:            "planned",
				},
				{
					ID:                "branch-3",
					Query:             "hippocampal replay during slow-wave sleep",
					ReasoningStrategy: "evidence_grounded_retrieval",
					Status:            "no_sources",
				},
			},
		},
	}
	lines := buildTUIResultLines(state, 100, resultPaneQueries)
	joined := removeEscapeSequences(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "pre retrieval hypothesis test") {
		t.Fatalf("expected hypothesis strategy group heading: %s", joined)
	}
	if !strings.Contains(joined, "evidence grounded retrieval") {
		t.Fatalf("expected retrieval strategy group heading: %s", joined)
	}
	if !strings.Contains(joined, "✓") || !strings.Contains(joined, "○") || !strings.Contains(joined, "✗") {
		t.Fatalf("expected status glyphs for retrieved/planned/no_sources: %s", joined)
	}
	if !strings.Contains(joined, "hypothesis: Sleep consolidates declarative memory") {
		t.Fatalf("expected hypothesis text for hypothesis branch: %s", joined)
	}
}

func TestFormatYOLOResultMarkdownGroupsBranches(t *testing.T) {
	doc := formatYOLOResultMarkdown("q", &agent.YOLOResult{
		SynthesisMode: "heuristic",
		BranchPlans: []agent.BranchPlan{
			{ID: "b1", Query: "alpha", ReasoningStrategy: "pre_retrieval_hypothesis_test", Hypothesis: "Alpha drives beta", Status: "retrieved"},
			{ID: "b2", Query: "beta", ReasoningStrategy: "evidence_grounded_retrieval", Status: "no_sources"},
		},
	}, 0, nil, nil)
	for _, want := range []string{
		"### pre retrieval hypothesis test",
		"- ✓ alpha [retrieved]",
		"- Hypothesis: Alpha drives beta",
		"### evidence grounded retrieval",
		"- ✗ beta [no_sources]",
		"- Synthesis: heuristic",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("expected markdown to contain %q:\n%s", want, doc)
		}
	}
}
