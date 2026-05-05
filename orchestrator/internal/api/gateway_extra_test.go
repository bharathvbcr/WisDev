package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockExecutionService struct {
	mock.Mock
}

func (m *mockExecutionService) Start(ctx context.Context, sessionID string) (*wisdev.ExecutionStartResult, error) {
	args := m.Called(ctx, sessionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*wisdev.ExecutionStartResult), args.Error(1)
}

func (m *mockExecutionService) Cancel(ctx context.Context, sessionID string) error {
	args := m.Called(ctx, sessionID)
	return args.Error(0)
}

func (m *mockExecutionService) Abandon(ctx context.Context, sessionID string) error {
	args := m.Called(ctx, sessionID)
	return args.Error(0)
}

func (m *mockExecutionService) Stream(ctx context.Context, sessionID string, emit func(wisdev.PlanExecutionEvent) error) error {
	args := m.Called(ctx, sessionID, emit)
	return args.Error(0)
}

func TestGatewayHandler_Extra(t *testing.T) {
	mockExec := &mockExecutionService{}
	gw := &wisdev.AgentGateway{
		Store:      wisdev.NewInMemorySessionStore(),
		Execution:  mockExec,
		SessionTTL: 1 * time.Hour,
	}
	handler := NewGatewayHandler(gw)

	t.Run("handleAgentCard - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/agent/card", nil)
		rec := httptest.NewRecorder()
		handler.HandleAgentCard(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("handleAgentCard - Runtime Missing", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/agent/card", nil)
		rec := httptest.NewRecorder()
		NewGatewayHandler(&wisdev.AgentGateway{}).HandleAgentCard(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("handleAgentCard - Not Exposed", func(t *testing.T) {
		localGW := &wisdev.AgentGateway{
			ADKRuntime: &wisdev.ADKRuntime{
				Config: wisdev.ADKRuntimeConfig{
					A2A: wisdev.ADKA2AConfig{
						Enabled:         true,
						ExposeAgentCard: false,
					},
				},
			},
		}
		req := httptest.NewRequest(http.MethodGet, "/agent/card", nil)
		rec := httptest.NewRecorder()
		NewGatewayHandler(localGW).HandleAgentCard(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("handleAgentCard - Forwarded Proto", func(t *testing.T) {
		localGW := &wisdev.AgentGateway{
			ADKRuntime: &wisdev.ADKRuntime{
				Config: wisdev.DefaultADKRuntimeConfig(),
			},
		}
		req := httptest.NewRequest(http.MethodGet, "/agent/card", nil)
		req.Host = "example.test"
		req.Header.Set("X-Forwarded-Proto", "https")
		rec := httptest.NewRecorder()
		NewGatewayHandler(localGW).HandleAgentCard(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		card := payload["agentCard"].(map[string]any)
		assert.Equal(t, "https://example.test/agent/card", card["url"])
	})

	t.Run("handleSessions - Missing UserID", func(t *testing.T) {
		reqBody := `{"originalQuery":"test query"}`
		req := httptest.NewRequest(http.MethodPost, "/agent/sessions", strings.NewReader(reqBody))
		rec := httptest.NewRecorder()
		handler.HandleSessions(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		session := resp["session"].(map[string]any)
		assert.Equal(t, "anonymous", session["userId"])
	})

	t.Run("handleSessions - Access Denied", func(t *testing.T) {
		reqBody := `{"userId":"user123", "originalQuery":"test query"}`
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions", strings.NewReader(reqBody)), "other-user")
		rec := httptest.NewRecorder()
		handler.HandleSessions(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("handleSessions - Idempotency Hit", func(t *testing.T) {
		localGW := &wisdev.AgentGateway{
			Store:       wisdev.NewInMemorySessionStore(),
			Registry:    wisdev.NewToolRegistry(),
			Idempotency: wisdev.NewIdempotencyStore(time.Minute),
			SessionTTL:  1 * time.Hour,
		}
		localHandler := NewGatewayHandler(localGW)
		reqBody := `{"userId":"user123","originalQuery":"test query"}`
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions", strings.NewReader(reqBody)), "user123")
		req.Header.Set("Idempotency-Key", "idem-1")
		key := localHandler.idempotencyKey(req, "user123", "test query")
		localGW.Idempotency.Put(key, http.StatusOK, map[string]any{
			"session": map[string]any{"sessionId": "cached-session"},
		})
		rec := httptest.NewRecorder()
		localHandler.HandleSessions(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "cached-session")
	})

	t.Run("handleAbandonSession - Success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		mockExec.On("Abandon", mock.Anything, session.SessionID).Run(func(args mock.Arguments) {
			loaded, err := gw.Store.Get(context.Background(), session.SessionID)
			if err == nil {
				loaded.Status = wisdev.StatusAbandoned
				loaded.UpdatedAt = wisdev.NowMillis()
				_ = gw.Store.Put(context.Background(), loaded, gw.SessionTTL)
			}
		}).Return(nil).Once()

		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/abandon", nil), "u1")
		rec := httptest.NewRecorder()

		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.True(t, resp["ok"].(bool))
		assert.Equal(t, string(wisdev.StatusAbandoned), resp["status"])

		updated, _ := gw.Store.Get(context.Background(), session.SessionID)
		assert.Equal(t, wisdev.StatusAbandoned, updated.Status)
	})

	t.Run("handleAbandonSession - Not Found", func(t *testing.T) {
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/nonexistent/abandon", nil), "u1")
		rec := httptest.NewRecorder()

		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("handleStreamSessionEvents - Success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		mockExec.On("Stream", mock.Anything, session.SessionID, mock.Anything).Run(func(args mock.Arguments) {
			emit := args.Get(2).(func(wisdev.PlanExecutionEvent) error)
			_ = emit(wisdev.PlanExecutionEvent{Type: wisdev.EventProgress, Message: "hello"})
		}).Return(nil).Once()

		req := withUser(httptest.NewRequest(http.MethodGet, "/agent/sessions/"+session.SessionID+"/events", nil), "u1")
		rec := httptest.NewRecorder()

		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
		assert.Contains(t, rec.Body.String(), "event: progress")
		assert.Contains(t, rec.Body.String(), "hello")
	})

	t.Run("handleStreamSessionEvents - Missing Flusher", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)
		req := withUser(httptest.NewRequest(http.MethodGet, "/agent/sessions/"+session.SessionID+"/events", nil), "u1")
		rec := httptest.NewRecorder()
		handler.handleStreamSessionEvents(&minimalistResponseWriter{ResponseWriter: rec}, req, session.SessionID)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("handleStreamSessionEvents - Stream Error", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)
		mockExec.ExpectedCalls = nil
		mockExec.On("Stream", mock.Anything, session.SessionID, mock.Anything).Return(errors.New("stream failed")).Once()
		req := withUser(httptest.NewRequest(http.MethodGet, "/agent/sessions/"+session.SessionID+"/events", nil), "u1")
		rec := httptest.NewRecorder()
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("handleStreamSessionEvents - Stream Error After Event", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)
		mockExec.ExpectedCalls = nil
		mockExec.On("Stream", mock.Anything, session.SessionID, mock.Anything).Run(func(args mock.Arguments) {
			emit := args.Get(2).(func(wisdev.PlanExecutionEvent) error)
			_ = emit(wisdev.PlanExecutionEvent{Type: wisdev.EventProgress, Message: "hello"})
		}).Return(errors.New("stream failed after event")).Once()
		req := withUser(httptest.NewRequest(http.MethodGet, "/agent/sessions/"+session.SessionID+"/events", nil), "u1")
		rec := httptest.NewRecorder()
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
		assert.Contains(t, rec.Body.String(), "event: progress")
		assert.NotContains(t, rec.Body.String(), "\"error\"")
	})

	t.Run("handleStreamSessionEvents - Method Not Allowed", func(t *testing.T) {
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/s1/events", nil), "u1")
		rec := httptest.NewRecorder()

		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("handleExecuteSession - Idempotency Hit", func(t *testing.T) {
		localExec := &mockExecutionService{}
		localGW := &wisdev.AgentGateway{
			Store:       wisdev.NewInMemorySessionStore(),
			Execution:   localExec,
			Idempotency: wisdev.NewIdempotencyStore(time.Minute),
			SessionTTL:  1 * time.Hour,
		}
		localHandler := NewGatewayHandler(localGW)
		session, _ := localGW.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{PlanID: "plan-1"}
		session.Status = wisdev.SessionGeneratingTree
		_ = localGW.Store.Put(context.Background(), session, localGW.SessionTTL)
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/execute", nil), "u1")
		req.Header.Set("Idempotency-Key", "exec-1")
		key := localHandler.idempotencyKey(req, "u1", session.SessionID)
		localGW.Idempotency.Put(key, http.StatusOK, map[string]any{
			"ok":        true,
			"sessionId": session.SessionID,
			"status":    string(wisdev.SessionExecutingPlan),
		})
		rec := httptest.NewRecorder()
		localHandler.handleExecuteSession(rec, req, session.SessionID)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), session.SessionID)
	})

	t.Run("handleExecuteSession - Start Error", func(t *testing.T) {
		localExec := &mockExecutionService{}
		localGW := &wisdev.AgentGateway{
			Store:      wisdev.NewInMemorySessionStore(),
			Execution:  localExec,
			SessionTTL: 1 * time.Hour,
		}
		localHandler := NewGatewayHandler(localGW)
		session, _ := localGW.CreateSession(context.Background(), "u1", "q1")
		session.Plan = &wisdev.PlanState{PlanID: "plan-2"}
		session.Status = wisdev.SessionGeneratingTree
		_ = localGW.Store.Put(context.Background(), session, localGW.SessionTTL)
		localExec.On("Start", mock.Anything, session.SessionID).Return((*wisdev.ExecutionStartResult)(nil), errors.New("start failed")).Once()
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/execute", nil), "u1")
		rec := httptest.NewRecorder()
		localHandler.handleExecuteSession(rec, req, session.SessionID)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("handleCancelSession - Idempotency Hit", func(t *testing.T) {
		localExec := &mockExecutionService{}
		localGW := &wisdev.AgentGateway{
			Store:       wisdev.NewInMemorySessionStore(),
			Execution:   localExec,
			Idempotency: wisdev.NewIdempotencyStore(time.Minute),
			SessionTTL:  1 * time.Hour,
		}
		localHandler := NewGatewayHandler(localGW)
		session, _ := localGW.CreateSession(context.Background(), "u1", "q1")
		_ = localGW.Store.Put(context.Background(), session, localGW.SessionTTL)
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/cancel", nil), "u1")
		req.Header.Set("Idempotency-Key", "cancel-1")
		key := localHandler.idempotencyKey(req, "u1", session.SessionID)
		localGW.Idempotency.Put(key, http.StatusOK, map[string]any{
			"ok":        true,
			"sessionId": session.SessionID,
			"status":    string(wisdev.SessionPaused),
		})
		rec := httptest.NewRecorder()
		localHandler.handleCancelSession(rec, req, session.SessionID)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), session.SessionID)
	})

	t.Run("handleCancelSession - Cancel Error", func(t *testing.T) {
		localExec := &mockExecutionService{}
		localGW := &wisdev.AgentGateway{
			Store:      wisdev.NewInMemorySessionStore(),
			Execution:  localExec,
			SessionTTL: 1 * time.Hour,
		}
		localHandler := NewGatewayHandler(localGW)
		session, _ := localGW.CreateSession(context.Background(), "u1", "q1")
		_ = localGW.Store.Put(context.Background(), session, localGW.SessionTTL)
		localExec.On("Cancel", mock.Anything, session.SessionID).Return(errors.New("cancel failed")).Once()
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/cancel", nil), "u1")
		rec := httptest.NewRecorder()
		localHandler.handleCancelSession(rec, req, session.SessionID)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("handleExecuteSession - Not Found", func(t *testing.T) {
		localExec := &mockExecutionService{}
		localGW := &wisdev.AgentGateway{
			Store:       wisdev.NewInMemorySessionStore(),
			Execution:   localExec,
			Idempotency: wisdev.NewIdempotencyStore(time.Minute),
			SessionTTL:  1 * time.Hour,
		}
		localHandler := NewGatewayHandler(localGW)
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/missing/execute", nil), "u1")
		rec := httptest.NewRecorder()
		localHandler.handleExecuteSession(rec, req, "missing")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("handleCancelSession - Not Found", func(t *testing.T) {
		localExec := &mockExecutionService{}
		localGW := &wisdev.AgentGateway{
			Store:       wisdev.NewInMemorySessionStore(),
			Execution:   localExec,
			Idempotency: wisdev.NewIdempotencyStore(time.Minute),
			SessionTTL:  1 * time.Hour,
		}
		localHandler := NewGatewayHandler(localGW)
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/missing/cancel", nil), "u1")
		rec := httptest.NewRecorder()
		localHandler.handleCancelSession(rec, req, "missing")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("handleResumeSession - Approval Validation Paths", func(t *testing.T) {
		localExec := &mockExecutionService{}
		localGW := &wisdev.AgentGateway{
			Store:      wisdev.NewInMemorySessionStore(),
			Execution:  localExec,
			SessionTTL: 1 * time.Hour,
		}
		localHandler := NewGatewayHandler(localGW)
		makeSession := func() *wisdev.AgentSession {
			session, _ := localGW.CreateSession(context.Background(), "u1", "q1")
			session.Status = wisdev.SessionPaused
			session.Plan = &wisdev.PlanState{
				PlanID:                   "plan-3",
				PendingApprovalID:        "approval-1",
				PendingApprovalStepID:    "step-1",
				PendingApprovalTokenHash: wisdev.HashApprovalToken("token-1"),
				PendingApprovalExpiresAt: wisdev.NowMillis() + int64(time.Minute/time.Millisecond),
			}
			_ = localGW.Store.Put(context.Background(), session, localGW.SessionTTL)
			return session
		}

		t.Run("missing approval token", func(t *testing.T) {
			session := makeSession()
			req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", strings.NewReader(`{"action":"approve"}`)), "u1")
			rec := httptest.NewRecorder()
			localHandler.handleResumeSession(rec, req, session.SessionID)
			assert.Equal(t, http.StatusConflict, rec.Code)
		})

		t.Run("invalid approval token", func(t *testing.T) {
			session := makeSession()
			req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", strings.NewReader(`{"approvalToken":"wrong","action":"approve"}`)), "u1")
			rec := httptest.NewRecorder()
			localHandler.handleResumeSession(rec, req, session.SessionID)
			assert.Equal(t, http.StatusConflict, rec.Code)
		})

		t.Run("expired approval token", func(t *testing.T) {
			session := makeSession()
			session.Plan.PendingApprovalExpiresAt = wisdev.NowMillis() - 1
			_ = localGW.Store.Put(context.Background(), session, localGW.SessionTTL)
			req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", strings.NewReader(`{"approvalToken":"token-1","action":"approve"}`)), "u1")
			rec := httptest.NewRecorder()
			localHandler.handleResumeSession(rec, req, session.SessionID)
			assert.Equal(t, http.StatusConflict, rec.Code)
		})

		t.Run("unsupported action", func(t *testing.T) {
			session := makeSession()
			req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", strings.NewReader(`{"approvalToken":"token-1","action":"dance"}`)), "u1")
			rec := httptest.NewRecorder()
			localHandler.handleResumeSession(rec, req, session.SessionID)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})

		t.Run("edit payload unsupported", func(t *testing.T) {
			session := makeSession()
			req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", strings.NewReader(`{"approvalToken":"token-1","action":"edit_payload"}`)), "u1")
			rec := httptest.NewRecorder()
			localHandler.handleResumeSession(rec, req, session.SessionID)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})

		t.Run("approval replay without pending confirmation", func(t *testing.T) {
			session, _ := localGW.CreateSession(context.Background(), "u1", "q1")
			session.Status = wisdev.SessionPaused
			session.Plan = &wisdev.PlanState{PlanID: "plan-4"}
			_ = localGW.Store.Put(context.Background(), session, localGW.SessionTTL)
			req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", strings.NewReader(`{"approvalToken":"token-1"}`)), "u1")
			rec := httptest.NewRecorder()
			localHandler.handleResumeSession(rec, req, session.SessionID)
			assert.Equal(t, http.StatusConflict, rec.Code)
		})

		t.Run("success with approval token", func(t *testing.T) {
			session := makeSession()
			localExec.On("Start", mock.Anything, session.SessionID).Return(&wisdev.ExecutionStartResult{Status: wisdev.SessionExecutingPlan}, nil).Once()
			req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", strings.NewReader(`{"approvalToken":"token-1","action":"approve"}`)), "u1")
			rec := httptest.NewRecorder()
			localHandler.handleResumeSession(rec, req, session.SessionID)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), session.SessionID)
		})
	})
}
