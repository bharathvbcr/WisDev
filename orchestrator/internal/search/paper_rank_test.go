package search

import "testing"

func TestRecencyNormPrefersRecentYears(t *testing.T) {
	currentYear := 2026
	recent := RecencyNorm(currentYear)
	mid := RecencyNorm(currentYear - 6)
	old := RecencyNorm(currentYear - 15)
	if recent <= mid || mid <= old {
		t.Fatalf("expected recent > mid > old, got recent=%v mid=%v old=%v", recent, mid, old)
	}
}

func TestSortPapersByPreferenceFavorsRecentOverStaleHighlyCited(t *testing.T) {
	currentYear := 2026
	sorted := SortPapersByPreference([]Paper{
		{Title: "Stale landmark", CitationCount: 291, Year: currentYear - 17},
		{Title: "Recent review", CitationCount: 60, Year: currentYear - 6},
		{Title: "Very recent", CitationCount: 12, Year: currentYear - 2},
	})
	if sorted[0].Title != "Very recent" {
		t.Fatalf("expected newest paper first, got %#v", sorted)
	}
	if sorted[0].Title == "Stale landmark" {
		t.Fatalf("stale highly-cited paper should not rank first: %#v", sorted)
	}
}

func TestPaperPreferenceScoreRecentBeatsOldWithSimilarCitations(t *testing.T) {
	currentYear := 2026
	recent := Paper{Year: currentYear - 2, CitationCount: 80}
	old := Paper{Year: currentYear - 14, CitationCount: 100}
	if PaperPreferenceScore(recent) <= PaperPreferenceScore(old) {
		t.Fatalf("expected recent paper to outrank stale paper with similar citations")
	}
}

func TestSortPapersByPreferenceWithQuery(t *testing.T) {
	currentYear := 2026

	t.Run("Recent query intent prefers new papers", func(t *testing.T) {
		papers := []Paper{
			{Title: "Stale landmark", CitationCount: 5000, Year: currentYear - 15},
			{Title: "Recent findings", CitationCount: 5, Year: currentYear - 1},
		}
		sorted := SortPapersByPreferenceWithQuery(papers, "latest sota trends in AI")
		if sorted[0].Title != "Recent findings" {
			t.Fatalf("expected recent findings first under recent query intent")
		}
	})

	t.Run("Cited query intent prefers classic highly cited papers", func(t *testing.T) {
		papers := []Paper{
			{Title: "Stale landmark", CitationCount: 5000, Year: currentYear - 15},
			{Title: "Recent findings", CitationCount: 5, Year: currentYear - 1},
		}
		sorted := SortPapersByPreferenceWithQuery(papers, "classic seminal papers on database")
		if sorted[0].Title != "Stale landmark" {
			t.Fatalf("expected classic seminal paper first under cited query intent")
		}
	})

	t.Run("Author impact query intent prefers influential citations", func(t *testing.T) {
		papers := []Paper{
			{Title: "Many low impact", CitationCount: 200, InfluentialCitationCount: 2, Year: currentYear - 2},
			{Title: "Medium citations highly influential", CitationCount: 100, InfluentialCitationCount: 80, Year: currentYear - 2},
		}
		sorted := SortPapersByPreferenceWithQuery(papers, "high h-index prestigious author research")
		if sorted[0].Title != "Medium citations highly influential" {
			t.Fatalf("expected paper with high influential citations first under author impact query intent")
		}
	})
}

