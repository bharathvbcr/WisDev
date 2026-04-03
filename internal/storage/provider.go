package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Session struct {
	SchemaVersion        string    `json:"schema_version"`
	PolicyVersion        string    `json:"policy_version"`
	TraceID              string    `json:"trace_id"`
	SessionID            string    `json:"session_id"`
	UserID               string    `json:"user_id"`
	Status               string    `json:"status"`
	CheckpointBlob       []byte    `json:"checkpoint_blob,omitempty"`
	QuestionSequence     []string  `json:"question_sequence"`
	CurrentQuestionIndex int32     `json:"current_question_index"`
	MinQuestions         int32     `json:"min_questions"`
	MaxQuestions         int32     `json:"max_questions"`
	ComplexityScore      float32   `json:"complexity_score"`
	ClarificationBudget  int32     `json:"clarification_budget"`
	QuestionStopReason   string    `json:"question_stop_reason"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type Provider interface {
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	SaveSession(ctx context.Context, session *Session) error
	DeleteSession(ctx context.Context, sessionID string) error
	ListSessions(ctx context.Context, userID string) ([]*Session, error)
	SaveCheckpoint(ctx context.Context, sessionID string, data []byte) error
	LoadCheckpoint(ctx context.Context, sessionID string) ([]byte, error)
	Close() error
}

func NewProvider(typ, dsn string) (Provider, error) {
	switch typ {
	case "memory", "":
		return NewInMemoryProvider(), nil
	case "sqlite":
		return NewSQLiteProvider(dsn)
	default:
		return nil, fmt.Errorf("unknown storage type: %s", typ)
	}
}

type InMemoryProvider struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewInMemoryProvider() *InMemoryProvider {
	return &InMemoryProvider{
		sessions: make(map[string]*Session),
	}
}

func (p *InMemoryProvider) GetSession(_ context.Context, sessionID string) (*Session, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.sessions[sessionID]
	if !ok {
		return nil, errors.New("session not found")
	}
	clone := *s
	return &clone, nil
}

func (p *InMemoryProvider) SaveSession(_ context.Context, session *Session) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	session.UpdatedAt = now
	clone := *session
	p.sessions[session.SessionID] = &clone
	return nil
}

func (p *InMemoryProvider) DeleteSession(_ context.Context, sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, sessionID)
	return nil
}

func (p *InMemoryProvider) ListSessions(_ context.Context, userID string) ([]*Session, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Session, 0, len(p.sessions))
	for _, s := range p.sessions {
		if userID != "" && s.UserID != userID {
			continue
		}
		clone := *s
		out = append(out, &clone)
	}
	return out, nil
}

func (p *InMemoryProvider) SaveCheckpoint(_ context.Context, sessionID string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sessions[sessionID]
	if !ok {
		return errors.New("session not found")
	}
	s.CheckpointBlob = make([]byte, len(data))
	copy(s.CheckpointBlob, data)
	s.UpdatedAt = time.Now()
	return nil
}

func (p *InMemoryProvider) LoadCheckpoint(_ context.Context, sessionID string) ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.sessions[sessionID]
	if !ok {
		return nil, errors.New("session not found")
	}
	if s.CheckpointBlob == nil {
		return nil, errors.New("no checkpoint found")
	}
	out := make([]byte, len(s.CheckpointBlob))
	copy(out, s.CheckpointBlob)
	return out, nil
}

func (p *InMemoryProvider) Close() error {
	return nil
}

type SQLiteProvider struct {
	mu       sync.RWMutex
	path     string
	sessions map[string]*Session
}

func NewSQLiteProvider(dsn string) (*SQLiteProvider, error) {
	if dsn == "" {
		dsn = "wisdev_state.db"
	}
	if dsn == "file:./wisdev_state.db" {
		dsn = "./wisdev_state.db"
	}
	p := &SQLiteProvider{
		path:     dsn,
		sessions: make(map[string]*Session),
	}
	if err := p.initDB(); err != nil {
		return nil, fmt.Errorf("init sqlite: %w", err)
	}
	return p, nil
}

func (p *SQLiteProvider) initDB() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := os.Stat(p.path); err == nil {
		return p.loadFromFile()
	}
	dir := filepath.Dir(p.path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return p.saveToFile()
}

func (p *SQLiteProvider) loadFromFile() error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &p.sessions); err != nil {
		return err
	}
	return nil
}

func (p *SQLiteProvider) saveToFile() error {
	data, err := json.MarshalIndent(p.sessions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.path, data, 0644)
}

func (p *SQLiteProvider) GetSession(_ context.Context, sessionID string) (*Session, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.sessions[sessionID]
	if !ok {
		return nil, errors.New("session not found")
	}
	clone := *s
	return &clone, nil
}

func (p *SQLiteProvider) SaveSession(_ context.Context, session *Session) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	session.UpdatedAt = now
	clone := *session
	p.sessions[session.SessionID] = &clone
	return p.saveToFile()
}

func (p *SQLiteProvider) DeleteSession(_ context.Context, sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, sessionID)
	return p.saveToFile()
}

func (p *SQLiteProvider) ListSessions(_ context.Context, userID string) ([]*Session, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Session, 0, len(p.sessions))
	for _, s := range p.sessions {
		if userID != "" && s.UserID != userID {
			continue
		}
		clone := *s
		out = append(out, &clone)
	}
	return out, nil
}

func (p *SQLiteProvider) SaveCheckpoint(_ context.Context, sessionID string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sessions[sessionID]
	if !ok {
		return errors.New("session not found")
	}
	s.CheckpointBlob = make([]byte, len(data))
	copy(s.CheckpointBlob, data)
	s.UpdatedAt = time.Now()
	return p.saveToFile()
}

func (p *SQLiteProvider) LoadCheckpoint(_ context.Context, sessionID string) ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.sessions[sessionID]
	if !ok {
		return nil, errors.New("session not found")
	}
	if s.CheckpointBlob == nil {
		return nil, errors.New("no checkpoint found")
	}
	out := make([]byte, len(s.CheckpointBlob))
	copy(out, s.CheckpointBlob)
	return out, nil
}

func (p *SQLiteProvider) Close() error {
	return nil
}
