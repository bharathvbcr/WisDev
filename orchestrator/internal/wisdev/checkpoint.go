package wisdev

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type CheckpointStore interface {
	Save(ctx context.Context, sessionID string, payload []byte, ttl time.Duration) error
	Load(ctx context.Context, sessionID string) ([]byte, error)
}

type RedisCheckpointStore struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisCheckpointStore(client redis.UniversalClient) *RedisCheckpointStore {
	return &RedisCheckpointStore{
		client: client,
		prefix: "wisdev_checkpoint:",
	}
}

func (s *RedisCheckpointStore) key(sessionID string) string {
	return s.prefix + sessionID
}

func (s *RedisCheckpointStore) Save(ctx context.Context, sessionID string, payload []byte, ttl time.Duration) error {
	if s.client == nil {
		return errors.New("redis_not_available")
	}
	return s.client.Set(ctx, s.key(sessionID), payload, ttl).Err()
}

func (s *RedisCheckpointStore) Load(ctx context.Context, sessionID string) ([]byte, error) {
	if s.client == nil {
		return nil, errors.New("redis_not_available")
	}
	val, err := s.client.Get(ctx, s.key(sessionID)).Bytes()
	if err != nil {
		return nil, errors.New("checkpoint_not_found")
	}
	return val, nil
}

type FallbackCheckpointStore struct {
	primary  CheckpointStore
	fallback CheckpointStore
}

func NewFallbackCheckpointStore(primary CheckpointStore, fallback CheckpointStore) *FallbackCheckpointStore {
	return &FallbackCheckpointStore{
		primary:  primary,
		fallback: fallback,
	}
}

func (s *FallbackCheckpointStore) Save(ctx context.Context, sessionID string, payload []byte, ttl time.Duration) error {
	if err := s.primary.Save(ctx, sessionID, payload, ttl); err != nil {
		return s.fallback.Save(ctx, sessionID, payload, ttl)
	}
	_ = s.fallback.Save(ctx, sessionID, payload, ttl)
	return nil
}

func (s *FallbackCheckpointStore) Load(ctx context.Context, sessionID string) ([]byte, error) {
	val, err := s.primary.Load(ctx, sessionID)
	if err == nil {
		return val, nil
	}
	return s.fallback.Load(ctx, sessionID)
}

type inMemoryCheckpointRecord struct {
	payload   []byte
	expiresAt time.Time
}

type InMemoryCheckpointStore struct {
	mu      sync.RWMutex
	records map[string]inMemoryCheckpointRecord
}

func NewInMemoryCheckpointStore() *InMemoryCheckpointStore {
	return &InMemoryCheckpointStore{
		records: make(map[string]inMemoryCheckpointRecord),
	}
}

func (s *InMemoryCheckpointStore) Save(_ context.Context, sessionID string, payload []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry := time.Now().Add(ttl)
	copyPayload := append([]byte(nil), payload...)
	s.records[sessionID] = inMemoryCheckpointRecord{
		payload:   copyPayload,
		expiresAt: expiry,
	}
	return nil
}

func (s *InMemoryCheckpointStore) Load(_ context.Context, sessionID string) ([]byte, error) {
	s.mu.RLock()
	record, ok := s.records[sessionID]
	s.mu.RUnlock()
	if !ok {
		return nil, errors.New("checkpoint_not_found")
	}
	if time.Now().After(record.expiresAt) {
		s.mu.Lock()
		delete(s.records, sessionID)
		s.mu.Unlock()
		return nil, errors.New("checkpoint_expired")
	}
	return append([]byte(nil), record.payload...), nil
}

func SaveSessionCheckpoint(ctx context.Context, store CheckpointStore, session *AgentSession, ttl time.Duration) error {
	payload, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return store.Save(ctx, session.SessionID, payload, ttl)
}

func LoadSessionCheckpoint(ctx context.Context, store CheckpointStore, sessionID string) (*AgentSession, error) {
	payload, err := store.Load(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var session AgentSession
	if err := json.Unmarshal(payload, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func NewTraceID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "trace_fallback"
	}
	return "trace_" + hex.EncodeToString(buf)
}
