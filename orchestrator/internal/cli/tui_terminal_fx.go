package cli

// Terminal integration effects for the TUI: window/tab title (OSC 0),
// taskbar progress (ConEmu / Windows Terminal OSC 9;4), and the
// completion chime. Terminals that do not understand a sequence ignore it.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	termTitleBase = "WisDev"

	taskbarStateClear         = 0
	taskbarStateNormal        = 1
	taskbarStateError         = 2
	taskbarStateIndeterminate = 3
	taskbarStatePaused        = 4

	completionChimeGap = 180 * time.Millisecond
)

var tuiSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func tuiSpinnerFrame(now time.Time) string {
	return tuiSpinnerFrames[int(now.UnixNano()/250000000)%len(tuiSpinnerFrames)]
}

// terminalTitleSequence returns the OSC 0 escape that sets the terminal
// window/tab title. Control characters are stripped so the title text can
// never terminate the sequence early.
func terminalTitleSequence(title string) string {
	title = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, title)
	return "\033]0;" + title + "\007"
}

// taskbarProgressSequence returns the OSC 9;4 escape understood by
// Windows Terminal and ConEmu to drive the taskbar progress indicator.
func taskbarProgressSequence(state, percent int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("\033]9;4;%d;%d\007", state, percent)
}

// saveTerminalTitleSequence / restoreTerminalTitleSequence push and pop the
// title on the terminal's title stack (XTWINOPS 22/23) so the user's
// original title comes back when the TUI exits.
func saveTerminalTitleSequence() string    { return "\033[22;0t" }
func restoreTerminalTitleSequence() string { return "\033[23;0t" }

// desiredTerminalTitle reflects the current TUI state into a title string.
func (s *tuiState) desiredTerminalTitle(now time.Time) string {
	switch s.mode {
	case modeRunning:
		if s.paused {
			return "⏸ " + termTitleBase + " — paused"
		}
		return tuiSpinnerFrame(now) + " " + termTitleBase + " — researching"
	case modeResults:
		if s.runError != nil {
			return "✗ " + termTitleBase + " — failed"
		}
		return "✓ " + termTitleBase + " — done"
	default:
		return termTitleBase
	}
}

// desiredTaskbarProgress maps TUI state onto an OSC 9;4 state/percent pair.
func (s *tuiState) desiredTaskbarProgress() (state, percent int) {
	switch s.mode {
	case modeRunning:
		pct := 0
		if s.requestedIterations > 0 && s.iterations > 0 {
			pct = s.iterations * 100 / s.requestedIterations
			if pct > 99 {
				pct = 99 // never show 100% while still running
			}
		}
		if s.paused {
			return taskbarStatePaused, pct
		}
		if pct == 0 {
			return taskbarStateIndeterminate, 0
		}
		return taskbarStateNormal, pct
	case modeResults:
		if s.runError != nil {
			return taskbarStateError, 100
		}
		return taskbarStateClear, 0
	default:
		return taskbarStateClear, 0
	}
}

// terminalStatusSequence returns the escapes needed to bring the terminal
// title and taskbar progress in sync with the TUI state, deduplicated so an
// unchanged state emits nothing.
func (s *tuiState) terminalStatusSequence(now time.Time) string {
	var b strings.Builder
	if title := s.desiredTerminalTitle(now); title != s.lastTermTitle {
		s.lastTermTitle = title
		b.WriteString(terminalTitleSequence(title))
		if s.nativeTitle {
			setConsoleTitleNative(title)
		}
	}
	if !s.disableTaskbarOSC {
		state, pct := s.desiredTaskbarProgress()
		if seq := taskbarProgressSequence(state, pct); seq != s.lastTaskbarSeq {
			s.lastTaskbarSeq = seq
			b.WriteString(seq)
		}
	}
	return b.String()
}

func (s *tuiState) bellOutput() io.Writer {
	if s.bellWriter != nil {
		return s.bellWriter
	}
	return os.Stdout
}

// playCompletionChime rings the terminal bell: twice (with a short gap) on
// success, once on failure. Honors the --no-bell flag.
func (s *tuiState) playCompletionChime(success bool) {
	if s.noBell {
		return
	}
	w := s.bellOutput()
	io.WriteString(w, "\a")
	if success {
		time.AfterFunc(completionChimeGap, func() {
			io.WriteString(w, "\a")
		})
	}
}

// taskbarProgressSupported reports whether the host terminal understands the
// ConEmu/Windows Terminal OSC 9;4 taskbar-progress sequence. iTerm2 shows a
// bare OSC 9 payload as a desktop notification — emitting progress there
// spams the user with popups on every state change — and Apple Terminal does
// not know the code at all, so allowlist terminals with known support
// instead of broadcasting it.
func taskbarProgressSupported() bool {
	if os.Getenv("WT_SESSION") != "" || os.Getenv("ConEmuANSI") == "ON" {
		return true
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm":
		return true
	}
	return false
}
