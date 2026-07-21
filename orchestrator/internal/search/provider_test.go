package search

import (
	"context"
	"testing"
)

// MockProvider for testing
type MockProvider struct {
	name     string
	papers   []Paper
	err      error
	domains  []string
	healthy  *bool
	searchFn func(context.Context, string, SearchOpts) ([]Paper, error)
}

func (m *MockProvider) Name() string { return m.name }
func (m *MockProvider) Domains() []string {
	if len(m.domains) == 0 {
		return []string{"general"}
	}
	return m.domains
}
func (m *MockProvider) Healthy() bool {
	if m.healthy == nil {
		return true
	}
	return *m.healthy
}
func (m *MockProvider) Tools() []string { return nil }
func (m *MockProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, query, opts)
	}
	return m.papers, m.err
}

func TestDeduplicate(t *testing.T) {
	papers := []Paper{
		{
			ID:         "p1",
			Title:      "Paper A",
			DOI:        "10.1111/a",
			Score:      0.9,
			Source:     "semantic_scholar",
			SourceApis: []string{"semantic_scholar"},
		},
		{
			ID:                       "p2",
			Title:                    "Paper A",
			DOI:                      "10.1111/a",
			Score:                    0.8,
			Source:                   "openalex",
			SourceApis:               []string{"openalex"},
			Authors:                  []string{"Ada Lovelace", "Grace Hopper"},
			Venue:                    "Nature",
			CitationCount:            12,
			ReferenceCount:           8,
			InfluentialCitationCount: 2,
			OpenAccessUrl:            "https://example.com/oa",
		}, // Duplicate by DOI
		{ID: "p3", Title: "Paper B", Abstract: "Test", Score: 0.7},
		{ID: "p4", Title: "Paper B", Score: 0.6}, // Duplicate by Title (and length > 5)
		{ID: "p5", Title: "Paper C", DOI: "10.2222/b", Score: 0.5},
	}

	deduped := Deduplicate(papers)

	if len(deduped) != 3 {
		t.Errorf("Expected 3 unique papers, got %d", len(deduped))
	}
	if len(deduped[0].SourceApis) != 2 {
		t.Errorf("Expected provider provenance to merge, got %+v", deduped[0].SourceApis)
	}
	if len(deduped[0].Authors) != 2 {
		t.Errorf("Expected author metadata to merge, got %+v", deduped[0].Authors)
	}
	if deduped[0].Venue != "Nature" || deduped[0].CitationCount != 12 || deduped[0].ReferenceCount != 8 {
		t.Errorf("Expected richer duplicate metadata to be preserved, got %+v", deduped[0])
	}
	if deduped[0].OpenAccessUrl != "https://example.com/oa" {
		t.Errorf("Expected open access URL to be preserved, got %+v", deduped[0])
	}
}

func TestDeduplicate_SourceFallback(t *testing.T) {
	papers := []Paper{
		{ID: "p1", Title: "Paper D", Source: "openalex"},
		{ID: "p2", Title: "Paper D", Source: "semantic_scholar"},
	}

	deduped := Deduplicate(papers)

	if len(deduped) != 1 {
		t.Fatalf("expected 1 unique paper, got %d", len(deduped))
	}
	if len(deduped[0].SourceApis) == 0 {
		t.Fatalf("expected source fallback to populate SourceApis, got %+v", deduped[0].SourceApis)
	}
	if deduped[0].SourceApis[0] != "openalex" && deduped[0].SourceApis[0] != "semantic_scholar" {
		t.Fatalf("unexpected source provenance: %+v", deduped[0].SourceApis)
	}
}

func TestParallelSearch(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{
		name: "mock1",
		papers: []Paper{
			{ID: "1", Title: "Mock Paper 1", DOI: "10.123/1", Source: "mock1"},
		},
	})
	reg.Register(&MockProvider{
		name: "mock2",
		papers: []Paper{
			{ID: "2", Title: "Mock Paper 1", DOI: "10.123/1", Source: "mock2"}, // Duplicate
			{ID: "3", Title: "Mock Paper 2", DOI: "10.123/2", Source: "mock2"},
		},
	})

	ApplyDomainRoutes(reg)

	opts := SearchOpts{
		Limit:       10,
		Domain:      "general",
		QualitySort: true,
	}

	result := ParallelSearch(context.Background(), reg, "test query", opts)

	if len(result.Papers) != 2 {
		t.Errorf("Expected 2 papers after deduplication, got %d", len(result.Papers))
	}

	if result.Providers["mock1"] != 1 || result.Providers["mock2"] != 2 {
		t.Errorf("Unexpected provider counts: %v", result.Providers)
	}
	if len(result.Papers[0].SourceApis) == 0 {
		t.Errorf("Expected fused paper to retain sourceApis metadata")
	}
}

func TestInferEvidenceLevel(t *testing.T) {
	cases := []struct {
		title    string
		abstract string
		expected string
	}{
		{"Systematic Review of AI", "Methods: exhaustive search", "systematic-review"},
		{"Meta-analysis of clinical trials", "We pooled results", "systematic-review"},
		{"Survey of Deep Learning", "Recent trends", "review"},
		{"Double-blind Randomized Controlled Trial", "RCT results", "rct"},
		{"A longitudinal cohort study", "Followed for 10 years", "cohort"},
		{"Case report: rare disease", "A 45-year old man", "case-report"},
		{"Cross-sectional analysis", "Snapshot of data", "cross-sectional"},
		{"Generic paper", "Nothing special", "unknown"},
	}

	for _, c := range cases {
		p := Paper{Title: c.title, Abstract: c.abstract}
		got := InferEvidenceLevel(p)
		if got != c.expected {
			t.Errorf("For %q, expected %q, got %q", c.title, c.expected, got)
		}
	}
}

func TestInferEvidenceLevel_CaseControl(t *testing.T) {
	got := InferEvidenceLevel(Paper{
		Title:    "Case-control study of disease risk",
		Abstract: "We evaluated matched case-control cohorts over time.",
	})
	if got != "case-control" {
		t.Fatalf("expected case-control evidence level, got %q", got)
	}
}
