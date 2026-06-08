package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

type searchProviderContract struct {
	CanonicalProviders []string          `json:"canonicalProviders"`
	Aliases            map[string]string `json:"aliases"`
}

func loadSearchProviderContract(t *testing.T) searchProviderContract {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for {
		path := filepath.Join(dir, "schema", "search_provider_contract.json")
		data, readErr := os.ReadFile(path)
		if readErr == nil {
			var contract searchProviderContract
			if err := json.Unmarshal(data, &contract); err != nil {
				t.Fatalf("unmarshal contract: %v", err)
			}
			return contract
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate search_provider_contract.json from %s", dir)
		}
		dir = parent
	}
}

func TestProviderContractParity(t *testing.T) {
	contract := loadSearchProviderContract(t)

	for _, provider := range contract.CanonicalProviders {
		assert.True(t, IsCanonicalProviderName(provider), provider)
		assert.Equal(t, provider, NormalizeProviderName(provider))
	}

	for alias, expected := range contract.Aliases {
		assert.Equal(t, expected, NormalizeProviderName(alias))
	}

	for domain, providers := range DomainRoutes {
		for _, provider := range providers {
			assert.True(t, IsCanonicalProviderName(provider), "domain %s uses unknown provider %s", domain, provider)
		}
	}

	for _, provider := range DefaultProviderOrder {
		assert.True(t, IsCanonicalProviderName(provider), provider)
	}
}
