package search

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type errorDB struct{}

func (e *errorDB) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (e *errorDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, errors.New("query failed")
}

func (e *errorDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return noopRow{}
}

type noopRow struct{}

func (n noopRow) Scan(dest ...any) error {
	return errors.New("scan unavailable")
}

func TestApplyStrategyAdaptive_ClickBoostChangesOrder(t *testing.T) {
	ab := NewABTestManager(1.0)
	intelligence := NewSearchIntelligence(nil)

	papers := []Paper{
		{ID: "p1", Title: "First", Score: 1.00, SourceApis: []string{"mock"}},
		{ID: "p2", Title: "Second", Score: 0.90, SourceApis: []string{"mock"}},
	}

	boosted := ab.ApplyStrategy(context.Background(), StrategyAdaptive, papers, intelligence, map[string]int{
		"p2": 100,
	})

	if len(boosted) != 2 {
		t.Fatalf("expected 2 papers, got %d", len(boosted))
	}
	if boosted[0].ID != "p2" {
		t.Fatalf("expected p2 to be promoted to rank 1, got %s", boosted[0].ID)
	}
}

func TestParallelSearch_ContinuesWhenClickFetchFails(t *testing.T) {
	reg := NewProviderRegistry()
	reg.Register(&MockProvider{
		name: "mock1",
		papers: []Paper{
			{ID: "1", Title: "Paper 1", DOI: "10.1000/1", Source: "mock1"},
			{ID: "2", Title: "Paper 2", DOI: "10.1000/2", Source: "mock1"},
		},
	})

	reg.SetDB(&errorDB{})
	reg.abTest = NewABTestManager(1.0)
	ApplyDomainRoutes(reg)

	result := ParallelSearch(context.Background(), reg, "test query", SearchOpts{
		UserID: "force-adaptive",
		Domain: "general",
		Limit:  10,
	})

	if len(result.Papers) != 2 {
		t.Fatalf("expected 2 papers despite click fetch failure, got %d", len(result.Papers))
	}
	if result.Providers["mock1"] != 2 {
		t.Fatalf("expected provider count to be recorded, got %v", result.Providers)
	}
}

func TestNewABTestManager_Defaults(t *testing.T) {
	ab := NewABTestManager(0.42)
	if ab == nil {
		t.Fatalf("expected manager to be initialized")
	}
	if ab.canaryPercentage != 0.42 {
		t.Fatalf("expected canary to be 0.42, got %v", ab.canaryPercentage)
	}
}

func TestGetStrategy_DeterministicForUserID(t *testing.T) {
	ab := NewABTestManager(0.5)

	strategy1 := ab.GetStrategy(context.Background(), "stable-user-id")
	strategy2 := ab.GetStrategy(context.Background(), "stable-user-id")
	strategy3 := ab.GetStrategy(context.Background(), "stable-user-id")

	if strategy1 != strategy2 || strategy2 != strategy3 {
		t.Fatalf("expected deterministic strategy for same user ID: %s, %s, %s", strategy1, strategy2, strategy3)
	}

	if strategy1 != StrategyBaseline && strategy1 != StrategyAdaptive {
		t.Fatalf("expected valid strategy, got %s", strategy1)
	}
}

func TestGetStrategy_DeterministicForUserID_CanaryExtremes(t *testing.T) {
	userID := "stable-user-id"

	abOff := NewABTestManager(0.0)
	if got := abOff.GetStrategy(context.Background(), userID); got != StrategyBaseline {
		t.Fatalf("expected baseline for canary 0.0, got %s", got)
	}

	abOn := NewABTestManager(1.0)
	if got := abOn.GetStrategy(context.Background(), userID); got != StrategyAdaptive {
		t.Fatalf("expected adaptive for canary 1.0, got %s", got)
	}
}

func TestGetStrategy_RespectsCanaryBoundsForEmptyUser(t *testing.T) {
	abOff := NewABTestManager(0.0)
	for i := 0; i < 200; i++ {
		if got := abOff.GetStrategy(context.Background(), ""); got != StrategyBaseline {
			t.Fatalf("expected baseline for canary 0.0, got %s at iteration %d", got, i)
		}
	}

	abOn := NewABTestManager(1.0)
	for i := 0; i < 50; i++ {
		if got := abOn.GetStrategy(context.Background(), ""); got != StrategyAdaptive {
			t.Fatalf("expected adaptive for canary 1.0, got %s at iteration %d", got, i)
		}
	}
}

func TestGetStrategy_RandomPathForEmptyUser(t *testing.T) {
	ab := NewABTestManager(1.0)
	if got := ab.GetStrategy(context.Background(), ""); got != StrategyAdaptive {
		t.Fatalf("expected deterministic random-path strategy %s, got %s", StrategyAdaptive, got)
	}
}

func TestApplyStrategy_DefaultBranchAndUnknownStrategy(t *testing.T) {
	ab := NewABTestManager(0.0)
	papers := []Paper{
		{ID: "p1"},
		{ID: "p2"},
	}
	intelligence := NewSearchIntelligence(nil)

	defaultOrder := ab.ApplyStrategy(context.Background(), "", papers, intelligence, map[string]int{})
	if len(defaultOrder) != 2 {
		t.Fatalf("expected passthrough when strategy defaults to RRF, got len=%d", len(defaultOrder))
	}

	baselineOrder := ab.ApplyStrategy(context.Background(), StrategyBaseline, papers, intelligence, map[string]int{})
	if len(baselineOrder) != 2 {
		t.Fatalf("expected passthrough for StrategyBaseline, got len=%d", len(baselineOrder))
	}
}
