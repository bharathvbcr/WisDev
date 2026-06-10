package researchquery

import "testing"

func TestCorrectTyposMeniscusStrategies(t *testing.T) {
	got := CorrectTypos("Menicius reconstruction stratiges")
	want := "meniscus reconstruction strategies"
	if got != want {
		t.Fatalf("CorrectTypos() = %q, want %q", got, want)
	}
}

func TestPrepareForProviderSearch(t *testing.T) {
	got := PrepareForProviderSearch("  Menicius   reconstruction stratiges  ")
	want := "meniscus reconstruction strategies"
	if got != want {
		t.Fatalf("PrepareForProviderSearch() = %q, want %q", got, want)
	}
}
