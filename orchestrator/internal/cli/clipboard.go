package cli

import (
	"bytes"
	"os/exec"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
)

// pasteFromInput reports whether a raw stdin chunk should be inserted as text
// and, if so, returns that text. It recognizes three cases: a bracketed-paste
// burst (\033[200~ … \033[201~, emitted by terminals once mode 2004 is on); a
// literal Ctrl+V (0x16) the host passed through instead of binding to paste, in
// which case it reads the system clipboard; and a typed multi-byte UTF-8 rune
// (or a non-bracketed multi-character burst). The last case is what lets
// non-ASCII characters be entered at all, since the per-key handlers only accept
// single printable ASCII bytes — the field editors index by rune, so this is
// safe.
func pasteFromInput(b []byte) (string, bool) {
	if bytes.Contains(b, []byte("\x1b[200~")) {
		return sanitizePastedInput(string(b)), true
	}
	if len(b) == 1 && b[0] == 0x16 { // Ctrl+V
		return sanitizePastedInput(readSystemClipboard()), true
	}
	if len(b) >= 2 && b[0] != 0x1b && utf8.Valid(b) {
		for _, r := range string(b) {
			if unicode.IsControl(r) {
				return "", false // an escape/function-key sequence, not text
			}
		}
		return string(b), true
	}
	return "", false
}

// readSystemClipboard returns the current system clipboard text, or an empty
// string if no clipboard tool is available. It shells out to the platform's
// standard utility so paste works without a cgo clipboard dependency.
func readSystemClipboard() string {
	var candidates [][]string
	switch runtime.GOOS {
	case "windows":
		candidates = [][]string{{"powershell", "-NoProfile", "-Command", "Get-Clipboard"}}
	case "darwin":
		candidates = [][]string{{"pbpaste"}}
	default: // linux / *bsd
		candidates = [][]string{
			{"wl-paste", "--no-newline"},
			{"xclip", "-selection", "clipboard", "-o"},
			{"xsel", "-b", "-o"},
		}
	}
	for _, args := range candidates {
		if _, err := exec.LookPath(args[0]); err != nil {
			continue
		}
		out, err := exec.Command(args[0], args[1:]...).Output()
		if err != nil {
			continue
		}
		return string(out)
	}
	return ""
}

// sanitizePastedInput flattens clipboard text into a single-line string
// suitable for a TUI text field. It strips bracketed-paste markers, converts
// newlines/tabs to spaces, and drops control runes (incl. ESC/DEL). Non-ASCII
// printable runes are kept — the field editors index by rune (backspace/cursor
// move on rune boundaries), so multi-byte characters are safe.
func sanitizePastedInput(s string) string {
	s = strings.ReplaceAll(s, "\x1b[200~", "")
	s = strings.ReplaceAll(s, "\x1b[201~", "")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r):
			// drop other control characters (incl. ESC, DEL)
		default:
			b.WriteRune(r) // keep printable runes, including non-ASCII
		}
	}
	// Collapse runs of spaces introduced by flattened newlines.
	return strings.Join(strings.Fields(b.String()), " ")
}
