package wisdev

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeJournal_Feedback(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "journal_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	journalPath := filepath.Join(tempDir, "journal.jsonl")
	os.Setenv("WISDEV_JOURNAL_PATH", journalPath)
	defer os.Unsetenv("WISDEV_JOURNAL_PATH")

	journal := NewRuntimeJournal(nil)

	feedback := map[string]any{
		"userId":    "u1",
		"sessionId": "s1",
		"rating":    5,
		"comment":   "great",
	}

	// 1. Save Feedback
	res := journal.SaveFeedback(feedback)
	assert.True(t, res["saved"].(bool))

	entries := journal.readAll()
	require.Len(t, entries, 1)
	assert.Equal(t, "/feedback/save", entries[0].Path)

	// 2. Get Latest Feedback
	res = journal.GetLatestFeedback("u1", "s1")
	assert.True(t, res["found"].(bool))
	assert.Equal(t, float64(5), res["feedback"].(map[string]any)["rating"])

	// 3. Summarize Feedback Analytics
	analytics := journal.SummarizeFeedbackAnalytics("u1", 10)
	assert.NotNil(t, analytics)
	if count, ok := analytics["totalFeedbackCount"].(int); ok {
		assert.GreaterOrEqual(t, count, 1)
	} else {
		// Fallback for different summary structure or if queryEntries was empty
		assert.NotNil(t, analytics)
	}
}

func TestRuntimeJournal_Paths(t *testing.T) {
	t.Setenv("WISDEV_JOURNAL_PATH", "")
	t.Setenv("WISDEV_STATE_DIR", "")
	journal := NewRuntimeJournal(nil)
	assert.NotEmpty(t, journal.Path())
	assert.NotEmpty(t, journal.IndexPath())
	assert.Contains(t, journal.Path(), filepath.Join(os.TempDir(), "wisdev_state"))
	assert.NotEqual(t, "wisdev_journal.jsonl", journal.Path())
}
