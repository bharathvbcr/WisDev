package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransitionSessionStatus(t *testing.T) {
	session := &AgentSession{
		Status:    SessionQuestioning,
		UpdatedAt: 1,
	}

	require.NoError(t, transitionSessionStatus(session, SessionGeneratingTree))
	assert.Equal(t, SessionGeneratingTree, session.Status)
	assert.Greater(t, session.UpdatedAt, int64(1))

	before := session.UpdatedAt
	require.NoError(t, transitionSessionStatus(session, SessionGeneratingTree))
	assert.Equal(t, before, session.UpdatedAt)

	err := transitionSessionStatus(session, SessionComplete)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_session_transition")
}
