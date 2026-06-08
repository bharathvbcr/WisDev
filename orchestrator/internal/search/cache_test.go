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
		setCache(ctx, db, key, result)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("checkCache hit", func(t *testing.T) {
		data, _ := json.Marshal(result)
		mock.ExpectGet(key).SetVal(string(data))

		cached, ok := checkCache(ctx, db, key)
		assert.True(t, ok)
		assert.Equal(t, "P1", cached.Papers[0].Title)
		assert.True(t, cached.Cached)
	})

	t.Run("checkCache miss", func(t *testing.T) {
		mock.ExpectGet(key).RedisNil()
		cached, ok := checkCache(ctx, db, key)
		assert.False(t, ok)
		assert.Nil(t, cached)
	})
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
		cached, ok := checkCache(ctx, nil, key)
		if ok || cached != nil {
			t.Fatalf("expected nil client to miss cache, got ok=%v cached=%v", ok, cached)
		}
	})

	t.Run("redis_error", func(t *testing.T) {
		db, mock := redismock.NewClientMock()
		mock.ExpectGet(key).SetErr(errors.New("boom"))

		cached, ok := checkCache(ctx, db, key)
		if ok || cached != nil {
			t.Fatalf("expected redis error to miss cache, got ok=%v cached=%v", ok, cached)
		}
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("invalid_json", func(t *testing.T) {
		db, mock := redismock.NewClientMock()
		mock.ExpectGet(key).SetVal("{not-json}")

		cached, ok := checkCache(ctx, db, key)
		if ok || cached != nil {
			t.Fatalf("expected invalid json to miss cache, got ok=%v cached=%v", ok, cached)
		}
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSetCache_NoopPaths(t *testing.T) {
	ctx := context.Background()
	key := "search:test"

	t.Run("nil_client", func(t *testing.T) {
		setCache(ctx, nil, key, SearchResult{Papers: []Paper{{Title: "P1"}}})
	})

	t.Run("empty_result", func(t *testing.T) {
		db, mock := redismock.NewClientMock()
		setCache(ctx, db, key, SearchResult{})
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("marshal_error", func(t *testing.T) {
		db, mock := redismock.NewClientMock()
		setCache(ctx, db, key, SearchResult{
			Papers: []Paper{{ID: "bad", Score: math.NaN()}},
		})
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}
