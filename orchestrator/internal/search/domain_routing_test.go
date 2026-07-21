package search

import "testing"

func TestInferDomainFromQuery(t *testing.T) {
	tests := []struct {
		query  string
		domain string
	}{
		{"meniscus scaffold ACL reconstruction", "medicine"},
		{"Cancer drug resistance", "medicine"},
		{"distributed neural network security", "cs"},
		{"transformer attention benchmark", "cs"},
		{"quantum gravity black hole", "physics"},
		{"market policy finance", "social"},
		{"random topic", "general"},
	}
	for _, tt := range tests {
		if got := InferDomainFromQuery(tt.query); got != tt.domain {
			t.Fatalf("InferDomainFromQuery(%q) = %q, want %q", tt.query, got, tt.domain)
		}
	}
}

func TestNormalizeRoutingDomain(t *testing.T) {
	if got := NormalizeRoutingDomain("Computer Science"); got != "cs" {
		t.Fatalf("expected cs, got %q", got)
	}
	if got := NormalizeRoutingDomain("medicine"); got != "medicine" {
		t.Fatalf("expected medicine, got %q", got)
	}
}

func TestDomainsMatchForRouting(t *testing.T) {
	if !DomainsMatchForRouting("medicine", "biomedical") {
		t.Fatal("expected medicine and biomedical to match for routing")
	}
	if DomainsMatchForRouting("cs", "physics") {
		t.Fatal("expected cs and physics to remain distinct")
	}
}

func TestProviderPresetForDomain(t *testing.T) {
	if preset, ok := ProviderPresetForDomain("medicine"); !ok || preset != "biomedical" {
		t.Fatalf("expected biomedical preset, got %q ok=%v", preset, ok)
	}
	if preset, ok := ProviderPresetForDomain("computer science"); !ok || preset != "cs" {
		t.Fatalf("expected cs preset, got %q ok=%v", preset, ok)
	}
	if _, ok := ProviderPresetForDomain("general"); ok {
		t.Fatal("expected no preset for general domain")
	}
}
