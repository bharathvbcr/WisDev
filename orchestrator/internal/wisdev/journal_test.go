package wisdev

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDB struct {
	execFn func(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func (m *mockDB) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	if m.execFn != nil {
		return m.execFn(ctx, sql, arguments...)
	}
	return pgconn.CommandTag{}, nil
}
func (m *mockDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, nil
}
func (m *mockDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row { return nil }
func (m *mockDB) Begin(ctx context.Context) (pgx.Tx, error)                     { return nil, nil }
func (m *mockDB) Ping(ctx context.Context) error                                { return nil }
func (m *mockDB) Close()                                                        {}

func TestRuntimeJournal_AppendAndRead(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "journal_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	journalPath := filepath.Join(tempDir, "test_journal.jsonl")
	os.Setenv("WISDEV_JOURNAL_PATH", journalPath)
	defer os.Unsetenv("WISDEV_JOURNAL_PATH")

	j := NewRuntimeJournal(nil)
	entry := RuntimeJournalEntry{
		EventID:   "e1",
		SessionID: "s1",
		UserID:    "u1",
		EventType: "test_event",
		CreatedAt: time.Now().UnixMilli(),
	}

	j.Append(entry)

	// Verify file exists
	_, err = os.Stat(journalPath)
	assert.NoError(t, err)

	// Read session
	entries := j.ReadSession("s1", 10)
	assert.Len(t, entries, 1)
	assert.Equal(t, "e1", entries[0].EventID)
}

func TestRuntimeJournal_DeleteSession(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "journal_delete_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	journalPath := filepath.Join(tempDir, "test_journal.jsonl")
	os.Setenv("WISDEV_JOURNAL_PATH", journalPath)
	defer os.Unsetenv("WISDEV_JOURNAL_PATH")

	j := NewRuntimeJournal(nil)
	j.Append(RuntimeJournalEntry{EventID: "e1", SessionID: "s1", CreatedAt: 100})
	j.Append(RuntimeJournalEntry{EventID: "e2", SessionID: "s2", CreatedAt: 200})

	removed := j.DeleteSession("s1", "", true)
	assert.Equal(t, 1, removed)

	entries := j.ReadSession("s1", 10)
	assert.Empty(t, entries)
}

func TestRuntimeJournal_EnforceRetention(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "journal_retention_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	journalPath := filepath.Join(tempDir, "test_journal.jsonl")
	os.Setenv("WISDEV_JOURNAL_PATH", journalPath)
	defer os.Unsetenv("WISDEV_JOURNAL_PATH")

	j := NewRuntimeJournal(nil)
	oldTime := time.Now().Add(-48 * time.Hour).UnixMilli()
	newTime := time.Now().UnixMilli()

	j.Append(RuntimeJournalEntry{EventID: "old", CreatedAt: oldTime})
	j.Append(RuntimeJournalEntry{EventID: "new", CreatedAt: newTime})

	removed := j.EnforceRetention(1) // 1 day retention
	assert.Equal(t, 1, removed)

	entries := j.readAll()
	assert.Len(t, entries, 1)
	assert.Equal(t, "new", entries[0].EventID)
}

func TestRuntimeJournal_Analytics(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "journal_analytics_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	journalPath := filepath.Join(tempDir, "test_journal.jsonl")
	os.Setenv("WISDEV_JOURNAL_PATH", journalPath)
	defer os.Unsetenv("WISDEV_JOURNAL_PATH")

	j := NewRuntimeJournal(nil)

	// Add some data for analytics
	j.Append(RuntimeJournalEntry{
		UserID: "u1", SessionID: "s1", EventType: "Plan",
	})
	j.Append(RuntimeJournalEntry{
		UserID: "u1", SessionID: "s1", EventType: "execute", Metadata: map[string]any{"action": "search"}, Payload: map[string]any{"applied": "true"},
	})
	j.Append(RuntimeJournalEntry{
		UserID: "u1", SessionID: "s1", EventType: EventPolicyFeedbackSave, Payload: map[string]any{
			"overallRating": float64(5), "domain": "science", "subtopic": "physics", "searchSuccess": true,
		},
	})

	t.Run("SummarizeRecentOutcomes", func(t *testing.T) {
		res := j.SummarizeRecentOutcomes("u1", 10)
		assert.NotNil(t, res)
		assert.Equal(t, 1, res["totalOutcomes"])
	})

	t.Run("SummarizeFeedbackAnalytics", func(t *testing.T) {
		res := j.SummarizeFeedbackAnalytics("u1", 10)
		assert.Equal(t, 1, res["totalSessions"])
		assert.Equal(t, 5.0, res["averageRating"])
	})

	t.Run("SummarizeReplay", func(t *testing.T) {
		res := j.SummarizeReplay("u1", "")
		assert.NotNil(t, res)
		assert.Equal(t, 3, res["samples"])
		assert.Equal(t, 1, res["planCount"])
	})

	t.Run("SummarizeResearchProfile", func(t *testing.T) {
		res := j.SummarizeResearchProfile("u1")
		assert.NotNil(t, res)
		profile := res["profile"].(map[string]any)
		assert.Equal(t, "u1", profile["userId"])
	})
}

func TestRuntimeJournal_DB(t *testing.T) {
	mdb := &mockDB{}
	j := NewRuntimeJournal(mdb)
	assert.True(t, j.ensureDBStorage())
}

func TestAsOptionalString(t *testing.T) {
	assert.Equal(t, "123", AsOptionalString(123))
	assert.Equal(t, "true", AsOptionalString(true))
	assert.Equal(t, "hello", AsOptionalString("hello"))
	assert.Equal(t, "", AsOptionalString(nil))
}
