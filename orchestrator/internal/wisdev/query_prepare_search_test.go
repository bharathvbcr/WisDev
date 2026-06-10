package wisdev

import (
	"strings"
	"sync"
	"testing"
)

func TestNormalizeLoopQueriesCorrectsTypos(t *testing.T) {
	queries := normalizeLoopQueries("Menicius reconstruction stratiges", []string{
		"reconstruction stratiges systematic review",
	})
	joined := strings.ToLower(strings.Join(queries, "|"))
	if strings.Contains(joined, "menicius") || strings.Contains(joined, "stratiges") {
		t.Fatalf("expected typo-free loop queries, got %#v", queries)
	}
	if !strings.Contains(joined, "meniscus") || !strings.Contains(joined, "strategies") {
		t.Fatalf("expected corrected meniscus strategies, got %#v", queries)
	}
}

func TestPrepareSearchQueryTextCorrectsTypos(t *testing.T) {
	got := prepareSearchQueryText("Menicius reconstruction stratiges")
	want := "meniscus reconstruction strategies"
	if got != want {
		t.Fatalf("prepareSearchQueryText(%q) = %q, want %q", "Menicius reconstruction stratiges", got, want)
	}
}

func TestBuildAutonomousResearchAgendaQueriesCorrectsTypos(t *testing.T) {
	preparedQueryCache = sync.Map{}
	queries := buildAutonomousResearchAgendaQueries("Menicius reconstruction stratiges", "medicine", string(WisDevModeYOLO), ResearchExecutionPlaneSimple, nil)
	joined := strings.ToLower(strings.Join(queries, "|"))
	if strings.Contains(joined, "menicius") || strings.Contains(joined, "stratiges") {
		t.Fatalf("expected corrected agenda queries, got %#v", queries)
	}
	if !strings.Contains(joined, "meniscus") || !strings.Contains(joined, "systematic review") {
		t.Fatalf("expected meniscus agenda branches, got %#v", queries)
	}
}

func TestNormalizeSearchQueryCorrectsTypos(t *testing.T) {
	got := normalizeSearchQuery("Menicius reconstruction stratiges")
	want := "meniscus reconstruction strategies"
	if got != want {
		t.Fatalf("normalizeSearchQuery() = %q, want %q", got, want)
	}
}

func TestNormalizeAgendaFocusAvoidsDuplicateRoot(t *testing.T) {
	root := "meniscus reconstruction strategies"
	got := normalizeAgendaFocus(root, root+" systematic review meta analysis")
	want := "meniscus reconstruction strategies systematic review meta analysis"
	if got != want {
		t.Fatalf("normalizeAgendaFocus duplicate root = %q, want %q", got, want)
	}
}
