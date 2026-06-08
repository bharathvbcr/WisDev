package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type providerFixture struct {
	Name                  string `json:"name"`
	Query                 string `json:"query"`
	DomainHint            string `json:"domainHint"`
	ExpectedFirstProvider string `json:"expectedFirstProvider"`
}

func TestResolveProviderOrderFixtures(t *testing.T) {
	path := filepath.Join("testdata", "provider_order_fixtures.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read fixture file: %v", err)
	}
	var fixtures []providerFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("failed to parse fixture file: %v", err)
	}

	cfg := DefaultPolicyConfig()
	for _, fixture := range fixtures {
		got := ResolveProviderOrder(cfg, fixture.Query, fixture.DomainHint)
		if len(got) == 0 {
			t.Fatalf("%s: expected non-empty provider order", fixture.Name)
		}
		if got[0] != fixture.ExpectedFirstProvider {
			t.Fatalf("%s: expected first provider %q, got %q", fixture.Name, fixture.ExpectedFirstProvider, got[0])
		}
	}
}
