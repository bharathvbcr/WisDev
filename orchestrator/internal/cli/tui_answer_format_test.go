package cli

import (
	"strings"
	"testing"
)

func TestHighlightAnswerMarkers(t *testing.T) {
	theme := scholarlmTheme
	raw := "See [1] and **Executive takeaway** in *Title*."
	got := highlightAnswerMarkers(raw, theme)
	if got == "" {
		t.Fatal("expected non-empty highlighted line")
	}
	if !plainUI() && !strings.Contains(got, "\033[") {
		t.Fatalf("expected ANSI styling for markers, got %q", got)
	}
}

func TestResultPaneContentWidthAccountsForChrome(t *testing.T) {
	if got := resultPaneContentWidth(120); got >= 120 {
		t.Fatalf("content width should be less than terminal width, got %d", got)
	}
	if got := resultPaneContentWidth(120); got < 100 {
		t.Fatalf("content width too small, got %d", got)
	}
}

func TestFormatAnswerLinesForTUIRendersHeadings(t *testing.T) {
	theme := scholarlmTheme
	answer := "# Main title\n\n## Research landscape\nSome landscape text.\n\n- [1] Smith (2021) reports that findings hold."
	lines := formatAnswerLinesForTUI(answer, 80, theme)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Main title") {
		t.Fatalf("expected main title rendered: %s", joined)
	}
	if !strings.Contains(joined, "Research landscape") {
		t.Fatalf("expected section heading rendered: %s", joined)
	}
	if !strings.Contains(joined, "[1]") || !strings.Contains(joined, "Smith (2021) reports that findings hold.") {
		t.Fatalf("expected bullet preserved: %s", joined)
	}
}

func TestHighlightAnswerMarkersGroundingWarning(t *testing.T) {
	theme := scholarlmTheme
	got := highlightAnswerMarkers("Claim text [requires verification against retrieved sources].", theme)
	if got == "" {
		t.Fatal("expected non-empty highlighted line")
	}
	if !plainUI() && !strings.Contains(got, "requires verification") {
		t.Fatalf("expected grounding warning preserved: %q", got)
	}
}

func TestHighlightAnswerMarkersEvidenceStrength(t *testing.T) {
	theme := scholarlmTheme
	got := highlightAnswerMarkers("Finding [strong evidence] with [1].", theme)
	if got == "" {
		t.Fatal("expected non-empty highlighted line")
	}
	if !plainUI() && !strings.Contains(got, "strong evidence") {
		t.Fatalf("expected evidence strength tag preserved: %q", got)
	}
}

func TestFormatAnswerLinesForTUIRendersNumberedList(t *testing.T) {
	theme := scholarlmTheme
	lines := formatAnswerLinesForTUI("## Questions\n\n1. First question?\n2. Second question?", 80, theme)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "1. First question?") || !strings.Contains(joined, "2. Second question?") {
		t.Fatalf("expected numbered list items: %s", joined)
	}
}
