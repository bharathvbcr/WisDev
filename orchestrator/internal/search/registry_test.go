package search

import (
	"testing"
)

func TestBuildRegistry(t *testing.T) {
	// Test full registry
	reg := BuildRegistry()
	providers := reg.ProvidersFor("general")
	if len(providers) == 0 {
		t.Errorf("Expected providers for general domain, got 0")
	}

	// Test filtered registry
	filteredReg := BuildRegistry("arxiv", "pubmed")

	// Should contain arxiv
	foundArxiv := false
	for _, p := range filteredReg.providers {
		if p.Name() == "arxiv" {
			foundArxiv = true
			break
		}
	}
	if !foundArxiv {
		t.Errorf("Expected arxiv in filtered registry")
	}

	// Should not contain openalex
	foundOpenAlex := false
	for _, p := range filteredReg.providers {
		if p.Name() == "openalex" {
			foundOpenAlex = true
			break
		}
	}
	if foundOpenAlex {
		t.Errorf("Did not expect openalex in filtered registry")
	}
}

func TestDomainRoutes(t *testing.T) {
	reg := BuildRegistry()

	medicineProviders := reg.ProvidersFor("medicine")
	if len(medicineProviders) == 0 {
		t.Errorf("Expected providers for medicine domain")
	}

	foundPubMed := false
	for _, p := range medicineProviders {
		if p.Name() == "pubmed" {
			foundPubMed = true
			break
		}
	}
	if !foundPubMed {
		t.Errorf("Expected pubmed to be routed for medicine domain")
	}

	csProviders := reg.ProvidersFor("cs")
	foundDBLP := false
	for _, p := range csProviders {
		if p.Name() == "dblp" {
			foundDBLP = true
			break
		}
	}
	if !foundDBLP {
		t.Errorf("Expected dblp to be routed for cs domain")
	}
}
