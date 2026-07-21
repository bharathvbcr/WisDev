package wisdev

import (
	"context"
	"log/slog"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/researchquery"
)

// PreparedResearchQuery holds grammar-corrected and search-optimized forms of a query.
type PreparedResearchQuery struct {
	Original    string
	Corrected   string
	SearchQuery string
	Domain      string
	SeedQueries   []string
	AgendaQueries []string
	Intent        string
	Keywords    []string
	Synonyms    []string
	Changed     bool
}

// ResearchQueryPrepareOptions configures AI query preparation.
type ResearchQueryPrepareOptions struct {
	LLMClient *llm.Client
	DisableAI bool
}

// PrepareResearchQuery applies normalization and optional AI preparation.
func PrepareResearchQuery(raw string) PreparedResearchQuery {
	return PrepareResearchQueryWithContext(context.Background(), raw, ResearchQueryPrepareOptions{
		LLMClient: GlobalLLMClient,
	})
}

// PrepareResearchQueryWithContext prepares a research query with one structured LLM call when available.
func PrepareResearchQueryWithContext(ctx context.Context, raw string, opts ResearchQueryPrepareOptions) PreparedResearchQuery {
	original := strings.TrimSpace(raw)
	if original == "" {
		return PreparedResearchQuery{}
	}
	if prep, ok := lookupPreparedQuery(original); ok {
		return prep
	}

	normalized := normalizeResearchQueryText(original)
	offlineCorrected := correctCommonResearchTypos(normalized)
	prep := offlinePreparedResearchQuery(original)
	prep.Corrected = offlineCorrected
	prep.SearchQuery = offlineCorrected
	prep.Changed = offlineCorrected != original

	client := opts.LLMClient
	if client == nil && !opts.DisableAI {
		client = GlobalLLMClient
	}
	if !opts.DisableAI && client != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("Recovered from panic during structured query prep (likely mock client mismatch)", "err", r)
				}
			}()
			brain := NewBrainCapabilities(client)
			if structured, err := brain.PrepareResearchQueryStructured(ctx, offlineCorrected, ""); err == nil {
				structured.Original = original
				if structured.Corrected == "" {
					structured.Corrected = normalized
				}
				structured.Changed = structured.Corrected != original
				prep = structured
			} else if !llm.IsProviderRateLimitError(err) {
				slog.Warn("research query structured prep unavailable; using normalized query",
					"component", "wisdev.query_prepare",
					"operation", "prepare_research_query",
					"stage", "structured_fallback",
					"error", err,
				)
			}
		}()
	}

	storePreparedQuery(prep)
	return prep
}

func normalizeResearchQueryText(query string) string {
	return researchquery.NormalizeText(query)
}

func applyResearchQueryCorrections(query string) string {
	if prep, ok := lookupPreparedQuery(query); ok && strings.TrimSpace(prep.Corrected) != "" {
		return prep.Corrected
	}
	return researchquery.PrepareForProviderSearch(query)
}

// prepareSearchQueryText normalizes and corrects a query immediately before provider search.
func prepareSearchQueryText(query string) string {
	return strings.TrimSpace(applyResearchQueryCorrections(query))
}

// PrepareJobResearchQuery corrects a raw job/query payload before durable storage or loop dispatch.
func PrepareJobResearchQuery(ctx context.Context, raw, domain string, client *llm.Client, disableAI bool) (originalQuery, searchQuery, detectedDomain string) {
	raw = strings.TrimSpace(raw)
	originalQuery, correctedQuery, planningQuery, detectedDomain := ApplyEarlySessionQueryPrep(ctx, raw, "", "", domain, client, disableAI)
	searchQuery = ResolveSessionSearchQuery(planningQuery, correctedQuery, originalQuery)
	if strings.TrimSpace(detectedDomain) == "" {
		detectedDomain = strings.TrimSpace(domain)
	}
	return originalQuery, searchQuery, detectedDomain
}

// EarlyPrepareResearchQuery runs offline typo correction plus optional LLM structured prep.
// Call this before any retrieval, worker fan-out, or agenda planning.
func EarlyPrepareResearchQuery(ctx context.Context, raw string, client *llm.Client, disableAI bool) PreparedResearchQuery {
	return PrepareResearchQueryWithContext(ctx, raw, ResearchQueryPrepareOptions{
		LLMClient: client,
		DisableAI: disableAI,
	})
}

// ApplyEarlySessionQueryPrep corrects session query fields before persistence or search.
func ApplyEarlySessionQueryPrep(ctx context.Context, originalQuery, correctedQuery, planningQuery, detectedDomain string, client *llm.Client, disableAI bool) (string, string, string, string) {
	originalQuery = strings.TrimSpace(originalQuery)
	if originalQuery == "" {
		return originalQuery, correctedQuery, planningQuery, detectedDomain
	}
	needsCorrection := strings.TrimSpace(correctedQuery) == "" || strings.EqualFold(strings.TrimSpace(correctedQuery), originalQuery)
	if !needsCorrection && strings.TrimSpace(planningQuery) != "" && strings.TrimSpace(detectedDomain) != "" {
		return originalQuery, correctedQuery, planningQuery, detectedDomain
	}
	prep := EarlyPrepareResearchQuery(ctx, originalQuery, client, disableAI)
	if needsCorrection {
		if corrected := strings.TrimSpace(prep.Corrected); corrected != "" {
			correctedQuery = corrected
		}
	}
	if strings.TrimSpace(planningQuery) == "" {
		if search := strings.TrimSpace(prep.SearchQuery); search != "" {
			planningQuery = search
		} else if strings.TrimSpace(correctedQuery) != "" {
			planningQuery = correctedQuery
		}
	}
	if strings.TrimSpace(detectedDomain) == "" {
		detectedDomain = strings.TrimSpace(prep.Domain)
	}
	return originalQuery, correctedQuery, planningQuery, detectedDomain
}
