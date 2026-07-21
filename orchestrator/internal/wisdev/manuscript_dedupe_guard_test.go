package wisdev

import (
	"strings"
	"testing"
)

// The coordinate-dedupe sidecar prompt truncates each section to
// dedupePromptWindow chars; replacing a longer section with the rewrite would
// silently amputate the unseen tail. Degenerate rewrites that gut a section
// must also be rejected.
func TestDedupeReplacementSafe(t *testing.T) {
	short := strings.Repeat("a", 1000)
	long := strings.Repeat("a", dedupePromptWindow+1)

	if !dedupeReplacementSafe(short, short) {
		t.Fatal("same-length rewrite of an in-window section must be safe")
	}
	if !dedupeReplacementSafe(short, strings.Repeat("a", 500)) {
		t.Fatal("a 50% trim (legit dedupe) must be safe")
	}
	if dedupeReplacementSafe(short, strings.Repeat("a", 100)) {
		t.Fatal("a rewrite keeping only 10% of the section must be rejected")
	}
	if dedupeReplacementSafe(long, long) {
		t.Fatal("a section longer than the sidecar prompt window was only partially seen — must be rejected")
	}
}
