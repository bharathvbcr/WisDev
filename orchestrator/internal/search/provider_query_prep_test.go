package search

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/researchquery"
)

func TestParallelSearchQueryPreparationCorrectsTypos(t *testing.T) {
	got := researchquery.PrepareForProviderSearch("Menicius reconstruction stratiges")
	want := "meniscus reconstruction strategies"
	if got != want {
		t.Fatalf("PrepareForProviderSearch() = %q, want %q", got, want)
	}
}
