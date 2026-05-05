package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapPlanExecutionEventToUpdateMapsCancelledEvent(t *testing.T) {
	update := mapPlanExecutionEventToUpdate(PlanExecutionEvent{
		Type:      EventPlanCancelled,
		TraceID:   "trace-cancelled",
		SessionID: "session-cancelled",
		PlanID:    "plan-cancelled",
		Message:   "Plan cancelled",
		Payload: map[string]any{
			"status": "cancelled",
		},
	})

	require.NotNil(t, update)
	cancelled := update.GetExecutionCancelled()
	require.NotNil(t, cancelled)
	assert.Equal(t, "trace-cancelled", update.GetTraceId())
	assert.Equal(t, "session-cancelled", update.GetSessionId())
	assert.Equal(t, "plan-cancelled", update.GetPlanId())
	assert.Equal(t, "Plan cancelled", cancelled.GetReason())
	assert.Equal(t, "cancelled", cancelled.GetStatus())
}
