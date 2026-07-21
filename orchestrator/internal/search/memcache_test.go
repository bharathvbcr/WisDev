package search

import (
	"fmt"
	"testing"
	"time"
)

func TestMemoryTTLCacheGetSet(t *testing.T) {
	cache := NewMemoryTTLCache(time.Minute, 4)

	if _, ok := cache.Get("missing"); ok {
		t.Fatal("expected miss on empty cache")
	}

	cache.Set("k1", []byte(`{"a":1}`))
	payload, ok := cache.Get("k1")
	if !ok || string(payload) != `{"a":1}` {
		t.Fatalf("expected hit with stored payload, got ok=%v payload=%q", ok, payload)
	}
}

func TestMemoryTTLCacheIgnoresEmptyKeyAndPayload(t *testing.T) {
	cache := NewMemoryTTLCache(time.Minute, 4)

	cache.Set("", []byte("x"))
	cache.Set("k", nil)
	cache.Set("k", []byte{})

	if cache.Len() != 0 {
		t.Fatalf("expected no entries, got %d", cache.Len())
	}
}

func TestMemoryTTLCacheExpiry(t *testing.T) {
	cache := NewMemoryTTLCache(time.Minute, 4)
	current := time.Unix(1_700_000_000, 0)
	cache.now = func() time.Time { return current }

	cache.Set("k1", []byte("v1"))
	if _, ok := cache.Get("k1"); !ok {
		t.Fatal("expected hit before expiry")
	}

	current = current.Add(2 * time.Minute)
	if _, ok := cache.Get("k1"); ok {
		t.Fatal("expected miss after expiry")
	}
	if cache.Len() != 0 {
		t.Fatalf("expected expired entry to be removed, got %d entries", cache.Len())
	}
}

func TestMemoryTTLCacheEvictsAtCapacity(t *testing.T) {
	cache := NewMemoryTTLCache(time.Minute, 3)
	current := time.Unix(1_700_000_000, 0)
	cache.now = func() time.Time { return current }

	for i := 0; i < 3; i++ {
		cache.Set(fmt.Sprintf("k%d", i), []byte("v"))
		current = current.Add(time.Second)
	}
	if cache.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", cache.Len())
	}

	// Inserting a fourth entry evicts the one closest to expiry (k0).
	cache.Set("k3", []byte("v"))
	if cache.Len() != 3 {
		t.Fatalf("expected capacity to hold at 3, got %d", cache.Len())
	}
	if _, ok := cache.Get("k0"); ok {
		t.Fatal("expected oldest entry k0 to be evicted")
	}
	if _, ok := cache.Get("k3"); !ok {
		t.Fatal("expected newest entry k3 to be present")
	}
}

func TestMemoryTTLCacheOverwriteDoesNotEvict(t *testing.T) {
	cache := NewMemoryTTLCache(time.Minute, 2)
	cache.Set("k1", []byte("v1"))
	cache.Set("k2", []byte("v2"))

	cache.Set("k1", []byte("v1-updated"))
	if cache.Len() != 2 {
		t.Fatalf("expected overwrite to keep 2 entries, got %d", cache.Len())
	}
	payload, ok := cache.Get("k1")
	if !ok || string(payload) != "v1-updated" {
		t.Fatalf("expected updated payload, got ok=%v payload=%q", ok, payload)
	}
	if _, ok := cache.Get("k2"); !ok {
		t.Fatal("expected k2 to survive overwrite of k1")
	}
}

func TestMemoryTTLCacheFlush(t *testing.T) {
	cache := NewMemoryTTLCache(time.Minute, 4)
	cache.Set("k1", []byte("v1"))
	cache.Flush()
	if cache.Len() != 0 {
		t.Fatalf("expected flush to clear entries, got %d", cache.Len())
	}
}

func TestMemoryTTLCacheDefaults(t *testing.T) {
	cache := NewMemoryTTLCache(0, 0)
	if cache.ttl != defaultMemoryCacheTTL {
		t.Fatalf("expected default TTL %v, got %v", defaultMemoryCacheTTL, cache.ttl)
	}
	if cache.maxEntries != defaultMemoryCacheMaxEntries {
		t.Fatalf("expected default max entries %d, got %d", defaultMemoryCacheMaxEntries, cache.maxEntries)
	}
}
