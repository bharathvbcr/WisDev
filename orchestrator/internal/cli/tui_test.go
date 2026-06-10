package cli

import (
	"bytes"
	"strings"
	"testing"

	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

func TestRemoveEscapeSequences(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"\033[1;36mHello\033[0m World", "Hello World"},
		{"Plain text", "Plain text"},
		{"\033[31mRed\033[0m \033[32mGreen\033[0m", "Red Green"},
		{"\033]8;;https://example.com\007link\033]8;;\007", "link"},
	}

	for _, tc := range cases {
		got := removeEscapeSequences(tc.input)
		if got != tc.expected {
			t.Errorf("removeEscapeSequences(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestWrapText(t *testing.T) {
	text := "This is a long sentence that should be wrapped into multiple lines."
	width := 20

	lines := wrapText(text, width)
	if len(lines) < 2 {
		t.Fatalf("expected wrapped text to have multiple lines, got %d", len(lines))
	}

	for i, line := range lines {
		if len(line) > width {
			t.Errorf("line %d (%q) is longer than width %d", i, line, width)
		}
	}
}

func TestTuiStateInitialization(t *testing.T) {
	state := &tuiState{
		mode:          modeInput,
		activeElement: 0,
		providers: []tuiProvider{
			{name: "OpenAlex", code: "openalex", enabled: true},
			{name: "arXiv", code: "arxiv", enabled: false},
		},
	}

	if state.mode != modeInput {
		t.Errorf("expected modeInput, got %v", state.mode)
	}

	if len(state.providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(state.providers))
	}

	if !state.providers[0].enabled {
		t.Error("expected first provider to be enabled")
	}

	if state.providers[1].enabled {
		t.Error("expected second provider to be disabled")
	}
}

func TestTuiRenderingDoesNotCrash(t *testing.T) {
	var output bytes.Buffer
	// A simple test ensuring render code works with typical inputs without panic
	state := &tuiState{
		mode:          modeInput,
		activeElement: 0,
		query:         "RAG models",
		providers: []tuiProvider{
			{name: "OpenAlex", code: "openalex", enabled: true},
		},
		output: &output,
		terminalSize: func() (int, int, error) {
			return 100, 30, nil
		},
	}

	// Capture panic if any
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("render function panicked: %v", r)
		}
	}()

	state.render()
	if !strings.Contains(output.String(), "WisDev Research") {
		t.Fatal("expected TUI render output to include input-screen title")
	}
}

func TestTuiWrapTextHandlesParagraphs(t *testing.T) {
	text := "First paragraph.\n\nSecond paragraph."
	lines := wrapText(text, 50)

	foundEmpty := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			foundEmpty = true
		}
	}
	if !foundEmpty {
		t.Error("expected wrapText to preserve paragraph breaks/empty lines")
	}
}

func TestDefaultTUIProviders(t *testing.T) {
	providers := defaultTUIProviders()
	if len(providers) == 0 {
		t.Fatal("expected at least one default provider")
	}

	hasOpenAlex := false
	for _, p := range providers {
		if p.code == "openalex" {
			hasOpenAlex = true
			if !p.enabled {
				t.Error("expected openalex to be enabled by default")
			}
		}
	}
	if !hasOpenAlex {
		t.Error("expected default providers to contain openalex")
	}
}

func TestTuiVisibleTruncationStripsAnsiSafely(t *testing.T) {
	input := "\033[1;32mabcdef\033[0m"
	if got := visibleWidth(input); got != 6 {
		t.Fatalf("visibleWidth() = %d; want 6", got)
	}

	got := truncateVisible(input, 4)
	if !strings.Contains(got, "abc") || !strings.Contains(got, "…") {
		t.Fatalf("truncateVisible() = %q; want truncated visible text with ellipsis", got)
	}
	if !strings.Contains(got, "\033[") {
		t.Fatalf("truncateVisible() should preserve ANSI sequences, got %q", got)
	}
}

func TestTuiEnabledProviderCount(t *testing.T) {
	state := &tuiState{
		providers: []tuiProvider{
			{name: "OpenAlex", code: "openalex", enabled: true},
			{name: "arXiv", code: "arxiv", enabled: false},
			{name: "PubMed", code: "pubmed", enabled: true},
		},
	}

	if got := state.enabledProviderCount(); got != 2 {
		t.Fatalf("enabledProviderCount() = %d; want 2", got)
	}
}

func TestTuiFocusWraps(t *testing.T) {
	state := &tuiState{activeElement: 5}
	state.focusNext()
	if state.activeElement != 0 {
		t.Fatalf("focusNext wrapped to %d; want 0", state.activeElement)
	}

	state.focusPrevious()
	if state.activeElement != 5 {
		t.Fatalf("focusPrevious wrapped to %d; want 5", state.activeElement)
	}
}

func TestShouldShowTUILogFiltersNoisyProviderLogs(t *testing.T) {
	if shouldShowTUILog("wisdev search query completed provider=openalex") {
		t.Fatal("expected provider lifecycle log to be hidden")
	}
	if !shouldShowTUILog("loop iteration complete") {
		t.Fatal("expected loop progress log to be visible")
	}
	if !shouldShowTUILog("[evaluate_sufficiency] using heuristic fallback") {
		t.Fatal("expected heuristic fallback log to be visible")
	}
}

func TestFormatProgressEvent(t *testing.T) {
	msg, tag := formatProgressEvent(agent.ProgressEvent{
		Stage:   "search_result_admitted",
		Message: "Admitted 3 papers for meniscus strategies",
		Payload: map[string]any{
			"accepted_count": 3,
			"stage":          "search_result_admitted",
		},
	})
	if tag != "I" {
		t.Fatalf("tag = %q; want I", tag)
	}
	if !strings.Contains(msg, "[search_result_admitted]") || !strings.Contains(msg, "Admitted 3 papers") {
		t.Fatalf("unexpected progress message: %q", msg)
	}

	msg, tag = formatProgressEvent(agent.ProgressEvent{
		Stage:    "evaluate_sufficiency",
		Message:  "Sufficiency evaluation failed; using heuristic fallback",
		Degraded: true,
		Payload: map[string]any{
			"degraded": true,
			"fallback": "heuristic",
		},
	})
	if tag != "W" {
		t.Fatalf("degraded tag = %q; want W", tag)
	}
	if !strings.Contains(msg, "fallback=heuristic") {
		t.Fatalf("expected fallback detail in %q", msg)
	}
}

func TestRenderProgressBar(t *testing.T) {
	bar := renderProgressBar(2, 4, 30, 10*1000000000)
	if !strings.Contains(bar, "█") || !strings.Contains(bar, "░") {
		t.Fatalf("unexpected progress bar: %q", bar)
	}
}
