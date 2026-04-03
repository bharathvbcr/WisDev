package search

import (
	"context"
	"testing"
)

func TestSSRNProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	p := NewSSRNProvider()
	ctx := context.Background()
	papers, err := p.Search(ctx, "generative ai", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("SSRN search failed: %v", err)
	}

	if len(papers) == 0 {
		t.Log("Warning: SSRN returned 0 papers, but this might be expected if no papers match.")
	}

	for _, paper := range papers {
		if paper.Title == "" {
			t.Errorf("SSRN paper has empty title")
		}
		if paper.Source != "ssrn" {
			t.Errorf("Expected source 'ssrn', got %q", paper.Source)
		}
	}
}

func TestDOAJProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	p := NewDOAJProvider()
	ctx := context.Background()
	papers, err := p.Search(ctx, "machine learning", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("DOAJ search failed: %v", err)
	}

	if len(papers) == 0 {
		t.Log("Warning: DOAJ returned 0 papers.")
	}

	for _, paper := range papers {
		if paper.Title == "" {
			t.Errorf("DOAJ paper has empty title")
		}
		if paper.Source != "doaj" {
			t.Errorf("Expected source 'doaj', got %q", paper.Source)
		}
	}
}
