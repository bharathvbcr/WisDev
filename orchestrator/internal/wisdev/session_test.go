package wisdev

import (
	"context"
	"os"
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
