package cli

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCharBoundaryHelpers(t *testing.T) {
	s := "aé\U0001F44D!" // a é 👍 ! — 1, 2, 4, 1 bytes

	if got := nextCharBoundary(s, 0); got != 1 {
		t.Fatalf("next from 0 = %d, want 1", got)
	}
	if got := nextCharBoundary(s, 1); got != 3 {
		t.Fatalf("next over é = %d, want 3", got)
	}
	if got := nextCharBoundary(s, 3); got != 7 {
		t.Fatalf("next over emoji = %d, want 7", got)
	}
	if got := nextCharBoundary(s, len(s)); got != len(s) {
		t.Fatalf("next at end = %d, want %d", got, len(s))
	}
	if got := prevCharBoundary(s, 7); got != 3 {
		t.Fatalf("prev over emoji = %d, want 3", got)
	}
	if got := prevCharBoundary(s, 3); got != 1 {
		t.Fatalf("prev over é = %d, want 1", got)
	}
	if got := prevCharBoundary(s, 0); got != 0 {
		t.Fatalf("prev at start = %d, want 0", got)
	}
}

func TestQueryEditingIsRuneAware(t *testing.T) {
	s := &tuiState{query: "café", cursorPos: len("café")}
	s.backspaceQueryChar()
	if s.query != "caf" || s.cursorPos != 3 {
		t.Fatalf("backspace over é: query=%q cursor=%d", s.query, s.cursorPos)
	}
	if !utf8.ValidString(s.query) {
		t.Fatalf("backspace produced invalid UTF-8: %q", s.query)
	}

	s = &tuiState{query: "éx", cursorPos: 0}
	s.deleteQueryChar()
	if s.query != "x" {
		t.Fatalf("delete at é: query=%q", s.query)
	}
}

func TestOutputPathAndChatEditingIsRuneAware(t *testing.T) {
	s := &tuiState{outputPath: "résumé.md"}
	s.outputPathCursorPos = len("résumé") // after the final é
	s.backspaceOutputPathChar()
	if s.outputPath != "résum.md" || !utf8.ValidString(s.outputPath) {
		t.Fatalf("path backspace: %q", s.outputPath)
	}
}

func TestRuneDisplayWidthZeroWidth(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{0x0301, 0}, // combining acute accent
		{0xFE0F, 0}, // variation selector-16
		{0x200D, 0}, // zero-width joiner
		{0x200B, 0}, // zero-width space
		{'e', 1},
		{0x4E2D, 2}, // CJK
	}
	for _, c := range cases {
		if got := runeDisplayWidth(c.r); got != c.want {
			t.Errorf("runeDisplayWidth(%U) = %d, want %d", c.r, got, c.want)
		}
	}
	// "é" composed as e + combining accent renders one column wide.
	if got := visibleWidth("é"); got != 1 {
		t.Errorf("visibleWidth(e + combining accent) = %d, want 1", got)
	}
}

func TestTruncateVisibleClosesHyperlink(t *testing.T) {
	link := "\033]8;;https://example.org/very/long/path\007example link text that overflows\033]8;;\007 tail"
	got := truncateVisible(link, 10)
	if !strings.HasSuffix(removeEscapeSequences(got), "…") {
		t.Fatalf("expected ellipsis, got %q", got)
	}
	// The truncated string must end with a hyperlink terminator so following
	// rows do not stay clickable.
	if !strings.Contains(got[strings.LastIndex(got, "…"):], "\033]8;;\007") {
		t.Fatalf("truncated hyperlink not closed: %q", got)
	}
}

// A saturated input layout (validation message + preview rows on a 24-row
// terminal) must never emit more rows than the terminal has: every extra row
// scrolls the alternate screen and clips the frame's top border.
func TestRenderNeverOverflowsTerminalHeight(t *testing.T) {
	const width, height = 80, 24
	var out bytes.Buffer
	s := &tuiState{
		mode:          modeInput,
		query:         strings.Repeat("long query ", 6),
		validationMsg: "Please select at least one provider before starting.",
		providers:     defaultTUIProviders(),
		activeElement: 1, // provider grid focused: adds the presets hint row
		output:        &out,
		terminalSize: func() (int, int, error) {
			return width, height, nil
		},
	}
	s.cursorPos = len(s.query)
	s.render()

	frame := out.String()
	// Rows are delimited by CRLF; a frame of N rows emits N-1 newlines (the
	// final row's newline is stripped by flushFrame).
	if rows := strings.Count(frame, "\r\n") + 1; rows > height {
		t.Fatalf("frame emits %d rows, terminal has %d — overflow scrolls the screen", rows, height)
	}
}
