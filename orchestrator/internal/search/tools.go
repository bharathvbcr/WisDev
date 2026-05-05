package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	ToolSearchPapersName     = "wisdevSearchPapers"
	maxToolSearchPapersLimit = 50
)

var (
	SearchPapersToolAliases = []string{"search_papers", "retrieve_papers", "research.retrievePapers", "wisdev.retrievePapers", "scholarlmSearchPapers", "scholarlm.retrievePapers"}
	SearchPapersToolSchema  = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":50},"domain":{"type":"string"},"sources":{"type":"array","items":{"type":"string"}},"retrievalStrategies":{"type":"array","items":{"type":"string"}},"yearFrom":{"type":"integer"},"yearTo":{"type":"integer"},"skipCache":{"type":"boolean"},"qualitySort":{"type":"boolean"},"stage2Rerank":{"type":"boolean"},"pageIndexRerank":{"type":"boolean"},"traceId":{"type":"string"}},"required":["query"]}`)
)

// AuthorSearcher is an optional interface for providers that can search by author ID.
type AuthorSearcher interface {
	SearchProvider
	SearchByAuthor(ctx context.Context, authorID string, limit int) ([]Paper, error)
}

// PaperLookupProvider is an optional interface for providers that can look up a single paper by ID.
type PaperLookupProvider interface {
	SearchProvider
	SearchByPaperID(ctx context.Context, paperID string) (*Paper, error)
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	ReadOnly    bool            `json:"readOnly"`
	Aliases     []string        `json:"aliases,omitempty"`
	Schema      json.RawMessage `json:"schema"`
}

func ListToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        ToolSearchPapersName,
			Description: "Run provider-backed academic paper retrieval with bounded fan-out, provider filters, year filters, cache controls, and quality sorting.",
			ReadOnly:    true,
			Aliases:     append([]string(nil), SearchPapersToolAliases...),
			Schema:      SearchPapersToolSchema,
		},
		{
			Name:        "paper_lookup",
			Description: "Look up one paper by a provider-specific stable paper ID.",
			ReadOnly:    true,
			Schema:      json.RawMessage(`{"type":"object","properties":{"paperId":{"type":"string","minLength":1}},"required":["paperId"]}`),
		},
		{
			Name:        "author_lookup",
			Description: "Retrieve papers for a provider-specific author ID.",
			ReadOnly:    true,
			Schema:      json.RawMessage(`{"type":"object","properties":{"authorId":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":50}},"required":["authorId"]}`),
		},
	}
}

// HandleToolSearch routes a tool-based search request to the appropriate provider.
func HandleToolSearch(ctx context.Context, reg *ProviderRegistry, tool string, params map[string]any) (SearchResult, error) {
	switch canonicalToolName(tool) {
	case ToolSearchPapersName:
		query := stringParam(params, "query")
		if query == "" {
			return SearchResult{}, fmt.Errorf("missing query parameter")
		}
		limit := intParam(params, "limit", 10)
		if limit <= 0 {
			limit = 10
		}
		if limit > maxToolSearchPapersLimit {
			limit = maxToolSearchPapersLimit
		}
		opts := SearchOpts{
			Limit:       limit,
			Domain:      stringParam(params, "domain"),
			Sources:     sourceHintsParam(params),
			YearFrom:    intParam(params, "yearFrom", 0),
			YearTo:      intParam(params, "yearTo", 0),
			SkipCache:   boolParam(params, "skipCache", false),
			QualitySort: boolParam(params, "qualitySort", true),
		}
		return ParallelSearch(ctx, reg, query, opts), nil

	case "author_lookup":
		authorID, _ := params["authorId"].(string)
		if authorID == "" {
			return SearchResult{}, fmt.Errorf("missing authorId parameter")
		}

		// Find a provider that supports author_lookup
		for _, p := range reg.All() {
			if searcher, ok := p.(AuthorSearcher); ok {
				for _, t := range p.Tools() {
					if t == "author_lookup" {
						papers, err := searcher.SearchByAuthor(ctx, authorID, 20)
						if err != nil {
							continue // Try next provider if one fails
						}
						return SearchResult{
							Papers:    papers,
							Providers: map[string]int{p.Name(): len(papers)},
						}, nil
					}
				}
			}
		}
		return SearchResult{}, fmt.Errorf("no provider found for author_lookup")

	case "paper_lookup":
		paperID, _ := params["paperId"].(string)
		if paperID == "" {
			return SearchResult{}, fmt.Errorf("missing paperId parameter")
		}

		for _, p := range reg.All() {
			if lookup, ok := p.(PaperLookupProvider); ok {
				for _, t := range p.Tools() {
					if t == "paper_lookup" {
						paper, err := lookup.SearchByPaperID(ctx, paperID)
						if err != nil {
							continue
						}
						return SearchResult{
							Papers:    []Paper{*paper},
							Providers: map[string]int{p.Name(): 1},
						}, nil
					}
				}
			}
		}
		return SearchResult{}, fmt.Errorf("no provider found for paper_lookup")

	default:
		return SearchResult{}, fmt.Errorf("unsupported tool: %s", tool)
	}
}

func canonicalToolName(tool string) string {
	trimmed := strings.TrimSpace(tool)
	if trimmed == ToolSearchPapersName {
		return ToolSearchPapersName
	}
	for _, alias := range SearchPapersToolAliases {
		if trimmed == alias {
			return ToolSearchPapersName
		}
	}
	switch trimmed {
	default:
		return trimmed
	}
}

func stringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return strings.TrimSpace(value)
}

func intParam(params map[string]any, key string, fallback int) int {
	switch value := params[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err == nil {
			return int(parsed)
		}
		return fallback
	default:
		return fallback
	}
}

func boolParam(params map[string]any, key string, fallback bool) bool {
	value, ok := params[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

func stringSliceParam(params map[string]any, key string) []string {
	switch values := params[key].(type) {
	case []string:
		return cleanStringSlice(values)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return cleanStringSlice(out)
	default:
		return nil
	}
}

func sourceHintsParam(params map[string]any) []string {
	if sources := stringSliceParam(params, "sources"); len(sources) > 0 {
		return sources
	}
	strategies := stringSliceParam(params, "retrievalStrategies")
	out := make([]string, 0, len(strategies))
	for _, strategy := range strategies {
		normalized := NormalizeProviderName(strategy)
		if IsCanonicalProviderName(normalized) {
			out = append(out, normalized)
		}
	}
	return cleanStringSlice(out)
}

func cleanStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
