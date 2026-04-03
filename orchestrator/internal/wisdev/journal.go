package wisdev

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RuntimeJournalEntry struct {
	EventID   string         `json:"eventId"`
	TraceID   string         `json:"traceId"`
	SessionID string         `json:"sessionId,omitempty"`
	UserID    string         `json:"userId,omitempty"`
	PlanID    string         `json:"planId,omitempty"`
	StepID    string         `json:"stepId,omitempty"`
	EventType string         `json:"eventType"`
	Path      string         `json:"path"`
	Status    string         `json:"status"`
	CreatedAt int64          `json:"createdAt"`
	Summary   string         `json:"summary,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type RuntimeJournal struct {
	path      string
	indexPath string
	index     runtimeJournalIndex
	mu        sync.Mutex
	dbMu      sync.Mutex
	dbReady   bool
	db        DBProvider
}

func NewRuntimeJournal(db DBProvider) *RuntimeJournal {
	path := os.Getenv("WISDEV_JOURNAL_PATH")
	if path == "" {
		path = "wisdev_journal.jsonl"
	}
	indexPath := path + ".index"
	j := &RuntimeJournal{
		path:      path,
		indexPath: indexPath,
		db:        db,
	}
	j.loadIndex()
	return j
}

func (j *RuntimeJournal) Path() string      { return j.path }
func (j *RuntimeJournal) IndexPath() string { return j.indexPath }

type runtimeJournalIndex struct {
	AllEntries     []int64            `json:"allEntries"`
	SessionOffsets map[string][]int64 `json:"sessionOffsets"`
	UserOffsets    map[string][]int64 `json:"userOffsets"`
	TypeOffsets    map[string][]int64 `json:"typeOffsets"`
	LatestFeedback map[string]int64   `json:"latestFeedback"`
	LatestProfile  map[string]int64   `json:"latestProfile"`
}

func EnvInt(name string, defaultValue int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return defaultValue
	}
	return parsed
}

func (j *RuntimeJournal) startRetentionLoop() {
	retentionDays := EnvInt("WISDEV_JOURNAL_RETENTION_DAYS", 0)
	intervalMinutes := EnvInt("WISDEV_RETENTION_SWEEP_MINUTES", 60)
	if retentionDays <= 0 || intervalMinutes <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Duration(intervalMinutes) * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			_ = j.EnforceRetention(retentionDays)
		}
	}()
}

func (j *RuntimeJournal) loadIndex() {
	if j == nil || strings.TrimSpace(j.indexPath) == "" {
		return
	}
	// Always ensure maps are initialized so AppendIndexLocked never hits nil-map writes.
	j.index.SessionOffsets = map[string][]int64{}
	j.index.UserOffsets = map[string][]int64{}
	j.index.TypeOffsets = map[string][]int64{}
	j.index.LatestFeedback = map[string]int64{}
	j.index.LatestProfile = map[string]int64{}

	data, err := os.ReadFile(j.indexPath)
	if err != nil || len(data) == 0 {
		return
	}
	var index runtimeJournalIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return
	}
	if index.SessionOffsets == nil {
		index.SessionOffsets = j.index.SessionOffsets
	}
	if index.UserOffsets == nil {
		index.UserOffsets = j.index.UserOffsets
	}
	if index.TypeOffsets == nil {
		index.TypeOffsets = j.index.TypeOffsets
	}
	if index.LatestFeedback == nil {
		index.LatestFeedback = j.index.LatestFeedback
	}
	if index.LatestProfile == nil {
		index.LatestProfile = j.index.LatestProfile
	}
	j.index = index
}

func (j *RuntimeJournal) persistIndexLocked() {
	if j == nil || strings.TrimSpace(j.indexPath) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(j.indexPath), 0o755); err != nil {
		return
	}
	encoded, err := json.Marshal(j.index)
	if err != nil {
		return
	}
	_ = os.WriteFile(j.indexPath, encoded, 0o644)
}

func (j *RuntimeJournal) AppendIndexLocked(offset int64, entry RuntimeJournalEntry) {
	j.index.AllEntries = append(j.index.AllEntries, offset)
	if sessionID := strings.TrimSpace(entry.SessionID); sessionID != "" {
		j.index.SessionOffsets[sessionID] = append(j.index.SessionOffsets[sessionID], offset)
	}
	if userID := strings.TrimSpace(entry.UserID); userID != "" {
		j.index.UserOffsets[userID] = append(j.index.UserOffsets[userID], offset)
	}
	if eventType := strings.TrimSpace(entry.EventType); eventType != "" {
		j.index.TypeOffsets[eventType] = append(j.index.TypeOffsets[eventType], offset)
	}
	if entry.EventType == "feedback_save" {
		key := entry.UserID + "::" + entry.SessionID
		j.index.LatestFeedback[key] = offset
	}
	if entry.EventType == "profile_learn" && strings.TrimSpace(entry.UserID) != "" {
		j.index.LatestProfile[entry.UserID] = offset
	}
}

func (j *RuntimeJournal) Append(entry RuntimeJournalEntry) {
	if j == nil || strings.TrimSpace(j.path) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return
	}

	encoded, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return
	}
	j.AppendIndexLocked(offset, entry)
	j.persistIndexLocked()
	j.persistEntryToDB(entry)
}

func (j *RuntimeJournal) ensureDBStorage() bool {
	if j == nil || j.db == nil {
		return false
	}
	j.dbMu.Lock()
	defer j.dbMu.Unlock()
	if j.dbReady {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := j.db.Exec(ctx, `
CREATE TABLE IF NOT EXISTS wisdev_runtime_journal_v2 (
	event_id TEXT PRIMARY KEY,
	trace_id TEXT NOT NULL,
	session_id TEXT NOT NULL DEFAULT '',
	user_id TEXT NOT NULL DEFAULT '',
	plan_id TEXT NOT NULL DEFAULT '',
	step_id TEXT NOT NULL DEFAULT '',
	event_type TEXT NOT NULL,
	path TEXT NOT NULL,
	status TEXT NOT NULL,
	created_at BIGINT NOT NULL,
	summary TEXT NOT NULL DEFAULT '',
	payload_json JSONB NOT NULL,
	metadata_json JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wisdev_runtime_journal_v2_session_id ON wisdev_runtime_journal_v2(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_runtime_journal_v2_user_id ON wisdev_runtime_journal_v2(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_runtime_journal_v2_event_type ON wisdev_runtime_journal_v2(event_type, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_runtime_journal_v2_path ON wisdev_runtime_journal_v2(path, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_wisdev_runtime_journal_v2_status ON wisdev_runtime_journal_v2(status, created_at DESC);
`)
	if err != nil {
		return false
	}
	j.dbReady = true
	return true
}

func (j *RuntimeJournal) persistEntryToDB(entry RuntimeJournalEntry) {
	if !j.ensureDBStorage() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	payload, _ := json.Marshal(entry.Payload)
	metadata, _ := json.Marshal(entry.Metadata)
	_, _ = j.db.Exec(ctx, `
INSERT INTO wisdev_runtime_journal_v2 (
	event_id, trace_id, session_id, user_id, plan_id, step_id, event_type, path, status, created_at, summary, payload_json, metadata_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (event_id) DO NOTHING
`,
		entry.EventID,
		entry.TraceID,
		entry.SessionID,
		entry.UserID,
		entry.PlanID,
		entry.StepID,
		entry.EventType,
		entry.Path,
		entry.Status,
		entry.CreatedAt,
		entry.Summary,
		payload,
		metadata,
	)
}

func (j *RuntimeJournal) rewriteFileEntries(keep func(RuntimeJournalEntry) bool) int {
	if j == nil || strings.TrimSpace(j.path) == "" {
		return 0
	}
	entries := j.readAll()
	filtered := make([]RuntimeJournalEntry, 0, len(entries))
	removed := 0
	for _, entry := range entries {
		if keep(entry) {
			filtered = append(filtered, entry)
		} else {
			removed++
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.index = runtimeJournalIndex{
		AllEntries:     []int64{},
		SessionOffsets: map[string][]int64{},
		UserOffsets:    map[string][]int64{},
		TypeOffsets:    map[string][]int64{},
		LatestFeedback: map[string]int64{},
		LatestProfile:  map[string]int64{},
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return 0
	}
	file, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0
	}
	defer file.Close()
	var offset int64
	for _, entry := range filtered {
		encoded, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			continue
		}
		j.AppendIndexLocked(offset, entry)
		offset += int64(len(encoded) + 1)
	}
	j.persistIndexLocked()
	return removed
}

func (j *RuntimeJournal) DeleteSession(sessionID string, userID string, hardDelete bool) int {
	sessionID = strings.TrimSpace(sessionID)
	userID = strings.TrimSpace(userID)
	if sessionID == "" {
		return 0
	}
	removed := 0
	if j.ensureDBStorage() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := `DELETE FROM wisdev_runtime_journal_v2 WHERE session_id = $1`
		args := []any{sessionID}
		if userID != "" && !hardDelete {
			command += ` AND user_id = $2`
			args = append(args, userID)
		}
		if tag, err := j.db.Exec(ctx, command, args...); err == nil {
			removed = int(tag.RowsAffected())
		}
	}
	fileRemoved := j.rewriteFileEntries(func(entry RuntimeJournalEntry) bool {
		if entry.SessionID != sessionID {
			return true
		}
		if userID != "" && !hardDelete && entry.UserID != userID {
			return true
		}
		return false
	})
	if removed == 0 {
		removed = fileRemoved
	}
	return removed
}

func (j *RuntimeJournal) EnforceRetention(retentionDays int) int {
	if retentionDays <= 0 {
		return 0
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli()
	removed := 0
	if j.ensureDBStorage() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if tag, err := j.db.Exec(ctx, `DELETE FROM wisdev_runtime_journal_v2 WHERE created_at < $1`, cutoff); err == nil {
			removed = int(tag.RowsAffected())
		}
	}
	fileRemoved := j.rewriteFileEntries(func(entry RuntimeJournalEntry) bool {
		return entry.CreatedAt >= cutoff
	})
	if removed == 0 {
		removed = fileRemoved
	}
	return removed
}

func (j *RuntimeJournal) queryEntries(limit int, userID string, sessionID string, eventTypes ...string) []RuntimeJournalEntry {
	if !j.ensureDBStorage() {
		return nil
	}
	if limit <= 0 {
		limit = 200
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	query := `
SELECT event_id, trace_id, session_id, user_id, plan_id, step_id, event_type, path, status, created_at, summary, payload_json, metadata_json
FROM wisdev_runtime_journal_v2
WHERE 1=1`
	args := []any{}
	if strings.TrimSpace(userID) != "" {
		args = append(args, userID)
		query += fmt.Sprintf(" AND user_id = $%d", len(args))
	}
	if strings.TrimSpace(sessionID) != "" {
		args = append(args, sessionID)
		query += fmt.Sprintf(" AND session_id = $%d", len(args))
	}
	if len(eventTypes) > 0 {
		cleanTypes := make([]string, 0, len(eventTypes))
		for _, eventType := range eventTypes {
			eventType = strings.TrimSpace(eventType)
			if eventType != "" {
				cleanTypes = append(cleanTypes, eventType)
			}
		}
		if len(cleanTypes) > 0 {
			args = append(args, cleanTypes)
			query += fmt.Sprintf(" AND event_type = ANY($%d)", len(args))
		}
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := j.db.Query(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	entries := make([]RuntimeJournalEntry, 0, limit)
	for rows.Next() {
		var entry RuntimeJournalEntry
		var payloadRaw []byte
		var metadataRaw []byte
		if err := rows.Scan(
			&entry.EventID,
			&entry.TraceID,
			&entry.SessionID,
			&entry.UserID,
			&entry.PlanID,
			&entry.StepID,
			&entry.EventType,
			&entry.Path,
			&entry.Status,
			&entry.CreatedAt,
			&entry.Summary,
			&payloadRaw,
			&metadataRaw,
		); err != nil {
			continue
		}
		entry.Payload = map[string]any{}
		entry.Metadata = map[string]any{}
		_ = json.Unmarshal(payloadRaw, &entry.Payload)
		_ = json.Unmarshal(metadataRaw, &entry.Metadata)
		entries = append(entries, entry)
	}
	for i, k := 0, len(entries)-1; i < k; i, k = i+1, k-1 {
		entries[i], entries[k] = entries[k], entries[i]
	}
	return entries
}

func (j *RuntimeJournal) readAll() []RuntimeJournalEntry {
	if j == nil || strings.TrimSpace(j.path) == "" {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	file, err := os.Open(j.path)
	if err != nil {
		return nil
	}
	defer file.Close()

	entries := make([]RuntimeJournalEntry, 0, 128)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry RuntimeJournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func (j *RuntimeJournal) readEntryAt(offset int64) (RuntimeJournalEntry, bool) {
	if j == nil || strings.TrimSpace(j.path) == "" || offset < 0 {
		return RuntimeJournalEntry{}, false
	}
	file, err := os.Open(j.path)
	if err != nil {
		return RuntimeJournalEntry{}, false
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return RuntimeJournalEntry{}, false
	}
	reader := bufio.NewReader(file)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return RuntimeJournalEntry{}, false
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return RuntimeJournalEntry{}, false
	}
	var entry RuntimeJournalEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return RuntimeJournalEntry{}, false
	}
	return entry, true
}

func (j *RuntimeJournal) readOffsets(offsets []int64) []RuntimeJournalEntry {
	entries := make([]RuntimeJournalEntry, 0, len(offsets))
	for _, offset := range offsets {
		entry, ok := j.readEntryAt(offset)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func (j *RuntimeJournal) ReadSession(sessionID string, limit int) []RuntimeJournalEntry {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if entries := j.queryEntries(limit, "", sessionID); len(entries) > 0 {
		return entries
	}
	j.mu.Lock()
	offsets := append([]int64(nil), j.index.SessionOffsets[sessionID]...)
	j.mu.Unlock()
	if len(offsets) > 0 {
		if limit > 0 && len(offsets) > limit {
			offsets = offsets[len(offsets)-limit:]
		}
		return j.readOffsets(offsets)
	}
	entries := j.readAll()
	filtered := make([]RuntimeJournalEntry, 0, MinInt(limit, len(entries)))
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.SessionID != sessionID {
			continue
		}
		filtered = append(filtered, entry)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}
	return filtered
}

func (j *RuntimeJournal) SummarizeReplay(userID string, policyVersion string) map[string]any {
	entries := j.queryEntries(5000, userID, "")
	if len(entries) == 0 {
		j.mu.Lock()
		offsets := append([]int64(nil), j.index.AllEntries...)
		if userID != "" {
			offsets = append([]int64(nil), j.index.UserOffsets[userID]...)
		}
		j.mu.Unlock()
		entries = j.readOffsets(offsets)
	}
	filteredSessions := map[string]struct{}{}
	filteredEntries := 0
	planCount := 0
	decisionCount := 0
	executionCount := 0
	observationCount := 0
	confirmationCount := 0
	for _, entry := range entries {
		if userID != "" && entry.UserID != userID {
			continue
		}
		if policyVersion != "" && strings.TrimSpace(AsOptionalString(entry.Metadata["policyVersion"])) != policyVersion {
			continue
		}
		filteredEntries++
		if entry.SessionID != "" {
			filteredSessions[entry.SessionID] = struct{}{}
		}
		switch entry.EventType {
		case "Plan":
			planCount++
		case "decision":
			decisionCount++
			if requiresConfirmation, ok := entry.Metadata["requiresConfirmation"].(bool); ok && requiresConfirmation {
				confirmationCount++
			}
		case "execute", "multi_agent", "deep_research", "rag_answer", "rag_retrieve":
			executionCount++
		case "observe":
			observationCount++
		}
	}

	decisionDenominator := MaxInt(decisionCount, 1)
	planDenominator := MaxInt(planCount, 1)
	return map[string]any{
		"policyVersion":           policyVersion,
		"userId":                  userID,
		"samples":                 filteredEntries,
		"observedSessions":        len(filteredSessions),
		"planCount":               planCount,
		"decisionCount":           decisionCount,
		"executionCount":          executionCount,
		"observationCount":        observationCount,
		"confirmationRate":        float64(confirmationCount) / float64(decisionDenominator),
		"decisionCoverageRate":    float64(decisionCount) / float64(planDenominator),
		"observationCoverageRate": float64(observationCount) / float64(planDenominator),
		"journalPath":             j.path,
		"generatedAt":             time.Now().UnixMilli(),
	}
}

func (j *RuntimeJournal) SummarizeRecentOutcomes(userID string, maxResults int) map[string]any {
	entries := j.queryEntries(MaxInt(maxResults*3, 100), userID, "", "execute", "observe", "decision")
	if len(entries) == 0 {
		j.mu.Lock()
		offsets := append([]int64(nil), j.index.UserOffsets[userID]...)
		if userID == "" {
			offsets = append([]int64(nil), j.index.AllEntries...)
		}
		j.mu.Unlock()
		entries = j.readOffsets(offsets)
	}
	failedTools := make([]string, 0)
	successfulTools := make([]string, 0)
	totalOutcomes := 0
	rewardSum := 0.0
	seenSuccess := map[string]struct{}{}
	seenFailure := map[string]struct{}{}

	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if userID != "" && entry.UserID != userID {
			continue
		}
		if entry.EventType != "execute" && entry.EventType != "observe" && entry.EventType != "decision" {
			continue
		}
		totalOutcomes++
		if maxResults > 0 && totalOutcomes > maxResults {
			break
		}
		action := strings.TrimSpace(AsOptionalString(entry.Metadata["action"]))
		if action == "" {
			action = strings.TrimSpace(AsOptionalString(entry.Payload["selectedTool"]))
		}
		if action == "" {
			action = strings.TrimSpace(AsOptionalString(entry.Path))
		}
		if entry.EventType == "execute" && AsOptionalString(entry.Payload["applied"]) == "false" {
			if _, exists := seenFailure[action]; action != "" && !exists {
				failedTools = append(failedTools, action)
				seenFailure[action] = struct{}{}
			}
			continue
		}
		if action != "" {
			if _, exists := seenSuccess[action]; !exists {
				successfulTools = append(successfulTools, action)
				seenSuccess[action] = struct{}{}
			}
		}
		rewardSum += 1
	}

	avgReward := 0.0
	if totalOutcomes > 0 {
		avgReward = rewardSum / float64(totalOutcomes)
	}

	return map[string]any{
		"avgReward":       avgReward,
		"failedTools":     failedTools,
		"successfulTools": successfulTools,
		"totalOutcomes":   totalOutcomes,
	}
}

func (j *RuntimeJournal) SaveFeedback(feedback map[string]any) map[string]any {
	userID := AsOptionalString(feedback["userId"])
	sessionID := AsOptionalString(feedback["sessionId"])

	j.Append(RuntimeJournalEntry{
		EventID:   NewTraceID(),
		TraceID:   NewTraceID(),
		UserID:    userID,
		SessionID: sessionID,
		EventType: "feedback_save",
		Path:      "/v2/feedback/save",
		Status:    "saved",
		CreatedAt: NowMillis(),
		Summary:   "User feedback saved",
		Payload:   feedback,
	})

	return map[string]any{
		"saved":    true,
		"feedback": feedback,
	}
}

func (j *RuntimeJournal) GetLatestFeedback(userID string, sessionID string) map[string]any {
	if entries := j.queryEntries(1, userID, sessionID, "feedback_save"); len(entries) > 0 {
		return map[string]any{
			"found":    true,
			"feedback": entries[len(entries)-1].Payload,
		}
	}
	key := userID + "::" + sessionID
	j.mu.Lock()
	offset, ok := j.index.LatestFeedback[key]
	j.mu.Unlock()
	if ok {
		if entry, found := j.readEntryAt(offset); found {
			return map[string]any{
				"found":    true,
				"feedback": entry.Payload,
			}
		}
	}
	return map[string]any{
		"found":    false,
		"feedback": nil,
	}
}

func (j *RuntimeJournal) SummarizeFeedbackAnalytics(userID string, limit int) map[string]any {
	entries := j.queryEntries(MaxInt(limit, 100), userID, "", "feedback_save")
	if len(entries) == 0 {
		j.mu.Lock()
		offsets := append([]int64(nil), j.index.UserOffsets[userID]...)
		if userID == "" {
			offsets = append([]int64(nil), j.index.AllEntries...)
		}
		j.mu.Unlock()
		entries = j.readOffsets(offsets)
	}
	totalSessions := 0
	ratingSum := 0.0
	ratingCount := 0
	sessionDurations := 0.0
	durationCount := 0
	searchSuccessCount := 0
	skipCounts := map[string]int{}
	preferredDomains := map[string]int{}
	preferredSubtopics := map[string]int{}

	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.EventType != "feedback_save" {
			continue
		}
		if userID != "" && entry.UserID != userID {
			continue
		}
		totalSessions++
		if limit > 0 && totalSessions > limit {
			break
		}
		if rating, ok := entry.Payload["overallRating"].(float64); ok {
			ratingSum += rating
			ratingCount++
		}
		if duration, ok := entry.Payload["sessionDuration"].(float64); ok {
			sessionDurations += duration
			durationCount++
		}
		if success, ok := entry.Payload["searchSuccess"].(bool); ok && success {
			searchSuccessCount++
		}
		if skipped, ok := entry.Payload["questionsSkipped"].([]any); ok {
			for _, item := range skipped {
				skipCounts[AsOptionalString(item)]++
			}
		}
		if domain := AsOptionalString(entry.Payload["domain"]); domain != "" {
			preferredDomains[domain]++
		}
		if subtopic := AsOptionalString(entry.Payload["subtopic"]); subtopic != "" {
			preferredSubtopics[subtopic]++
		}
	}

	return map[string]any{
		"totalSessions":          totalSessions,
		"averageRating":          safeAverage(ratingSum, ratingCount),
		"skipRate":               skipCounts,
		"preferredDomains":       topKeys(preferredDomains, 5),
		"preferredSubtopics":     topKeys(preferredSubtopics, 5),
		"averageSessionDuration": safeAverage(sessionDurations, durationCount),
		"searchSuccessRate":      safeAverage(float64(searchSuccessCount), totalSessions),
	}
}

func (j *RuntimeJournal) SummarizeResearchProfile(userID string) map[string]any {
	if entries := j.queryEntries(1, userID, "", "profile_learn"); len(entries) > 0 {
		return map[string]any{
			"found":   true,
			"profile": entries[len(entries)-1].Payload,
		}
	}
	j.mu.Lock()
	profileOffset, hasProfile := j.index.LatestProfile[userID]
	offsets := append([]int64(nil), j.index.UserOffsets[userID]...)
	j.mu.Unlock()
	if hasProfile {
		if entry, found := j.readEntryAt(profileOffset); found {
			return map[string]any{
				"found":   true,
				"profile": entry.Payload,
			}
		}
	}
	entries := j.queryEntries(5000, userID, "", "feedback_save")
	if len(entries) == 0 {
		entries = j.readOffsets(offsets)
	}
	profile := map[string]any{
		"userId":              userID,
		"preferredDomains":    []string{},
		"typicalScope":        "balanced",
		"commonExclusions":    []string{},
		"expertiseLevel":      "intermediate",
		"expertiseTrend":      []int{},
		"totalSessions":       0,
		"successfulSubtopics": []string{},
		"lastUpdated":         time.Now().UnixMilli(),
	}
	domainCounts := map[string]int{}
	subtopicCounts := map[string]int{}
	totalSessions := 0
	for _, entry := range entries {
		if userID != "" && entry.UserID != userID {
			continue
		}
		if entry.EventType == "feedback_save" {
			totalSessions++
			if domain := AsOptionalString(entry.Payload["domain"]); domain != "" {
				domainCounts[domain]++
			}
			if subtopic := AsOptionalString(entry.Payload["subtopic"]); subtopic != "" {
				subtopicCounts[subtopic]++
			}
		}
	}
	profile["preferredDomains"] = topKeys(domainCounts, 5)
	profile["successfulSubtopics"] = topKeys(subtopicCounts, 5)
	profile["totalSessions"] = totalSessions
	return map[string]any{
		"found":   true,
		"profile": profile,
	}
}

func safeAverage(sum float64, count int) float64 {
	if count <= 0 {
		return 0
	}
	return sum / float64(count)
}

func topKeys(counts map[string]int, limit int) []string {
	type pair struct {
		key   string
		count int
	}
	pairs := make([]pair, 0, len(counts))
	for key, count := range counts {
		if strings.TrimSpace(key) == "" {
			continue
		}
		pairs = append(pairs, pair{key: key, count: count})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].count == pairs[j].count {
			return pairs[i].key < pairs[j].key
		}
		return pairs[i].count > pairs[j].count
	})
	out := make([]string, 0, MinInt(limit, len(pairs)))
	for _, item := range pairs {
		out = append(out, item.key)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func AsOptionalString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimSpace(strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", typed), "0"), "."))
	case int:
		return fmt.Sprintf("%d", typed)
	default:
		return strings.TrimSpace("")
	}
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

