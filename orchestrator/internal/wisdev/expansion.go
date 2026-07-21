package wisdev

import (
	"context"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
)

// applyMultiSourceScoreBoost boosts the Score of papers that appear in 2 or
// more sources (i.e. SourceCount >= 2) by +0.2.
func applyMultiSourceScoreBoost(papers []Source) []Source {
	const boost = 0.2
	for i := range papers {
		if papers[i].SourceCount >= 2 {
			papers[i].Score += boost
		}
	}
	return papers
}

// ExpandQuery performs LLM-backed query expansion when a client is available.
func ExpandQuery(query string) EnhancedQuery {
	return ExpandQueryWithContext(context.Background(), query, GlobalLLMClient)
}

// ExpandQueryWithContext expands a query using structured prep output.
func ExpandQueryWithContext(ctx context.Context, query string, client *llm.Client) EnhancedQuery {
	query = strings.TrimSpace(query)
	if query == "" {
		return EnhancedQuery{}
	}

	opts := ResearchQueryPrepareOptions{LLMClient: client}
	if client == nil {
		opts.DisableAI = true
	}
	prep := PrepareResearchQueryWithContext(ctx, query, opts)

	searchQuery := strings.TrimSpace(prep.SearchQuery)
	if searchQuery == "" {
		searchQuery = strings.TrimSpace(prep.Corrected)
	}
	if searchQuery == "" {
		searchQuery = query
	}

	parts := []string{searchQuery}
	for i, synonym := range prep.Synonyms {
		if i >= 2 {
			break
		}
		synonym = strings.TrimSpace(synonym)
		if synonym != "" {
			parts = append(parts, synonym)
		}
	}

	intent := strings.TrimSpace(prep.Intent)
	if intent == "" {
		intent = "academic"
	}

	return EnhancedQuery{
		Original: query,
		Expanded: strings.Join(parts, " OR "),
		Intent:   intent,
		Strategy: "llm_prep",
		Keywords: append([]string(nil), prep.Keywords...),
		Synonyms: append([]string(nil), prep.Synonyms...),
	}
}
