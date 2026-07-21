package wisdev

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSessionManager_Edge(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "wisdev_edge_*")
	defer os.RemoveAll(tmpDir)
	mgr := NewSessionManager(tmpDir)
	ctx := context.Background()

	t.Run("SaveSession Write Error", func(t *testing.T) {
		// Create a file where the session would be saved, making it a directory to force error
		session := &Session{ID: "error_id"}
		targetPath := filepath.Join(tmpDir, session.ID+".json")
		os.MkdirAll(targetPath, 0755)

		err := mgr.SaveSession(ctx, session)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to save session")
	})

	t.Run("GetSession Decode Error", func(t *testing.T) {
		sessionID := "bad_json"
		path := filepath.Join(tmpDir, sessionID+".json")
		os.WriteFile(path, []byte("invalid json"), 0644)

		loaded, err := mgr.GetSession(ctx, sessionID)
		assert.Error(t, err)
		assert.Nil(t, loaded)
		assert.Contains(t, err.Error(), "failed to decode session")
	})
}

func TestStateMachine_Transition(t *testing.T) {
	t.Run("Valid Transitions", func(t *testing.T) {
		session := &AgentSession{Status: SessionQuestioning}

		err := transitionSessionStatus(session, SessionGeneratingTree)
		assert.NoError(t, err)
		assert.Equal(t, SessionGeneratingTree, session.Status)

		err = transitionSessionStatus(session, SessionExecutingPlan)
		assert.NoError(t, err)

		err = transitionSessionStatus(session, SessionComplete)
		assert.NoError(t, err)
	})

	t.Run("Invalid Transitions", func(t *testing.T) {
		session := &AgentSession{Status: SessionComplete}

		err := transitionSessionStatus(session, SessionExecutingPlan)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid_session_transition")
	})
}
