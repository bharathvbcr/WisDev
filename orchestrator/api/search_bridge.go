package api

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

// buildBridgeRegistry and runBridgeParallelSearch are package-level vars so
// tests can swap them without touching production search infrastructure.
var buildBridgeRegistry = search.BuildRegistry
var runBridgeParallelSearch = search.ParallelSearch

func WireLegacySearch(rdb redis.UniversalClient) {
	// Flip the legacy function variables to use the modular provider pipeline.
	wisdev.ParallelSearch = func(ctx context.Context, _ redis.UniversalClient, query string, opts wisdev.SearchOptions) (*wisdev.MultiSourceResult, error) {
		return runModularParallelSearch(ctx, rdb, query, opts)
	}
	wisdev.FastParallelSearch = func(ctx context.Context, _ redis.UniversalClient, query string, limit int) ([]wisdev.Source, error) {
		return runModularFastSearch(ctx, rdb, query, limit)
	}
}

func runModularParallelSearch(ctx context.Context, rdb redis.UniversalClient, query string, opts wisdev.SearchOptions) (resultPayload *wisdev.MultiSourceResult, panicErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr = fmt.Errorf("modular parallel search panic: %v", recovered)
			resultPayload = nil
		}
	}()
	searchOpts := search.SearchOpts{
		Limit:       opts.Limit,
		Domain:      opts.Domain,
		QualitySort: opts.QualitySort,
		SkipCache:   opts.SkipCache,
	}

	registry := search.BuildRegistry()
	registry.SetRedis(rdb)
	result := search.ParallelSearch(ctx, registry, query, searchOpts)

	resultPayload = &wisdev.MultiSourceResult{
		Papers:  mapPapers(result.Papers),
		Sources: mapStats(result.Providers),
		Timing: wisdev.TimingStats{
			Total: result.LatencyMs,
		},
		Cached: result.Cached,
	}
	return resultPayload, nil
}

func runModularFastSearch(ctx context.Context, rdb redis.UniversalClient, query string, limit int) (papers []wisdev.Source, panicErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr = fmt.Errorf("modular fast search panic: %v", recovered)
			papers = nil
		}
	}()
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
			ID:            p.ID,
			Title:         p.Title,
			Summary:       p.Abstract,
			Link:          p.Link,
			DOI:           p.DOI,
			Source:        p.Source,
			SourceApis:    append([]string(nil), p.SourceApis...),
			SiteName:      p.Source,
			Publication:   p.Venue,
			Keywords:      append([]string(nil), p.Keywords...),
			Authors:       append([]string(nil), p.Authors...),
			Year:          p.Year,
			CitationCount: p.CitationCount,
			Score:         p.Score,
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
