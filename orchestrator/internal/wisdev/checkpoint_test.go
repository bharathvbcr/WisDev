package wisdev

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type mockCheckpointStore struct {
	saveFn func(string, []byte) error
	loadFn func(string) ([]byte, error)
}

func (m *mockCheckpointStore) Save(ctx context.Context, id string, payload []byte, ttl time.Duration) error {
	return m.saveFn(id, payload)
}
func (m *mockCheckpointStore) Load(ctx context.Context, id string) ([]byte, error) {
	return m.loadFn(id)
}

func TestCheckpointStores(t *testing.T) {
	ctx := context.Background()
	payload := []byte("test payload")

	t.Run("InMemoryCheckpointStore", func(t *testing.T) {
		s := NewInMemoryCheckpointStore()
		err := s.Save(ctx, "s1", payload, 1*time.Hour)
		assert.NoError(t, err)

		got, err := s.Load(ctx, "s1")
		assert.NoError(t, err)
		assert.Equal(t, payload, got)

		_, err = s.Load(ctx, "nosession")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "checkpoint_not_found")
	})

	t.Run("InMemoryCheckpointStore - Expiry", func(t *testing.T) {
		s := NewInMemoryCheckpointStore()
		err := s.Save(ctx, "s1", payload, -1*time.Second) // already expired
		assert.NoError(t, err)

		_, err = s.Load(ctx, "s1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "checkpoint_expired")
	})

	t.Run("RedisCheckpointStore - No Redis", func(t *testing.T) {
		s := NewRedisCheckpointStore(nil)
		assert.Equal(t, "wisdev_checkpoint:s1", s.key("s1"))
		// rdb is nil by default in tests unless initialized
		err := s.Save(ctx, "s1", payload, 1*time.Hour)
		assert.Error(t, err)
		assert.Equal(t, "redis_not_available", err.Error())

		got, err := s.Load(ctx, "s1")
		assert.Nil(t, got)
		assert.Error(t, err)
		assert.Equal(t, "redis_not_available", err.Error())
	})

	t.Run("FallbackCheckpointStore", func(t *testing.T) {
		primary := &mockCheckpointStore{
			saveFn: func(id string, p []byte) error { return errors.New("primary fail") },
			loadFn: func(id string) ([]byte, error) { return nil, errors.New("primary fail") },
		}
		fallback := &mockCheckpointStore{
			saveFn: func(id string, p []byte) error { return nil },
			loadFn: func(id string) ([]byte, error) { return payload, nil },
		}
		s := NewFallbackCheckpointStore(primary, fallback)

		err := s.Save(ctx, "s1", payload, 1*time.Hour)
		assert.NoError(t, err)

		got, err := s.Load(ctx, "s1")
		assert.NoError(t, err)
		assert.Equal(t, payload, got)
	})

	t.Run("FallbackCheckpointStore - Primary Success", func(t *testing.T) {
		primary := &mockCheckpointStore{
			saveFn: func(id string, p []byte) error { return nil },
			loadFn: func(id string) ([]byte, error) { return payload, nil },
		}
		fallback := &mockCheckpointStore{
			saveFn: func(id string, p []byte) error { return nil },
		}
		s := NewFallbackCheckpointStore(primary, fallback)

		err := s.Save(ctx, "s1", payload, 1*time.Hour)
		assert.NoError(t, err)

		got, err := s.Load(ctx, "s1")
		assert.NoError(t, err)
		assert.Equal(t, payload, got)
	})
}

func TestSessionCheckpointHelpers(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryCheckpointStore()
	session := &AgentSession{SessionID: "s1", UserID: "u1"}

	err := SaveSessionCheckpoint(ctx, store, session, 1*time.Hour)
	assert.NoError(t, err)

	loaded, err := LoadSessionCheckpoint(ctx, store, "s1")
	assert.NoError(t, err)
	assert.Equal(t, session.UserID, loaded.UserID)

	_, err = LoadSessionCheckpoint(ctx, store, "nosession")
	assert.Error(t, err)
}

func TestSessionCheckpointHelpersErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("Load Unmarshal Error", func(t *testing.T) {
		store := &mockCheckpointStore{
			loadFn: func(id string) ([]byte, error) {
				return []byte("invalid json"), nil
			},
		}
		_, err := LoadSessionCheckpoint(ctx, store, "s1")
		assert.Error(t, err)
	})
}

func TestNewTraceID(t *testing.T) {
	id := NewTraceID()
	assert.NotEmpty(t, id)
	assert.True(t, len(id) > 6)
}
