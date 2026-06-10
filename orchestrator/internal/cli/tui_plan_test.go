package cli

import (
	"bytes"
	"strings"
	"testing"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

func TestFuzzyMatch(t *testing.T) {
	if !fuzzyMatch("meniscus scaffold hydrogel", "mscaffold") {
		t.Fatal("expected fuzzy subsequence match")
	}
	if fuzzyMatch("bone tissue engineering", "meniscus") {
		t.Fatal("expected no match for unrelated text")
	}
}

func TestUpdateResultFilterMatches(t *testing.T) {
	s := &tuiState{
		result: &agent.YOLOResult{
			FinalAnswer: "Meniscus scaffold hydrogel outcomes",
			Papers: []agent.Paper{{
				Title:         "ACL reconstruction strategies",
				CitationCount: 3,
			}},
		},
		resultPane: resultPaneAll,
		terminalSize: func() (int, int, error) {
			return 100, 40, nil
		},
	}
	s.updateResultFilterMatches()
	if len(s.resultFilterMatch) != 0 {
		t.Fatalf("expected no matches for empty filter, got %d", len(s.resultFilterMatch))
	}

	s.resultFilter = "meniscus"
	s.updateResultFilterMatches()
	if len(s.resultFilterMatch) == 0 {
		t.Fatal("expected filter matches for meniscus")
	}
}

func TestProviderDisplayNameAliases(t *testing.T) {
	for code, want := range map[string]string{
		"semantic_scholar": "Semantic Scholar",
		"europe_pmc":       "Europe PMC",
		"biorxiv":          "bioRxiv",
		"clinical_trials":  "ClinicalTrials",
	} {
		if got := providerDisplayName(code); got != want {
			t.Fatalf("providerDisplayName(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestParseCtrlArrow(t *testing.T) {
	if dir, ok := parseCtrlArrow([]byte{27, '[', '1', ';', '5', 'C'}); !ok || dir != 1 {
		t.Fatalf("expected ctrl+right, got dir=%d ok=%v", dir, ok)
	}
	if dir, ok := parseCtrlArrow([]byte{27, '[', '1', ';', '5', 'D'}); !ok || dir != -1 {
		t.Fatalf("expected ctrl+left, got dir=%d ok=%v", dir, ok)
	}
	if dir, ok := parseCtrlArrow([]byte{27, '[', '5', 'C'}); !ok || dir != 1 {
		t.Fatalf("expected 4-byte ctrl+right, got dir=%d ok=%v", dir, ok)
	}
	if dir, ok := parseCtrlArrow([]byte{27, '[', '5', 'D'}); !ok || dir != -1 {
		t.Fatalf("expected 4-byte ctrl+left, got dir=%d ok=%v", dir, ok)
	}
}

func TestAvailableResultPanesIncludesCompare(t *testing.T) {
	s := &tuiState{
		result: &agent.YOLOResult{FinalAnswer: "current"},
	}
	panes := s.availableResultPanes()
	if len(panes) != 7 {
		t.Fatalf("expected compare+reasoning panes when results exist, got %d panes", len(panes))
	}
	if panes[len(panes)-2] != resultPaneCompare {
		t.Fatalf("expected second-to-last pane to be compare, got %v", panes)
	}
	if panes[len(panes)-1] != resultPaneReasoning {
		t.Fatalf("expected last pane to be reasoning, got %v", panes)
	}
}

func TestRenderTextInputInlineCaret(t *testing.T) {
	theme := activeTheme()
	line := renderTextInput(" Q: ", "meniscus", 3, true, theme)
	if !strings.Contains(line, "men|iscus") && !strings.Contains(removeEscapeSequences(line), "men|iscus") {
		plain := removeEscapeSequences(line)
		if !strings.Contains(plain, "|") {
			t.Fatalf("expected inline caret in %q", plain)
		}
	}
	if strings.Count(removeEscapeSequences(line), "|") != 1 {
		t.Fatalf("expected single caret, got %q", removeEscapeSequences(line))
	}

	inactiveLine := renderTextInput(" Q: ", "meniscus", 3, false, theme)
	plainInactive := removeEscapeSequences(inactiveLine)
	if strings.Contains(plainInactive, "|") || strings.Contains(plainInactive, "_") {
		t.Fatalf("expected no caret in inactive input, got %q", plainInactive)
	}
}

func TestFocusLabelInputMode(t *testing.T) {
	s := &tuiState{mode: modeInput, activeElement: 2}
	if got := s.focusLabel(); got != "settings" {
		t.Fatalf("focusLabel() = %q, want settings", got)
	}
}

func TestResultsViewportHeight(t *testing.T) {
	if got := resultsViewportHeight(30, false); got != 20 {
		t.Fatalf("viewport at height 30 = %d, want 20", got)
	}
	if got := resultsViewportHeight(30, true); got != 19 {
		t.Fatalf("viewport with footer = %d, want 19", got)
	}
}

func TestClampResultsScrollOffset(t *testing.T) {
	s := &tuiState{
		mode:         modeResults,
		result:       &agent.YOLOResult{FinalAnswer: "line1\nline2\nline3\nline4\nline5"},
		resultPane:   resultPaneAll,
		scrollOffset: 999,
		terminalSize: func() (int, int, error) { return 100, 30, nil },
	}
	s.clampResultsScrollOffset()
	if s.scrollOffset >= 999 {
		t.Fatalf("expected scroll offset to be clamped, got %d", s.scrollOffset)
	}
}

func TestPaneContentColumn(t *testing.T) {
	if got := paneContentColumn(0); got != 3 {
		t.Fatalf("expected content column 3, got %d", got)
	}
	if got := paneContentColumn(5); got != 8 {
		t.Fatalf("expected content column 8, got %d", got)
	}
}

func TestDrawLineMatchesPaneWidth(t *testing.T) {
	var buf bytes.Buffer
	width := 80
	r := &tuiRenderer{buf: &buf, width: width, theme: activeTheme()}
	r.drawLine("hello", "")
	line := strings.TrimSuffix(buf.String(), "\n")
	if visibleWidth(line) != width {
		t.Fatalf("rendered line visible width=%d, want %d line=%q", visibleWidth(line), width, line)
	}
}

func TestMoveSettingGridNavigation(t *testing.T) {
	if got := moveSettingRight(0); got != 1 {
		t.Fatalf("expected right from 0 to be 1, got %d", got)
	}
	if got := moveSettingLeft(1); got != 0 {
		t.Fatalf("expected left from 1 to be 0, got %d", got)
	}
	if got := moveSettingRight(2); got != 0 {
		t.Fatalf("expected right from 2 to wrap to 0, got %d", got)
	}
	if got := moveSettingLeft(0); got != 2 {
		t.Fatalf("expected left from 0 to wrap to 2, got %d", got)
	}
	if got := moveSettingDown(1); got != 4 {
		t.Fatalf("down from planning should reach hypotheses, got %d", got)
	}
	if got := moveSettingUp(5); got != 2 {
		t.Fatalf("up from exhaustive should reach offline, got %d", got)
	}
}

func TestProviderGridColumnsForWidth(t *testing.T) {
	if got := providerGridColumnsForWidth(80); got != 3 {
		t.Fatalf("expected 3 columns at width 80, got %d", got)
	}
	if got := providerGridColumnsForWidth(50); got != 2 {
		t.Fatalf("expected 2 columns at width 50, got %d", got)
	}
}

func TestFuzzyMatchScorePrefersTighterMatches(t *testing.T) {
	tight := fuzzyMatchScore("meniscus scaffold", "mscaffold")
	loose := fuzzyMatchScore("m---e---n---iscus scaffold", "mscaffold")
	if tight < 0 || loose < 0 {
		t.Fatal("expected both patterns to match")
	}
	if loose <= tight {
		t.Fatalf("expected tighter match to score lower, tight=%d loose=%d", tight, loose)
	}
}

func TestClampLogScrollOffset(t *testing.T) {
	s := &tuiState{
		logs: make([]tuiLogEntry, 50),
		terminalSize: func() (int, int, error) {
			return 100, 30, nil
		},
	}
	s.logScrollOffset = 999
	s.clampLogScrollOffset()
	if s.logScrollOffset != 34 {
		t.Fatalf("expected capped offset 34, got %d", s.logScrollOffset)
	}
}

func TestHighlightFuzzyMatch(t *testing.T) {
	out := highlightFuzzyMatch("meniscus scaffold hydrogel", "mscaffold")
	if !strings.Contains(out, scholarlmHighlight) {
		t.Fatalf("expected highlighted chars, got %q", out)
	}
	if removeEscapeSequences(out) != "meniscus scaffold hydrogel" {
		t.Fatalf("highlight should preserve visible text, got %q", removeEscapeSequences(out))
	}
}

func TestRenderSparkline(t *testing.T) {
	out := renderSparkline([]float64{0.1, 0.5, 0.9})
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected non-empty sparkline")
	}
}

func TestMoveWordLeftRight(t *testing.T) {
	text := "meniscus scaffold repair"
	if got := moveWordRight(text, 0); got <= 0 {
		t.Fatalf("expected moveWordRight to advance, got %d", got)
	}
	pos := moveWordRight(text, 0)
	if got := moveWordLeft(text, pos); got != 0 {
		t.Fatalf("expected moveWordLeft to return to start, got %d", got)
	}
}
