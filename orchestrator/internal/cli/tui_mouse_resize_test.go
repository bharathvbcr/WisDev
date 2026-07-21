package cli

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRuneAwareQueryEditing(t *testing.T) {
	s := &tuiState{mode: modeInput, activeElement: 0}
	s.insertQueryChar("café")
	if s.query != "café" || s.cursorPos != len("café") {
		t.Fatalf("after insert: query=%q cursor=%d", s.query, s.cursorPos)
	}
	s.backspaceQueryChar() // must remove the 2-byte 'é' as one rune, not one byte
	if s.query != "caf" || s.cursorPos != 3 {
		t.Fatalf("after backspace: query=%q cursor=%d, want (\"caf\", 3)", s.query, s.cursorPos)
	}
	if !utf8.ValidString(s.query) {
		t.Fatalf("backspace corrupted UTF-8: %q", s.query)
	}
}

func TestClassifyMouseEvent(t *testing.T) {
	cases := []struct {
		name              string
		in                []byte
		mouse, up, down   bool
	}{
		{"sgr wheel up", []byte("\033[<64;10;20M"), true, true, false},
		{"sgr wheel down", []byte("\033[<65;10;20M"), true, false, true},
		{"sgr wheel up + ctrl", []byte("\033[<80;1;1M"), true, true, false},
		{"sgr left click", []byte("\033[<0;5;5M"), true, false, false},
		{"sgr release", []byte("\033[<0;5;5m"), true, false, false},
		{"sgr horizontal wheel", []byte("\033[<66;1;1M"), true, false, false},
		{"legacy wheel up", []byte{27, '[', 'M', 0x60, '!', '!'}, true, true, false},
		{"legacy wheel down", []byte{27, '[', 'M', 0x61, '!', '!'}, true, false, true},
		{"legacy left click", []byte{27, '[', 'M', 0x20, '!', '!'}, true, false, false},
		{"plain arrow up (not mouse)", []byte{27, '[', 'A'}, false, false, false},
		{"printable", []byte("q"), false, false, false},
		{"malformed sgr", []byte("\033[<"), false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotMouse, gotUp, gotDown := classifyMouseEvent(c.in)
			if gotMouse != c.mouse || gotUp != c.up || gotDown != c.down {
				t.Fatalf("classifyMouseEvent(%q) = (%v,%v,%v), want (%v,%v,%v)",
					c.in, gotMouse, gotUp, gotDown, c.mouse, c.up, c.down)
			}
		})
	}
}

func TestPasteFromInput(t *testing.T) {
	// Bracketed paste is extracted and sanitized (markers stripped, newline
	// flattened to a space).
	if got, ok := pasteFromInput([]byte("\x1b[200~hello\nworld\x1b[201~")); !ok || got != "hello world" {
		t.Fatalf("bracketed paste = (%q, %v), want (\"hello world\", true)", got, ok)
	}
	// Non-ASCII printable runes are kept (the editors are rune-aware); a control
	// char inside is dropped.
	if got, ok := pasteFromInput([]byte("\x1b[200~café ☕\x1b[201~")); !ok || got != "café ☕" {
		t.Fatalf("non-ASCII paste = (%q, %v), want (\"café ☕\", true)", got, ok)
	}
	// A typed multi-byte UTF-8 rune is inserted as text (this is what makes
	// non-ASCII typing possible at all).
	if got, ok := pasteFromInput([]byte("é")); !ok || got != "é" {
		t.Fatalf("typed multi-byte rune = (%q, %v), want (\"é\", true)", got, ok)
	}
	// A literal Ctrl+V is always treated as a paste request (text comes from the
	// system clipboard, which we don't assert here).
	if _, ok := pasteFromInput([]byte{0x16}); !ok {
		t.Fatalf("Ctrl+V should be reported as a paste")
	}
	// Ordinary keystrokes and escape sequences are not pastes.
	for _, b := range [][]byte{[]byte("a"), {27, '[', 'A'}, {13}} {
		if _, ok := pasteFromInput(b); ok {
			t.Fatalf("%q should not be a paste", b)
		}
	}
}

func TestInsertIntoActiveTextField(t *testing.T) {
	// Query field: insert at the cursor.
	q := &tuiState{mode: modeInput, activeElement: 0, query: "ac", cursorPos: 1}
	if !q.insertIntoActiveTextField("b") || q.query != "abc" || q.cursorPos != 2 {
		t.Fatalf("query insert: query=%q cursor=%d", q.query, q.cursorPos)
	}
	// Chat field: insert at the chat cursor.
	c := &tuiState{mode: modeResults, chatOn: true, chatInput: "hi", chatCursorPos: 2}
	if !c.insertIntoActiveTextField("!") || c.chatInput != "hi!" || c.chatCursorPos != 3 {
		t.Fatalf("chat insert: input=%q cursor=%d", c.chatInput, c.chatCursorPos)
	}
	// Empty text and non-text focus are not consumed.
	if (&tuiState{mode: modeInput, activeElement: 0}).insertIntoActiveTextField("") {
		t.Fatalf("empty paste should not be consumed")
	}
	if (&tuiState{mode: modeInput, activeElement: 1}).insertIntoActiveTextField("x") {
		t.Fatalf("paste into a non-text element should not be consumed")
	}
}

func TestInputElementIsTextField(t *testing.T) {
	// 0 = query and 3 = output path are free-text fields where 'h'/'?' must be
	// typed; every other focusable element treats them as shortcuts.
	textFields := map[int]bool{0: true, 3: true}
	for el := 0; el <= 5; el++ {
		s := &tuiState{activeElement: el}
		if got := s.inputElementIsTextField(); got != textFields[el] {
			t.Fatalf("activeElement=%d: inputElementIsTextField()=%v, want %v", el, got, textFields[el])
		}
	}
}

func TestFlushFrameNoTrailingNewlineAndPerLineErase(t *testing.T) {
	var out bytes.Buffer
	s := &tuiState{output: &out}

	var frame bytes.Buffer
	frame.WriteString("\033[H")
	frame.WriteString("line one\n")
	frame.WriteString("line two\n")
	frame.WriteString("status bar\n")
	frame.WriteString("\033[J")
	s.flushFrame(&frame)

	got := out.String()

	// Every interior line keeps its CRLF newline but gains a tail-erase. CRLF
	// (not bare LF) is required because raw mode clears OPOST, so a lone \n would
	// staircase the frame on mac/Linux.
	if !strings.Contains(got, "line one\033[K\r\n") || !strings.Contains(got, "line two\033[K\r\n") {
		t.Fatalf("interior lines missing per-line erase / CRLF: %q", got)
	}
	// The final row erases its tail but must NOT end with a newline before the
	// trailing \033[J, otherwise the bottom row scrolls the top off-screen.
	if !strings.Contains(got, "status bar\033[K\033[J") {
		t.Fatalf("final row should be erased without a trailing newline: %q", got)
	}
	if strings.Contains(got, "status bar\033[K\r\n") {
		t.Fatalf("final row still emits a trailing newline: %q", got)
	}
	// We must never emit a full-screen clear from flushFrame (flicker source).
	if strings.Contains(got, "\033[2J") {
		t.Fatalf("flushFrame must not emit \\033[2J: %q", got)
	}
}
