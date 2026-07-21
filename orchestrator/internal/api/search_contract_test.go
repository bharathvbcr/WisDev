package api

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

func TestNormalizeSearchProviderHint_ContractParity(t *testing.T) {
	contract := loadSearchProviderContract(t)

	for _, provider := range contract.CanonicalProviders {
		normalized, ok := normalizeSearchProviderHint(provider)
		assert.True(t, ok, provider)
		assert.Equal(t, provider, normalized)
	}

	for alias, expected := range contract.Aliases {
		normalized, ok := normalizeSearchProviderHint(alias)
		assert.True(t, ok, alias)
		assert.Equal(t, expected, normalized)
	}

	_, ok := normalizeSearchProviderHint("not_a_provider")
	assert.False(t, ok)
}
