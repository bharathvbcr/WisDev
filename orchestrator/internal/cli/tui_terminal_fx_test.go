package cli

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDesiredTerminalTitle(t *testing.T) {
	now := time.Unix(0, 0)
	cases := []struct {
		name  string
		state *tuiState
		want  string
	}{
		{"input mode", &tuiState{mode: modeInput}, "WisDev"},
		{"running", &tuiState{mode: modeRunning}, tuiSpinnerFrame(now) + " WisDev — researching"},
		{"paused", &tuiState{mode: modeRunning, paused: true}, "⏸ WisDev — paused"},
		{"results ok", &tuiState{mode: modeResults}, "✓ WisDev — done"},
		{"results failed", &tuiState{mode: modeResults, runError: fmt.Errorf("boom")}, "✗ WisDev — failed"},
	}
	for _, tc := range cases {
		if got := tc.state.desiredTerminalTitle(now); got != tc.want {
			t.Errorf("%s: desiredTerminalTitle = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTerminalTitleSequenceStripsControlChars(t *testing.T) {
	got := terminalTitleSequence("Wis\x07Dev\x1b]")
	want := "\033]0;WisDev]\007"
	if got != want {
		t.Errorf("terminalTitleSequence = %q, want %q", got, want)
	}
}

func TestDesiredTaskbarProgress(t *testing.T) {
	cases := []struct {
		name      string
		state     *tuiState
		wantState int
		wantPct   int
	}{
		{"input clears", &tuiState{mode: modeInput}, taskbarStateClear, 0},
		{"running no iterations is indeterminate", &tuiState{mode: modeRunning, requestedIterations: 6}, taskbarStateIndeterminate, 0},
		{"running mid-way", &tuiState{mode: modeRunning, requestedIterations: 6, iterations: 3}, taskbarStateNormal, 50},
		{"running capped below 100", &tuiState{mode: modeRunning, requestedIterations: 6, iterations: 6}, taskbarStateNormal, 99},
		{"paused keeps percent", &tuiState{mode: modeRunning, requestedIterations: 6, iterations: 3, paused: true}, taskbarStatePaused, 50},
		{"results ok clears", &tuiState{mode: modeResults}, taskbarStateClear, 0},
		{"results failed shows error", &tuiState{mode: modeResults, runError: fmt.Errorf("boom")}, taskbarStateError, 100},
	}
	for _, tc := range cases {
		gotState, gotPct := tc.state.desiredTaskbarProgress()
		if gotState != tc.wantState || gotPct != tc.wantPct {
			t.Errorf("%s: desiredTaskbarProgress = (%d, %d), want (%d, %d)", tc.name, gotState, gotPct, tc.wantState, tc.wantPct)
		}
	}
}

func TestTaskbarProgressSequenceClamps(t *testing.T) {
	if got := taskbarProgressSequence(taskbarStateNormal, 150); got != "\033]9;4;1;100\007" {
		t.Errorf("clamp high = %q", got)
	}
	if got := taskbarProgressSequence(taskbarStateNormal, -5); got != "\033]9;4;1;0\007" {
		t.Errorf("clamp low = %q", got)
	}
}

func TestTerminalStatusSequenceDeduplicates(t *testing.T) {
	s := &tuiState{mode: modeInput}
	now := time.Unix(0, 0)
	first := s.terminalStatusSequence(now)
	if !strings.Contains(first, "\033]0;WisDev\007") {
		t.Fatalf("first sequence missing title: %q", first)
	}
	if !strings.Contains(first, "\033]9;4;0;0\007") {
		t.Fatalf("first sequence missing taskbar clear: %q", first)
	}
	if again := s.terminalStatusSequence(now); again != "" {
		t.Fatalf("unchanged state should emit nothing, got %q", again)
	}
	s.mode = modeResults
	second := s.terminalStatusSequence(now)
	if !strings.Contains(second, "✓ WisDev — done") {
		t.Fatalf("transition to results should re-emit title, got %q", second)
	}
}

func TestRenderEmitsTerminalTitle(t *testing.T) {
	var out bytes.Buffer
	s := &tuiState{
		mode:   modeInput,
		output: &out,
		terminalSize: func() (int, int, error) {
			return 80, 24, nil
		},
	}
	s.render()
	if !strings.Contains(out.String(), "\033]0;WisDev\007") {
		t.Fatalf("render output missing terminal title sequence")
	}
}

type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriter) bells() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Count(w.buf.String(), "\a")
}

func TestPlayCompletionChime(t *testing.T) {
	w := &lockedWriter{}
	s := &tuiState{bellWriter: w}
	s.playCompletionChime(false)
	if got := w.bells(); got != 1 {
		t.Fatalf("failure chime: %d bells, want 1", got)
	}

	w2 := &lockedWriter{}
	s2 := &tuiState{bellWriter: w2}
	s2.playCompletionChime(true)
	deadline := time.Now().Add(2 * time.Second)
	for w2.bells() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := w2.bells(); got != 2 {
		t.Fatalf("success chime: %d bells, want 2", got)
	}

	w3 := &lockedWriter{}
	s3 := &tuiState{bellWriter: w3, noBell: true}
	s3.playCompletionChime(true)
	if got := w3.bells(); got != 0 {
		t.Fatalf("noBell chime: %d bells, want 0", got)
	}
}
