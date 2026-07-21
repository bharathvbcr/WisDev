package docgen

import (
	"context"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/citations"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func TestGenerateOfflineReport(t *testing.T) {
	papers := []search.Paper{
		{Title: "Paper A", Authors: []string{"Smith, J."}, Year: 2024, Abstract: "Finding one."},
		{Title: "Paper B", Authors: []string{"Jones, K."}, Year: 2023, Abstract: "Finding two."},
	}
	result, err := Generate(context.Background(), Options{
		Query:         "test topic",
		Intent:        IntentReport,
		CitationStyle: citations.StyleAPA,
		Papers:        papers,
		Offline:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.Intent != IntentReport {
		t.Errorf("intent=%q want report", result.Document.Intent)
	}
	if len(result.Document.Sections) == 0 {
		t.Fatal("expected sections")
	}
	if len(result.Document.References) < 2 {
		t.Errorf("expected >=2 references, got %d", len(result.Document.References))
	}
}

func TestGenerateOfflineLitReview(t *testing.T) {
	papers := []search.Paper{
		{Title: "Survey Paper", Authors: []string{"Lee, M."}, Year: 2022, Abstract: "A survey."},
	}
	result, err := Generate(context.Background(), Options{
		Query:         "survey topic",
		Intent:        IntentLitReview,
		CitationStyle: citations.StyleMLA,
		Papers:        papers,
		Offline:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.Intent != IntentLitReview {
		t.Errorf("intent=%q want litreview", result.Document.Intent)
	}
	if !strings.Contains(result.Document.Sections[0].Content, "Survey Paper") {
		t.Error("expected paper title in scaffold content")
	}
}

func TestGenerateOfflineFullPaper(t *testing.T) {
	papers := []search.Paper{
		{Title: "Grounded Source", Abstract: "Evidence text.", Year: 2021},
	}
	result, err := Generate(context.Background(), Options{
		Query:   "full paper topic",
		Intent:  IntentFullPaper,
		Papers:  papers,
		Offline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.Intent != IntentFullPaper {
		t.Errorf("intent=%q want fullpaper", result.Document.Intent)
	}
	// Offline full-paper still runs the manuscript pipeline (offline mode).
	if len(result.Document.Sections) == 0 && len(result.Pipeline.SectionDrafts) == 0 {
		t.Error("expected at least scaffold sections from offline pipeline")
	}
}

func TestGenerateRequiresQuery(t *testing.T) {
	_, err := Generate(context.Background(), Options{Intent: IntentReport})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestGenerateDefaultIntent(t *testing.T) {
	// Empty intent defaults to fullpaper inside Generate.
	result, err := Generate(context.Background(), Options{
		Query:   "topic",
		Papers:  nil,
		Offline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.Intent != IntentFullPaper {
		t.Errorf("default intent=%q want fullpaper", result.Document.Intent)
	}
}
