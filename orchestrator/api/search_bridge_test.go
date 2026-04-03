package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestRunModularFastSearchUsesBridgeRegistry(t *testing.T) {
	originalBuildBridgeRegistry := buildBridgeRegistry
	originalRunBridgeParallelSearch := runBridgeParallelSearch
	t.Cleanup(func() {
		buildBridgeRegistry = originalBuildBridgeRegistry
		runBridgeParallelSearch = originalRunBridgeParallelSearch
	})

	buildBridgeRegistry = func(requestedProviders ...string) *search.ProviderRegistry {
		expected := []string{"semantic_scholar", "openalex", "pubmed", "core", "arxiv", "europe_pmc", "crossref", "dblp"}
		if len(requestedProviders) != len(expected) {
			t.Fatalf("unexpected provider count: %+v", requestedProviders)
		}
		for index, provider := range expected {
			if requestedProviders[index] != provider {
				t.Fatalf("unexpected providers: %+v", requestedProviders)
			}
		}
		return search.NewProviderRegistry()
	}

	runBridgeParallelSearch = func(_ context.Context, _ *search.ProviderRegistry, query string, opts search.SearchOpts) search.SearchResult {
		if query != "rlhf" {
			t.Fatalf("expected query rlhf, got %q", query)
		}
		if opts.Limit != 8 {
			t.Fatalf("expected limit 8, got %d", opts.Limit)
		}
		if !opts.QualitySort {
			t.Fatalf("expected quality sort to be enabled")
		}
		return search.SearchResult{
			Papers: []search.Paper{
				{
					ID:            "p1",
					Title:         "Reward Modeling for RLHF",
					Abstract:      "Abstract",
					Link:          "https://example.com/p1",
					DOI:           "10.1000/p1",
					Source:        "semantic_scholar",
					SourceApis:    []string{"semantic_scholar"},
					Authors:       []string{"Ada Lovelace"},
					Year:          2024,
					Venue:         "NeurIPS",
					Keywords:      []string{"rlhf", "reward modeling"},
					CitationCount: 11,
					Score:         0.88,
				},
			},
			Providers: map[string]int{
				"semantic_scholar": 1,
			},
			LatencyMs: 17,
			Cached:    true,
		}
	}

	papers, err := runModularFastSearch(context.Background(), nil, "rlhf", 8)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(papers) != 1 {
		t.Fatalf("expected 1 paper, got %d", len(papers))
	}
	if papers[0].Title != "Reward Modeling for RLHF" {
		t.Fatalf("unexpected paper title: %q", papers[0].Title)
	}
	if len(papers[0].SourceApis) != 1 || papers[0].SourceApis[0] != "semantic_scholar" {
		t.Fatalf("expected sourceApis metadata, got %+v", papers[0].SourceApis)
	}
	if papers[0].Publication != "NeurIPS" {
		t.Fatalf("expected publication metadata, got %q", papers[0].Publication)
	}
}

func TestRunModularFastSearchRecoversFromPanics(t *testing.T) {
	originalBuildBridgeRegistry := buildBridgeRegistry
	originalRunBridgeParallelSearch := runBridgeParallelSearch
	t.Cleanup(func() {
		buildBridgeRegistry = originalBuildBridgeRegistry
		runBridgeParallelSearch = originalRunBridgeParallelSearch
	})

	buildBridgeRegistry = func(_ ...string) *search.ProviderRegistry {
		return search.NewProviderRegistry()
	}
	runBridgeParallelSearch = func(_ context.Context, _ *search.ProviderRegistry, _ string, _ search.SearchOpts) search.SearchResult {
		panic("boom")
	}

	papers, err := runModularFastSearch(context.Background(), nil, "rlhf", 8)
	if err == nil {
		t.Fatalf("expected panic to be converted into an error")
	}
	if len(papers) != 0 {
		t.Fatalf("expected no papers on recovered panic, got %d", len(papers))
	}
	if !strings.Contains(err.Error(), "modular fast search panic") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAutonomousResearchRouteReturnsEnvelope(t *testing.T) {
	t.Skip("requires full autonomous loop with search providers and LLM client")
	originalFastParallelSearch := wisdev.FastParallelSearch
	t.Cleanup(func() {
		wisdev.FastParallelSearch = originalFastParallelSearch
	})

	wisdev.FastParallelSearch = func(_ context.Context, _ redis.UniversalClient, query string, limit int) ([]wisdev.Source, error) {
		if query != "RLHF reinforcement learning" {
			t.Fatalf("expected corrected query, got %q", query)
		}
		if limit != 8 {
			t.Fatalf("expected limit 8, got %d", limit)
		}
		return []wisdev.Source{
			{
				ID:            "p1",
				Title:         "Reward Modeling for RLHF",
				Summary:       "A useful paper",
				Link:          "https://example.com/p1",
				DOI:           "10.1000/p1",
				Source:        "semantic_scholar",
				CitationCount: 10,
				Score:         0.86,
			},
		}, nil
	}

	mux := http.NewServeMux()
	RegisterV2WisDevRoutes(mux, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"RLHF reinforcement learning"},
		"plan":{"query":"ignored"}
	}`))
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body["traceId"] == "" {
		t.Fatalf("expected traceId in envelope")
	}
	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	prismaReport, ok := payload["prismaReport"].(map[string]any)
	if !ok {
		t.Fatalf("expected prismaReport payload")
	}
	if prismaReport["included"] != float64(1) {
		t.Fatalf("expected included count 1, got %v", prismaReport["included"])
	}
}

func TestAutonomousResearchRouteDegradesWhenSearchFails(t *testing.T) {
	t.Skip("requires full autonomous loop with search providers")
	originalFastParallelSearch := wisdev.FastParallelSearch
	t.Cleanup(func() {
		wisdev.FastParallelSearch = originalFastParallelSearch
	})

	wisdev.FastParallelSearch = func(_ context.Context, _ redis.UniversalClient, _ string, _ int) ([]wisdev.Source, error) {
		return nil, errors.New("search backend unavailable")
	}

	mux := http.NewServeMux()
	RegisterV2WisDevRoutes(mux, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v2/wisdev/research/autonomous", strings.NewReader(`{
		"session":{"sessionId":"s1","correctedQuery":"RLHF reinforcement learning"}
	}`))
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected degraded 200 response, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	payload, ok := body["autonomousResearch"].(map[string]any)
	if !ok {
		t.Fatalf("expected autonomousResearch payload in envelope")
	}
	warnings, ok := payload["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("expected one degraded warning, got %#v", payload["warnings"])
	}
	prismaReport, ok := payload["prismaReport"].(map[string]any)
	if !ok {
		t.Fatalf("expected prismaReport payload")
	}
	if prismaReport["included"] != float64(0) {
		t.Fatalf("expected included count 0 on degraded path, got %v", prismaReport["included"])
	}
}
