package cli

import (
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func TestCorpusDumpLoadRoundTrip(t *testing.T) {
	papers := []search.Paper{
		{ID: "p1", Title: "RAG for clinical QA", Abstract: "An abstract.", Year: 2024, CitationCount: 11, Authors: []string{"A. One"}, DOI: "10.1/x"},
		{ID: "p2", Title: "Grounding clinical LLMs", Abstract: "Another abstract.", Year: 2023, Authors: []string{"B. Two", "C. Three"}},
	}
	path := filepath.Join(t.TempDir(), "corpus.json")
	if err := dumpCorpusPapers(path, papers); err != nil {
		t.Fatalf("dump: %v", err)
	}
	loaded, err := loadCorpusPapers(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != len(papers) {
		t.Fatalf("round-trip changed count: got %d want %d", len(loaded), len(papers))
	}
	if loaded[0].Title != papers[0].Title || loaded[1].Year != papers[1].Year || loaded[0].DOI != papers[0].DOI {
		t.Fatalf("round-trip lost fields: %+v", loaded)
	}
	if len(loaded[1].Authors) != 2 {
		t.Fatalf("round-trip lost authors: %+v", loaded[1].Authors)
	}
}

func TestLoadCorpusPapersMissingFileErrors(t *testing.T) {
	if _, err := loadCorpusPapers(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatalf("expected error for missing corpus file")
	}
}
