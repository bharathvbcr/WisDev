package search

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/go-redis/redismock/v9"
	"github.com/stretchr/testify/assert"
)

func TestSearchCache(t *testing.T) {
	db, mock := redismock.NewClientMock()
	ctx := context.Background()

	query := "test query"
	opts := SearchOpts{Limit: 10, Domain: "science"}
	key := getCacheKey(query, opts)

	result := SearchResult{
		Papers: []Paper{{Title: "P1"}},
	}

	t.Run("setCache", func(t *testing.T) {
		data, _ := json.Marshal(result)
		mock.ExpectSet(key, string(data), 24*time.Hour).SetVal("OK")
		setCache(ctx, NewMemoryTTLCache(0, 0), db, key, result)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("checkCache hit from redis populates memory", func(t *testing.T) {
		mem := NewMemoryTTLCache(0, 0)
		data, _ := json.Marshal(result)
		mock.ExpectGet(key).SetVal(string(data))

		cached, ok := checkCache(ctx, mem, db, key)
		assert.True(t, ok)
		assert.Equal(t, "P1", cached.Papers[0].Title)
		assert.True(t, cached.Cached)
		assert.NoError(t, mock.ExpectationsWereMet())

		// Second lookup is served from process memory — no further Redis GET
		// is expected on the mock.
		cachedAgain, okAgain := checkCache(ctx, mem, db, key)
		assert.True(t, okAgain)
		assert.Equal(t, "P1", cachedAgain.Papers[0].Title)
		assert.True(t, cachedAgain.Cached)
	})

	t.Run("checkCache miss", func(t *testing.T) {
		mock.ExpectGet(key).RedisNil()
		cached, ok := checkCache(ctx, NewMemoryTTLCache(0, 0), db, key)
		assert.False(t, ok)
		assert.Nil(t, cached)
	})

	t.Run("setCache serves later reads without redis", func(t *testing.T) {
		mem := NewMemoryTTLCache(0, 0)
		setCache(ctx, mem, nil, key, result)

		cached, ok := checkCache(ctx, mem, nil, key)
		assert.True(t, ok)
		assert.Equal(t, "P1", cached.Papers[0].Title)
		assert.True(t, cached.Cached)
	})
}

func TestGetCacheKeyNormalizesQueryAndDomain(t *testing.T) {
	opts := SearchOpts{Limit: 10, Domain: "  Science  ", Sources: []string{"OpenAlex"}}
	keyA := getCacheKey("  Adaptive   Graph of Thoughts  ", opts)
	keyB := getCacheKey("adaptive graph OF thoughts", SearchOpts{Limit: 10, Domain: "Science", Sources: []string{"openalex"}})

	if keyA != keyB {
		t.Fatalf("expected equivalent cache keys, got %q and %q", keyA, keyB)
	}
	if keyA != "search:adaptive graph of thoughts:d:science:l:10:yf:0:yt:0:s:openalex" {
		t.Fatalf("unexpected normalized cache key: %s", keyA)
	}
}

func TestNormalizeSourceCacheKey(t *testing.T) {
	t.Run("empty_sources", func(t *testing.T) {
		if got := normalizeSourceCacheKey(nil); got != "-" {
			t.Fatalf("expected '-' for empty sources, got %q", got)
		}
	})

	t.Run("dedupes_and_sorts", func(t *testing.T) {
		got := normalizeSourceCacheKey([]string{"  CORE  ", "google_scholar", "core", "", "Google_Scholar"})
		if got != "core,google_scholar" {
			t.Fatalf("expected canonical sorted key, got %q", got)
		}
	})

	t.Run("all_blank_after_normalization", func(t *testing.T) {
		if got := normalizeSourceCacheKey([]string{"", "   "}); got != "-" {
			t.Fatalf("expected '-' when all sources normalize away, got %q", got)
		}
	})
}

func TestCheckCache_Errors(t *testing.T) {
	ctx := context.Background()
	key := "search:test"

	t.Run("nil_client", func(t *testing.T) {
		cached, ok := checkCache(ctx, NewMemoryTTLCache(0, 0), nil, key)
		if ok || cached != nil {
			t.Fatalf("expected nil client to miss cache, got ok=%v cached=%v", ok, cached)
		}
	})

	t.Run("nil_memory_cache_degrades_to_redis_path", func(t *testing.T) {
		db, mock := redismock.NewClientMock()
		mock.ExpectGet(key).RedisNil()

		cached, ok := checkCache(ctx, nil, db, key)
		if ok || cached != nil {
			t.Fatalf("expected nil memory cache to fall through to redis miss, got ok=%v cached=%v", ok, cached)
		}
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("redis_error", func(t *testing.T) {
		db, mock := redismock.NewClientMock()
		mock.ExpectGet(key).SetErr(errors.New("boom"))

		cached, ok := checkCache(ctx, NewMemoryTTLCache(0, 0), db, key)
		if ok || cached != nil {
			t.Fatalf("expected redis error to miss cache, got ok=%v cached=%v", ok, cached)
		}
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("invalid_json", func(t *testing.T) {
		db, mock := redismock.NewClientMock()
		mock.ExpectGet(key).SetVal("{not-json}")

		cached, ok := checkCache(ctx, NewMemoryTTLCache(0, 0), db, key)
		if ok || cached != nil {
			t.Fatalf("expected invalid json to miss cache, got ok=%v cached=%v", ok, cached)
		}
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSetCache_NoopPaths(t *testing.T) {
	ctx := context.Background()
	key := "search:test"

	t.Run("nil_client_still_populates_memory", func(t *testing.T) {
		mem := NewMemoryTTLCache(0, 0)
		setCache(ctx, mem, nil, key, SearchResult{Papers: []Paper{{Title: "P1"}}})
		cached, ok := checkCache(ctx, mem, nil, key)
		if !ok || cached == nil || cached.Papers[0].Title != "P1" {
			t.Fatalf("expected memory-backed hit after nil-client set, got ok=%v cached=%v", ok, cached)
		}
	})

	t.Run("empty_result", func(t *testing.T) {
		mem := NewMemoryTTLCache(0, 0)
		db, mock := redismock.NewClientMock()
		setCache(ctx, mem, db, key, SearchResult{})
		assert.NoError(t, mock.ExpectationsWereMet())
		if _, ok := checkCache(ctx, mem, nil, key); ok {
			t.Fatal("expected empty result to be excluded from memory cache")
		}
	})

	t.Run("marshal_error", func(t *testing.T) {
		mem := NewMemoryTTLCache(0, 0)
		db, mock := redismock.NewClientMock()
		setCache(ctx, mem, db, key, SearchResult{
			Papers: []Paper{{ID: "bad", Score: math.NaN()}},
		})
		assert.NoError(t, mock.ExpectationsWereMet())
		if _, ok := checkCache(ctx, mem, nil, key); ok {
			t.Fatal("expected marshal failure to leave memory cache empty")
		}
	})
}
