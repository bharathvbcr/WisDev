package wisdev

import (
	"encoding/json"
	"sync"
	"time"
)

type idempotencyRecord struct {
	statusCode int
	body       []byte
	expiresAt  time.Time
}

type IdempotencyStore struct {
	mu      sync.RWMutex
	records map[string]idempotencyRecord
	ttl     time.Duration
}

func NewIdempotencyStore(ttl time.Duration) *IdempotencyStore {
	return &IdempotencyStore{
		records: make(map[string]idempotencyRecord),
		ttl:     ttl,
	}
}

func (s *IdempotencyStore) Get(key string) (int, []byte, bool) {
	s.mu.RLock()
	record, ok := s.records[key]
	s.mu.RUnlock()
	if !ok {
		return 0, nil, false
	}
	if time.Now().After(record.expiresAt) {
		s.mu.Lock()
		delete(s.records, key)
		s.mu.Unlock()
		return 0, nil, false
	}
	return record.statusCode, append([]byte(nil), record.body...), true
}

func (s *IdempotencyStore) Put(key string, statusCode int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.records[key] = idempotencyRecord{
		statusCode: statusCode,
		body:       body,
		expiresAt:  time.Now().Add(s.ttl),
	}
	s.mu.Unlock()
}
