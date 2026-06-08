package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type synchronizedFlushRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   bytes.Buffer
	code   int
}

func newSynchronizedFlushRecorder() *synchronizedFlushRecorder {
	return &synchronizedFlushRecorder{header: make(http.Header)}
}

func (r *synchronizedFlushRecorder) Header() http.Header {
	return r.header
}

func (r *synchronizedFlushRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		r.code = statusCode
	}
}

func (r *synchronizedFlushRecorder) Write(payload []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(payload)
}

func (r *synchronizedFlushRecorder) Flush() {}

func (r *synchronizedFlushRecorder) Code() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		return http.StatusOK
	}
	return r.code
}

func (r *synchronizedFlushRecorder) BodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func TestGatewayHandleResumeSession_RejectsApprovalReplayWithoutPendingConfirmation(t *testing.T) {
	gateway := wisdev.NewAgentGateway(nil, nil, nil)
	gateway.Executor = wisdev.NewPlanExecutor(nil, policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil)
	session := &wisdev.AgentSession{
		SessionID:      "session-replay",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Plan: &wisdev.PlanState{
			ApprovedStepIDs:  map[string]bool{"step-1": true},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
		},
		CreatedAt: wisdev.NowMillis(),
		UpdatedAt: wisdev.NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	handler := NewGatewayHandler(gateway)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions/session-replay/resume", bytes.NewBufferString(`{"approvalToken":"tok-replayed","action":"approve"}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleResumeSession(rec, req, session.SessionID)

	assert.Equal(t, http.StatusConflict, rec.Code)
	var resp APIError
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
}

func TestGatewayHandleResumeSession_ResumeIdempotencyReturnsCachedResponse(t *testing.T) {
	gateway := wisdev.NewAgentGateway(nil, nil, nil)
	gateway.Executor = wisdev.NewPlanExecutor(nil, policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil)
	gateway.Idempotency = wisdev.NewIdempotencyStore(time.Hour)
	session := &wisdev.AgentSession{
		SessionID:      "session-idempotent-resume",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Plan: &wisdev.PlanState{
			ApprovedStepIDs:  map[string]bool{},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
		},
		CreatedAt: wisdev.NowMillis(),
		UpdatedAt: wisdev.NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	handler := NewGatewayHandler(gateway)
	buildReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", bytes.NewBufferString(`{"action":"approve"}`))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
		req.Header.Set("Idempotency-Key", "resume-1")
		return req
	}

	first := httptest.NewRecorder()
	handler.handleResumeSession(first, buildReq(), session.SessionID)
	require.Equal(t, http.StatusOK, first.Code)

	updated, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	updated.Status = wisdev.SessionPaused
	require.NoError(t, gateway.Store.Put(context.Background(), updated, gateway.SessionTTL))

	second := httptest.NewRecorder()
	handler.handleResumeSession(second, buildReq(), session.SessionID)
	require.Equal(t, http.StatusOK, second.Code)
	assert.JSONEq(t, first.Body.String(), second.Body.String())
}

func TestGatewayHandleStreamSessionEvents_DoesNotStartExecution(t *testing.T) {
	gateway := wisdev.NewAgentGateway(nil, nil, nil)
	session := &wisdev.AgentSession{
		SessionID:      "session-stream-only",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionPaused,
		Plan: &wisdev.PlanState{
			ApprovedStepIDs:  map[string]bool{},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
		},
		CreatedAt: wisdev.NowMillis(),
		UpdatedAt: wisdev.NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	execution := wisdev.NewDurableExecutionService(gateway)
	gateway.Execution = execution
	handler := NewGatewayHandler(gateway)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/agent/sessions/"+session.SessionID+"/events", nil).WithContext(context.WithValue(ctx, ctxUserID, "user-1"))
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.handleStreamSessionEvents(rec, req, session.SessionID)
	}()

	time.Sleep(350 * time.Millisecond)
	assert.False(t, execution.IsActive(session.SessionID))
	cancel()
	<-done
}

func TestGatewayHandleStreamSessionEvents_SendsKeepAliveWhileIdle(t *testing.T) {
	previousInterval := agentSessionEventKeepAliveInterval
	agentSessionEventKeepAliveInterval = 10 * time.Millisecond
	defer func() {
		agentSessionEventKeepAliveInterval = previousInterval
	}()

	mockExec := &mockExecutionService{}
	gateway := &wisdev.AgentGateway{
		Store:      wisdev.NewInMemorySessionStore(),
		Execution:  mockExec,
		SessionTTL: time.Hour,
	}
	session := &wisdev.AgentSession{
		SessionID:      "session-stream-keepalive",
		UserID:         "user-1",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
		Status:         wisdev.SessionExecutingPlan,
		CreatedAt:      wisdev.NowMillis(),
		UpdatedAt:      wisdev.NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	started := make(chan struct{})
	mockExec.On("Stream", mock.Anything, session.SessionID, mock.Anything).Run(func(args mock.Arguments) {
		close(started)
		<-args.Get(0).(context.Context).Done()
	}).Return(nil).Once()

	handler := NewGatewayHandler(gateway)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/agent/sessions/"+session.SessionID+"/events", nil).WithContext(context.WithValue(ctx, ctxUserID, "user-1"))
	rec := newSynchronizedFlushRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.handleStreamSessionEvents(rec, req, session.SessionID)
	}()

	<-started
	require.Eventually(t, func() bool {
		return strings.Contains(rec.BodyString(), ": keepalive")
	}, 250*time.Millisecond, 10*time.Millisecond)
	cancel()
	<-done

	assert.Equal(t, http.StatusOK, rec.Code())
	mockExec.AssertExpectations(t)
}

func TestGatewayHandleResumeSession_MissingQueryDoesNotMutateSession(t *testing.T) {
	gateway := wisdev.NewAgentGateway(nil, nil, nil)
	gateway.Executor = wisdev.NewPlanExecutor(nil, policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil)
	session := &wisdev.AgentSession{
		SessionID: "session-missing-query",
		UserID:    "user-1",
		Status:    wisdev.SessionPaused,
		Plan: &wisdev.PlanState{
			PendingApprovalID:        "approval-1",
			PendingApprovalStepID:    "step-1",
			PendingApprovalTokenHash: wisdev.HashApprovalToken("tok-1"),
			ApprovedStepIDs:          map[string]bool{},
			CompletedStepIDs:         map[string]bool{},
			FailedStepIDs:            map[string]string{},
		},
		CreatedAt: wisdev.NowMillis(),
		UpdatedAt: wisdev.NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	handler := NewGatewayHandler(gateway)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", bytes.NewBufferString(`{"approvalToken":"tok-1","action":"approve"}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "user-1"))
	rec := httptest.NewRecorder()

	handler.handleResumeSession(rec, req, session.SessionID)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp APIError
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	assert.Equal(t, "session query is required", resp.Error.Message)

	updated, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, wisdev.SessionPaused, updated.Status)
	assert.Equal(t, "approval-1", updated.Plan.PendingApprovalID)
	assert.Equal(t, "step-1", updated.Plan.PendingApprovalStepID)
	assert.Equal(t, wisdev.HashApprovalToken("tok-1"), updated.Plan.PendingApprovalTokenHash)
	assert.Empty(t, updated.Plan.ApprovedStepIDs)
}
