package search

import (
	"sync"
	"time"
)

// MemoryTTLCache is a bounded process-local cache that backstops the Redis
// search caches. Cloud deployments without UPSTASH_REDIS_URL otherwise run
// every repeat query through the full provider fan-out; this keeps repeat
// queries fast on each instance. Values are stored as serialized JSON so
// readers get the same copy isolation as the Redis path.
type MemoryTTLCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[string]memoryCacheEntry
	now        func() time.Time
}

type memoryCacheEntry struct {
	payload   []byte
	expiresAt time.Time
}

const (
	defaultMemoryCacheTTL        = 15 * time.Minute
	defaultMemoryCacheMaxEntries = 256
)

func NewMemoryTTLCache(ttl time.Duration, maxEntries int) *MemoryTTLCache {
	if ttl <= 0 {
		ttl = defaultMemoryCacheTTL
	}
	if maxEntries <= 0 {
		maxEntries = defaultMemoryCacheMaxEntries
	}
	return &MemoryTTLCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[string]memoryCacheEntry),
		now:        time.Now,
	}
}

func (c *MemoryTTLCache) Get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.now().After(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.payload, true
}

func (c *MemoryTTLCache) Set(key string, payload []byte) {
	if c == nil || key == "" || len(payload) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxEntries {
		c.evictLocked()
	}
	c.entries[key] = memoryCacheEntry{payload: payload, expiresAt: c.now().Add(c.ttl)}
}

// evictLocked drops expired entries first; if the cache is still full it
// drops the entries closest to expiry so fresh inserts always fit.
func (c *MemoryTTLCache) evictLocked() {
	now := c.now()
	for key, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
	for len(c.entries) >= c.maxEntries {
		oldestKey := ""
		var oldestExpiry time.Time
		for key, entry := range c.entries {
			if oldestKey == "" || entry.expiresAt.Before(oldestExpiry) {
				oldestKey = key
				oldestExpiry = entry.expiresAt
			}
		}
		delete(c.entries, oldestKey)
	}
}

// Flush clears all entries. Exposed so tests can isolate cache state.
func (c *MemoryTTLCache) Flush() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]memoryCacheEntry)
}

// Len reports the current entry count (test/diagnostic helper).
func (c *MemoryTTLCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
