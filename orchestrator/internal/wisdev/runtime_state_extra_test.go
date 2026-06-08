package wisdev

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeStateStore_DeleteSessionState(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "delete_state_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	sessionID := "s1"
	userID := "u1"

	// Create some files
	store.SaveFullPaperJob("job1", map[string]any{"sessionId": sessionID, "userId": userID})
	store.saveAgentSession(sessionID, userID, map[string]any{"foo": "bar"})

	// Create another user's session
	store.saveAgentSession("s2", "u2", map[string]any{"foo": "baz"})

	// 1. Delete with wrong user (soft delete)
	removed := store.DeleteSessionState(sessionID, "wrong_user", false)
	assert.Equal(t, 0, removed)

	// 2. Delete with correct user (soft delete)
	removed = store.DeleteSessionState(sessionID, userID, false)
	assert.Equal(t, 2, removed)

	// 3. Hard delete
	store.SaveFullPaperJob("job2", map[string]any{"sessionId": "s3", "userId": "u3"})
	removed = store.DeleteSessionState("s3", "any_user", true)
	assert.Equal(t, 1, removed)
}

func TestRuntimeStateStore_EnforceRetention(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "retention_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	// Create an old file manually
	oldJobPath := filepath.Join(tempDir, "full_paper_old.json")
	oldTime := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()

	err = store.writeJSONFile(oldJobPath, map[string]any{"updatedAt": oldTime})
	require.NoError(t, err)

	// Create a new file
	store.SaveFullPaperJob("new_job", map[string]any{"foo": "bar"})

	// Enforce 5 day retention
	policyRemoved, jobRemoved := store.EnforceRetention(5)
	assert.Equal(t, 0, policyRemoved)
	assert.Equal(t, 1, jobRemoved) // old.json should be removed

	_, err = os.Stat(oldJobPath)
	assert.True(t, os.IsNotExist(err))
}

func TestRuntimeStateStore_LoadPolicyHistory_Filters(t *testing.T) {
	tempDir, _ := os.MkdirTemp("", "history_filter_test")
	defer os.RemoveAll(tempDir)
	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	store.appendPolicyEvent(PersistedPolicyEvent{PolicyVersion: "v1", EventType: "t1"})
	store.appendPolicyEvent(PersistedPolicyEvent{PolicyVersion: "v2", EventType: "t2"})

	history, err := store.LoadPolicyHistory("v1", 10)
	assert.NoError(t, err)
	assert.Len(t, history, 1)
	assert.Equal(t, "v1", history[0].PolicyVersion)
}

func TestRuntimeStateStore_Errors(t *testing.T) {
	tempDir, _ := os.MkdirTemp("", "error_test")
	defer os.RemoveAll(tempDir)
	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	t.Run("LoadNonExistent", func(t *testing.T) {
		_, err := store.LoadPolicyRecord("none")
		assert.Error(t, err)
	})

	t.Run("EmptyVersion", func(t *testing.T) {
		_, err := store.LoadPolicyRecord("")
		assert.Error(t, err)
		err = store.savePolicyRecord(PersistedPolicyRecord{})
		assert.Error(t, err)
	})

	t.Run("LoadLatestNoUser", func(t *testing.T) {
		_, err := store.LoadLatestPolicyRecordForUser("no_user")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no persisted policy")
	})
}

func TestRuntimeStateStore_EvidenceDossierRoundTrip(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "evidence_dossier_roundtrip")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	payload := map[string]any{
		"jobId":  "job_123",
		"userId": "u1",
		"query":  "reward modeling",
		"coverageMetrics": map[string]any{
			"sourceCount": 2,
		},
	}
	err = store.SaveEvidenceDossier("dossier_abc", payload)
	assert.NoError(t, err)

	loaded, err := store.LoadEvidenceDossier("dossier_abc")
	assert.NoError(t, err)
	assert.Equal(t, "dossier_abc", AsOptionalString(loaded["dossierId"]))
	assert.Equal(t, "job_123", AsOptionalString(loaded["jobId"]))
	assert.Equal(t, "u1", AsOptionalString(loaded["userId"]))
	assert.Equal(t, "reward modeling", AsOptionalString(loaded["query"]))
	assert.NotEmpty(t, loaded["updatedAt"])
}

func TestRuntimeStateStore_EvidenceDossierRejectsInvalidID(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "evidence_dossier_invalid_id")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	for _, badID := range []string{"", "../escape", "bad/segment", "bad\\segment", strings.Repeat("a", 161)} {
		err := store.SaveEvidenceDossier(badID, map[string]any{"query": "q"})
		assert.Error(t, err, "expected invalid dossierId error for %q", badID)
	}

	for _, badID := range []string{"../escape", "bad/segment", "bad\\segment"} {
		_, err := store.LoadEvidenceDossier(badID)
		assert.Error(t, err, "expected invalid dossierId error for %q", badID)
	}
}

func TestRuntimeStateStore_EvidenceDossierRejectsOversizedFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "evidence_dossier_oversized")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	oversizedPath := filepath.Join(tempDir, "evidence_dossier_dossier_big.json")
	oversizedContent := strings.Repeat("a", (32<<20)+1)
	err = os.WriteFile(oversizedPath, []byte(oversizedContent), 0o644)
	require.NoError(t, err)

	_, err = store.LoadEvidenceDossier("dossier_big")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "state file too large")
}

func TestRuntimeStateStore_ModeManifestRoundTrip(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mode_manifest_roundtrip")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)
	payload := map[string]any{
		"sessionId":   "s_mode_1",
		"userId":      "u1",
		"mode":        "guided",
		"serviceTier": "standard",
		"toolPolicy": map[string]any{
			"allowAutonomousExecution": false,
			"requireToolAllowList":     true,
		},
		"budgetPolicy": map[string]any{
			"maxStepCostCents":     15,
			"allowSandboxSpending": true,
		},
		"verificationPolicy": map[string]any{
			"requireCitationVerification": true,
			"requireBlindVerifier":        true,
			"minSourceAgreement":          2,
		},
	}

	err = store.SaveModeManifest("s_mode_1", payload)
	assert.NoError(t, err)

	loaded, err := store.LoadModeManifest("s_mode_1")
	assert.NoError(t, err)
	assert.Equal(t, "s_mode_1", AsOptionalString(loaded["sessionId"]))
	assert.Equal(t, "guided", AsOptionalString(loaded["mode"]))
	assert.Equal(t, "standard", AsOptionalString(loaded["serviceTier"]))
	assert.NotEmpty(t, loaded["updatedAt"])
}

func TestRuntimeStateStore_ModeManifestRejectsInvalidSessionID(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mode_manifest_invalid")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	store := NewRuntimeStateStore(nil, nil)

	for _, badID := range []string{"", "../escape", "bad/segment", "bad\\segment", strings.Repeat("a", 161)} {
		err := store.SaveModeManifest(badID, map[string]any{"mode": "guided"})
		assert.Error(t, err, "expected invalid sessionId error for %q", badID)
	}

	for _, badID := range []string{"../escape", "bad/segment", "bad\\segment"} {
		_, err := store.LoadModeManifest(badID)
		assert.Error(t, err, "expected invalid sessionId error for %q", badID)
	}
}

func TestRuntimeStateStore_DefaultJournalUsesStateDir(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "runtime_state_journal")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")
	os.Unsetenv("WISDEV_JOURNAL_PATH")

	store := NewRuntimeStateStore(nil, nil)
	require.NotNil(t, store.journal)
	assert.Equal(t, filepath.Join(tempDir, "wisdev_journal.jsonl"), store.journal.Path())

	err = store.PersistAgentSessionMutation("s1", "u1", map[string]any{
		"sessionId": "s1",
		"userId":    "u1",
		"status":    "active",
	}, RuntimeJournalEntry{EventType: "agent_session_initialize", Summary: "init"})
	require.NoError(t, err)

	entries := store.journal.ReadSession("s1", 10)
	if assert.Len(t, entries, 1) {
		assert.Equal(t, "agent_session_initialize", entries[0].EventType)
	}
}

func TestRuntimeStateStore_PersistAgentSessionMutationBackfillsJournalMetadata(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "runtime_state_journal_backfill")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	os.Setenv("WISDEV_STATE_DIR", tempDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")
	os.Unsetenv("WISDEV_JOURNAL_PATH")

	store := NewRuntimeStateStore(nil, nil)
	require.NotNil(t, store.journal)

	err = store.PersistAgentSessionMutation("s2", "u2", map[string]any{
		"sessionId": "s2",
		"userId":    "u2",
		"status":    "active",
	}, RuntimeJournalEntry{Summary: "saved"})
	require.NoError(t, err)

	entries := store.journal.ReadSession("s2", 10)
	if assert.Len(t, entries, 1) {
		assert.Equal(t, "s2", entries[0].SessionID)
		assert.Equal(t, "u2", entries[0].UserID)
		assert.Equal(t, "agent_session_mutation", entries[0].EventType)
		assert.Equal(t, "/runtime/agent-session", entries[0].Path)
		assert.Equal(t, "persisted", entries[0].Status)
		assert.NotEmpty(t, entries[0].TraceID)
	}
}
