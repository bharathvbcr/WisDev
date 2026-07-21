package wisdev

import (
	"context"
	"strings"
)

// InferResearchDomain returns a coarse search domain for provider routing.
func InferResearchDomain(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	if prep, ok := lookupPreparedQuery(query); ok {
		return strings.TrimSpace(prep.Domain)
	}
	prep := PrepareResearchQueryWithContext(context.Background(), query, ResearchQueryPrepareOptions{
		LLMClient: GlobalLLMClient,
	})
	return strings.TrimSpace(prep.Domain)
}
