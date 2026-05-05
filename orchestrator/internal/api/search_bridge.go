package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

// buildBridgeRegistry and runBridgeParallelSearch are package-level vars so
// tests can exercise the bridge helpers without mutating canonical WisDev
// search ownership.
var buildBridgeRegistry = search.BuildRegistry
var runBridgeParallelSearch = search.ParallelSearch
var runCanonicalWisdevParallelSearch func(context.Context, redis.UniversalClient, string, wisdev.SearchOptions) (*wisdev.MultiSourceResult, error)

// WireLegacySearch remains as a compatibility shim for older callers. The
// canonical wisdev search functions already route through the unified provider
// pipeline, so this function intentionally avoids rewriting package-level
// callbacks and introducing cross-test global state.
func WireLegacySearch(_ redis.UniversalClient) {
}

func runModularParallelSearch(ctx context.Context, rdb redis.UniversalClient, registry *search.ProviderRegistry, query string, opts wisdev.SearchOptions) (resultPayload *wisdev.MultiSourceResult, panicErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr = fmt.Errorf("modular parallel search panic: %v", recovered)
			resultPayload = nil
		}
	}()

	normalizedQuery := strings.TrimSpace(query)
	if normalizedQuery == "" {
		slog.Warn("search_bridge: runModularParallelSearch called with empty query",
			"stage", "bridge_rejected_empty_query",
			"queryLen", len(query),
		)
		return nil, fmt.Errorf("search query is required")
	}

	slog.Debug("search_bridge: runModularParallelSearch entry",
		"stage", "bridge_parallel_entry",
		"queryPreview", truncateForLog(normalizedQuery, 120),
		"queryLen", len(normalizedQuery),
		"hasRegistry", registry != nil,
	)

	effectiveRegistry := opts.Registry
	if effectiveRegistry == nil {
		effectiveRegistry = registry
	}
	if effectiveRegistry != nil {
		effectiveRegistry.SetRedis(rdb)
	}
	opts.Registry = effectiveRegistry

	runner := runCanonicalWisdevParallelSearch
	if runner == nil {
		runner = wisdev.ParallelSearch
	}
	return runner(ctx, rdb, query, opts)
}

func runModularFastSearch(ctx context.Context, rdb redis.UniversalClient, query string, limit int) (papers []wisdev.Source, panicErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr = fmt.Errorf("modular fast search panic: %v", recovered)
			papers = nil
		}
	}()

	normalizedQuery := strings.TrimSpace(query)
	if normalizedQuery == "" {
		slog.Warn("search_bridge: runModularFastSearch called with empty query",
			"stage", "bridge_fast_rejected_empty_query",
			"queryLen", len(query),
		)
		return nil, fmt.Errorf("search query is required")
	}

	slog.Debug("search_bridge: runModularFastSearch entry",
		"stage", "bridge_fast_entry",
		"queryPreview", truncateForLog(normalizedQuery, 120),
		"limit", limit,
	)

	searchOpts := search.SearchOpts{
		Limit:       limit,
		QualitySort: true,
	}
	registry := buildBridgeRegistry(
		"semantic_scholar",
		"openalex",
		"pubmed",
		"core",
		"arxiv",
		"europe_pmc",
		"crossref",
		"dblp",
	)
	registry.SetRedis(rdb)
	result := runBridgeParallelSearch(ctx, registry, query, searchOpts)
	papers = mapPapers(result.Papers)
	return papers, nil
}

func mapPapers(papers []search.Paper) []wisdev.Source {
	out := make([]wisdev.Source, 0, len(papers))
	for _, p := range papers {
		out = append(out, wisdev.Source{
			ID:                       p.ID,
			Title:                    p.Title,
			Summary:                  p.Abstract,
			Abstract:                 p.Abstract,
			Link:                     p.Link,
			DOI:                      p.DOI,
			Source:                   p.Source,
			SourceApis:               append([]string(nil), p.SourceApis...),
			SiteName:                 p.Source,
			Publication:              p.Venue,
			Keywords:                 append([]string(nil), p.Keywords...),
			Authors:                  append([]string(nil), p.Authors...),
			Year:                     p.Year,
			Month:                    p.Month,
			CitationCount:            p.CitationCount,
			ReferenceCount:           p.ReferenceCount,
			InfluentialCitationCount: p.InfluentialCitationCount,
			OpenAccessUrl:            p.OpenAccessUrl,
			PdfUrl:                   p.PdfUrl,
			Score:                    p.Score,
		})
	}
	return out
}

func mapStats(stats map[string]int) wisdev.SourcesStats {
	return wisdev.SourcesStats{
		SemanticScholar: stats["semantic_scholar"],
		OpenAlex:        stats["openalex"],
		PubMed:          stats["pubmed"],
		CORE:            stats["core"],
		ArXiv:           stats["arxiv"],
		BioRxiv:         stats["biorxiv"] + stats["medrxiv"],
		EuropePMC:       stats["europe_pmc"],
		CrossRef:        stats["crossref"],
		DBLP:            stats["dblp"],
		IEEE:            stats["ieee"],
		NASAADS:         stats["nasa_ads"],
	}
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
