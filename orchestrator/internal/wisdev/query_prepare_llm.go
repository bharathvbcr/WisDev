package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

// GlobalLLMClient is injected by the server/gateway for query prep paths without an explicit client.
var GlobalLLMClient *llm.Client

var preparedQueryCache sync.Map

// preparedQueryCacheSize tracks the number of distinct keys stored in
// preparedQueryCache so long-running servers can evict the cache once it
// exceeds preparedQueryCacheMaxEntries instead of growing without bound.
var preparedQueryCacheSize atomic.Int64

const preparedQueryCacheMaxEntries = 512

// ResetQueryPreparationStateForTest clears process-global query-preparation
// state (the prepared-query cache and the global LLM client) so tests in other
// packages can isolate themselves from cross-test pollution. Test use only.
func ResetQueryPreparationStateForTest() {
	preparedQueryCache = sync.Map{}
	preparedQueryCacheSize.Store(0)
	GlobalLLMClient = nil
}

const (
	researchQueryPrepTimeout       = 20 * time.Second
	queryVariationGenerationTimeout = 18 * time.Second

	researchQueryPrepSchema = `{
  "type": "object",
  "properties": {
    "corrected_query": {"type": "string"},
    "search_query": {"type": "string"},
    "domain": {"type": "string"},
    "intent": {"type": "string"},
    "keywords": {"type": "array", "items": {"type": "string"}},
    "synonyms": {"type": "array", "items": {"type": "string"}},
    "seed_queries": {"type": "array", "items": {"type": "string"}},
    "agenda_queries": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["corrected_query", "search_query", "domain", "intent", "keywords", "synonyms", "seed_queries", "agenda_queries"]
}`

	queryVariationsSchema = `{
  "type": "object",
  "properties": {
    "variations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "query": {"type": "string"},
          "strategy": {"type": "string"},
          "priority": {"type": "integer"},
          "description": {"type": "string"},
          "target_api": {"type": "string"}
        },
        "required": ["query", "strategy", "priority"]
      }
    },
    "primary_keywords": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["variations"]
}`
)

// QueryVariationOptions configures LLM query-variation generation.
type QueryVariationOptions struct {
	IncludeMeSH          bool
	IncludeAbbreviations bool
	IncludeTemporal      bool
	TargetAPIs           []string
}

type structuredResearchQueryPrep struct {
	CorrectedQuery string   `json:"corrected_query"`
	SearchQuery    string   `json:"search_query"`
	Domain         string   `json:"domain"`
	Intent         string   `json:"intent"`
	Keywords       []string `json:"keywords"`
	Synonyms       []string `json:"synonyms"`
	SeedQueries    []string `json:"seed_queries"`
	AgendaQueries  []string `json:"agenda_queries"`
}

type structuredQueryVariations struct {
	Variations       []QueryVariation `json:"variations"`
	PrimaryKeywords  []string         `json:"primary_keywords"`
}

func preparedQueryCacheKey(query string) string {
	return normalizeResearchQueryText(query)
}

func lookupPreparedQuery(query string) (PreparedResearchQuery, bool) {
	key := preparedQueryCacheKey(query)
	if value, ok := preparedQueryCache.Load(key); ok {
		if prep, ok := value.(PreparedResearchQuery); ok {
			return prep, true
		}
	}
	return PreparedResearchQuery{}, false
}

func lookupAgendaQueries(query string) []string {
	query = prepareSearchQueryText(query)
	if prep, ok := lookupPreparedQuery(query); ok && len(prep.AgendaQueries) > 0 {
		return sanitizeAgendaFoci(query, prep.AgendaQueries)
	}
	return offlineAgendaQueries(query)
}

func sanitizeAgendaFoci(root string, agenda []string) []string {
	root = prepareSearchQueryText(root)
	out := make([]string, 0, len(agenda))
	rootLower := strings.ToLower(root)
	for _, item := range dedupeTrimmedStrings(agenda) {
		item = strings.TrimSpace(applyResearchQueryCorrections(item))
		if item == "" {
			continue
		}
		itemLower := strings.ToLower(item)
		if root != "" && strings.EqualFold(item, root) {
			continue
		}
		if root != "" && strings.HasPrefix(itemLower, rootLower+" ") {
			if suffix := strings.TrimSpace(item[len(root):]); suffix != "" {
				out = append(out, suffix)
				continue
			}
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return offlineAgendaQueries(root)
	}
	return out
}

func normalizeAgendaFocus(root, focus string) string {
	root = prepareSearchQueryText(root)
	focus = strings.TrimSpace(applyResearchQueryCorrections(focus))
	if focus == "" {
		return root
	}
	if root == "" {
		return focus
	}
	rootLower := strings.ToLower(root)
	focusLower := strings.ToLower(focus)
	if focusLower == rootLower {
		return root
	}
	if strings.HasPrefix(focusLower, rootLower+" ") {
		return focus
	}
	if strings.Contains(focusLower, rootLower) && len(focus) > len(root)+3 {
		return focus
	}
	return strings.TrimSpace(root + " " + focus)
}

func ensureQueryPrepared(ctx context.Context, client *llm.Client, query string) PreparedResearchQuery {
	return EarlyPrepareResearchQuery(ctx, query, client, false)
}

func resolveLoopResearchQuery(ctx context.Context, client *llm.Client, req LoopRequest) (researchQuery string, originalQuery string, prep PreparedResearchQuery) {
	prepInput := strings.TrimSpace(req.OriginalQuery)
	if prepInput == "" {
		prepInput = strings.TrimSpace(req.Query)
	}
	prep = EarlyPrepareResearchQuery(ctx, prepInput, client, req.DisableQueryEnhance)
	researchQuery = strings.TrimSpace(prep.SearchQuery)
	if researchQuery == "" {
		researchQuery = strings.TrimSpace(prep.Corrected)
	}
	if researchQuery == "" {
		researchQuery = strings.TrimSpace(req.Query)
	}
	originalQuery = strings.TrimSpace(req.OriginalQuery)
	if originalQuery == "" {
		originalQuery = strings.TrimSpace(prep.Original)
	}
	if originalQuery == "" {
		originalQuery = researchQuery
	}
	return researchQuery, originalQuery, prep
}

func storePreparedQuery(prep PreparedResearchQuery) {
	keys := dedupeTrimmedStrings([]string{
		preparedQueryCacheKey(prep.Original),
		preparedQueryCacheKey(prep.Corrected),
		preparedQueryCacheKey(prep.SearchQuery),
	})
	if len(keys) == 0 {
		return
	}
	newKeys := int64(0)
	for _, key := range keys {
		if _, loaded := preparedQueryCache.LoadOrStore(key, prep); loaded {
			// Overwrite existing entries without growing the size counter.
			preparedQueryCache.Store(key, prep)
			continue
		}
		newKeys++
	}
	if newKeys == 0 {
		return
	}
	if preparedQueryCacheSize.Add(newKeys) > preparedQueryCacheMaxEntries {
		// Bounded eviction: clear everything, then re-store only the current
		// entry so the freshest preparation survives the reset.
		preparedQueryCache.Clear()
		for _, key := range keys {
			preparedQueryCache.Store(key, prep)
		}
		preparedQueryCacheSize.Store(int64(len(keys)))
	}
}

func offlinePreparedResearchQuery(original string) PreparedResearchQuery {
	normalized := correctCommonResearchTypos(normalizeResearchQueryText(original))
	return PreparedResearchQuery{
		Original:      original,
		Corrected:     normalized,
		SearchQuery:   normalized,
		Domain:        "",
		Intent:        "academic",
		Keywords:      simpleQueryKeywords(normalized),
		Synonyms:      nil,
		SeedQueries:   offlineSeedQueries(normalized),
		AgendaQueries: offlineAgendaQueries(normalized),
		Changed:       normalized != original,
	}
}

func offlineAgendaQueries(root string) []string {
	return []string{
		"systematic review meta analysis",
		"limitations contradictory evidence",
	}
}

func offlineSeedQueries(root string) []string {
	if clauses := queryTopicClauses(root); len(clauses) >= 2 {
		queries := []string{strings.TrimSpace(root)}
		for _, clause := range clauses {
			queries = append(queries, strings.TrimSpace(clause))
		}
		return dedupeTrimmedStrings(queries)
	}
	anchors := queryAnchorTerms(root)
	if len(anchors) == 0 {
		return nil
	}
	focus := strings.Join(anchors[:minInt(2, len(anchors))], " ")
	return dedupeTrimmedStrings([]string{focus, focus + " systematic review"})
}

func simpleQueryKeywords(query string) []string {
	words := strings.Fields(strings.ToLower(query))
	out := make([]string, 0, minInt(8, len(words)))
	seen := make(map[string]struct{}, len(words))
	for _, word := range words {
		word = strings.Trim(word, ".,;:!?\"'()[]{}/-")
		if len(word) <= 3 {
			continue
		}
		if _, exists := seen[word]; exists {
			continue
		}
		seen[word] = struct{}{}
		out = append(out, word)
		if len(out) == 8 {
			break
		}
	}
	return out
}

func preparedFromStructured(original string, structured structuredResearchQueryPrep) PreparedResearchQuery {
	corrected := strings.TrimSpace(structured.CorrectedQuery)
	if corrected == "" {
		corrected = correctCommonResearchTypos(normalizeResearchQueryText(original))
	} else {
		corrected = correctCommonResearchTypos(corrected)
	}
	searchQuery := strings.TrimSpace(structured.SearchQuery)
	if searchQuery == "" {
		searchQuery = corrected
	} else {
		searchQuery = correctCommonResearchTypos(searchQuery)
	}
	domain := normalizeResearchDomain(structured.Domain)
	intent := strings.TrimSpace(structured.Intent)
	if intent == "" {
		intent = "academic"
	}
	keywords := dedupeTrimmedStrings(structured.Keywords)
	if len(keywords) == 0 {
		keywords = simpleQueryKeywords(corrected)
	}
	synonyms := dedupeTrimmedStrings(structured.Synonyms)
	seeds := dedupeTrimmedStrings(structured.SeedQueries)
	if len(seeds) == 0 {
		seeds = offlineSeedQueries(corrected)
	}
	agenda := sanitizeAgendaFoci(corrected, dedupeTrimmedStrings(structured.AgendaQueries))
	if len(agenda) == 0 {
		agenda = offlineAgendaQueries(corrected)
	}
	return PreparedResearchQuery{
		Original:      original,
		Corrected:     corrected,
		SearchQuery:   searchQuery,
		Domain:        domain,
		Intent:        intent,
		Keywords:      keywords,
		Synonyms:      synonyms,
		SeedQueries:   seeds,
		AgendaQueries: agenda,
		Changed:       corrected != strings.TrimSpace(original),
	}
}

func normalizeResearchDomain(domain string) string {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "medicine", "medical", "clinical", "biomedical":
		return "medicine"
	case "cs", "computer_science", "computer science":
		return "cs"
	case "neuro", "neuroscience":
		return "neuro"
	case "biology", "bio":
		return "biology"
	case "physics", "engineering":
		return "physics"
	case "social", "social_science", "social science":
		return "social"
	default:
		return ""
	}
}

func (c *BrainCapabilities) PrepareResearchQueryStructured(ctx context.Context, query string, model string) (PreparedResearchQuery, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return PreparedResearchQuery{}, fmt.Errorf("PrepareResearchQueryStructured: query is required")
	}
	if c == nil || c.llmClient == nil {
		return offlinePreparedResearchQuery(query), nil
	}
	if model == "" {
		model = llm.ResolveLightModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c, model); remaining > 0 {
		slog.Warn("wisdev brain structured query prep using cooldown fallback",
			"component", "wisdev.query_prepare",
			"operation", "prepare_research_query_structured",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return offlinePreparedResearchQuery(query), nil
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`Prepare this academic research query for scholarly search.

Tasks:
- Fix spelling and grammar without changing the research topic.
- Produce a compact search_query suitable for academic APIs (expand abbreviations only when needed for retrieval).
- Infer domain: medicine, cs, neuro, biology, physics, social, or empty when unclear.
- Infer intent (examples: medical, computer_science, review, implementation, academic).
- Return up to 8 keywords, up to 6 synonyms/related terms, up to 8 focused seed_queries, and up to 4 agenda_queries.
- seed_queries: parallel retrieval branches on the same topic.
- agenda_queries: short research angles (reviews, trials, limitations, methods) to schedule in a multi-step loop.

Query: %s`, query))

	reqCtx, cancel := context.WithTimeout(ctx, researchQueryPrepTimeout)
	defer cancel()

	resp, err := c.llmClient.StructuredOutput(reqCtx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: researchQueryPrepSchema,
	}, "light", true))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain structured query prep using provider-rate-limit fallback",
				"component", "wisdev.query_prepare",
				"operation", "prepare_research_query_structured",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return offlinePreparedResearchQuery(query), nil
		}
		return PreparedResearchQuery{}, err
	}

	var structured structuredResearchQueryPrep
	if err := json.Unmarshal([]byte(resp.GetJsonResult()), &structured); err != nil {
		return PreparedResearchQuery{}, err
	}
	return preparedFromStructured(query, structured), nil
}

func (c *BrainCapabilities) GenerateQueryVariations(ctx context.Context, query string, maxVariations int, opts QueryVariationOptions, model string) ([]QueryVariation, []string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil, fmt.Errorf("GenerateQueryVariations: query is required")
	}
	if maxVariations <= 0 {
		maxVariations = 15
	}
	if c == nil || c.llmClient == nil {
		return nil, simpleQueryKeywords(query), nil
	}
	if model == "" {
		model = llm.ResolveLightModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c, model); remaining > 0 {
		return nil, simpleQueryKeywords(query), nil
	}

	targetAPIs := strings.Join(opts.TargetAPIs, ", ")
	if targetAPIs == "" {
		targetAPIs = "semantic_scholar, openalex, pubmed, arxiv"
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`Generate diverse academic search query variations for: %q

Constraints:
- Return up to %d variations excluding the original query.
- Prioritize recall without drifting off-topic.
- Include MeSH/clinical phrasing: %t
- Include abbreviation expansions/contractions: %t
- Include temporal/recent phrasing: %t
- Prefer target APIs when useful: %s
- Each variation needs strategy (synonym, mesh, abbreviation, boolean, phrase, broader, narrower, temporal, api_optimized, llm) and priority 3-10.

Query: %s`, query, maxVariations, opts.IncludeMeSH, opts.IncludeAbbreviations, opts.IncludeTemporal, targetAPIs, query))

	reqCtx, cancel := context.WithTimeout(ctx, queryVariationGenerationTimeout)
	defer cancel()

	resp, err := c.llmClient.StructuredOutput(reqCtx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: queryVariationsSchema,
	}, "light", true))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			return nil, simpleQueryKeywords(query), nil
		}
		return nil, nil, err
	}

	var structured structuredQueryVariations
	if err := json.Unmarshal([]byte(resp.GetJsonResult()), &structured); err != nil {
		return nil, nil, err
	}
	variations := make([]QueryVariation, 0, len(structured.Variations))
	for _, item := range structured.Variations {
		item.Query = strings.TrimSpace(item.Query)
		if item.Query == "" {
			continue
		}
		if item.Strategy == "" {
			item.Strategy = "llm"
		}
		if item.Priority <= 0 {
			item.Priority = 5
		}
		variations = append(variations, item)
	}
	keywords := structured.PrimaryKeywords
	if len(keywords) == 0 {
		keywords = simpleQueryKeywords(query)
	}
	return variations, keywords, nil
}
