package cli

import (
	"os"
	"testing"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

func TestTuiCursorInsertionDeletion(t *testing.T) {
	s := &tuiState{
		query:     "test query",
		cursorPos: 5,
	}

	s.insertQueryChar("abc")
	if s.query != "test abcquery" {
		t.Fatalf("insertQueryChar failed: got %q, want %q", s.query, "test abcquery")
	}
	if s.cursorPos != 8 {
		t.Fatalf("cursorPos incorrect after insert: got %d, want %d", s.cursorPos, 8)
	}

	s.backspaceQueryChar()
	if s.query != "test abquery" {
		t.Fatalf("backspaceQueryChar failed: got %q, want %q", s.query, "test abquery")
	}
	if s.cursorPos != 7 {
		t.Fatalf("cursorPos incorrect after backspace: got %d, want %d", s.cursorPos, 7)
	}

	s.deleteQueryChar()
	if s.query != "test abuery" {
		t.Fatalf("deleteQueryChar failed: got %q, want %q", s.query, "test abuery")
	}
	if s.cursorPos != 7 {
		t.Fatalf("cursorPos incorrect after delete: got %d, want %d", s.cursorPos, 7)
	}
}

func TestTuiThemeActiveSelection(t *testing.T) {
	os.Setenv("WISDEV_THEME", "high-contrast")
	theme := activeTheme()
	if theme.Border != "\033[1;37m" {
		t.Fatalf("expected high contrast theme border, got %q", theme.Border)
	}

	os.Setenv("WISDEV_THEME", "monochrome")
	theme = activeTheme()
	if theme.Border != "\033[1m" {
		t.Fatalf("expected monochrome theme border, got %q", theme.Border)
	}

	os.Setenv("WISDEV_THEME", "default")
	theme = activeTheme()
	if theme.Border != scholarlmRedBold {
		t.Fatalf("expected default theme border %q, got %q", scholarlmRedBold, theme.Border)
	}

	os.Unsetenv("WISDEV_THEME")
}

func TestTuiHistoryPersistence(t *testing.T) {
	tempHome, err := os.MkdirTemp("", "wisdev-test-home")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempHome)

	origHome := os.Getenv("USERPROFILE")
	os.Setenv("USERPROFILE", tempHome)
	defer os.Setenv("USERPROFILE", origHome)

	s := &tuiState{}
	s.loadHistory()
	if len(s.history) != 0 {
		t.Fatalf("expected empty history, got %d", len(s.history))
	}

	s.appendHistory("test question 1")
	s.appendHistory("test question 2")

	s2 := &tuiState{}
	s2.loadHistory()
	if len(s2.history) != 2 {
		t.Fatalf("expected 2 history entries loaded, got %d", len(s2.history))
	}
	if s2.history[0].Query != "test question 1" || s2.history[1].Query != "test question 2" {
		t.Fatalf("history loaded incorrectly: %+v", s2.history)
	}
}

func TestTuiLogSeverityTagging(t *testing.T) {
	s := &tuiState{}
	s.addLog("simple info log", "I")
	s.addLog("warning log message", "W")
	s.addLog("critical error occurred", "E")

	if len(s.logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(s.logs))
	}

	if s.logs[0].tag != "I" || s.logs[1].tag != "W" || s.logs[2].tag != "E" {
		t.Fatalf("log tagging failed: %+v", s.logs)
	}
}

func TestFormatPaperDOI(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"   ", ""},
		{"http://example.com/doi", "http://example.com/doi"},
		{"https://example.com/doi", "https://example.com/doi"},
		{"10.1101/123456", "https://doi.org/10.1101/123456"},
		{"doi:10.1101/123456", "https://doi.org/10.1101/123456"},
		{"DOI:10.1101/123456", "https://doi.org/10.1101/123456"},
	}

	for _, c := range cases {
		got := formatPaperDOI(c.input)
		if got != c.expected {
			t.Errorf("formatPaperDOI(%q) = %q, expected %q", c.input, got, c.expected)
		}
	}
}

func TestRemoveEscapeSequencesOSC(t *testing.T) {
	// Simple CSI escape sequence
	input := "\033[1;36mHello\033[0m"
	expected := "Hello"
	if got := removeEscapeSequences(input); got != expected {
		t.Errorf("removeEscapeSequences(%q) = %q, expected %q", input, got, expected)
	}

	// OSC hyperlink escape sequence with BEL terminator
	inputOSC1 := "\033]8;;http://example.com\007Link Text\033]8;;\007"
	expectedOSC1 := "Link Text"
	if got := removeEscapeSequences(inputOSC1); got != expectedOSC1 {
		t.Errorf("removeEscapeSequences(%q) = %q, expected %q", inputOSC1, got, expectedOSC1)
	}

	// OSC hyperlink escape sequence with ST terminator (\033\\)
	inputOSC2 := "\033]8;;http://example.com\033\\Link Text\033]8;;\033\\"
	expectedOSC2 := "Link Text"
	if got := removeEscapeSequences(inputOSC2); got != expectedOSC2 {
		t.Errorf("removeEscapeSequences(%q) = %q, expected %q", inputOSC2, got, expectedOSC2)
	}
}

func TestTruncateVisiblePreservingAnsi(t *testing.T) {
	// Test no truncation needed
	input := "\033[31mHello\033[0m"
	if got := truncateVisible(input, 10); got != input {
		t.Errorf("truncateVisible(%q, 10) = %q, expected %q", input, got, input)
	}

	// Test truncation is needed
	input2 := "\033[31mHelloWorld\033[0m"
	expected2 := "\033[31mHell…\033[0m"
	if got := truncateVisible(input2, 5); got != expected2 {
		t.Errorf("truncateVisible(%q, 5) = %q, expected %q", input2, got, expected2)
	}
}

func TestTuiUndoRedoStacks(t *testing.T) {
	s := &tuiState{
		query:     "initial query",
		cursorPos: 13,
	}

	// Save state, modify query
	s.saveQueryUndoState()
	s.query = "modified query"
	s.cursorPos = 14

	// Save another state, modify again
	s.saveQueryUndoState()
	s.query = "final query"
	s.cursorPos = 11

	// Undo
	s.performUndo()
	if s.query != "modified query" || s.cursorPos != 14 {
		t.Errorf("Undo failed: query=%q, cursorPos=%d", s.query, s.cursorPos)
	}

	// Undo again
	s.performUndo()
	if s.query != "initial query" || s.cursorPos != 13 {
		t.Errorf("Undo failed: query=%q, cursorPos=%d", s.query, s.cursorPos)
	}

	// Redo
	s.performRedo()
	if s.query != "modified query" || s.cursorPos != 14 {
		t.Errorf("Redo failed: query=%q, cursorPos=%d", s.query, s.cursorPos)
	}

	// Redo again
	s.performRedo()
	if s.query != "final query" || s.cursorPos != 11 {
		t.Errorf("Redo failed: query=%q, cursorPos=%d", s.query, s.cursorPos)
	}
}

func TestScrollSelectedPaperIntoView(t *testing.T) {
	s := &tuiState{
		resultPane: resultPaneSources,
		result: &agent.YOLOResult{
			Papers: []agent.Paper{
				{Title: "Paper 1", Abstract: "Abstract 1"},
				{Title: "Paper 2", Abstract: "Abstract 2"},
				{Title: "Paper 3", Abstract: "Abstract 3"},
				{Title: "Paper 4", Abstract: "Abstract 4"},
				{Title: "Paper 5", Abstract: "Abstract 5"},
			},
		},
		terminalSize: func() (int, int, error) {
			return 80, 20, nil // viewport = 20 - 10 = 10 lines
		},
	}

	// 1. Initial selection at 0
	s.paperDetailIdx = 0
	s.scrollSelectedPaperIntoView()
	if s.scrollOffset != 0 {
		t.Errorf("expected scrollOffset 0, got %d", s.scrollOffset)
	}

	// 2. Select 5th paper, which is way below the 10 lines viewport
	s.paperDetailIdx = 4
	s.scrollSelectedPaperIntoView()
	if s.scrollOffset == 0 {
		t.Errorf("expected scrollOffset to increase, got 0")
	}

	// 3. Move back to 1st paper
	s.paperDetailIdx = 0
	s.scrollSelectedPaperIntoView()
	if s.scrollOffset != 0 {
		t.Errorf("expected scrollOffset to reset to 0, got %d", s.scrollOffset)
	}
}

