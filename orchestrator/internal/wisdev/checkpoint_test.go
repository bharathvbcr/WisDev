package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-redis/redismock/v9"
	"github.com/stretchr/testify/assert"
)

func TestRedisCheckpointStore(t *testing.T) {
	db, mock := redismock.NewClientMock()
	store := NewRedisCheckpointStore(db)
	ctx := context.Background()
	session := &AgentSession{SessionID: "s1"}
	data, _ := json.Marshal(session)

	t.Run("Save Success", func(t *testing.T) {
		mock.ExpectSet("wisdev_checkpoint:s1", data, time.Hour).SetVal("OK")
		err := store.Save(ctx, "s1", data, time.Hour)
		assert.NoError(t, err)
	})

	t.Run("Save Failure", func(t *testing.T) {
		mock.ExpectSet("wisdev_checkpoint:s1", data, time.Hour).SetErr(errors.New("redis down"))
		err := store.Save(ctx, "s1", data, time.Hour)
		assert.Error(t, err)
	})

	t.Run("Load Success", func(t *testing.T) {
		mock.ExpectGet("wisdev_checkpoint:s1").SetVal(string(data))
		loaded, err := store.Load(ctx, "s1")
		assert.NoError(t, err)
		assert.Equal(t, data, loaded)
	})

	t.Run("Load NotFound", func(t *testing.T) {
		mock.ExpectGet("wisdev_checkpoint:s2").RedisNil()
		_, err := store.Load(ctx, "s2")
		assert.Error(t, err)
		assert.Equal(t, "checkpoint_not_found", err.Error())
	})
}

func TestCheckpointHelpers(t *testing.T) {
	store := NewInMemoryCheckpointStore()
	session := &AgentSession{SessionID: "s1"}

	err := SaveSessionCheckpoint(context.Background(), store, session, time.Hour)
	assert.NoError(t, err)

	loaded, err := LoadSessionCheckpoint(context.Background(), store, "s1")
	assert.NoError(t, err)
	assert.Equal(t, "s1", loaded.SessionID)

	tid := NewTraceID()
	assert.NotEmpty(t, tid)
}

func TestFallbackCheckpointStore(t *testing.T) {
	primary := NewInMemoryCheckpointStore()
	secondary := NewInMemoryCheckpointStore()
	store := &FallbackCheckpointStore{
		primary:  primary,
		fallback: secondary,
	}
	ctx := context.Background()
	data := []byte("payload")

	// Save should go to primary
	err := store.Save(ctx, "s1", data, time.Hour)
	assert.NoError(t, err)

	val, _ := primary.Load(ctx, "s1")
	assert.Equal(t, data, val)

	// Load from primary
	val, err = store.Load(ctx, "s1")
	assert.NoError(t, err)
	assert.Equal(t, data, val)

	// Load from fallback
	secondary.Save(ctx, "s2", []byte("fallback_val"), time.Hour)
	val, err = store.Load(ctx, "s2")
	assert.NoError(t, err)
	assert.Equal(t, []byte("fallback_val"), val)
}
