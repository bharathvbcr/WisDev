package wisdev

import (
	"context"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func TestTreeSearchRuntimeKeepsBranchesIsolatedUntilMerge(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{
		name: "tree-search-provider",
		SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			return []search.Paper{{
				ID:       stableWisDevID("paper", query),
				Title:    "Evidence for " + query,
				Abstract: "Branch-local evidence for " + query,
				Year:     2025,
			}}, nil
		},
	})
	loop := NewAutonomousLoop(reg, nil)
	runtime := NewTreeSearchRuntime(loop, loop.hypothesisExplorer, TreeSearchConfig{
		MaxBranches:      2,
		PruneBelow:       0.1,
		MaxDepth:         1,
		MaxUniquePapers:  10,
		QueriesPerBranch: 1,
	})
	coverage := map[string][]search.Paper{}
	result := runtime.Run(context.Background(), LoopRequest{Query: "root query"}, []Hypothesis{
		{ID: "h1", Query: "root query", Claim: "claim alpha", ConfidenceScore: 0.6, FalsifiabilityCondition: "alpha falsifier"},
		{ID: "h2", Query: "root query", Claim: "claim beta", ConfidenceScore: 0.5, FalsifiabilityCondition: "beta falsifier"},
	}, search.SearchOpts{Limit: 1}, coverage)

	if len(result.Branches) == 0 {
		t.Fatal("expected isolated tree-search branches")
	}
	if result.MergeCandidate == nil {
		t.Fatal("expected isolated tree-search merge candidate")
	}
	for _, branch := range result.Branches {
		if len(branch.ExecutedQueries) == 0 {
			t.Fatalf("expected branch-local executed queries: %+v", branch)
		}
		if len(branch.Papers) == 0 || len(branch.Evidence) == 0 {
			t.Fatalf("expected branch-local paper and evidence pools: %+v", branch)
		}
	}
	for key := range coverage {
		if !strings.HasPrefix(key, "tree:") && !strings.HasPrefix(key, "branch:") {
			t.Fatalf("expected tree-search coverage to stay branch scoped, got key %q", key)
		}
	}
}
