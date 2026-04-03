package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionManager handles WisDev session lifecycle and persistence.
type SessionManager struct {
	baseDir string
	mu      sync.RWMutex
}

func NewSessionManager(baseDir string) *SessionManager {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "wisdev_wisdev_sessions")
	}
	_ = os.MkdirAll(baseDir, 0755)
	return &SessionManager{baseDir: baseDir}
}

func (m *SessionManager) CreateSession(ctx context.Context, userID, query string) (*Session, error) {
	sessionID := fmt.Sprintf("wd_%d", time.Now().UnixNano())
	session := &Session{
		ID:                   sessionID,
		UserID:               userID,
		OriginalQuery:        query,
		CorrectedQuery:       query,
		ExpertiseLevel:       DetectExpertiseLevel(query),
		Answers:              make(map[string]Answer),
		Status:               StatusQuestioning,
		CurrentQuestionIndex: 0,
		CreatedAt:            NowMillis(),
		UpdatedAt:            NowMillis(),
	}
	session.QuestionSequence, _, _ = BuildAdaptiveQuestionSequence(
		EstimateComplexityScore(query),
		session.DetectedDomain,
	)

	if err := m.SaveSession(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

func (m *SessionManager) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	path := filepath.Join(m.baseDir, sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to decode session: %w", err)
	}
	return &session, nil
}

func (m *SessionManager) SaveSession(ctx context.Context, session *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session.UpdatedAt = NowMillis()
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to encode session: %w", err)
	}

	path := filepath.Join(m.baseDir, session.ID+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}
