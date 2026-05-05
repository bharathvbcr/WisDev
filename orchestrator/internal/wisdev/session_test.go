package wisdev

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSessionManager(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "wisdev_state_*")
	defer os.RemoveAll(tmpDir)

	mgr := NewSessionManager(tmpDir)
	ctx := context.Background()

	t.Run("Create and Get Session", func(t *testing.T) {
		session, err := mgr.CreateSession(ctx, "user1", "query1")
		assert.NoError(t, err)
		assert.NotNil(t, session)
		assert.Equal(t, "user1", session.UserID)
		assert.Equal(t, "query1", session.OriginalQuery)
		// Initial status might be questioning or something else depending on NewAgentSession
		assert.NotEmpty(t, session.Status)

		loaded, err := mgr.GetSession(ctx, session.ID)
		assert.NoError(t, err)
		assert.Equal(t, session.ID, loaded.ID)
		assert.Equal(t, "query1", loaded.OriginalQuery)
	})

	t.Run("Save and Load Updated Session", func(t *testing.T) {
		session, _ := mgr.CreateSession(ctx, "u2", "q2")
		session.Status = SessionGeneratingTree
		session.DetectedDomain = "biology"

		err := mgr.SaveSession(ctx, session)
		assert.NoError(t, err)

		loaded, _ := mgr.GetSession(ctx, session.ID)
		assert.Equal(t, SessionGeneratingTree, loaded.Status)
		assert.Equal(t, "biology", loaded.DetectedDomain)
	})

	t.Run("Get Non-existent Session", func(t *testing.T) {
		loaded, err := mgr.GetSession(ctx, "non-existent")
		assert.Error(t, err)
		assert.Nil(t, loaded)
	})
}

func TestSessionManagerRejectsUnsafeSessionIDs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_state_unsafe_*")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	mgr := NewSessionManager(tmpDir)
	ctx := context.Background()

	err = mgr.SaveSession(ctx, &Session{ID: "../escape"})
	assert.Error(t, err)

	_, err = mgr.GetSession(ctx, `..\escape`)
	assert.Error(t, err)

	_, statErr := os.Stat(filepath.Join(filepath.Dir(tmpDir), "escape.json"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestInferDomainFromQueryUsesCanonicalDomains(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "medicine", query: "clinical treatment options for diabetes patients", want: "medicine"},
		{name: "biology", query: "protein dynamics in cancer cell signaling", want: "biology"},
		{name: "computer science", query: "machine learning algorithms for retrieval", want: "cs"},
		{name: "social", query: "social science policy outcomes in education", want: "social"},
		{name: "empty", query: "weather patterns in local newspapers", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, inferDomainFromQuery(tt.query))
		})
	}
}
