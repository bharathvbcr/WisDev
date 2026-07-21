package cli

import "unicode/utf8"

// Text-input cursors (query, save path, chat) track byte offsets into their
// strings. Stepping them by ±1 byte lands inside multi-byte UTF-8 runes —
// é, em dashes, CJK, emoji — which corrupts the string on backspace/delete
// and renders mojibake where the caret splits a rune. These helpers step a
// byte offset over one full rune in either direction.

func nextCharBoundary(s string, pos int) int {
	if pos < 0 {
		return 0
	}
	if pos >= len(s) {
		return len(s)
	}
	_, size := utf8.DecodeRuneInString(s[pos:])
	return pos + size
}

func prevCharBoundary(s string, pos int) int {
	if pos > len(s) {
		return len(s)
	}
	if pos <= 0 {
		return 0
	}
	_, size := utf8.DecodeLastRuneInString(s[:pos])
	return pos - size
}
