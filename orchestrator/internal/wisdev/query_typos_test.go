package wisdev

import (
	"strings"
	"testing"
)

func TestCorrectCommonResearchTyposMeniscusQuery(t *testing.T) {
	got := correctCommonResearchTypos("Menicius reconstruction stratiges")
	want := "meniscus reconstruction strategies"
	if got != want {
		t.Fatalf("correctCommonResearchTypos() = %q, want %q", got, want)
	}
}

func TestCorrectCommonResearchTyposACLScaffoldQuery(t *testing.T) {
	got := correctCommonResearchTypos("meniscus scaffolds and acl re constricution stratigies_")
	if got == "meniscus scaffolds and acl re constricution stratigies_" {
		t.Fatalf("expected typo corrections, got %q", got)
	}
	if !containsAll(got, "meniscus", "scaffolds", "reconstruction", "strategies") {
		t.Fatalf("expected scaffold/reconstruction/strategies tokens, got %q", got)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !containsFold(text, part) {
			return false
		}
	}
	return true
}

func containsFold(text, part string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(part))
}
