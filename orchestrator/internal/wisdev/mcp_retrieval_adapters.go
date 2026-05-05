package wisdev

import (
	"context"
	"fmt"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func retrieveCanonicalSearchPapers(ctx context.Context, registry *search.ProviderRegistry, query string, opts search.SearchOpts) ([]search.Paper, map[string]any, error) {
	sources, payload, err := RetrieveCanonicalPapersWithOptions(ctx, nil, registry, query, SearchOptions{
		Limit:       opts.Limit,
		Domain:      opts.Domain,
		Sources:     append([]string(nil), opts.Sources...),
		YearFrom:    opts.YearFrom,
		YearTo:      opts.YearTo,
		SkipCache:   opts.SkipCache,
		QualitySort: opts.QualitySort,
		Registry:    registry,
	})
	if err != nil {
		return nil, payload, err
	}
	return sourcesToSearchPapers(sources), payload, nil
}

func retrieveCanonicalSearchResult(ctx context.Context, registry *search.ProviderRegistry, query string, opts search.SearchOpts) (search.SearchResult, error) {
	papers, payload, err := retrieveCanonicalSearchPapers(ctx, registry, query, opts)
	result := search.SearchResult{
		Papers:   papers,
		Warnings: providerWarningsFromRetrievalPayload(payload),
	}
	if providers := providersFromRetrievalPayload(payload); len(providers) > 0 {
		result.Providers = providers
	}
	if latencyMs, ok := int64FromRetrievalPayload(payload["latencyMs"]); ok {
		result.LatencyMs = latencyMs
	}
	return result, err
}

func sourcesToSearchPapers(sources []Source) []search.Paper {
	papers := make([]search.Paper, 0, len(sources))
	for _, source := range sources {
		papers = append(papers, sourceToSearchPaper(source))
	}
	return papers
}

func sourceToSearchPaper(source Source) search.Paper {
	return search.Paper{
		ID:                       source.ID,
		Title:                    source.Title,
		Abstract:                 firstNonEmpty(source.Abstract, source.Summary),
		Link:                     firstNonEmpty(source.Link, source.URL),
		DOI:                      source.DOI,
		ArxivID:                  source.ArxivID,
		Source:                   firstNonEmpty(source.Source, source.SiteName),
		SourceApis:               append([]string(nil), source.SourceApis...),
		Authors:                  append([]string(nil), source.Authors...),
		Year:                     source.Year,
		Month:                    source.Month,
		Venue:                    source.Publication,
		Keywords:                 append([]string(nil), source.Keywords...),
		CitationCount:            source.CitationCount,
		ReferenceCount:           source.ReferenceCount,
		InfluentialCitationCount: source.InfluentialCitationCount,
		OpenAccessUrl:            source.OpenAccessUrl,
		PdfUrl:                   source.PdfUrl,
		Score:                    source.Score,
		FullText:                 source.FullText,
		StructureMap:             append([]any(nil), source.StructureMap...),
	}
}

func providerWarningsFromRetrievalPayload(payload map[string]any) []search.ProviderWarning {
	rawWarnings, ok := payload["warnings"].([]search.ProviderWarning)
	if ok {
		return append([]search.ProviderWarning(nil), rawWarnings...)
	}
	items, ok := payload["warnings"].([]any)
	if !ok {
		return nil
	}
	warnings := make([]search.ProviderWarning, 0, len(items))
	for _, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			continue
		}
		warning := search.ProviderWarning{
			Provider: stringFromRetrievalPayload(fields["provider"]),
			Message:  stringFromRetrievalPayload(fields["message"]),
		}
		if warning.Provider != "" || warning.Message != "" {
			warnings = append(warnings, warning)
		}
	}
	return warnings
}

func providersFromRetrievalPayload(payload map[string]any) map[string]int {
	switch typed := payload["providers"].(type) {
	case map[string]int:
		return cloneProviderCounts(typed)
	case map[string]any:
		out := make(map[string]int, len(typed))
		for provider, value := range typed {
			if count, ok := intFromRetrievalPayload(value); ok {
				out[provider] = count
			}
		}
		return out
	default:
		return nil
	}
}

func cloneProviderCounts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func intFromRetrievalPayload(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case float32:
		return int(typed), true
	default:
		return 0, false
	}
}

func int64FromRetrievalPayload(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case float32:
		return int64(typed), true
	default:
		return 0, false
	}
}

func stringFromRetrievalPayload(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}
