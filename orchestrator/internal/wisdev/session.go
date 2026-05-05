package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// inferDomainFromQuery returns a coarse domain hint based on keyword
// matching so that SessionManager.CreateSession can set DetectedDomain
// before building the adaptive question sequence. This mirrors what the
// handleInitializeSession handler does when a detectedDomain is explicitly
// passed from the frontend.
func inferDomainFromQuery(query string) string {
	lower := strings.ToLower(strings.TrimSpace(query))
	switch {
	case containsAnyDomainPhrase(lower, []string{"medicine", "drug", "clinical", "patient", "diagnosis", "treatment", "therapy", "healthcare"}):
		return "medicine"
	case containsAnyDomainPhrase(lower, []string{"machine learning", "deep learning", "neural network", "algorithm", "computer science", "artificial intelligence", "generative ai"}):
		return "cs"
	case containsAnyDomainPhrase(lower, []string{"neuroscience", "neuro", "brain"}):
		return "neuro"
	case containsAnyDomainPhrase(lower, []string{"cancer", "biology", "genomics", "genetics", "protein", "cell"}):
		return "biology"
	case containsAnyDomainPhrase(lower, []string{"physics", "quantum", "chemistry", "material", "engineering"}):
		return "physics"
	case containsAnyDomainPhrase(lower, []string{"social science", "sociology", "psychology", "economics", "policy"}):
		return "social"
	default:
		return ""
	}
}

func containsAnyDomainPhrase(query string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(query, phrase) {
			return true
		}
	}
	return false
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
	// Infer DetectedDomain from the query before building the adaptive question
	// sequence. Without this the sequence is always built with an empty
	// domain hint, suppressing domain-specific questions. Use the same
	// heuristic the API handler uses when no explicit domain is provided.
	detectedDomain := inferDomainFromQuery(query)
	session := &Session{
		ID:                   sessionID,
		UserID:               userID,
		Query:                query,
		OriginalQuery:        query,
		CorrectedQuery:       query,
		ExpertiseLevel:       DetectExpertiseLevel(query),
		DetectedDomain:       detectedDomain,
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
