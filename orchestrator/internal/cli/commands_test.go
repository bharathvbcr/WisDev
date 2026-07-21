package cli

import "testing"

func TestNormalizeInvocationBareQuestion(t *testing.T) {
	got := normalizeInvocation([]string{"RAG", "for", "papers"})
	want := []string{"search", "RAG", "for", "papers"}
	if len(got) != len(want) {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestNormalizeInvocationAliases(t *testing.T) {
	cases := map[string]string{
		"ask":     "search",
		"check":   "doctor",
		"sources": "providers",
		"setup":   "mcp-config",
	}
	for input, want := range cases {
		got := normalizeInvocation([]string{input})
		if len(got) != 1 || got[0] != want {
			t.Fatalf("%s: expected %q, got %#v", input, want, got)
		}
	}
}
