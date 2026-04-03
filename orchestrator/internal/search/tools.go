package search

import (
	"context"
	"fmt"
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

// HandleToolSearch routes a tool-based search request to the appropriate provider.
func HandleToolSearch(ctx context.Context, reg *ProviderRegistry, tool string, params map[string]any) (SearchResult, error) {
	switch tool {
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
