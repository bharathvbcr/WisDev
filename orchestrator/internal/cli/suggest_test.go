package cli

import "testing"

func TestSuggestCommand(t *testing.T) {
	if got := suggestCommand("serch"); got != "search" {
		t.Fatalf("expected search, got %q", got)
	}
	if got := suggestCommand("doctr"); got != "doctor" {
		t.Fatalf("expected doctor, got %q", got)
	}
	if got := suggestCommand("zzzz"); got != "" {
		t.Fatalf("expected empty suggestion, got %q", got)
	}
}
