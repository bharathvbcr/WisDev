package wisdev

import "testing"

// qualitySort gives relevance 0.50 of ScoreQuality and citation/venue/author
// impact 0.35, so on a narrow technical query it surfaces famous papers from
// adjacent fields. On an agent surface, where the caller cannot see why the
// results are off, that default cost more than it bought.
func TestQualitySortDefaultsOffOnAgentSurface(t *testing.T) {
	for _, spec := range knobRegistry() {
		if spec.Key != CfgSearchQualitySort {
			continue
		}
		on, ok := spec.Default.(bool)
		if !ok {
			t.Fatalf("%s default is %T, want bool", spec.Key, spec.Default)
		}
		if on {
			t.Fatal("search.qualitySort must default off on the MCP surface")
		}
		if spec.Description == "" || len(spec.Description) < 60 {
			t.Fatal("the default must carry the reason it was chosen, not just the value")
		}
		return
	}
	t.Fatalf("%s not found in config specs", CfgSearchQualitySort)
}
