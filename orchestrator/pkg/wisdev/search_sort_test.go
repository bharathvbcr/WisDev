package wisdev

import "testing"

func TestPreferCitedPapers(t *testing.T) {
	mixed := PreferCitedPapers([]Paper{
		{Title: "Uncited", CitationCount: 0},
		{Title: "Cited", CitationCount: 12},
	})
	if len(mixed) != 1 || mixed[0].Title != "Cited" {
		t.Fatalf("expected only cited paper, got %#v", mixed)
	}
	allUncited := PreferCitedPapers([]Paper{
		{Title: "A", CitationCount: 0},
		{Title: "B", CitationCount: 0},
	})
	if len(allUncited) != 2 {
		t.Fatalf("expected fallback when no citations, got %#v", allUncited)
	}
}

func TestSortPapersByCitations(t *testing.T) {
	sorted := SortPapersByCitations([]Paper{
		{Title: "Stale", CitationCount: 200, Year: 2009},
		{Title: "Recent", CitationCount: 40, Year: 2024},
		{Title: "Mid", CitationCount: 50, Year: 2018},
	})
	if sorted[0].Title != "Recent" {
		t.Fatalf("expected recent paper first, got %#v", sorted)
	}
	if sorted[len(sorted)-1].Title != "Stale" {
		t.Fatalf("expected stale highly-cited paper last, got %#v", sorted)
	}
}
