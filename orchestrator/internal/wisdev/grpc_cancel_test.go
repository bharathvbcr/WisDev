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

func TestMapPlanExecutionEventToUpdateMapsNativeADKConfirmation(t *testing.T) {
	update := mapPlanExecutionEventToUpdate(PlanExecutionEvent{
		Type:      EventConfirmationNeed,
		TraceID:   "trace-adk-confirmation",
		SessionID: "session-adk-confirmation",
		PlanID:    "plan-adk-confirmation",
		StepID:    "step-fallback",
		Message:   "Needs approval",
		Payload: map[string]any{
			"approvalToken":               "approval-token-1",
			"allowedActions":              []any{"approve", "skip"},
			"stepId":                      "step-from-payload",
			"action":                      "run_search",
			"rationale":                   "Search fan-out needs approval",
			"guardrailReason":             "requires human confirmation",
			"adkConfirmationFunction":     "adk_request_confirmation",
			"adkConfirmationRequestId":    "confirm-call-1",
			"adkOriginalFunctionCallId":   "original-call-1",
			"adkOriginalFunctionCallName": "run_search",
			"adkConfirmationHint":         "Review the search fan-out",
			"adkConfirmationPayload":      map[string]any{"risk": "medium"},
			"adkOriginalFunctionCallArgs": map[string]any{"query": "adk"},
		},
	})

	require.NotNil(t, update)
	confirmation := update.GetConfirmationNeeded()
	require.NotNil(t, confirmation)
	assert.Equal(t, "trace-adk-confirmation", update.GetTraceId())
	assert.Equal(t, "session-adk-confirmation", update.GetSessionId())
	assert.Equal(t, "plan-adk-confirmation", update.GetPlanId())
	assert.Equal(t, "run_search", confirmation.GetAction())
	assert.Equal(t, "Search fan-out needs approval", confirmation.GetRationale())
	assert.Equal(t, "approval-token-1", confirmation.GetApprovalToken())
	assert.Equal(t, []string{"approve", "skip"}, confirmation.GetAllowedActions())
	assert.Equal(t, "step-fallback", confirmation.GetStepId())
	assert.Equal(t, "requires human confirmation", confirmation.GetGuardrailReason())
	assert.Equal(t, "adk_request_confirmation", confirmation.GetAdkConfirmationFunction())
	assert.Equal(t, "confirm-call-1", confirmation.GetAdkConfirmationRequestId())
	assert.Equal(t, "original-call-1", confirmation.GetAdkOriginalFunctionCallId())
	assert.Equal(t, "run_search", confirmation.GetAdkOriginalFunctionCallName())
	assert.Equal(t, "Review the search fan-out", confirmation.GetAdkConfirmationHint())
	assert.JSONEq(t, `{"risk":"medium"}`, confirmation.GetAdkConfirmationPayloadJson())
	assert.JSONEq(t, `{"query":"adk"}`, confirmation.GetAdkOriginalFunctionCallArgsJson())
}
