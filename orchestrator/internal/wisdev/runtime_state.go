package wisdev

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RuntimeStateStore struct {
	baseDir  string
	initOnce sync.Once
	initErr  error
	fileMu   sync.Mutex
	db       DBProvider
	journal  *RuntimeJournal
}

func (s *RuntimeStateStore) BaseDir() string { return s.baseDir }

type PersistedPolicyRecord struct {
	PolicyVersion string         `json:"policyVersion"`
	UserID        string         `json:"userId,omitempty"`
	State         map[string]any `json:"state"`
	Promoted      bool           `json:"promoted"`
	RolledBack    bool           `json:"rolledBack"`
	UpdatedAt     int64          `json:"updatedAt"`
}

type PersistedPolicyEvent struct {
	EventID       string         `json:"eventId"`
	EventType     string         `json:"eventType"`
	PolicyVersion string         `json:"policyVersion"`
	Payload       map[string]any `json:"payload"`
	CreatedAt     int64          `json:"createdAt"`
}

type persistedAgentSession struct {
	SessionID string         `json:"sessionId"`
	UserID    string         `json:"userId"`
	Payload   map[string]any `json:"payload"`
	UpdatedAt int64          `json:"updatedAt"`
}

type PersistedEvidenceDossier struct {
	DossierID string         `json:"dossierId"`
	JobID     string         `json:"jobId,omitempty"`
	UserID    string         `json:"userId,omitempty"`
	Payload   map[string]any `json:"payload"`
	UpdatedAt int64          `json:"updatedAt"`
}

type PersistedModeManifest struct {
	SessionID string         `json:"sessionId"`
	UserID    string         `json:"userId,omitempty"`
	Payload   map[string]any `json:"payload"`
	UpdatedAt int64          `json:"updatedAt"`
}

const maxStateFileBytes = 32 << 20

func newRuntimeStateJournal(baseDir string, db DBProvider) *RuntimeJournal {
	path := strings.TrimSpace(os.Getenv("WISDEV_JOURNAL_PATH"))
	if path == "" {
		path = filepath.Join(baseDir, "wisdev_journal.jsonl")
	}
	journal := &RuntimeJournal{
		path:      path,
		indexPath: path + ".index",
		db:        NormalizeDBProvider(db),
	}
	journal.loadIndex()
	return journal
}

func NewRuntimeStateStore(db DBProvider, journal *RuntimeJournal) *RuntimeStateStore {
	baseDir := strings.TrimSpace(os.Getenv("WISDEV_STATE_DIR"))
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "wisdev_state")
	}
	normalizedDB := NormalizeDBProvider(db)
	if journal == nil {
		journal = newRuntimeStateJournal(baseDir, normalizedDB)
	}
	store := &RuntimeStateStore{
		baseDir: baseDir,
		db:      normalizedDB,
		journal: journal,
	}
	store.startRetentionLoop()
	return store
}

func (s *RuntimeStateStore) startRetentionLoop() {
	retentionDays := EnvInt("WISDEV_STATE_RETENTION_DAYS", 0)
	intervalMinutes := EnvInt("WISDEV_RETENTION_SWEEP_MINUTES", 60)
	if retentionDays <= 0 || intervalMinutes <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			_, _ = s.EnforceRetention(retentionDays)
		}
	}()
}

func (s *RuntimeStateStore) ensureStorage() error {
	s.initOnce.Do(func() {
		if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
			s.initErr = err
			return
		}
		if s.db != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			renameLegacyRuntimeStateSchema(ctx, s.db)
			_, err := s.db.Exec(ctx, `
CREATE TABLE IF NOT EXISTS wisdev_policy_state (
	policy_version TEXT PRIMARY KEY,
	user_id TEXT NOT NULL DEFAULT '',
	state_json JSONB NOT NULL,
	promoted BOOLEAN NOT NULL DEFAULT FALSE,
	rolled_back BOOLEAN NOT NULL DEFAULT FALSE,
	updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS wisdev_policy_history (
	event_id TEXT PRIMARY KEY,
	event_type TEXT NOT NULL,
	policy_version TEXT NOT NULL,
	payload_json JSONB NOT NULL,
	created_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS wisdev_full_paper_jobs (
	job_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	payload_json JSONB NOT NULL,
	updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS wisdev_research_jobs (
	job_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	plane TEXT NOT NULL DEFAULT '',
	payload_json JSONB NOT NULL,
	updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS wisdev_agent_sessions (
	session_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL DEFAULT '',
	payload_json JSONB NOT NULL,
	updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS wisdev_evidence_dossiers (
	dossier_id TEXT PRIMARY KEY,
	job_id TEXT NOT NULL DEFAULT '',
	user_id TEXT NOT NULL DEFAULT '',
	payload_json JSONB NOT NULL,
	updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS wisdev_quest_states (
	quest_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL DEFAULT '',
	payload_json JSONB NOT NULL,
	updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS wisdev_mode_manifests (
	session_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL DEFAULT '',
	payload_json JSONB NOT NULL,
	updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS wisdev_quest_iterations (
	quest_id TEXT NOT NULL DEFAULT '',
	iteration BIGINT NOT NULL,
	payload_json JSONB NOT NULL,
	created_at BIGINT NOT NULL,
	PRIMARY KEY (quest_id, iteration)
);
CREATE TABLE IF NOT EXISTS wisdev_schedules (
	id TEXT PRIMARY KEY,
	project_id TEXT,
	schedule TEXT,
	query TEXT,
	created_at BIGINT
);
CREATE INDEX IF NOT EXISTS idx_wisdev_policy_state_user_id ON wisdev_policy_state(user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_policy_history_policy_version ON wisdev_policy_history(policy_version, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_full_paper_jobs_session_id ON wisdev_full_paper_jobs(session_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_research_jobs_session_id ON wisdev_research_jobs(session_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_research_jobs_status ON wisdev_research_jobs(status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_agent_sessions_user_id ON wisdev_agent_sessions(user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_evidence_dossiers_job_id ON wisdev_evidence_dossiers(job_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_quest_states_user_id ON wisdev_quest_states(user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_mode_manifests_user_id ON wisdev_mode_manifests(user_id, updated_at DESC);
`)
			if err != nil {
				s.initErr = err
			}
		}
	})
	if s.initErr == nil {
		retentionDays := EnvInt("WISDEV_STATE_RETENTION_DAYS", 0)
		if retentionDays > 0 {
			_, _ = s.EnforceRetention(retentionDays)
		}
	}
	return s.initErr
}

func (s *RuntimeStateStore) pathFor(name string) string {
	return filepath.Join(s.baseDir, name)
}

func (s *RuntimeStateStore) readJSONFile(path string, target any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > maxStateFileBytes {
		return fmt.Errorf("state file too large: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (s *RuntimeStateStore) writeJSONFile(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, encoded, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(path)
		if retryErr := os.Rename(tmpPath, path); retryErr != nil {
			return retryErr
		}
	}
	return nil
}

func normalizePersistenceKey(kind string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", kind)
	}
	if len(value) > 160 {
		return "", fmt.Errorf("%s exceeds max length", kind)
	}
	if strings.Contains(value, "..") || strings.ContainsAny(value, `/\\`) {
		return "", fmt.Errorf("%s contains invalid path characters", kind)
	}
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			continue
		}
		switch ch {
		case '-', '_', '.', ':':
			continue
		default:
			return "", fmt.Errorf("%s contains unsupported characters", kind)
		}
	}
	return value, nil
}

func (s *RuntimeStateStore) LoadPolicyRecord(policyVersion string) (*PersistedPolicyRecord, error) {
	normalizedPolicyVersion, err := normalizePersistenceKey("policyVersion", policyVersion)
	if err != nil {
		return nil, err
	}
	policyVersion = normalizedPolicyVersion
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var raw []byte
		record := &PersistedPolicyRecord{}
		err := s.db.QueryRow(ctx, `
SELECT user_id, state_json, promoted, rolled_back, updated_at
FROM wisdev_policy_state
WHERE policy_version = $1
`, policyVersion).Scan(&record.UserID, &raw, &record.Promoted, &record.RolledBack, &record.UpdatedAt)
		if err == nil {
			record.PolicyVersion = policyVersion
			record.State = map[string]any{}
			_ = json.Unmarshal(raw, &record.State)
			return record, nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	record := &PersistedPolicyRecord{}
	if err := s.readJSONFile(s.pathFor("policy_"+policyVersion+".json"), record); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *RuntimeStateStore) savePolicyRecord(record PersistedPolicyRecord) error {
	normalizedPolicyVersion, err := normalizePersistenceKey("policyVersion", record.PolicyVersion)
	if err != nil {
		return err
	}
	record.PolicyVersion = normalizedPolicyVersion
	record.UpdatedAt = time.Now().UnixMilli()
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		raw, _ := json.Marshal(record.State)
		_, err := s.db.Exec(ctx, `
INSERT INTO wisdev_policy_state (policy_version, user_id, state_json, promoted, rolled_back, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (policy_version) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	state_json = EXCLUDED.state_json,
	promoted = EXCLUDED.promoted,
	rolled_back = EXCLUDED.rolled_back,
	updated_at = EXCLUDED.updated_at
`, record.PolicyVersion, record.UserID, raw, record.Promoted, record.RolledBack, record.UpdatedAt)
		if err == nil {
			return nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	return s.writeJSONFile(s.pathFor("policy_"+record.PolicyVersion+".json"), record)
}

func (s *RuntimeStateStore) appendPolicyEvent(event PersistedPolicyEvent) error {
	event.CreatedAt = time.Now().UnixMilli()
	if strings.TrimSpace(event.EventID) == "" {
		event.EventID = NewTraceID()
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		raw, _ := json.Marshal(event.Payload)
		_, err := s.db.Exec(ctx, `
INSERT INTO wisdev_policy_history (event_id, event_type, policy_version, payload_json, created_at)
VALUES ($1, $2, $3, $4, $5)
`, event.EventID, event.EventType, event.PolicyVersion, raw, event.CreatedAt)
		if err == nil {
			return nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	path := s.pathFor("policy_history.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = f.Write(append(encoded, '\n'))
	return err
}

func normalizeMutationJournalEntry(entry RuntimeJournalEntry, sessionID string, userID string, defaultEventType string, defaultPath string, payload map[string]any) RuntimeJournalEntry {
	if strings.TrimSpace(entry.EventID) == "" {
		entry.EventID = NewTraceID()
	}
	if strings.TrimSpace(entry.TraceID) == "" {
		entry.TraceID = NewTraceID()
	}
	if entry.CreatedAt == 0 {
		entry.CreatedAt = time.Now().UnixMilli()
	}
	if strings.TrimSpace(entry.SessionID) == "" {
		entry.SessionID = strings.TrimSpace(sessionID)
	}
	if strings.TrimSpace(entry.UserID) == "" {
		entry.UserID = strings.TrimSpace(userID)
	}
	if strings.TrimSpace(entry.EventType) == "" {
		entry.EventType = defaultEventType
	}
	if strings.TrimSpace(entry.Path) == "" {
		entry.Path = defaultPath
	}
	if strings.TrimSpace(entry.Status) == "" {
		entry.Status = "persisted"
	}
	if entry.Payload == nil && len(payload) > 0 {
		entry.Payload = cloneAnyMap(payload)
	}
	return entry
}

func (s *RuntimeStateStore) PersistPolicyMutation(record PersistedPolicyRecord, event PersistedPolicyEvent, journalEntry RuntimeJournalEntry) error {
	if strings.TrimSpace(record.PolicyVersion) == "" {
		return errors.New("policyVersion is required")
	}
	if strings.TrimSpace(event.PolicyVersion) == "" {
		event.PolicyVersion = record.PolicyVersion
	}
	record.UpdatedAt = time.Now().UnixMilli()
	event.CreatedAt = time.Now().UnixMilli()
	if strings.TrimSpace(event.EventID) == "" {
		event.EventID = NewTraceID()
	}
	journalEntry = normalizeMutationJournalEntry(journalEntry, "", record.UserID, "policy_mutation", "/runtime/policy", map[string]any{
		"policyVersion": record.PolicyVersion,
		"userId":        record.UserID,
	})
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	journal := s.journal
	if s.db != nil && journal != nil && journal.ensureDBStorage() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback(ctx)
		}()
		recordRaw, _ := json.Marshal(record.State)
		if _, err := tx.Exec(ctx, `
INSERT INTO wisdev_policy_state (policy_version, user_id, state_json, promoted, rolled_back, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (policy_version) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	state_json = EXCLUDED.state_json,
	promoted = EXCLUDED.promoted,
	rolled_back = EXCLUDED.rolled_back,
	updated_at = EXCLUDED.updated_at
`, record.PolicyVersion, record.UserID, recordRaw, record.Promoted, record.RolledBack, record.UpdatedAt); err != nil {
			return err
		}
		eventRaw, _ := json.Marshal(event.Payload)
		if _, err := tx.Exec(ctx, `
INSERT INTO wisdev_policy_history (event_id, event_type, policy_version, payload_json, created_at)
VALUES ($1, $2, $3, $4, $5)
`, event.EventID, event.EventType, event.PolicyVersion, eventRaw, event.CreatedAt); err != nil {
			return err
		}
		journalPayload, _ := json.Marshal(journalEntry.Payload)
		journalMetadata, _ := json.Marshal(journalEntry.Metadata)
		if _, err := tx.Exec(ctx, `
INSERT INTO wisdev_runtime_journal (
	event_id, trace_id, session_id, user_id, plan_id, step_id, event_type, path, status, created_at, summary, payload_json, metadata_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (event_id) DO NOTHING
`, journalEntry.EventID, journalEntry.TraceID, journalEntry.SessionID, journalEntry.UserID, journalEntry.PlanID, journalEntry.StepID, journalEntry.EventType, journalEntry.Path, journalEntry.Status, journalEntry.CreatedAt, journalEntry.Summary, journalPayload, journalMetadata); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return nil
	}
	if err := s.savePolicyRecord(record); err != nil {
		return err
	}
	if err := s.appendPolicyEvent(event); err != nil {
		return err
	}
	if journal != nil {
		journal.Append(journalEntry)
	}
	return nil
}

func (s *RuntimeStateStore) loadLatestPolicyRecord() (*PersistedPolicyRecord, error) {
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var policyVersion string
		var userID string
		var raw []byte
		var promoted bool
		var rolledBack bool
		var updatedAt int64
		err := s.db.QueryRow(ctx, `
SELECT policy_version, user_id, state_json, promoted, rolled_back, updated_at
FROM wisdev_policy_state
ORDER BY updated_at DESC
LIMIT 1
`).Scan(&policyVersion, &userID, &raw, &promoted, &rolledBack, &updatedAt)
		if err == nil {
			state := map[string]any{}
			_ = json.Unmarshal(raw, &state)
			return &PersistedPolicyRecord{
				PolicyVersion: policyVersion,
				UserID:        userID,
				State:         state,
				Promoted:      promoted,
				RolledBack:    rolledBack,
				UpdatedAt:     updatedAt,
			}, nil
		}
	}
	return nil, errors.New("no persisted policy")
}

func (s *RuntimeStateStore) LoadLatestPolicyRecordForUser(userID string) (*PersistedPolicyRecord, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return s.loadLatestPolicyRecord()
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var policyVersion string
		var raw []byte
		var promoted bool
		var rolledBack bool
		var updatedAt int64
		err := s.db.QueryRow(ctx, `
SELECT policy_version, state_json, promoted, rolled_back, updated_at
FROM wisdev_policy_state
WHERE user_id = $1
ORDER BY updated_at DESC
LIMIT 1
`, userID).Scan(&policyVersion, &raw, &promoted, &rolledBack, &updatedAt)
		if err == nil {
			state := map[string]any{}
			_ = json.Unmarshal(raw, &state)
			return &PersistedPolicyRecord{
				PolicyVersion: policyVersion,
				UserID:        userID,
				State:         state,
				Promoted:      promoted,
				RolledBack:    rolledBack,
				UpdatedAt:     updatedAt,
			}, nil
		}
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	pattern := s.pathFor("policy_*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var latest *PersistedPolicyRecord
	for _, path := range paths {
		record := &PersistedPolicyRecord{}
		if err := s.readJSONFile(path, record); err != nil {
			continue
		}
		if strings.TrimSpace(record.UserID) != userID {
			continue
		}
		if latest == nil || record.UpdatedAt > latest.UpdatedAt {
			copyRecord := *record
			latest = &copyRecord
		}
	}
	if latest == nil {
		return nil, errors.New("no persisted policy")
	}
	return latest, nil
}

func (s *RuntimeStateStore) LoadPolicyHistory(policyVersion string, limit int) ([]PersistedPolicyEvent, error) {
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	events := make([]PersistedPolicyEvent, 0, limit)
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		query := `
SELECT event_id, event_type, policy_version, payload_json, created_at
FROM wisdev_policy_history
`
		args := []any{}
		if strings.TrimSpace(policyVersion) != "" {
			query += ` WHERE policy_version = $1`
			args = append(args, policyVersion)
		}
		query += ` ORDER BY created_at DESC LIMIT `
		query += strconv.Itoa(limit)
		rows, err := s.db.Query(ctx, query, args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var event PersistedPolicyEvent
				var raw []byte
				if scanErr := rows.Scan(&event.EventID, &event.EventType, &event.PolicyVersion, &raw, &event.CreatedAt); scanErr != nil {
					continue
				}
				event.Payload = map[string]any{}
				_ = json.Unmarshal(raw, &event.Payload)
				events = append(events, event)
			}
			return events, nil
		}
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	path := s.pathFor("policy_history.jsonl")
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event PersistedPolicyEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if policyVersion != "" && event.PolicyVersion != policyVersion {
			continue
		}
		events = append(events, event)
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].CreatedAt > events[j].CreatedAt
	})
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func (s *RuntimeStateStore) SaveFullPaperJob(jobID string, payload map[string]any) error {
	normalizedJobID, err := normalizePersistenceKey("jobId", jobID)
	if err != nil {
		return err
	}
	payload = cloneAnyMap(payload)
	updatedAt := time.Now().UnixMilli()
	if requestedUpdatedAt := IntValue64(payload["updatedAt"]); requestedUpdatedAt > updatedAt {
		updatedAt = requestedUpdatedAt
	}
	payload["updatedAt"] = updatedAt
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		raw, _ := json.Marshal(payload)
		userID := strings.TrimSpace(AsOptionalString(payload["userId"]))
		sessionID := strings.TrimSpace(AsOptionalString(payload["sessionId"]))
		_, err := s.db.Exec(ctx, `
INSERT INTO wisdev_full_paper_jobs (job_id, user_id, session_id, payload_json, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (job_id) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	session_id = EXCLUDED.session_id,
	payload_json = EXCLUDED.payload_json,
	updated_at = EXCLUDED.updated_at
`, normalizedJobID, userID, sessionID, raw, updatedAt)
		if err == nil {
			return nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	return s.writeJSONFile(s.pathFor("full_paper_"+normalizedJobID+".json"), payload)
}

func (s *RuntimeStateStore) SaveResearchJob(jobID string, payload map[string]any) error {
	normalizedJobID, err := normalizePersistenceKey("jobId", jobID)
	if err != nil {
		return err
	}
	updatedAt := time.Now().UnixMilli()
	payload = cloneAnyMap(payload)
	payload["jobId"] = normalizedJobID
	payload["updatedAt"] = updatedAt
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal research job: %w", err)
	}
	var dbErr error
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		userID := strings.TrimSpace(AsOptionalString(payload["userId"]))
		sessionID := strings.TrimSpace(AsOptionalString(payload["sessionId"]))
		status := strings.TrimSpace(AsOptionalString(payload["status"]))
		plane := strings.TrimSpace(AsOptionalString(payload["plane"]))
		_, dbErr = s.db.Exec(ctx, `
INSERT INTO wisdev_research_jobs (job_id, user_id, session_id, status, plane, payload_json, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (job_id) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	session_id = EXCLUDED.session_id,
	status = EXCLUDED.status,
	plane = EXCLUDED.plane,
	payload_json = EXCLUDED.payload_json,
	updated_at = EXCLUDED.updated_at
`, normalizedJobID, userID, sessionID, status, plane, raw, updatedAt)
		if dbErr == nil {
			return nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	record := persistedAgentSession{
		SessionID: strings.TrimSpace(AsOptionalString(payload["sessionId"])),
		UserID:    strings.TrimSpace(AsOptionalString(payload["userId"])),
		Payload:   payload,
		UpdatedAt: updatedAt,
	}
	if err := s.writeJSONFile(s.pathFor("research_job_"+normalizedJobID+".json"), record); err != nil {
		if dbErr != nil {
			return fmt.Errorf("save research job failed: db=%v file=%w", dbErr, err)
		}
		return err
	}
	return nil
}

func (s *RuntimeStateStore) LoadResearchJob(jobID string) (map[string]any, error) {
	normalizedJobID, err := normalizePersistenceKey("jobId", jobID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	var dbErr error
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var raw []byte
		dbErr = s.db.QueryRow(ctx, `
SELECT payload_json FROM wisdev_research_jobs WHERE job_id = $1
`, normalizedJobID).Scan(&raw)
		if dbErr == nil {
			payload := map[string]any{}
			_ = json.Unmarshal(raw, &payload)
			return payload, nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	record := persistedAgentSession{}
	if err := s.readJSONFile(s.pathFor("research_job_"+normalizedJobID+".json"), &record); err != nil {
		if dbErr != nil {
			return nil, fmt.Errorf("load research job failed: db=%v file=%w", dbErr, err)
		}
		return nil, err
	}
	return cloneAnyMap(record.Payload), nil
}

func (s *RuntimeStateStore) PersistFullPaperMutation(jobID string, payload map[string]any, journalEntry RuntimeJournalEntry) error {
	normalizedJobID, err := normalizePersistenceKey("jobId", jobID)
	if err != nil {
		return err
	}
	jobID = normalizedJobID
	payload = cloneAnyMap(payload)
	updatedAt := time.Now().UnixMilli()
	payload["updatedAt"] = updatedAt
	userID := strings.TrimSpace(AsOptionalString(payload["userId"]))
	sessionID := strings.TrimSpace(AsOptionalString(payload["sessionId"]))
	journalEntry = normalizeMutationJournalEntry(journalEntry, sessionID, userID, "full_paper_mutation", "/runtime/full-paper", map[string]any{
		"jobId":     jobID,
		"userId":    userID,
		"sessionId": sessionID,
	})
	if journalEntry.CreatedAt == 0 {
		journalEntry.CreatedAt = updatedAt
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	journal := s.journal
	if s.db != nil && journal != nil && journal.ensureDBStorage() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback(ctx)
		}()
		raw, _ := json.Marshal(payload)
		if _, err := tx.Exec(ctx, `
INSERT INTO wisdev_full_paper_jobs (job_id, user_id, session_id, payload_json, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (job_id) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	session_id = EXCLUDED.session_id,
	payload_json = EXCLUDED.payload_json,
	updated_at = EXCLUDED.updated_at
`, jobID, userID, sessionID, raw, updatedAt); err != nil {
			return err
		}
		journalPayload, _ := json.Marshal(journalEntry.Payload)
		journalMetadata, _ := json.Marshal(journalEntry.Metadata)
		if _, err := tx.Exec(ctx, `
INSERT INTO wisdev_runtime_journal (
	event_id, trace_id, session_id, user_id, plan_id, step_id, event_type, path, status, created_at, summary, payload_json, metadata_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (event_id) DO NOTHING
`, journalEntry.EventID, journalEntry.TraceID, journalEntry.SessionID, journalEntry.UserID, journalEntry.PlanID, journalEntry.StepID, journalEntry.EventType, journalEntry.Path, journalEntry.Status, journalEntry.CreatedAt, journalEntry.Summary, journalPayload, journalMetadata); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return nil
	}
	if err := s.SaveFullPaperJob(jobID, payload); err != nil {
		return err
	}
	if journal != nil {
		journal.Append(journalEntry)
	}
	return nil
}

func (s *RuntimeStateStore) LoadFullPaperJob(jobID string) (map[string]any, error) {
	normalizedJobID, err := normalizePersistenceKey("jobId", jobID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var raw []byte
		err := s.db.QueryRow(ctx, `
SELECT payload_json FROM wisdev_full_paper_jobs WHERE job_id = $1
`, normalizedJobID).Scan(&raw)
		if err == nil {
			payload := map[string]any{}
			_ = json.Unmarshal(raw, &payload)
			return payload, nil
		}
	}
	payload := map[string]any{}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if err := s.readJSONFile(s.pathFor("full_paper_"+normalizedJobID+".json"), &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *RuntimeStateStore) SaveEvidenceDossier(dossierID string, payload map[string]any) error {
	normalizedDossierID, err := normalizePersistenceKey("dossierId", dossierID)
	if err != nil {
		return err
	}
	updatedAt := time.Now().UnixMilli()
	payload = cloneAnyMap(payload)
	payload["dossierId"] = normalizedDossierID
	payload["updatedAt"] = updatedAt
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal evidence dossier: %w", err)
	}
	var dbErr error
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		jobID := strings.TrimSpace(AsOptionalString(payload["jobId"]))
		userID := strings.TrimSpace(AsOptionalString(payload["userId"]))
		_, dbErr = s.db.Exec(ctx, `
INSERT INTO wisdev_evidence_dossiers (dossier_id, job_id, user_id, payload_json, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (dossier_id) DO UPDATE SET
	job_id = EXCLUDED.job_id,
	user_id = EXCLUDED.user_id,
	payload_json = EXCLUDED.payload_json,
	updated_at = EXCLUDED.updated_at
`, normalizedDossierID, jobID, userID, raw, updatedAt)
		if dbErr == nil {
			return nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	record := PersistedEvidenceDossier{
		DossierID: normalizedDossierID,
		JobID:     strings.TrimSpace(AsOptionalString(payload["jobId"])),
		UserID:    strings.TrimSpace(AsOptionalString(payload["userId"])),
		Payload:   payload,
		UpdatedAt: updatedAt,
	}
	fileErr := s.writeJSONFile(s.pathFor("evidence_dossier_"+normalizedDossierID+".json"), record)
	if fileErr != nil && dbErr != nil {
		return fmt.Errorf("save evidence dossier failed: db=%v file=%w", dbErr, fileErr)
	}
	return fileErr
}

func (s *RuntimeStateStore) LoadEvidenceDossier(dossierID string) (map[string]any, error) {
	normalizedDossierID, err := normalizePersistenceKey("dossierId", dossierID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	var dbErr error
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var raw []byte
		dbErr = s.db.QueryRow(ctx, `
SELECT payload_json FROM wisdev_evidence_dossiers WHERE dossier_id = $1
`, normalizedDossierID).Scan(&raw)
		if dbErr == nil {
			payload := map[string]any{}
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil, fmt.Errorf("decode evidence dossier payload: %w", err)
			}
			return payload, nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	record := PersistedEvidenceDossier{}
	if err := s.readJSONFile(s.pathFor("evidence_dossier_"+normalizedDossierID+".json"), &record); err != nil {
		if dbErr != nil {
			return nil, fmt.Errorf("load evidence dossier failed: db=%v file=%w", dbErr, err)
		}
		return nil, err
	}
	return cloneAnyMap(record.Payload), nil
}

func (s *RuntimeStateStore) SaveQuestState(questID string, payload map[string]any) error {
	normalizedQuestID, err := normalizePersistenceKey("questId", questID)
	if err != nil {
		return err
	}
	updatedAt := time.Now().UnixMilli()
	payload = cloneAnyMap(payload)
	payload["questId"] = normalizedQuestID
	payload["updatedAt"] = updatedAt
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal quest state: %w", err)
	}
	var dbErr error
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		userID := strings.TrimSpace(AsOptionalString(payload["userId"]))
		_, dbErr = s.db.Exec(ctx, `
INSERT INTO wisdev_quest_states (quest_id, user_id, payload_json, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (quest_id) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	payload_json = EXCLUDED.payload_json,
	updated_at = EXCLUDED.updated_at
`, normalizedQuestID, userID, raw, updatedAt)
		if dbErr == nil {
			return nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	record := PersistedQuestState{
		QuestID:   normalizedQuestID,
		UserID:    strings.TrimSpace(AsOptionalString(payload["userId"])),
		Payload:   payload,
		UpdatedAt: updatedAt,
	}
	fileErr := s.writeJSONFile(s.pathFor("quest_state_"+normalizedQuestID+".json"), record)
	if fileErr != nil && dbErr != nil {
		return fmt.Errorf("save quest state failed: db=%v file=%w", dbErr, fileErr)
	}
	return fileErr
}

func (s *RuntimeStateStore) LoadQuestState(questID string) (map[string]any, error) {
	normalizedQuestID, err := normalizePersistenceKey("questId", questID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	var dbErr error
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var raw []byte
		dbErr = s.db.QueryRow(ctx, `
SELECT payload_json FROM wisdev_quest_states WHERE quest_id = $1
`, normalizedQuestID).Scan(&raw)
		if dbErr == nil {
			payload := map[string]any{}
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil, fmt.Errorf("decode quest state payload: %w", err)
			}
			return payload, nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	record := PersistedQuestState{}
	if err := s.readJSONFile(s.pathFor("quest_state_"+normalizedQuestID+".json"), &record); err != nil {
		if dbErr != nil {
			return nil, fmt.Errorf("load quest state failed: db=%v file=%w", dbErr, err)
		}
		return nil, err
	}
	return cloneAnyMap(record.Payload), nil
}

func (s *RuntimeStateStore) SaveModeManifest(sessionID string, payload map[string]any) error {
	normalizedSessionID, err := normalizePersistenceKey("sessionId", sessionID)
	if err != nil {
		return err
	}
	updatedAt := time.Now().UnixMilli()
	payload = cloneAnyMap(payload)
	payload["sessionId"] = normalizedSessionID
	payload["updatedAt"] = updatedAt
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal mode manifest: %w", err)
	}
	var dbErr error
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		userID := strings.TrimSpace(AsOptionalString(payload["userId"]))
		_, dbErr = s.db.Exec(ctx, `
INSERT INTO wisdev_mode_manifests (session_id, user_id, payload_json, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (session_id) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	payload_json = EXCLUDED.payload_json,
	updated_at = EXCLUDED.updated_at
`, normalizedSessionID, userID, raw, updatedAt)
		if dbErr == nil {
			return nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	record := PersistedModeManifest{
		SessionID: normalizedSessionID,
		UserID:    strings.TrimSpace(AsOptionalString(payload["userId"])),
		Payload:   payload,
		UpdatedAt: updatedAt,
	}
	fileErr := s.writeJSONFile(s.pathFor("mode_manifest_"+normalizedSessionID+".json"), record)
	if fileErr != nil && dbErr != nil {
		return fmt.Errorf("save mode manifest failed: db=%v file=%w", dbErr, fileErr)
	}
	return fileErr
}

func (s *RuntimeStateStore) LoadModeManifest(sessionID string) (map[string]any, error) {
	normalizedSessionID, err := normalizePersistenceKey("sessionId", sessionID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	var dbErr error
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var raw []byte
		dbErr = s.db.QueryRow(ctx, `
SELECT payload_json FROM wisdev_mode_manifests WHERE session_id = $1
`, normalizedSessionID).Scan(&raw)
		if dbErr == nil {
			payload := map[string]any{}
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil, fmt.Errorf("decode mode manifest payload: %w", err)
			}
			return payload, nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	record := PersistedModeManifest{}
	if err := s.readJSONFile(s.pathFor("mode_manifest_"+normalizedSessionID+".json"), &record); err != nil {
		if dbErr != nil {
			return nil, fmt.Errorf("load mode manifest failed: db=%v file=%w", dbErr, err)
		}
		return nil, err
	}
	return cloneAnyMap(record.Payload), nil
}

func resolvePersistedAgentSessionUpdatedAt(payload map[string]any) int64 {
	now := time.Now().UnixMilli()
	if updatedAt := IntValue64(payload["updatedAt"]); updatedAt > 0 {
		if updatedAt > now {
			return updatedAt
		}
		return now
	}
	return now
}

func (s *RuntimeStateStore) saveAgentSession(sessionID string, userID string, payload map[string]any) error {
	normalizedSessionID, err := normalizePersistenceKey("sessionId", sessionID)
	if err != nil {
		return err
	}
	payload = cloneAnyMap(payload)
	payload["sessionId"] = normalizedSessionID
	if strings.TrimSpace(userID) == "" {
		userID = strings.TrimSpace(AsOptionalString(payload["userId"]))
	}
	payload["userId"] = userID
	if _, ok := payload["modeManifest"].(map[string]any); !ok {
		mode := NormalizeWisDevMode(AsOptionalString(payload["mode"]))
		tier := ServiceTier(strings.TrimSpace(AsOptionalString(payload["serviceTier"])))
		payload["modeManifest"] = BuildModeManifestMap(mode, tier)
	}
	updatedAt := resolvePersistedAgentSessionUpdatedAt(payload)
	payload["updatedAt"] = updatedAt
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		raw, _ := json.Marshal(payload)
		_, err := s.db.Exec(ctx, `
INSERT INTO wisdev_agent_sessions (session_id, user_id, payload_json, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (session_id) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	payload_json = EXCLUDED.payload_json,
	updated_at = EXCLUDED.updated_at
`, normalizedSessionID, userID, raw, updatedAt)
		if err == nil {
			return nil
		}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	return s.writeJSONFile(s.pathFor("agent_session_"+normalizedSessionID+".json"), persistedAgentSession{
		SessionID: normalizedSessionID,
		UserID:    userID,
		Payload:   payload,
		UpdatedAt: updatedAt,
	})
}

func (s *RuntimeStateStore) PersistAgentSessionMutation(sessionID string, userID string, payload map[string]any, journalEntry RuntimeJournalEntry) error {
	normalizedSessionID, err := normalizePersistenceKey("sessionId", sessionID)
	if err != nil {
		return err
	}
	payload = cloneAnyMap(payload)
	sessionID = normalizedSessionID
	payload["sessionId"] = sessionID
	if strings.TrimSpace(userID) == "" {
		userID = strings.TrimSpace(AsOptionalString(payload["userId"]))
	}
	payload["userId"] = userID
	if _, ok := payload["modeManifest"].(map[string]any); !ok {
		mode := NormalizeWisDevMode(AsOptionalString(payload["mode"]))
		tier := ServiceTier(strings.TrimSpace(AsOptionalString(payload["serviceTier"])))
		payload["modeManifest"] = BuildModeManifestMap(mode, tier)
	}
	updatedAt := resolvePersistedAgentSessionUpdatedAt(payload)
	payload["updatedAt"] = updatedAt
	journalEntry = normalizeMutationJournalEntry(journalEntry, sessionID, userID, "agent_session_mutation", "/runtime/agent-session", map[string]any{
		"sessionId": sessionID,
		"userId":    userID,
	})
	if journalEntry.CreatedAt == 0 {
		journalEntry.CreatedAt = updatedAt
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return err
	}
	journal := s.journal
	if s.db != nil && journal != nil && journal.ensureDBStorage() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() {
			_ = tx.Rollback(ctx)
		}()
		raw, _ := json.Marshal(payload)
		if _, err := tx.Exec(ctx, `
INSERT INTO wisdev_agent_sessions (session_id, user_id, payload_json, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (session_id) DO UPDATE SET
	user_id = EXCLUDED.user_id,
	payload_json = EXCLUDED.payload_json,
	updated_at = EXCLUDED.updated_at
`, sessionID, userID, raw, updatedAt); err != nil {
			return err
		}
		journalPayload, _ := json.Marshal(journalEntry.Payload)
		journalMetadata, _ := json.Marshal(journalEntry.Metadata)
		if _, err := tx.Exec(ctx, `
INSERT INTO wisdev_runtime_journal (
	event_id, trace_id, session_id, user_id, plan_id, step_id, event_type, path, status, created_at, summary, payload_json, metadata_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (event_id) DO NOTHING
`, journalEntry.EventID, journalEntry.TraceID, journalEntry.SessionID, journalEntry.UserID, journalEntry.PlanID, journalEntry.StepID, journalEntry.EventType, journalEntry.Path, journalEntry.Status, journalEntry.CreatedAt, journalEntry.Summary, journalPayload, journalMetadata); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return nil
	}
	if err := s.saveAgentSession(sessionID, userID, payload); err != nil {
		return err
	}
	if journal != nil {
		journal.Append(journalEntry)
	}
	return nil
}

func (s *RuntimeStateStore) LoadAgentSession(sessionID string) (map[string]any, error) {
	normalizedSessionID, err := normalizePersistenceKey("sessionId", sessionID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureStorage(); err != nil && s.db == nil {
		return nil, err
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var raw []byte
		var userID string
		var updatedAt int64
		err := s.db.QueryRow(ctx, `
SELECT user_id, payload_json, updated_at
FROM wisdev_agent_sessions
WHERE session_id = $1
`, normalizedSessionID).Scan(&userID, &raw, &updatedAt)
		if err == nil {
			payload := map[string]any{}
			_ = json.Unmarshal(raw, &payload)
			payload["sessionId"] = normalizedSessionID
			payload["userId"] = userID
			if IntValue64(payload["updatedAt"]) == 0 && updatedAt > 0 {
				payload["updatedAt"] = updatedAt
			}
			return payload, nil
		}
	}
	record := &persistedAgentSession{}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if err := s.readJSONFile(s.pathFor("agent_session_"+normalizedSessionID+".json"), record); err != nil {
		return nil, err
	}
	payload := cloneAnyMap(record.Payload)
	payload["sessionId"] = record.SessionID
	payload["userId"] = record.UserID
	if IntValue64(payload["updatedAt"]) == 0 && record.UpdatedAt > 0 {
		payload["updatedAt"] = record.UpdatedAt
	}
	return payload, nil
}

func (s *RuntimeStateStore) DeleteSessionState(sessionID string, userID string, hardDelete bool) int {
	sessionID = strings.TrimSpace(sessionID)
	userID = strings.TrimSpace(userID)
	if sessionID == "" {
		return 0
	}
	removed := 0
	if err := s.ensureStorage(); err == nil && s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := `DELETE FROM wisdev_full_paper_jobs WHERE session_id = $1`
		args := []any{sessionID}
		if userID != "" && !hardDelete {
			command += ` AND user_id = $2`
			args = append(args, userID)
		}
		if tag, err := s.db.Exec(ctx, command, args...); err == nil {
			removed += int(tag.RowsAffected())
		}
		command = `DELETE FROM wisdev_agent_sessions WHERE session_id = $1`
		args = []any{sessionID}
		if userID != "" && !hardDelete {
			command += ` AND user_id = $2`
			args = append(args, userID)
		}
		if tag, err := s.db.Exec(ctx, command, args...); err == nil {
			removed += int(tag.RowsAffected())
		}
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	pattern := s.pathFor("full_paper_*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return removed
	}
	for _, path := range paths {
		payload := map[string]any{}
		if err := s.readJSONFile(path, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(AsOptionalString(payload["sessionId"])) != sessionID {
			continue
		}
		if userID != "" && !hardDelete && strings.TrimSpace(AsOptionalString(payload["userId"])) != userID {
			continue
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
	}
	agentPaths, err := filepath.Glob(s.pathFor("agent_session_*.json"))
	if err == nil {
		for _, path := range agentPaths {
			record := &persistedAgentSession{}
			if err := s.readJSONFile(path, record); err != nil {
				continue
			}
			if strings.TrimSpace(record.SessionID) != sessionID {
				continue
			}
			if userID != "" && !hardDelete && strings.TrimSpace(record.UserID) != userID {
				continue
			}
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
	}
	return removed
}

func (s *RuntimeStateStore) EnforceRetention(retentionDays int) (int, int) {
	if retentionDays <= 0 {
		return 0, 0
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli()
	policyRemoved := 0
	jobRemoved := 0
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if tag, err := s.db.Exec(ctx, `DELETE FROM wisdev_policy_history WHERE created_at < $1`, cutoff); err == nil {
			policyRemoved += int(tag.RowsAffected())
		}
		if tag, err := s.db.Exec(ctx, `DELETE FROM wisdev_full_paper_jobs WHERE updated_at < $1`, cutoff); err == nil {
			jobRemoved += int(tag.RowsAffected())
		}
		if tag, err := s.db.Exec(ctx, `DELETE FROM wisdev_agent_sessions WHERE updated_at < $1`, cutoff); err == nil {
			jobRemoved += int(tag.RowsAffected())
		}
		if tag, err := s.db.Exec(ctx, `DELETE FROM wisdev_quest_states WHERE updated_at < $1`, cutoff); err == nil {
			jobRemoved += int(tag.RowsAffected())
		}
	}

	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	historyPath := s.pathFor("policy_history.jsonl")
	if file, err := os.Open(historyPath); err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		kept := make([]PersistedPolicyEvent, 0)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			event := PersistedPolicyEvent{}
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				continue
			}
			if event.CreatedAt < cutoff {
				policyRemoved++
				continue
			}
			kept = append(kept, event)
		}
		rewrite, err := os.OpenFile(historyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err == nil {
			defer rewrite.Close()
			for _, event := range kept {
				encoded, err := json.Marshal(event)
				if err != nil {
					continue
				}
				_, _ = rewrite.Write(append(encoded, '\n'))
			}
		}
	}

	fullPaperPaths, err := filepath.Glob(s.pathFor("full_paper_*.json"))
	if err == nil {
		for _, path := range fullPaperPaths {
			payload := map[string]any{}
			if err := s.readJSONFile(path, &payload); err != nil {
				continue
			}
			updatedAt := int64(0)
			switch value := payload["updatedAt"].(type) {
			case float64:
				updatedAt = int64(value)
			case int64:
				updatedAt = value
			case int:
				updatedAt = int64(value)
			}
			if updatedAt > 0 && updatedAt < cutoff {
				if err := os.Remove(path); err == nil {
					jobRemoved++
				}
			}
		}
	}
	agentPaths, err := filepath.Glob(s.pathFor("agent_session_*.json"))
	if err == nil {
		for _, path := range agentPaths {
			record := &persistedAgentSession{}
			if err := s.readJSONFile(path, record); err != nil {
				continue
			}
			if record.UpdatedAt > 0 && record.UpdatedAt < cutoff {
				if err := os.Remove(path); err == nil {
					jobRemoved++
				}
			}
		}
	}
	questPaths, err := filepath.Glob(s.pathFor("quest_state_*.json"))
	if err == nil {
		for _, path := range questPaths {
			record := &PersistedQuestState{}
			if err := s.readJSONFile(path, record); err != nil {
				continue
			}
			if record.UpdatedAt > 0 && record.UpdatedAt < cutoff {
				if err := os.Remove(path); err == nil {
					jobRemoved++
				}
			}
		}
	}
	return policyRemoved, jobRemoved
}
