package search

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

func getCacheKey(query string, opts SearchOpts) string {
	return fmt.Sprintf("search:%s:d:%s:l:%d:yf:%d:yt:%d:s:%s",
		query, opts.Domain, opts.Limit, opts.YearFrom, opts.YearTo, normalizeSourceCacheKey(opts.Sources))
}

func normalizeSourceCacheKey(sources []string) string {
	if len(sources) == 0 {
		return "-"
	}

	seen := make(map[string]struct{}, len(sources))
	normalized := make([]string, 0, len(sources))
	for _, source := range sources {
		source = normalizeRequestedProviderName(source)
		if source == "" {
			continue
		}
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		normalized = append(normalized, source)
	}
	if len(normalized) == 0 {
		return "-"
	}

	sort.Strings(normalized)
	return strings.Join(normalized, ",")
}

func checkCache(ctx context.Context, rdb redis.UniversalClient, key string) (*SearchResult, bool) {
	if rdb == nil {
		return nil, false
	}

	val, err := rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, false
	}

	var res SearchResult
	if err := json.Unmarshal([]byte(val), &res); err != nil {
		return nil, false
	}
	res.Cached = true
	return &res, true
}

func setCache(ctx context.Context, rdb redis.UniversalClient, key string, result SearchResult) {
	if rdb == nil {
		return
	}

	// Don't cache empty results
	if len(result.Papers) == 0 {
		return
	}

	data, err := json.Marshal(result)
	if err == nil {
		// Cache for 24 hours
		rdb.Set(ctx, key, string(data), 24*time.Hour)
	}
}
