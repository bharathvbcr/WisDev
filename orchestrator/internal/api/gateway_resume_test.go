package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestGatewayHandleResumeSession_ApprovesPendingStep(t *testing.T) {
	gateway := wisdev.NewAgentGateway(nil, nil, nil)
	session := &wisdev.AgentSession{
		SessionID:      "session-approve",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Answers:        map[string]wisdev.Answer{},
		FailureMemory:  map[string]int{},
		CreatedAt:      wisdev.NowMillis(),
		UpdatedAt:      wisdev.NowMillis(),
		Plan: &wisdev.PlanState{
			ApprovedStepIDs:          map[string]bool{},
			CompletedStepIDs:         map[string]bool{},
			FailedStepIDs:            map[string]string{},
			PendingApprovalID:        "approval-1",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-1"),
			PendingApprovalStepID:    "step-1",
			PendingApprovalExpiresAt: wisdev.NowMillis() + 60_000,
		},
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	handler := NewGatewayHandler(gateway)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions/session-approve/resume", bytes.NewBufferString(`{"approvalToken":"tok-1","action":"approve"}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleResumeSession(rec, req, session.SessionID)

	assert.Equal(t, http.StatusOK, rec.Code)
	updated, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Contains(t, []wisdev.SessionStatus{
		wisdev.SessionExecutingPlan,
		wisdev.SessionComplete,
	}, updated.Status)
	assert.True(t, updated.Plan.ApprovedStepIDs["step-1"])
	assert.Empty(t, updated.Plan.PendingApprovalID)
}

func TestGatewayHandleResumeSession_RestoresPendingApprovalWhenExecutionStartFails(t *testing.T) {
	localExec := &mockExecutionService{}
	gateway := &wisdev.AgentGateway{
		Store:      wisdev.NewInMemorySessionStore(),
		Execution:  localExec,
		SessionTTL: time.Hour,
	}
	originalUpdatedAt := wisdev.NowMillis()
	session := &wisdev.AgentSession{
		SessionID:      "session-start-fail",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Answers:        map[string]wisdev.Answer{},
		FailureMemory:  map[string]int{},
		CreatedAt:      originalUpdatedAt,
		UpdatedAt:      originalUpdatedAt,
		Plan: &wisdev.PlanState{
			ApprovedStepIDs:          map[string]bool{},
			CompletedStepIDs:         map[string]bool{},
			FailedStepIDs:            map[string]string{},
			PendingApprovalID:        "approval-start-fail",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-start-fail"),
			PendingApprovalStepID:    "step-start-fail",
			PendingApprovalExpiresAt: wisdev.NowMillis() + 60_000,
		},
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))
	localExec.On("Start", mock.Anything, session.SessionID).Return((*wisdev.ExecutionStartResult)(nil), errors.New("start failed")).Once()

	handler := NewGatewayHandler(gateway)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions/session-start-fail/resume", bytes.NewBufferString(`{"approvalToken":"tok-start-fail","action":"approve"}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleResumeSession(rec, req, session.SessionID)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	updated, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, wisdev.SessionPaused, updated.Status)
	assert.Empty(t, updated.Plan.ApprovedStepIDs)
	assert.Empty(t, updated.Plan.CompletedStepIDs)
	assert.Empty(t, updated.Plan.FailedStepIDs)
	assert.Equal(t, "approval-start-fail", updated.Plan.PendingApprovalID)
	assert.Equal(t, "step-start-fail", updated.Plan.PendingApprovalStepID)
	assert.Equal(t, wisdev.HashApprovalToken("tok-start-fail"), updated.Plan.PendingApprovalTokenHash)
	assert.Positive(t, updated.Plan.PendingApprovalExpiresAt)
	assert.Equal(t, originalUpdatedAt, updated.UpdatedAt)
}

func TestGatewayHandleResumeSession_RestoresNilPlanWhenExecutionStartFails(t *testing.T) {
	localExec := &mockExecutionService{}
	gateway := &wisdev.AgentGateway{
		Store:      wisdev.NewInMemorySessionStore(),
		Execution:  localExec,
		SessionTTL: time.Hour,
	}
	session := &wisdev.AgentSession{
		SessionID:      "session-start-fail-nil-plan",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Answers:        map[string]wisdev.Answer{},
		FailureMemory:  map[string]int{},
		CreatedAt:      wisdev.NowMillis(),
		UpdatedAt:      wisdev.NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))
	localExec.On("Start", mock.Anything, session.SessionID).Return((*wisdev.ExecutionStartResult)(nil), errors.New("start failed")).Once()

	handler := NewGatewayHandler(gateway)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions/session-start-fail-nil-plan/resume", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleResumeSession(rec, req, session.SessionID)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	updated, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, wisdev.SessionPaused, updated.Status)
	assert.Nil(t, updated.Plan)
}

func TestGatewayHandleResumeSession_RejectsMissingApprovalToken(t *testing.T) {
	gateway := wisdev.NewAgentGateway(nil, nil, nil)
	session := &wisdev.AgentSession{
		SessionID:      "session-missing-token",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Answers:        map[string]wisdev.Answer{},
		FailureMemory:  map[string]int{},
		CreatedAt:      wisdev.NowMillis(),
		UpdatedAt:      wisdev.NowMillis(),
		Plan: &wisdev.PlanState{
			ApprovedStepIDs:          map[string]bool{},
			CompletedStepIDs:         map[string]bool{},
			FailedStepIDs:            map[string]string{},
			PendingApprovalID:        "approval-2",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-2"),
			PendingApprovalStepID:    "step-2",
			PendingApprovalExpiresAt: wisdev.NowMillis() + 60_000,
		},
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	handler := NewGatewayHandler(gateway)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions/session-missing-token/resume", bytes.NewBufferString(`{"action":"approve"}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleResumeSession(rec, req, session.SessionID)

	assert.Equal(t, http.StatusConflict, rec.Code)
	updated, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, wisdev.SessionPaused, updated.Status)
	assert.Empty(t, updated.Plan.ApprovedStepIDs)
	assert.Equal(t, "approval-2", updated.Plan.PendingApprovalID)
	assert.Equal(t, "step-2", updated.Plan.PendingApprovalStepID)
	assert.Equal(t, wisdev.HashApprovalToken("tok-2"), updated.Plan.PendingApprovalTokenHash)
	assert.Positive(t, updated.Plan.PendingApprovalExpiresAt)
}

func TestGatewayHandleResumeSession_InvalidBodyDoesNotMutateSession(t *testing.T) {
	gateway := wisdev.NewAgentGateway(nil, nil, nil)
	session := &wisdev.AgentSession{
		SessionID:      "session-invalid-body",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Answers:        map[string]wisdev.Answer{},
		FailureMemory:  map[string]int{},
		CreatedAt:      wisdev.NowMillis(),
		UpdatedAt:      wisdev.NowMillis(),
		Plan: &wisdev.PlanState{
			ApprovedStepIDs:          map[string]bool{},
			CompletedStepIDs:         map[string]bool{},
			FailedStepIDs:            map[string]string{},
			PendingApprovalID:        "approval-invalid-body",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-invalid-body"),
			PendingApprovalStepID:    "step-invalid-body",
			PendingApprovalExpiresAt: wisdev.NowMillis() + 60_000,
		},
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	handler := NewGatewayHandler(gateway)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions/session-invalid-body/resume", bytes.NewBufferString(`{bad`))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleResumeSession(rec, req, session.SessionID)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp APIError
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, ErrBadRequest, resp.Error.Code)

	updated, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, wisdev.SessionPaused, updated.Status)
	assert.Empty(t, updated.Plan.ApprovedStepIDs)
	assert.Empty(t, updated.Plan.CompletedStepIDs)
	assert.Empty(t, updated.Plan.FailedStepIDs)
	assert.Equal(t, "approval-invalid-body", updated.Plan.PendingApprovalID)
	assert.Equal(t, "step-invalid-body", updated.Plan.PendingApprovalStepID)
	assert.Equal(t, wisdev.HashApprovalToken("tok-invalid-body"), updated.Plan.PendingApprovalTokenHash)
	assert.Positive(t, updated.Plan.PendingApprovalExpiresAt)
}

func TestGatewayHandleResumeSession_RejectsExpiredApprovalTokenWithoutTokenAndClearsCheckpoint(t *testing.T) {
	gateway := wisdev.NewAgentGateway(nil, nil, nil)
	session := &wisdev.AgentSession{
		SessionID:      "session-expired-without-token",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Answers:        map[string]wisdev.Answer{},
		FailureMemory:  map[string]int{},
		CreatedAt:      wisdev.NowMillis(),
		UpdatedAt:      wisdev.NowMillis(),
		Plan: &wisdev.PlanState{
			ApprovedStepIDs:          map[string]bool{},
			CompletedStepIDs:         map[string]bool{},
			FailedStepIDs:            map[string]string{},
			PendingApprovalID:        "approval-expired-no-token",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-expired-no-token"),
			PendingApprovalStepID:    "step-expired-no-token",
			PendingApprovalExpiresAt: wisdev.NowMillis() - 1,
		},
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	handler := NewGatewayHandler(gateway)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions/session-expired-without-token/resume", bytes.NewBufferString(`{"action":"approve"}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleResumeSession(rec, req, session.SessionID)

	assert.Equal(t, http.StatusConflict, rec.Code)
	updated, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, wisdev.SessionPaused, updated.Status)
	assert.Empty(t, updated.Plan.PendingApprovalID)
	assert.Empty(t, updated.Plan.PendingApprovalStepID)
	assert.Empty(t, updated.Plan.PendingApprovalTokenHash)
	assert.Zero(t, updated.Plan.PendingApprovalExpiresAt)
}

func TestGatewayHandleResumeSession_RejectAndReplanMarksFailedStep(t *testing.T) {
	gateway := wisdev.NewAgentGateway(nil, nil, nil)
	session := &wisdev.AgentSession{
		SessionID:      "session-replan",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Answers:        map[string]wisdev.Answer{},
		FailureMemory:  map[string]int{},
		CreatedAt:      wisdev.NowMillis(),
		UpdatedAt:      wisdev.NowMillis(),
		Plan: &wisdev.PlanState{
			ApprovedStepIDs:          map[string]bool{},
			CompletedStepIDs:         map[string]bool{},
			FailedStepIDs:            map[string]string{},
			PendingApprovalID:        "approval-3",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-3"),
			PendingApprovalStepID:    "step-3",
			PendingApprovalExpiresAt: wisdev.NowMillis() + 60_000,
		},
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	handler := NewGatewayHandler(gateway)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions/session-replan/resume", bytes.NewBufferString(`{"approvalToken":"tok-3","action":"reject_and_replan"}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleResumeSession(rec, req, session.SessionID)

	assert.Equal(t, http.StatusOK, rec.Code)
	updated, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, "human_rejected_replan", updated.Plan.FailedStepIDs["step-3"])
	assert.Equal(t, wisdev.SessionExecutingPlan, updated.Status)
}

func TestGatewayHandleResumeSession_RejectsExpiredApprovalToken(t *testing.T) {
	gateway := wisdev.NewAgentGateway(nil, nil, nil)
	session := &wisdev.AgentSession{
		SessionID:      "session-expired-token",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Answers:        map[string]wisdev.Answer{},
		FailureMemory:  map[string]int{},
		CreatedAt:      wisdev.NowMillis(),
		UpdatedAt:      wisdev.NowMillis(),
		Plan: &wisdev.PlanState{
			ApprovedStepIDs:          map[string]bool{},
			CompletedStepIDs:         map[string]bool{},
			FailedStepIDs:            map[string]string{},
			PendingApprovalID:        "approval-expired",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-expired"),
			PendingApprovalStepID:    "step-expired",
			PendingApprovalExpiresAt: wisdev.NowMillis() - 1,
		},
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	handler := NewGatewayHandler(gateway)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions/session-expired-token/resume", bytes.NewBufferString(`{"approvalToken":"tok-expired","action":"approve"}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleResumeSession(rec, req, session.SessionID)

	assert.Equal(t, http.StatusConflict, rec.Code)
	updated, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, wisdev.SessionPaused, updated.Status)
	assert.Empty(t, updated.Plan.ApprovedStepIDs)
	assert.Empty(t, updated.Plan.PendingApprovalID)
	assert.Empty(t, updated.Plan.PendingApprovalStepID)
	assert.Empty(t, updated.Plan.PendingApprovalTokenHash)
	assert.Zero(t, updated.Plan.PendingApprovalExpiresAt)
}
