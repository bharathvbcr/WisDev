package wisdev

import "fmt"

var allowedSessionTransitions = map[SessionStatus]map[SessionStatus]bool{
	SessionQuestioning: {
		SessionGeneratingTree: true,
		SessionPaused:         true,
		SessionFailed:         true,
	},
	SessionGeneratingTree: {
		SessionEditingTree:   true,
		SessionExecutingPlan: true,
		SessionPaused:        true,
		SessionFailed:        true,
	},
	SessionEditingTree: {
		SessionExecutingPlan: true,
		SessionPaused:        true,
		SessionFailed:        true,
	},
	SessionExecutingPlan: {
		SessionPaused:   true,
		SessionComplete: true,
		SessionFailed:   true,
	},
	SessionPaused: {
		SessionQuestioning:    true,
		SessionGeneratingTree: true,
		SessionEditingTree:    true,
		SessionExecutingPlan:  true,
		SessionFailed:         true,
	},
	SessionComplete: {},
	SessionFailed: {
		SessionPaused: true,
	},
}

func transitionSessionStatus(session *AgentSession, next SessionStatus) error {
	if session.Status == next {
		return nil
	}
	allowed, ok := allowedSessionTransitions[session.Status]
	if !ok || !allowed[next] {
		return fmt.Errorf("invalid_session_transition: %s -> %s", session.Status, next)
	}
	session.Status = next
	session.UpdatedAt = NowMillis()
	return nil
}
