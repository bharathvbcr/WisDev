package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	redismock "github.com/go-redis/redismock/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisMemoryStore_SaveWorkingMemorySkipsEmptyEntries(t *testing.T) {
	client, redisMock := redismock.NewClientMock()
	store := NewRedisMemoryStore(client)

	err := store.SaveWorkingMemory(context.Background(), "session-empty", nil, 0)
	require.NoError(t, err)
	assert.NoError(t, redisMock.ExpectationsWereMet())
}

func TestRedisMemoryStore_AppendLongTermVectorMergesEntriesAndDefaultsTTL(t *testing.T) {
	client, redisMock := redismock.NewClientMock()
	store := NewRedisMemoryStore(client)

	existing := []MemoryEntry{
		{ID: "keep", Type: "finding", Content: "existing finding", CreatedAt: 10},
		{ID: "dup", Type: "finding", Content: "original content", CreatedAt: 20},
	}
	newEntries := []MemoryEntry{
		{ID: "dup", Type: "finding", Content: "replacement should be ignored", CreatedAt: 30},
		{ID: "new", Type: "finding", Content: "new finding", CreatedAt: 40},
	}

	existingJSON, err := json.Marshal(existing)
	require.NoError(t, err)
	expectedMergedJSON, err := json.Marshal([]MemoryEntry{
		{ID: "keep", Type: "finding", Content: "existing finding", CreatedAt: 10},
		{ID: "dup", Type: "finding", Content: "original content", CreatedAt: 20},
		{ID: "new", Type: "finding", Content: "new finding", CreatedAt: 40},
	})
	require.NoError(t, err)

	redisMock.ExpectGet(ltmKey("user-1")).SetVal(string(existingJSON))
	redisMock.ExpectSet(ltmKey("user-1"), expectedMergedJSON, DefaultLongTermTTL).SetVal("OK")

	err = store.AppendLongTermVector(context.Background(), "user-1", newEntries, 0)
	require.NoError(t, err)
	assert.NoError(t, redisMock.ExpectationsWereMet())
}

func TestRedisMemoryStore_AppendLongTermVectorStartsFreshOnLoadError(t *testing.T) {
	client, redisMock := redismock.NewClientMock()
	store := NewRedisMemoryStore(client)

	entries := []MemoryEntry{{ID: "fresh", Type: "finding", Content: "fresh only", CreatedAt: 55}}
	entriesJSON, err := json.Marshal(entries)
	require.NoError(t, err)

	redisMock.ExpectGet(ltmKey("user-2")).SetErr(errors.New("redis unavailable"))
	redisMock.ExpectSet(ltmKey("user-2"), entriesJSON, DefaultLongTermTTL).SetVal("OK")

	err = store.AppendLongTermVector(context.Background(), "user-2", entries, 0)
	require.NoError(t, err)
	assert.NoError(t, redisMock.ExpectationsWereMet())
}

func TestRedisMemoryStore_SaveTiersUsesDefaultTTLsAndLoadTiersReturnsPartialData(t *testing.T) {
	client, redisMock := redismock.NewClientMock()
	store := NewRedisMemoryStore(client)

	tiers := &MemoryTierState{
		ShortTermWorking: []MemoryEntry{{ID: "w1", Type: "working", Content: "working memory", CreatedAt: 1}},
		LongTermVector:   []MemoryEntry{{ID: "l1", Type: "ltm", Content: "long term memory", CreatedAt: 2}},
		ArtifactMemory:   []MemoryEntry{{ID: "a1", Type: "artifact", Content: "artifact memory", CreatedAt: 3}},
		UserPersonalized: []MemoryEntry{{ID: "p1", Type: "pref", Content: "preference", CreatedAt: 4}},
	}

	workingJSON, err := json.Marshal(tiers.ShortTermWorking)
	require.NoError(t, err)
	ltmJSON, err := json.Marshal(tiers.LongTermVector)
	require.NoError(t, err)
	artifactJSON, err := json.Marshal(tiers.ArtifactMemory)
	require.NoError(t, err)
	prefsJSON, err := json.Marshal(tiers.UserPersonalized)
	require.NoError(t, err)

	redisMock.ExpectSet(workingKey("session-1"), workingJSON, DefaultWorkingTTL).SetVal("OK")
	redisMock.ExpectSet(ltmKey("user-1"), ltmJSON, DefaultLongTermTTL).SetVal("OK")
	redisMock.ExpectSet(artifactKey("session-1"), artifactJSON, time.Duration(0)).SetVal("OK")
	redisMock.ExpectSet(prefsKey("user-1"), prefsJSON, time.Duration(0)).SetVal("OK")

	err = store.SaveTiers(context.Background(), "session-1", "user-1", tiers, 0, 0)
	require.NoError(t, err)

	redisMock.ExpectGet(workingKey("session-1")).SetVal(string(workingJSON))
	redisMock.ExpectGet(ltmKey("user-1")).SetVal("{broken json")
	redisMock.ExpectGet(artifactKey("session-1")).SetVal(string(artifactJSON))
	redisMock.ExpectGet(prefsKey("user-1")).RedisNil()

	loaded, err := store.LoadTiers(context.Background(), "session-1", "user-1")
	require.Error(t, err)
	require.NotNil(t, loaded)
	assert.Contains(t, err.Error(), "partial load error")
	assert.Equal(t, tiers.ShortTermWorking, loaded.ShortTermWorking)
	assert.Equal(t, tiers.ArtifactMemory, loaded.ArtifactMemory)
	assert.Nil(t, loaded.UserPersonalized)
	assert.NoError(t, redisMock.ExpectationsWereMet())
}

func TestNewMemoryStoreFromRedisSelectsExpectedImplementation(t *testing.T) {
	store := NewMemoryStoreFromRedis(nil)
	assert.IsType(t, &NoopMemoryStore{}, store)

	client, _ := redismock.NewClientMock()
	store = NewMemoryStoreFromRedis(client)
	assert.IsType(t, &RedisMemoryStore{}, store)
}
