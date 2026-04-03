package rag

import (
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"testing"
)

func TestBM25(t *testing.T) {
	bm25 := NewBM25()
	docs := []string{
		"The quick brown fox jumps over the lazy dog",
		"Academic papers on artificial intelligence and machine learning",
		"Retrieval augmented generation for research assistants",
	}

	// Query matches second doc
	scores1 := bm25.Score("artificial intelligence", docs)
	if scores1[1] <= scores1[0] || scores1[1] <= scores1[2] {
		t.Errorf("Expected second doc to have highest score for AI query")
	}

	// Query matches third doc
	scores2 := bm25.Score("research RAG", docs)
	if scores2[2] <= scores2[0] || scores2[2] <= scores2[1] {
		t.Errorf("Expected third doc to have highest score for RAG query")
	}
}

func TestFusion(t *testing.T) {
	list1 := []search.Paper{
		{ID: "A", Title: "Paper A", DOI: "10.1"},
		{ID: "B", Title: "Paper B", DOI: "10.2"},
	}
	list2 := []search.Paper{
		{ID: "C", Title: "Paper C", DOI: "10.3"},
		{ID: "A", Title: "Paper A", DOI: "10.1"}, // Duplicate
	}

	fused := RRF([][]search.Paper{list1, list2}, 60)

	if len(fused) != 3 {
		t.Errorf("Expected 3 unique papers after fusion, got %d", len(fused))
	}

	// Paper A should be first as it's in both lists
	if fused[0].ID != "A" {
		t.Errorf("Expected Paper A to be first after fusion")
	}
}

func TestExtractCitations(t *testing.T) {
	e := &Engine{}
	papers := []search.Paper{
		{ID: "P1", Title: "Climate Change 2024"},
		{ID: "P2", Title: "Renewable Energy Analysis"},
	}

	text := "Recent studies show global temperatures rising [1]. Solar power is efficient [2]."
	citations := e.extractCitations(text, papers)

	if len(citations) != 2 {
		t.Errorf("Expected 2 citations, got %d", len(citations))
	}

	if citations[0].SourceID != "P1" || citations[1].SourceID != "P2" {
		t.Errorf("Citations mapped incorrectly: %v", citations)
	}
}
