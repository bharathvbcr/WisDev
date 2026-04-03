package search

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

func getCacheKey(query string, opts SearchOpts) string {
	return fmt.Sprintf("search:%s:d:%s:l:%d:yf:%d:yt:%d",
		query, opts.Domain, opts.Limit, opts.YearFrom, opts.YearTo)
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
