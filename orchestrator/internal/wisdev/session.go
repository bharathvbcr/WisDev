package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	internalsearch "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// inferDomainFromQuery returns a coarse domain hint for session planning.
func inferDomainFromQuery(query string) string {
	domain := internalsearch.InferDomainFromQuery(query)
	if domain == "general" {
		return ""
	}
	return domain
}

// SessionManager handles WisDev session lifecycle and persistence.
type SessionManager struct {
	baseDir string
	mu      sync.RWMutex
}

func NewSessionManager(baseDir string) *SessionManager {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "wisdev_sessions")
	}
	_ = os.MkdirAll(baseDir, 0755)
	return &SessionManager{baseDir: baseDir}
}

func (m *SessionManager) CreateSession(ctx context.Context, userID, query string) (*Session, error) {
	sessionID := newSessionManagerID()
	originalQuery := strings.TrimSpace(query)
	originalQuery, correctedQuery, planningQuery, detectedDomain := ApplyEarlySessionQueryPrep(ctx, originalQuery, "", "", "", nil, false)
	if strings.TrimSpace(correctedQuery) == "" {
		correctedQuery = originalQuery
	}
	if strings.TrimSpace(planningQuery) == "" {
		planningQuery = ResolveSessionQueryText(correctedQuery, originalQuery)
	}
	if strings.TrimSpace(detectedDomain) == "" {
		detectedDomain = inferDomainFromQuery(planningQuery)
	}
	session := &Session{
		ID:                   sessionID,
		UserID:               userID,
		Query:                planningQuery,
		OriginalQuery:        originalQuery,
		CorrectedQuery:       correctedQuery,
		ExpertiseLevel:       DetectExpertiseLevel(planningQuery),
		DetectedDomain:       detectedDomain,
		Answers:              make(map[string]Answer),
		Status:               StatusQuestioning,
		CurrentQuestionIndex: 0,
		CreatedAt:            NowMillis(),
		UpdatedAt:            NowMillis(),
	}
	session.QuestionSequence, _, _ = BuildAdaptiveQuestionSequence(
		EstimateComplexityScore(planningQuery),
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

	normalizedSessionID, err := normalizePersistenceKey("sessionId", sessionID)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(m.baseDir, normalizedSessionID+".json")
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

	normalizedSessionID, err := normalizePersistenceKey("sessionId", session.ID)
	if err != nil {
		return err
	}
	session.ID = normalizedSessionID
	session.UpdatedAt = NowMillis()
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to encode session: %w", err)
	}

	path := filepath.Join(m.baseDir, normalizedSessionID+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func newSessionManagerID() string {
	id := strings.TrimPrefix(strings.TrimSpace(NewTraceID()), "trace_")
	if id == "" || id == "fallback" {
		id = fmt.Sprintf("%d", NowMillis())
	}
	return "wd_" + id
}
