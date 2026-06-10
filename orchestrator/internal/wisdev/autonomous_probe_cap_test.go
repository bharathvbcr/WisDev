package wisdev

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

// hypothesisProbeQuery reports whether a query looks like one of the
// pre-retrieval hypothesis probes built by researchBranchPlansFromHypotheses.
// Only the evidence and contradiction prefixes are used because they are
// unique to the pre-retrieval seeding path ("falsification check" is shared
// with belief-feedback queries enqueued mid-loop).
func hypothesisProbeQuery(query string) bool {
	lower := strings.ToLower(query)
	return strings.Contains(lower, "hypothesis evidence") ||
		strings.Contains(lower, "contradiction and bias check")
}

// TestRunCapsHypothesisProbesUnderSmallBudget proves that with a small
// MaxSearchTerms the pre-retrieval hypothesis probes are capped at
// maxInt(1, searchTermBudget/2) so they cannot crowd out the user's
// agenda/topic queries. Modeled on TestRunYOLOExposesResearchAgentAgenda in
// pkg/wisdev/agent_test.go, which exercises YOLO mode with a stub provider.
func TestRunCapsHypothesisProbesUnderSmallBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var mu sync.Mutex
	var executed []string
	reg := search.NewProviderRegistry()
	provider := &mockSearchProvider{
		name: "probe_cap_mock",
		SearchFunc: func(_ context.Context, query string, _ search.SearchOpts) ([]search.Paper, error) {
			mu.Lock()
			executed = append(executed, query)
			mu.Unlock()
			return []search.Paper{{
				ID:     "probe-cap-" + query,
				Title:  "Stub evidence for " + query,
				Source: "probe_cap_mock",
				Year:   2026,
			}}, nil
		},
	}
	reg.Register(provider)
	reg.SetDefaultOrder([]string{"probe_cap_mock"})

	loop := NewAutonomousLoop(reg, nil)
	maxSearchTerms := 4
	res, err := loop.Run(ctx, LoopRequest{
		Query:           "map evidence for open source research agents",
		Mode:            string(WisDevModeYOLO),
		Domain:          "cs",
		MaxIterations:   4,
		MaxSearchTerms:  maxSearchTerms,
		HitsPerSearch:   1,
		MaxUniquePapers: 8,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res == nil {
		t.Fatal("expected result")
	}
	if res.GapAnalysis == nil {
		t.Fatal("expected gap analysis coverage state")
	}

	// The full planned-query set is the union of executed queries and planned
	// queries that never executed.
	planned := append(append([]string(nil), res.ExecutedQueries...), res.GapAnalysis.Coverage.UnexecutedPlannedQueries...)
	probeCap := maxInt(1, maxSearchTerms/2)
	plannedProbes := 0
	for _, query := range planned {
		if hypothesisProbeQuery(query) {
			plannedProbes++
		}
	}
	if plannedProbes > probeCap {
		t.Fatalf("expected at most %d hypothesis probes to be enqueued, got %d (planned=%#v)", probeCap, plannedProbes, planned)
	}

	// The capped probes must leave budget for non-probe agenda/topic queries.
	mu.Lock()
	defer mu.Unlock()
	if len(executed) == 0 {
		t.Fatal("expected the loop to execute queries")
	}
	agendaExecuted := false
	for _, query := range executed {
		if !hypothesisProbeQuery(query) {
			agendaExecuted = true
			break
		}
	}
	if !agendaExecuted {
		t.Fatalf("expected at least one agenda/topic query to execute alongside capped hypothesis probes, got %#v", executed)
	}
}
