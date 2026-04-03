package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var errSessionNotFound = errors.New("session_not_found")

type SessionStore interface {
	Get(ctx context.Context, sessionID string) (*AgentSession, error)
	Put(ctx context.Context, session *AgentSession, ttl time.Duration) error
	Delete(ctx context.Context, sessionID string) error
	List(ctx context.Context, userID string) ([]*AgentSession, error)
}

type PostgresSessionStore struct {
	db DBProvider
}

func NewPostgresSessionStore(db DBProvider) *PostgresSessionStore {
	return &PostgresSessionStore{db: db}
}

func (s *PostgresSessionStore) Get(ctx context.Context, sessionID string) (*AgentSession, error) {
	if s.db == nil {
		return nil, errors.New("db_not_available")
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `
SELECT payload_json FROM wisdev_agent_sessions_v2 WHERE session_id = $1
`, sessionID).Scan(&raw)
	if err != nil {
		return nil, errSessionNotFound
	}
	var session AgentSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *PostgresSessionStore) Put(ctx context.Context, session *AgentSession, _ time.Duration) error {
	if s.db == nil {
		return errors.New("db_not_available")
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
INSERT INTO wisdev_agent_sessions_v2 (session_id, user_id, payload_json, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (session_id) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	payload_json = EXCLUDED.payload_json,
	updated_at = EXCLUDED.updated_at
`, session.SessionID, session.UserID, raw, time.Now().UnixMilli())
	return err
}

func (s *PostgresSessionStore) Delete(ctx context.Context, sessionID string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(ctx, `DELETE FROM wisdev_agent_sessions_v2 WHERE session_id = $1`, sessionID)
	return err
}

func (s *PostgresSessionStore) List(ctx context.Context, userID string) ([]*AgentSession, error) {
	if s.db == nil {
		return nil, errors.New("db_not_available")
	}
	rows, err := s.db.Query(ctx, `
SELECT payload_json FROM wisdev_agent_sessions_v2 WHERE user_id = $1 ORDER BY updated_at DESC
`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []*AgentSession
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var session AgentSession
		if err := json.Unmarshal(raw, &session); err == nil {
			sessions = append(sessions, &session)
		}
	}
	return sessions, nil
}

type RedisSessionStore struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisSessionStore(client redis.UniversalClient) *RedisSessionStore {
	return &RedisSessionStore{
		client: client,
		prefix: "wisdev_session:",
	}
}

func (s *RedisSessionStore) key(sessionID string) string {
	return s.prefix + sessionID
}

func (s *RedisSessionStore) Get(ctx context.Context, sessionID string) (*AgentSession, error) {
	if s.client == nil {
		return nil, errSessionNotFound
	}
	val, err := s.client.Get(ctx, s.key(sessionID)).Result()
	if err != nil {
		return nil, errSessionNotFound
	}
	var session AgentSession
	if err := json.Unmarshal([]byte(val), &session); err != nil {
		return nil, fmt.Errorf("redis_session_unmarshal_failed: %w", err)
	}
	return &session, nil
}

func (s *RedisSessionStore) Put(ctx context.Context, session *AgentSession, ttl time.Duration) error {
	if s.client == nil {
		return errors.New("redis_not_available")
	}
	b, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.key(session.SessionID), string(b), ttl).Err()
}

func (s *RedisSessionStore) Delete(ctx context.Context, sessionID string) error {
	if s.client == nil {
		return nil
	}
	return s.client.Del(ctx, s.key(sessionID)).Err()
}

func (s *RedisSessionStore) List(ctx context.Context, userID string) ([]*AgentSession, error) {
	if s.client == nil {
		return []*AgentSession{}, nil
	}
	keys, err := s.client.Keys(ctx, s.prefix+"*").Result()
	if err != nil {
		return nil, err
	}
	out := make([]*AgentSession, 0, len(keys))
	for _, key := range keys {
		val, getErr := s.client.Get(ctx, key).Result()
		if getErr != nil {
			continue
		}
		var session AgentSession
		if unmarshalErr := json.Unmarshal([]byte(val), &session); unmarshalErr != nil {
			continue
		}
		if userID != "" && session.UserID != userID {
			continue
		}
		out = append(out, &session)
	}
	return out, nil
}

type FallbackSessionStore struct {
	primary  SessionStore
	fallback SessionStore
}

func NewFallbackSessionStore(primary SessionStore, fallback SessionStore) *FallbackSessionStore {
	return &FallbackSessionStore{primary: primary, fallback: fallback}
}

func (s *FallbackSessionStore) Get(ctx context.Context, sessionID string) (*AgentSession, error) {
	session, err := s.primary.Get(ctx, sessionID)
	if err == nil {
		return session, nil
	}
	return s.fallback.Get(ctx, sessionID)
}

func (s *FallbackSessionStore) Put(ctx context.Context, session *AgentSession, ttl time.Duration) error {
	if err := s.primary.Put(ctx, session, ttl); err != nil {
		return s.fallback.Put(ctx, session, ttl)
	}
	// Also mirror to fallback for resilience.
	_ = s.fallback.Put(ctx, session, ttl)
	return nil
}

func (s *FallbackSessionStore) Delete(ctx context.Context, sessionID string) error {
	_ = s.primary.Delete(ctx, sessionID)
	_ = s.fallback.Delete(ctx, sessionID)
	return nil
}

func (s *FallbackSessionStore) List(ctx context.Context, userID string) ([]*AgentSession, error) {
	sessions, err := s.primary.List(ctx, userID)
	if err == nil && len(sessions) > 0 {
		return sessions, nil
	}
	return s.fallback.List(ctx, userID)
}

type memorySessionRecord struct {
	session   AgentSession
	expiresAt time.Time
}

type InMemorySessionStore struct {
	mu      sync.RWMutex
	records map[string]memorySessionRecord
}

func NewInMemorySessionStore() *InMemorySessionStore {
	return &InMemorySessionStore{
		records: make(map[string]memorySessionRecord),
	}
}

func (s *InMemorySessionStore) Get(_ context.Context, sessionID string) (*AgentSession, error) {
	s.mu.RLock()
	record, ok := s.records[sessionID]
	s.mu.RUnlock()
	if !ok {
		return nil, errSessionNotFound
	}
	if !record.expiresAt.IsZero() && time.Now().After(record.expiresAt) {
		s.mu.Lock()
		delete(s.records, sessionID)
		s.mu.Unlock()
		return nil, errSessionNotFound
	}
	clone := record.session
	return &clone, nil
}

func (s *InMemorySessionStore) Put(_ context.Context, session *AgentSession, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt := time.Time{}
	if ttl != 0 {
		expiresAt = time.Now().Add(ttl)
	}
	// Clone via marshal/unmarshal to avoid accidental shared mutable maps.
	b, err := json.Marshal(session)
	if err != nil {
		return err
	}
	var clone AgentSession
	if err := json.Unmarshal(b, &clone); err != nil {
		return err
	}
	s.records[session.SessionID] = memorySessionRecord{
		session:   clone,
		expiresAt: expiresAt,
	}
	return nil
}

func (s *InMemorySessionStore) Delete(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, sessionID)
	return nil
}

func (s *InMemorySessionStore) List(_ context.Context, userID string) ([]*AgentSession, error) {
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*AgentSession, 0)
	for _, record := range s.records {
		if !record.expiresAt.IsZero() && now.After(record.expiresAt) {
			continue
		}
		if userID != "" && record.session.UserID != userID {
			continue
		}
		clone := record.session
		out = append(out, &clone)
	}
	return out, nil
}
