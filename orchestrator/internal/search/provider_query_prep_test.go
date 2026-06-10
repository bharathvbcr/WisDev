package search

import (
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/researchquery"
)

func TestParallelSearchQueryPreparationCorrectsTypos(t *testing.T) {
	got := researchquery.PrepareForProviderSearch("Menicius reconstruction stratiges")
	want := "meniscus reconstruction strategies"
	if got != want {
		t.Fatalf("PrepareForProviderSearch() = %q, want %q", got, want)
	}
}
