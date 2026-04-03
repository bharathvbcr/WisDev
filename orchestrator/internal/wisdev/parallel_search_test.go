package wisdev

import (
	"context"
	internalsearch "github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestParallelSearchDelegatesToUnifiedSearch(t *testing.T) {
	originalBuildRegistry := buildUnifiedSearchRegistry
	originalRunSearch := runUnifiedParallelSearch
	t.Cleanup(func() {
		buildUnifiedSearchRegistry = originalBuildRegistry
		runUnifiedParallelSearch = originalRunSearch
	})

	buildUnifiedSearchRegistry = func(_ ...string) *internalsearch.ProviderRegistry {
		return internalsearch.NewProviderRegistry()
	}
	runUnifiedParallelSearch = func(_ context.Context, _ *internalsearch.ProviderRegistry, query string, opts internalsearch.SearchOpts) internalsearch.SearchResult {
		if query != "expanded query" {
			t.Fatalf("expected expanded query, got %q", query)
		}
		if opts.Limit != 7 {
			t.Fatalf("expected limit 7, got %d", opts.Limit)
		}
		return internalsearch.SearchResult{
			Papers: []internalsearch.Paper{
				{
					ID:            "p1",
					Title:         "Reward Modeling for RLHF",
					Abstract:      "A synthetic abstract",
					Link:          "https://example.com/p1",
					DOI:           "10.1000/p1",
					Source:        "semantic_scholar",
					CitationCount: 12,
					Score:         0.91,
				},
				{
					ID:            "p2",
					Title:         "Preference Optimization Methods",
					Abstract:      "Another synthetic abstract",
					Link:          "https://example.com/p2",
					DOI:           "10.1000/p2",
					Source:        "openalex",
					CitationCount: 4,
					Score:         0.73,
				},
			},
			LatencyMs: 42,
			Cached:    true,
		}
	}

	originalExpandQueryAnalysis := expandQueryAnalysis
	expandQueryAnalysis = func(_ context.Context, query string) (EnhancedQuery, error) {
		return EnhancedQuery{
			Original: query,
			Expanded: "expanded query",
			Intent:   "academic",
			Keywords: []string{"rlhf", "reward modeling"},
		}, nil
	}
	t.Cleanup(func() {
		expandQueryAnalysis = originalExpandQueryAnalysis
	})

	result, err := ParallelSearch(context.Background(), nil, "RLHF", SearchOptions{
		Limit:       7,
		ExpandQuery: true,
		QualitySort: true,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if len(result.Papers) != 2 {
		t.Fatalf("expected 2 papers, got %d", len(result.Papers))
	}
	if result.EnhancedQuery.Expanded != "expanded query" {
		t.Fatalf("expected expanded query metadata to be preserved, got %q", result.EnhancedQuery.Expanded)
	}
	if !result.Cached {
		t.Fatalf("expected cached result to be propagated")
	}
	if result.Sources.SemanticScholar != 1 || result.Sources.OpenAlex != 1 {
		t.Fatalf("expected per-source counts to be tracked, got %+v", result.Sources)
	}
}

func TestFastParallelSearchReturnsAdaptedPapers(t *testing.T) {
	originalParallelSearch := ParallelSearch
	t.Cleanup(func() {
		ParallelSearch = originalParallelSearch
	})

	ParallelSearch = func(_ context.Context, _ redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
		if query != "test query" {
			t.Fatalf("expected test query, got %q", query)
		}
		if opts.ExpandQuery {
			t.Fatalf("expected fast search to skip expansion")
		}
		return &MultiSourceResult{
			Papers: []Source{
				{ID: "p1", Title: "Fast search paper"},
			},
		}, nil
	}

	papers, err := FastParallelSearch(context.Background(), nil, "test query", 5)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(papers) != 1 || papers[0].Title != "Fast search paper" {
		t.Fatalf("unexpected papers: %+v", papers)
	}
}

func TestCallPRMIsDeterministic(t *testing.T) {
	reward, err := callPRM(context.Background(), "session-1", map[string]any{
		"paperCount":            4,
		"searchSuccess":         0.75,
		"citationVerifiedRatio": 0.5,
		"coverageScore":         0.4,
		"success":               true,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if reward <= 0.0 || reward > 1.0 {
		t.Fatalf("unexpected reward: %v", reward)
	}

	reward2, err := callPRM(context.Background(), "session-1", map[string]any{
		"paperCount":            4,
		"searchSuccess":         0.75,
		"citationVerifiedRatio": 0.5,
		"coverageScore":         0.4,
		"success":               true,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if reward != reward2 {
		t.Fatalf("expected deterministic reward, got %v and %v", reward, reward2)
	}
}

func TestBuildDeterministicFollowUpQueries(t *testing.T) {
	queries := buildDeterministicFollowUpQueries(
		context.Background(),
		[]string{"sparse attention"},
		[]Source{
			{Title: "Sparse Attention for Long Contexts", Summary: "We study retrieval augmented transformer methods."},
			{Title: "Transformer Efficiency in Biomedical NLP", Summary: "Efficiency and long context modeling."},
		},
	)

	if len(queries) == 0 {
		t.Fatal("expected follow-up queries")
	}
	if queries[0] != "sparse attention" {
		t.Fatalf("expected original query first, got %q", queries[0])
	}
	for _, q := range queries {
		if q == "" {
			t.Fatal("expected non-empty query")
		}
	}
}
