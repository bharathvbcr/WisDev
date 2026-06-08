package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

// buildBridgeRegistry and runBridgeParallelSearch are package-level vars so
// tests can exercise the bridge helpers without mutating canonical WisDev
// search ownership.
var buildBridgeRegistry = search.BuildRegistry
var runBridgeParallelSearch = search.ParallelSearch
var runCanonicalWisdevParallelSearch func(context.Context, redis.UniversalClient, string, wisdev.SearchOptions) (*wisdev.MultiSourceResult, error)

func buildBridgeSearchLogArgs(operation string, stage string, query string, attrs ...any) []any {
	normalizedStage := strings.TrimSpace(stage)
	if normalizedStage == "" {
		normalizedStage = "unspecified"
	}
	normalizedQuery := wisdev.ResolveSessionQueryText(query, "")
	queryHash := ""
	if normalizedQuery != "" {
		queryHash = searchQueryFingerprint(normalizedQuery)
	}
	base := []any{
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "api.search_bridge",
		"operation", strings.TrimSpace(operation),
		"stage", normalizedStage,
		"query_preview", truncateForLog(normalizedQuery, 120),
		"query_length", len(normalizedQuery),
		"query_hash", queryHash,
	}
	return append(base, attrs...)
}

func logBridgeSearchStage(ctx context.Context, operation string, stage string, query string, attrs ...any) {
	if ctx == nil {
		ctx = context.Background()
	}
	telemetry.FromCtx(ctx).InfoContext(ctx, "search bridge lifecycle", buildBridgeSearchLogArgs(operation, stage, query, attrs...)...)
}

func logBridgeSearchFailure(ctx context.Context, operation string, stage string, query string, attrs ...any) {
	if ctx == nil {
		ctx = context.Background()
	}
	telemetry.FromCtx(ctx).WarnContext(ctx, "search bridge lifecycle", buildBridgeSearchLogArgs(operation, stage, query, attrs...)...)
}

func runModularParallelSearch(ctx context.Context, rdb redis.UniversalClient, registry *search.ProviderRegistry, query string, opts wisdev.SearchOptions) (resultPayload *wisdev.MultiSourceResult, panicErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr = fmt.Errorf("modular parallel search panic: %v", recovered)
			resultPayload = nil
			logBridgeSearchFailure(ctx, "bridge_parallel_search", "panic_recovered", query,
				"result", "failure",
				"error_code", "SEARCH_BRIDGE_PANIC",
				"error", panicErr.Error(),
			)
		}
	}()

	normalizedQuery := strings.TrimSpace(query)
	if normalizedQuery == "" {
		logBridgeSearchFailure(ctx, "bridge_parallel_search", "validation_failed", query,
			"result", "failure",
			"error_code", "QUERY_REQUIRED",
			"query_length_raw", len(query),
		)
		return nil, fmt.Errorf("search query is required")
	}

	logBridgeSearchStage(ctx, "bridge_parallel_search", "entry", normalizedQuery,
		"has_registry_arg", registry != nil,
		"has_opts_registry", opts.Registry != nil,
	)

	effectiveRegistry := opts.Registry
	if effectiveRegistry == nil {
		effectiveRegistry = registry
	}
	if effectiveRegistry != nil {
		effectiveRegistry.SetRedis(rdb)
	}
	opts.Registry = effectiveRegistry
	logBridgeSearchStage(ctx, "bridge_parallel_search", "registry_resolved", normalizedQuery,
		"has_effective_registry", effectiveRegistry != nil,
	)

	runner := runCanonicalWisdevParallelSearch
	if runner == nil {
		runner = wisdev.ParallelSearch
	}
	logBridgeSearchStage(ctx, "bridge_parallel_search", "dispatch_start", normalizedQuery)
	resultPayload, err := runner(ctx, rdb, query, opts)
	if err != nil {
		logBridgeSearchFailure(ctx, "bridge_parallel_search", "dispatch_failed", normalizedQuery,
			"result", "failure",
			"error_code", "SEARCH_BRIDGE_DISPATCH_FAILED",
			"error", err.Error(),
		)
		return resultPayload, err
	}
	resultCount := 0
	if resultPayload != nil {
		resultCount = len(resultPayload.Papers)
	}
	logBridgeSearchStage(ctx, "bridge_parallel_search", "response_ready", normalizedQuery,
		"result", "success",
		"result_count", resultCount,
	)
	return resultPayload, nil
}

func runModularFastSearch(ctx context.Context, rdb redis.UniversalClient, query string, limit int) (papers []wisdev.Source, panicErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr = fmt.Errorf("modular fast search panic: %v", recovered)
			papers = nil
			logBridgeSearchFailure(ctx, "bridge_fast_search", "panic_recovered", query,
				"result", "failure",
				"error_code", "SEARCH_BRIDGE_PANIC",
				"error", panicErr.Error(),
			)
		}
	}()

	normalizedQuery := strings.TrimSpace(query)
	if normalizedQuery == "" {
		logBridgeSearchFailure(ctx, "bridge_fast_search", "validation_failed", query,
			"result", "failure",
			"error_code", "QUERY_REQUIRED",
			"query_length_raw", len(query),
		)
		return nil, fmt.Errorf("search query is required")
	}

	logBridgeSearchStage(ctx, "bridge_fast_search", "entry", normalizedQuery,
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
	logBridgeSearchStage(ctx, "bridge_fast_search", "dispatch_start", normalizedQuery,
		"limit", limit,
	)
	result := runBridgeParallelSearch(ctx, registry, query, searchOpts)
	papers = mapPapers(result.Papers)
	logBridgeSearchStage(ctx, "bridge_fast_search", "response_ready", normalizedQuery,
		"result", "success",
		"result_count", len(papers),
		"warning_count", len(result.Warnings),
	)
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
