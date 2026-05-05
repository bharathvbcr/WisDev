package wisdev

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeStateStore_PolicyRecord(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "state_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	journal := NewRuntimeJournal(nil)
	store := NewRuntimeStateStore(nil, journal)

	record := PersistedPolicyRecord{
		PolicyVersion: "v1",
		UserID:        "u1",
		State:         map[string]any{"key": "value"},
	}

	err = store.savePolicyRecord(record)
	assert.NoError(t, err)

	loaded, err := store.LoadPolicyRecord("v1")
	assert.NoError(t, err)
	assert.Equal(t, "u1", loaded.UserID)
	assert.Equal(t, "value", loaded.State["key"])

	latest, err := store.LoadLatestPolicyRecordForUser("u1")
	assert.NoError(t, err)
	assert.Equal(t, "v1", latest.PolicyVersion)
}

func TestRuntimeStateStore_FullPaperJob(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "paper_job_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	jobID := "job1"
	payload := map[string]any{"paper": "p1", "userId": "u1"}

	err = store.SaveFullPaperJob(jobID, payload)
	assert.NoError(t, err)

	loaded, err := store.LoadFullPaperJob(jobID)
	assert.NoError(t, err)
	assert.Equal(t, "p1", loaded["paper"])
}

func TestRuntimeStateStore_AgentSession(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "agent_session_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	sessionID := "s1"
	userID := "u1"
	payload := map[string]any{"step": 1}

	err = store.saveAgentSession(sessionID, userID, payload)
	assert.NoError(t, err)

	loaded, err := store.LoadAgentSession(sessionID)
	assert.NoError(t, err)
	assert.Equal(t, float64(1), loaded["step"])
	assert.Equal(t, "u1", loaded["userId"])
}

func TestRuntimeStateStore_RejectsUnsafeFileBackedKeys(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "unsafe_state_keys")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	assert.Error(t, store.SaveFullPaperJob("../escape", map[string]any{"userId": "u1"}))
	_, err = store.LoadFullPaperJob(`..\escape`)
	assert.Error(t, err)
	assert.Error(t, store.saveAgentSession(`session/escape`, "u1", map[string]any{}))
	_, err = store.LoadAgentSession(`session\escape`)
	assert.Error(t, err)
	assert.Error(t, store.PersistAgentSessionMutation("../session", "u1", map[string]any{}, RuntimeJournalEntry{}))

	_, statErr := os.Stat(filepath.Join(filepath.Dir(tempDir), "escape.json"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestRuntimeStateStore_PersistMutations(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mutation_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	journal := NewRuntimeJournal(nil)
	store := NewRuntimeStateStore(nil, journal)

	t.Run("Policy Mutation", func(t *testing.T) {
		record := PersistedPolicyRecord{PolicyVersion: "v2", UserID: "u1"}
		event := PersistedPolicyEvent{EventType: "created"}
		journalEntry := RuntimeJournalEntry{Summary: "journal msg"}

		err := store.PersistPolicyMutation(record, event, journalEntry)
		assert.NoError(t, err)

		loaded, _ := store.LoadPolicyRecord("v2")
		assert.Equal(t, "u1", loaded.UserID)
	})

	t.Run("FullPaper Mutation", func(t *testing.T) {
		payload := map[string]any{"job": "j1"}
		journalEntry := RuntimeJournalEntry{Summary: "job journal"}

		err := store.PersistFullPaperMutation("j1", payload, journalEntry)
		assert.NoError(t, err)

		loaded, _ := store.LoadFullPaperJob("j1")
		assert.Equal(t, "j1", loaded["job"])
	})

	t.Run("AgentSession Mutation", func(t *testing.T) {
		payload := map[string]any{"session": "s1"}
		journalEntry := RuntimeJournalEntry{Summary: "session journal"}

		err := store.PersistAgentSessionMutation("s1", "u1", payload, journalEntry)
		assert.NoError(t, err)

		loaded, _ := store.LoadAgentSession("s1")
		assert.Equal(t, "s1", loaded["session"])
		assert.NotZero(t, IntValue64(loaded["updatedAt"]))
	})
}

func TestRuntimeStateStore_PersistAgentSessionMutationAdvancesNumericUpdatedAt(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "agent_session_numeric_updated_at")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)
	initialUpdatedAt := time.Now().Add(-2 * time.Second).UnixMilli()

	err = store.PersistAgentSessionMutation("s-numeric", "u1", map[string]any{
		"sessionId": "s-numeric",
		"userId":    "u1",
		"updatedAt": initialUpdatedAt,
		"status":    "questioning",
	}, RuntimeJournalEntry{Summary: "session journal"})
	require.NoError(t, err)

	loaded, err := store.LoadAgentSession("s-numeric")
	require.NoError(t, err)
	assert.Greater(t, IntValue64(loaded["updatedAt"]), initialUpdatedAt)
	assert.NotZero(t, IntValue64(loaded["updatedAt"]))
}

func TestRuntimeStateStore_PersistAgentSessionMutationAdvancesLoadedSessionVersion(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "agent_session_loaded_version_advance")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	err = store.PersistAgentSessionMutation("s-loaded", "u1", map[string]any{
		"sessionId": "s-loaded",
		"userId":    "u1",
		"status":    "questioning",
	}, RuntimeJournalEntry{Summary: "first mutation"})
	require.NoError(t, err)

	first, err := store.LoadAgentSession("s-loaded")
	require.NoError(t, err)
	firstUpdatedAt := IntValue64(first["updatedAt"])
	require.NotZero(t, firstUpdatedAt)

	time.Sleep(5 * time.Millisecond)
	first["assessedComplexity"] = "moderate"
	err = store.PersistAgentSessionMutation("s-loaded", "u1", first, RuntimeJournalEntry{Summary: "second mutation"})
	require.NoError(t, err)

	second, err := store.LoadAgentSession("s-loaded")
	require.NoError(t, err)
	assert.Greater(t, IntValue64(second["updatedAt"]), firstUpdatedAt)
	assert.Equal(t, "moderate", AsOptionalString(second["assessedComplexity"]))
}

func TestRuntimeStateStore_History(t *testing.T) {
	tempDir, _ := os.MkdirTemp("", "history_test")
	defer os.RemoveAll(tempDir)
	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	// Create history file manually to test LoadPolicyHistory
	historyPath := filepath.Join(tempDir, "policy_history.jsonl")
	os.WriteFile(historyPath, []byte(`{"policyVersion": "v1", "eventType": "type1"}
{"policyVersion": "v1", "eventType": "type2"}
`), 0644)

	history, err := store.LoadPolicyHistory("v1", 10)
	assert.NoError(t, err)
	assert.Len(t, history, 2)
}
