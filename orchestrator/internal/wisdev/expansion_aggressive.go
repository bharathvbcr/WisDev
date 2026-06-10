package wisdev

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// QueryVariation is one variant of an expanded query.
type QueryVariation struct {
	Query       string `json:"query"`
	Strategy    string `json:"strategy"`
	Priority    int    `json:"priority"`
	TargetAPI   string `json:"target_api,omitempty"`
	Description string `json:"description,omitempty"`
}

// AggressiveExpansionRequest is the request body for POST /expand/aggressive.
type AggressiveExpansionRequest struct {
	Query                string   `json:"query"`
	MaxVariations        int      `json:"max_variations"`
	IncludeMeSH          bool     `json:"include_mesh"`
	IncludeAbbreviations bool     `json:"include_abbreviations"`
	IncludeTemporal      bool     `json:"include_temporal"`
	TargetAPIs           []string `json:"target_apis"`
}

// AggressiveExpansionResponse is the response for POST /expand/aggressive.
type AggressiveExpansionResponse struct {
	Original   string            `json:"original"`
	Variations []QueryVariation  `json:"variations"`
	Metadata   expansionMetadata `json:"metadata"`
	LatencyMs  int64             `json:"latency_ms"`
}

type expansionMetadata struct {
	TotalVariations   int      `json:"total_variations"`
	Strategies        []string `json:"strategies"`
	EstimatedCoverage float64  `json:"estimated_coverage"`
	PrimaryKeywords   []string `json:"primary_keywords"`
}

// SPLADEExpansionRequest is the request body for POST /expand/splade.
type SPLADEExpansionRequest struct {
	Query string `json:"query"`
}

// SPLADEExpansionResponse wraps EnhancedQuery with typed fields.
type SPLADEExpansionResponse struct {
	Original  string   `json:"original"`
	Expanded  string   `json:"expanded"`
	Intent    string   `json:"intent"`
	Keywords  []string `json:"keywords"`
	Synonyms  []string `json:"synonyms"`
	LatencyMs int64    `json:"latency_ms"`
}

func aggressiveCacheKey(query string, maxVariations int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("expand:v2:%s:%d", query, maxVariations)))
	return fmt.Sprintf("expand_aggressive:%x", h[:8])
}

func getAggressiveCache(rdb redis.UniversalClient, query string, maxVariations int) (*AggressiveExpansionResponse, bool) {
	if rdb == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	val, err := rdb.Get(ctx, aggressiveCacheKey(query, maxVariations)).Result()
	if err != nil {
		return nil, false
	}
	var result AggressiveExpansionResponse
	if err := json.Unmarshal([]byte(val), &result); err != nil {
		return nil, false
	}
	return &result, true
}

func setAggressiveCache(rdb redis.UniversalClient, query string, maxVariations int, result *AggressiveExpansionResponse) {
	if rdb == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		data, err := json.Marshal(result)
		if err == nil {
			rdb.Set(ctx, aggressiveCacheKey(query, maxVariations), string(data), time.Hour)
		}
	}()
}

func deduplicateQueryVariations(vs []QueryVariation) []QueryVariation {
	seen := make(map[string]bool, len(vs))
	out := make([]QueryVariation, 0, len(vs))
	for _, v := range vs {
		key := strings.ToLower(strings.TrimSpace(v.Query))
		if !seen[key] {
			seen[key] = true
			out = append(out, v)
		}
	}
	return out
}

func extractPrimaryKeywords(query string) []string {
	return simpleQueryKeywords(query)
}

func calculateCoverageEstimate(variationCount, strategyCount int) float64 {
	v := float64(variationCount) / 15.0
	if v > 1 {
		v = 1
	}
	s := float64(strategyCount) / 10.0
	if s > 1 {
		s = 1
	}
	result := v*0.6 + s*0.4
	return float64(int(result*100)) / 100
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func limitVariations(vs []QueryVariation, n int) []QueryVariation {
	if len(vs) > n {
		return vs[:n]
	}
	return vs
}

func normalizeAdaptiveQuery(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(query))), " ")
}

// GenerateAdaptiveExpansion ranks aggressive query variations using learned
// historical performance for both strategies and specific expanded queries.
func GenerateAdaptiveExpansion(ctx context.Context, si *search.SearchIntelligence, rdb redis.UniversalClient, query string, maxVariations int, includeMeSH, includeAbbrev, includeTemporal bool, targetAPIs []string) AggressiveExpansionResponse {
	resp := GenerateAggressiveExpansionWithContext(ctx, rdb, query, maxVariations*2, includeMeSH, includeAbbrev, includeTemporal, targetAPIs)
	if si == nil {
		return resp
	}

	strategyScores, _ := si.GetStrategyScores(ctx)
	expansionScores, _ := si.GetExpandedQueryScores(ctx, query, maxVariations*3)
	byExpandedQuery := make(map[string]search.ExpandedQueryScore, len(expansionScores))
	for _, item := range expansionScores {
		normalized := normalizeAdaptiveQuery(item.Query)
		if normalized == "" {
			continue
		}
		if current, exists := byExpandedQuery[normalized]; !exists || item.Score > current.Score {
			byExpandedQuery[normalized] = item
		}
	}

	for i := range resp.Variations {
		boost := 0
		if score, ok := strategyScores[resp.Variations[i].Strategy]; ok && score > 0 {
			boost += min2(4, int(score/4.0)+1)
		}
		if historical, ok := byExpandedQuery[normalizeAdaptiveQuery(resp.Variations[i].Query)]; ok && historical.Score > 0 {
			boost += min2(8, int(historical.Score/3.0)+2)
			if resp.Variations[i].Strategy == "" && historical.Strategy != "" {
				resp.Variations[i].Strategy = historical.Strategy
			}
		}
		resp.Variations[i].Priority += boost
	}

	sort.Slice(resp.Variations, func(i, j int) bool {
		if resp.Variations[i].Priority == resp.Variations[j].Priority {
			return resp.Variations[i].Query < resp.Variations[j].Query
		}
		return resp.Variations[i].Priority > resp.Variations[j].Priority
	})
	resp.Variations = limitVariations(resp.Variations, maxVariations)
	resp.Metadata.TotalVariations = len(resp.Variations)
	return resp
}

// GenerateAggressiveExpansion runs LLM-backed query variation generation.
func GenerateAggressiveExpansion(rdb redis.UniversalClient, query string, maxVariations int, includeMeSH, includeAbbrev, includeTemporal bool, targetAPIs []string) AggressiveExpansionResponse {
	return GenerateAggressiveExpansionWithContext(context.Background(), rdb, query, maxVariations, includeMeSH, includeAbbrev, includeTemporal, targetAPIs)
}

// GenerateAggressiveExpansionWithContext generates query variations with the light model.
func GenerateAggressiveExpansionWithContext(ctx context.Context, rdb redis.UniversalClient, query string, maxVariations int, includeMeSH, includeAbbrev, includeTemporal bool, targetAPIs []string) AggressiveExpansionResponse {
	start := time.Now()
	if maxVariations <= 0 {
		maxVariations = 15
	}

	if cached, ok := getAggressiveCache(rdb, query, maxVariations); ok {
		return *cached
	}

	cleanQuery := strings.TrimSpace(query)
	variations := []QueryVariation{{
		Query:       query,
		Strategy:    "original",
		Priority:    10,
		Description: "Original query",
	}}
	strategiesUsed := []string{"original"}
	primaryKeywords := extractPrimaryKeywords(cleanQuery)

	if client := GlobalLLMClient; client != nil {
		brain := NewBrainCapabilities(client)
		llmVariations, keywords, err := brain.GenerateQueryVariations(ctx, query, maxVariations, QueryVariationOptions{
			IncludeMeSH:          includeMeSH,
			IncludeAbbreviations: includeAbbrev,
			IncludeTemporal:      includeTemporal,
			TargetAPIs:           targetAPIs,
		}, "")
		if err == nil && len(llmVariations) > 0 {
			variations = append(variations, llmVariations...)
			strategiesUsed = append(strategiesUsed, "llm")
			if len(keywords) > 0 {
				primaryKeywords = keywords
			}
		}
	}

	variations = deduplicateQueryVariations(variations)
	sort.Slice(variations, func(i, j int) bool {
		return variations[i].Priority > variations[j].Priority
	})
	if len(variations) > maxVariations {
		variations = variations[:maxVariations]
	}

	finalRes := AggressiveExpansionResponse{
		Original:   query,
		Variations: variations,
		Metadata: expansionMetadata{
			TotalVariations:   len(variations),
			Strategies:        strategiesUsed,
			EstimatedCoverage: calculateCoverageEstimate(len(variations), len(strategiesUsed)),
			PrimaryKeywords:   primaryKeywords,
		},
		LatencyMs: time.Since(start).Milliseconds(),
	}
	setAggressiveCache(rdb, query, maxVariations, &finalRes)
	return finalRes
}
