package search

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBProvider is an interface for database operations.
type DBProvider interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SearchIntelligence handles learning from search history and clicks.
type SearchIntelligence struct {
	db DBProvider
	mu sync.RWMutex

	// In-memory cache for provider performance to avoid hitting DB every request
	perfCache  map[string]float64 // domain -> provider performance map (simplified)
	lastUpdate time.Time
}

func normalizeQuery(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

func normalizeOptionalUserID(userID string) any {
	trimmed := strings.TrimSpace(userID)
	if trimmed == "" || strings.EqualFold(trimmed, "anonymous") || strings.EqualFold(trimmed, "internal-service") {
		return nil
	}

	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return nil
	}
	return parsed.String()
}

func NewSearchIntelligence(db DBProvider) *SearchIntelligence {
	return &SearchIntelligence{
		db:        db,
		perfCache: make(map[string]float64),
	}
}

// RecordSearch logs search metadata for future learning.
func (si *SearchIntelligence) RecordSearch(ctx context.Context, query string, provider string, informationGain float64, latencyMs int64) error {
	if si.db == nil {
		return nil
	}

	normalizedQuery := normalizeQuery(query)
	_, err := si.db.Exec(ctx,
		"INSERT INTO hindsight_logs (query, provider, information_gain, latency_ms) VALUES ($1, $2, $3, $4)",
		normalizedQuery, provider, informationGain, latencyMs,
	)
	return err
}

// RecordExpansionPerformance logs how well a specific expansion strategy performed.
func (si *SearchIntelligence) RecordExpansionPerformance(ctx context.Context, original, expanded, strategy string, resultCount int, confidence float64) error {
	if si.db == nil {
		return nil
	}

	_, err := si.db.Exec(ctx,
		"INSERT INTO expansion_performance (original_query, expanded_query, strategy, result_count, confidence) VALUES ($1, $2, $3, $4, $5)",
		normalizeQuery(original), normalizeQuery(expanded), strategy, resultCount, confidence,
	)
	return err
}

// RecordClick logs a user click on a search result.
func (si *SearchIntelligence) RecordClick(ctx context.Context, userID string, query string, paperID string, provider string, rank int) error {
	if si.db == nil {
		return nil
	}

	normalizedQuery := normalizeQuery(query)
	_, err := si.db.Exec(ctx,
		"INSERT INTO search_clicks (user_id, query, paper_id, provider, rank) VALUES ($1, $2, $3, $4, $5)",
		normalizeOptionalUserID(userID), normalizedQuery, paperID, provider, rank,
	)
	return err
}

// GetTopProviders returns the best providers for a given domain based on historical performance.
// When domain is non-empty, it filters hindsight_logs to that domain so provider scores are
// domain-specific (e.g. PubMed ranks higher for biomedical than for CS).
func (si *SearchIntelligence) GetTopProviders(ctx context.Context, domain string, limit int) ([]string, error) {
	if si.db == nil {
		return nil, fmt.Errorf("no database connection")
	}
	if limit <= 0 {
		limit = 5
	}

	clickCounts, err := si.getProviderClickCounts(ctx)
	if err != nil {
		return nil, err
	}

	avgGains, err := si.getProviderAverageGains(ctx, domain)
	if err != nil {
		return nil, err
	}
	if len(avgGains) == 0 && strings.TrimSpace(domain) != "" {
		avgGains, err = si.getProviderAverageGains(ctx, "")
		if err != nil {
			return nil, err
		}
	}

	type providerScore struct {
		name  string
		score float64
	}

	ranked := make([]providerScore, 0, len(avgGains))
	for provider, avgGain := range avgGains {
		ranked = append(ranked, providerScore{
			name:  provider,
			score: avgGain*0.7 + float64(clickCounts[provider])*0.3,
		})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].name < ranked[j].name
		}
		return ranked[i].score > ranked[j].score
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	providers := make([]string, 0, len(ranked))
	for _, item := range ranked {
		providers = append(providers, item.name)
	}

	return providers, nil
}

func (si *SearchIntelligence) getProviderAverageGains(ctx context.Context, domain string) (map[string]float64, error) {
	trimmedDomain := strings.TrimSpace(domain)
	if trimmedDomain == "" {
		rows, err := si.db.Query(ctx, `
			SELECT provider, AVG(information_gain) AS avg_gain
			FROM hindsight_logs
			GROUP BY provider
		`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		scores := make(map[string]float64)
		for rows.Next() {
			var provider string
			var avgGain float64
			if err := rows.Scan(&provider, &avgGain); err != nil {
				return nil, err
			}
			scores[provider] = avgGain
		}
		return scores, nil
	}

	rows, err := si.db.Query(ctx, `
		SELECT provider, query, information_gain
		FROM hindsight_logs
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totalGainByProvider := make(map[string]float64)
	sampleCountByProvider := make(map[string]int)
	for rows.Next() {
		var provider string
		var query string
		var informationGain float64
		if err := rows.Scan(&provider, &query, &informationGain); err != nil {
			return nil, err
		}
		if inferDomainFromQuery(query) != trimmedDomain {
			continue
		}
		totalGainByProvider[provider] += informationGain
		sampleCountByProvider[provider]++
	}

	scores := make(map[string]float64, len(totalGainByProvider))
	for provider, totalGain := range totalGainByProvider {
		sampleCount := sampleCountByProvider[provider]
		scores[provider] = totalGain / float64(sampleCount)
	}
	return scores, nil
}

func (si *SearchIntelligence) getProviderClickCounts(ctx context.Context) (map[string]int, error) {
	rows, err := si.db.Query(ctx, `
		SELECT provider, COUNT(*) AS click_count
		FROM search_clicks
		GROUP BY provider
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	clickCounts := make(map[string]int)
	for rows.Next() {
		var provider string
		var clickCount int
		if err := rows.Scan(&provider, &clickCount); err != nil {
			return nil, err
		}
		clickCounts[provider] = clickCount
	}
	return clickCounts, nil
}

// GetProviderScores returns a mapping of provider to their intelligence-based score for reranking.
func (si *SearchIntelligence) GetProviderScores(ctx context.Context) (map[string]float64, error) {
	if si.db == nil {
		return nil, nil
	}

	query := `
		SELECT provider, AVG(information_gain) as score
		FROM hindsight_logs
		GROUP BY provider
	`

	rows, err := si.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scores := make(map[string]float64)
	for rows.Next() {
		var p string
		var s float64
		if err := rows.Scan(&p, &s); err != nil {
			return nil, err
		}
		scores[p] = s
	}

	return scores, nil
}

// GetClickCountsForQuery returns paper-level click counts for a query.
// The returned map is keyed by paper ID, value is click count.
func (si *SearchIntelligence) GetClickCountsForQuery(ctx context.Context, query string, limit int) (map[string]int, error) {
	if si.db == nil {
		return map[string]int{}, nil
	}
	if limit <= 0 {
		limit = 200
	}

	normalizedQuery := normalizeQuery(query)
	rows, err := si.db.Query(ctx, `
		SELECT paper_id, COUNT(*) AS click_count
		FROM search_clicks
		WHERE query = $1
		GROUP BY paper_id
		ORDER BY click_count DESC
		LIMIT $2
	`, normalizedQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var paperID string
		var clickCount int
		if scanErr := rows.Scan(&paperID, &clickCount); scanErr != nil {
			return nil, scanErr
		}
		counts[paperID] = clickCount
	}

	return counts, nil
}

// ExpandedQueryScore captures historical performance for a specific expanded query.
type ExpandedQueryScore struct {
	Query    string
	Strategy string
	Score    float64
}

// GetExpandedQueryScores returns the highest-performing historical expansions for
// an original query ordered by score descending.
func (si *SearchIntelligence) GetExpandedQueryScores(ctx context.Context, original string, limit int) ([]ExpandedQueryScore, error) {
	if si.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	rows, err := si.db.Query(ctx, `
		SELECT expanded_query, strategy, AVG(CAST(result_count AS FLOAT) * confidence) AS score
		FROM expansion_performance
		WHERE original_query = $1
		GROUP BY expanded_query, strategy
		ORDER BY score DESC
		LIMIT $2
	`, normalizeQuery(original), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scores := make([]ExpandedQueryScore, 0, limit)
	for rows.Next() {
		var item ExpandedQueryScore
		if err := rows.Scan(&item.Query, &item.Strategy, &item.Score); err != nil {
			return nil, err
		}
		if strings.TrimSpace(item.Query) == "" {
			continue
		}
		scores = append(scores, item)
	}
	return scores, nil
}

// GetStrategyScores returns average performance per expansion strategy.
func (si *SearchIntelligence) GetStrategyScores(ctx context.Context) (map[string]float64, error) {
	if si.db == nil {
		return nil, nil
	}

	query := `
		SELECT strategy, AVG(CAST(result_count AS FLOAT) * confidence) as score
		FROM expansion_performance
		GROUP BY strategy
	`

	rows, err := si.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scores := make(map[string]float64)
	for rows.Next() {
		var s string
		var sc float64
		if err := rows.Scan(&s, &sc); err != nil {
			return nil, err
		}
		scores[s] = sc
	}

	return scores, nil
}
