package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// MemoryStore is the persistence contract for all WisDev memory tiers.
// The four tiers map directly to MemoryTierState:
//
//   - Working memory  → per-session, short TTL (session lifetime)
//   - Long-term vector → per-user, longer TTL (configurable)
//   - Artifact memory  → per-session, persisted until explicit eviction
//   - User preferences → per-user, no TTL
type MemoryStore interface {
	// SaveWorkingMemory persists the short-term scratchpad for a session.
	SaveWorkingMemory(ctx context.Context, sessionID string, entries []MemoryEntry, ttl time.Duration) error
	// LoadWorkingMemory retrieves the scratchpad; returns nil if not found.
	LoadWorkingMemory(ctx context.Context, sessionID string) ([]MemoryEntry, error)

	// SaveLongTermVector persists high-confidence evidence for a user across sessions.
	SaveLongTermVector(ctx context.Context, userID string, entries []MemoryEntry, ttl time.Duration) error
	// LoadLongTermVector retrieves all long-term entries for a user.
	LoadLongTermVector(ctx context.Context, userID string) ([]MemoryEntry, error)
	// AppendLongTermVector appends new entries to an existing user LTM store,
	// deduplicating by MemoryEntry.ID before writing.
	AppendLongTermVector(ctx context.Context, userID string, entries []MemoryEntry, ttl time.Duration) error

	// SaveArtifacts persists artifact memory (verified claims, plan summaries) for a session.
	SaveArtifacts(ctx context.Context, sessionID string, entries []MemoryEntry) error
	// LoadArtifacts retrieves artifact memory for a session.
	LoadArtifacts(ctx context.Context, sessionID string) ([]MemoryEntry, error)

	// SaveUserPreferences persists stable user preferences (no TTL).
	SaveUserPreferences(ctx context.Context, userID string, entries []MemoryEntry) error
	// LoadUserPreferences retrieves user preferences.
	LoadUserPreferences(ctx context.Context, userID string) ([]MemoryEntry, error)

	// SaveTiers writes all four tiers from a MemoryTierState in a single call.
	SaveTiers(ctx context.Context, sessionID, userID string, tiers *MemoryTierState, workingTTL, ltmTTL time.Duration) error
	// LoadTiers reads all four tiers and returns a populated MemoryTierState.
	LoadTiers(ctx context.Context, sessionID, userID string) (*MemoryTierState, error)
}

// DefaultWorkingTTL is the default TTL for per-session working memory.
const DefaultWorkingTTL = 4 * time.Hour

// DefaultLongTermTTL is the default TTL for user-scoped long-term vector memory.
const DefaultLongTermTTL = 30 * 24 * time.Hour

// RedisMemoryStore implements MemoryStore using Redis.
// Keys follow the schema:
//
//	wisdev:mem:working:{sessionID}    → JSON []MemoryEntry
//	wisdev:mem:ltm:{userID}           → JSON []MemoryEntry
//	wisdev:mem:artifact:{sessionID}   → JSON []MemoryEntry
//	wisdev:mem:prefs:{userID}         → JSON []MemoryEntry
type RedisMemoryStore struct {
	client redis.UniversalClient
}

// NewRedisMemoryStore constructs a store backed by the given Redis client.
func NewRedisMemoryStore(client redis.UniversalClient) *RedisMemoryStore {
	return &RedisMemoryStore{client: client}
}

// ---- key helpers -----------------------------------------------------------

func workingKey(sessionID string) string {
	return fmt.Sprintf("wisdev:mem:working:%s", sessionID)
}

func ltmKey(userID string) string {
	return fmt.Sprintf("wisdev:mem:ltm:%s", userID)
}

func artifactKey(sessionID string) string {
	return fmt.Sprintf("wisdev:mem:artifact:%s", sessionID)
}

func prefsKey(userID string) string {
	return fmt.Sprintf("wisdev:mem:prefs:%s", userID)
}

// ---- working memory --------------------------------------------------------

func (s *RedisMemoryStore) SaveWorkingMemory(ctx context.Context, sessionID string, entries []MemoryEntry, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultWorkingTTL
	}
	return s.setEntries(ctx, workingKey(sessionID), entries, ttl)
}

func (s *RedisMemoryStore) LoadWorkingMemory(ctx context.Context, sessionID string) ([]MemoryEntry, error) {
	return s.getEntries(ctx, workingKey(sessionID))
}

// ---- long-term vector memory -----------------------------------------------

func (s *RedisMemoryStore) SaveLongTermVector(ctx context.Context, userID string, entries []MemoryEntry, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultLongTermTTL
	}
	return s.setEntries(ctx, ltmKey(userID), entries, ttl)
}

func (s *RedisMemoryStore) LoadLongTermVector(ctx context.Context, userID string) ([]MemoryEntry, error) {
	return s.getEntries(ctx, ltmKey(userID))
}

// AppendLongTermVector merges new entries into existing LTM, deduplicating by ID.
func (s *RedisMemoryStore) AppendLongTermVector(ctx context.Context, userID string, entries []MemoryEntry, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultLongTermTTL
	}
	key := ltmKey(userID)
	existing, err := s.getEntries(ctx, key)
	if err != nil {
		slog.Warn("AppendLongTermVector: failed to load existing entries, starting fresh", "error", err)
		existing = nil
	}
	merged := mergeEntries(existing, entries)
	return s.setEntries(ctx, key, merged, ttl)
}

// ---- artifact memory -------------------------------------------------------

func (s *RedisMemoryStore) SaveArtifacts(ctx context.Context, sessionID string, entries []MemoryEntry) error {
	// Artifacts have no TTL — they persist until the session is explicitly deleted
	// or a manual eviction is run.
	return s.setEntries(ctx, artifactKey(sessionID), entries, 0)
}

func (s *RedisMemoryStore) LoadArtifacts(ctx context.Context, sessionID string) ([]MemoryEntry, error) {
	return s.getEntries(ctx, artifactKey(sessionID))
}

// ---- user preferences ------------------------------------------------------

func (s *RedisMemoryStore) SaveUserPreferences(ctx context.Context, userID string, entries []MemoryEntry) error {
	return s.setEntries(ctx, prefsKey(userID), entries, 0)
}

func (s *RedisMemoryStore) LoadUserPreferences(ctx context.Context, userID string) ([]MemoryEntry, error) {
	return s.getEntries(ctx, prefsKey(userID))
}

// ---- convenience tier I/O --------------------------------------------------

// SaveTiers writes all four memory tiers in a pipeline to reduce round-trips.
func (s *RedisMemoryStore) SaveTiers(
	ctx context.Context,
	sessionID, userID string,
	tiers *MemoryTierState,
	workingTTL, ltmTTL time.Duration,
) error {
	if tiers == nil {
		return nil
	}
	if workingTTL <= 0 {
		workingTTL = DefaultWorkingTTL
	}
	if ltmTTL <= 0 {
		ltmTTL = DefaultLongTermTTL
	}

	pipe := s.client.Pipeline()

	if err := s.pipeSetEntries(ctx, pipe, workingKey(sessionID), tiers.ShortTermWorking, workingTTL); err != nil {
		return fmt.Errorf("save working memory: %w", err)
	}
	if err := s.pipeSetEntries(ctx, pipe, ltmKey(userID), tiers.LongTermVector, ltmTTL); err != nil {
		return fmt.Errorf("save ltm: %w", err)
	}
	if err := s.pipeSetEntries(ctx, pipe, artifactKey(sessionID), tiers.ArtifactMemory, 0); err != nil {
		return fmt.Errorf("save artifacts: %w", err)
	}
	if err := s.pipeSetEntries(ctx, pipe, prefsKey(userID), tiers.UserPersonalized, 0); err != nil {
		return fmt.Errorf("save user prefs: %w", err)
	}

	_, err := pipe.Exec(ctx)
	return err
}

// LoadTiers reads all four tiers concurrently and returns a populated MemoryTierState.
func (s *RedisMemoryStore) LoadTiers(ctx context.Context, sessionID, userID string) (*MemoryTierState, error) {
	type result struct {
		entries []MemoryEntry
		err     error
	}

	keys := []string{
		workingKey(sessionID),
		ltmKey(userID),
		artifactKey(sessionID),
		prefsKey(userID),
	}

	results := make([]result, len(keys))
	// Fan-out with a simple pipeline GET rather than goroutines to stay simple.
	cmds := make([]*redis.StringCmd, len(keys))
	pipe := s.client.Pipeline()
	for i, k := range keys {
		cmds[i] = pipe.Get(ctx, k)
	}
	_, _ = pipe.Exec(ctx) // errors surfaced per-command below

	for i, cmd := range cmds {
		raw, err := cmd.Result()
		if err == redis.Nil {
			results[i] = result{entries: nil, err: nil}
			continue
		}
		if err != nil {
			results[i] = result{err: err}
			continue
		}
		var entries []MemoryEntry
		if jsonErr := json.Unmarshal([]byte(raw), &entries); jsonErr != nil {
			results[i] = result{err: jsonErr}
			continue
		}
		results[i] = result{entries: entries}
	}

	tiers := &MemoryTierState{
		ShortTermWorking: results[0].entries,
		LongTermVector:   results[1].entries,
		ArtifactMemory:   results[2].entries,
		UserPersonalized: results[3].entries,
	}
	// Return the first non-nil error as advisory; partial data is still usable.
	for _, r := range results {
		if r.err != nil {
			return tiers, fmt.Errorf("partial load error: %w", r.err)
		}
	}
	return tiers, nil
}

// ---- low-level Redis helpers -----------------------------------------------

func (s *RedisMemoryStore) setEntries(ctx context.Context, key string, entries []MemoryEntry, ttl time.Duration) error {
	if len(entries) == 0 {
		// Preserve the key's existing TTL; don't overwrite with an empty slice
		// unless the caller explicitly wants to clear it.
		return nil
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal entries for %s: %w", key, err)
	}
	return s.client.Set(ctx, key, data, ttl).Err()
}

func (s *RedisMemoryStore) getEntries(ctx context.Context, key string) ([]MemoryEntry, error) {
	raw, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	var entries []MemoryEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", key, err)
	}
	return entries, nil
}

// pipeSetEntries queues a SET command onto an existing pipeline.
func (s *RedisMemoryStore) pipeSetEntries(ctx context.Context, pipe redis.Pipeliner, key string, entries []MemoryEntry, ttl time.Duration) error {
	if len(entries) == 0 {
		return nil
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal for pipe %s: %w", key, err)
	}
	pipe.Set(ctx, key, data, ttl)
	return nil
}

// ---- helpers ----------------------------------------------------------------

// mergeEntries appends newEntries to existing, deduplicating by MemoryEntry.ID.
// Existing entries take precedence for duplicate IDs.
func mergeEntries(existing, newEntries []MemoryEntry) []MemoryEntry {
	seen := make(map[string]bool, len(existing))
	for _, e := range existing {
		seen[e.ID] = true
	}
	out := make([]MemoryEntry, len(existing), len(existing)+len(newEntries))
	copy(out, existing)
	for _, e := range newEntries {
		if !seen[e.ID] {
			out = append(out, e)
			seen[e.ID] = true
		}
	}
	return out
}

// NoopMemoryStore is a MemoryStore that silently discards all writes and
// returns empty slices on reads. Used in tests and when Redis is unavailable.
type NoopMemoryStore struct{}

func (n *NoopMemoryStore) SaveWorkingMemory(_ context.Context, _ string, _ []MemoryEntry, _ time.Duration) error {
	return nil
}
func (n *NoopMemoryStore) LoadWorkingMemory(_ context.Context, _ string) ([]MemoryEntry, error) {
	return nil, nil
}
func (n *NoopMemoryStore) SaveLongTermVector(_ context.Context, _ string, _ []MemoryEntry, _ time.Duration) error {
	return nil
}
func (n *NoopMemoryStore) LoadLongTermVector(_ context.Context, _ string) ([]MemoryEntry, error) {
	return nil, nil
}
func (n *NoopMemoryStore) AppendLongTermVector(_ context.Context, _ string, _ []MemoryEntry, _ time.Duration) error {
	return nil
}
func (n *NoopMemoryStore) SaveArtifacts(_ context.Context, _ string, _ []MemoryEntry) error {
	return nil
}
func (n *NoopMemoryStore) LoadArtifacts(_ context.Context, _ string) ([]MemoryEntry, error) {
	return nil, nil
}
func (n *NoopMemoryStore) SaveUserPreferences(_ context.Context, _ string, _ []MemoryEntry) error {
	return nil
}
func (n *NoopMemoryStore) LoadUserPreferences(_ context.Context, _ string) ([]MemoryEntry, error) {
	return nil, nil
}
func (n *NoopMemoryStore) SaveTiers(_ context.Context, _, _ string, _ *MemoryTierState, _, _ time.Duration) error {
	return nil
}
func (n *NoopMemoryStore) LoadTiers(_ context.Context, _, _ string) (*MemoryTierState, error) {
	return &MemoryTierState{}, nil
}

// NewMemoryStoreFromRedis returns a RedisMemoryStore if the client is non-nil,
// otherwise returns a NoopMemoryStore. Call this during gateway initialization.
func NewMemoryStoreFromRedis(client redis.UniversalClient) MemoryStore {
	if client == nil {
		slog.Warn("No Redis client available; using NoopMemoryStore for WisDev memory")
		return &NoopMemoryStore{}
	}
	return NewRedisMemoryStore(client)
}
