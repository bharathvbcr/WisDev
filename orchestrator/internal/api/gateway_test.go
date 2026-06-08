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

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

type errorStore struct {
	wisdev.SessionStore
}

func (s *errorStore) Get(ctx context.Context, id string) (*wisdev.AgentSession, error) {
	return nil, errors.New("get error")
}
func (s *errorStore) Put(ctx context.Context, session *wisdev.AgentSession, ttl time.Duration) error {
	return errors.New("put error")
}

type minimalistResponseWriter struct {
	http.ResponseWriter
}

func withUser(req *http.Request, userID string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
}

func TestGatewayHandler(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:      wisdev.NewInMemorySessionStore(),
		Registry:   wisdev.NewToolRegistry(),
		SessionTTL: 1 * time.Hour,
	}
	handler := NewGatewayHandler(gw)

	t.Run("RegisterRoutes", func(t *testing.T) {
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
	})

	t.Run("HandleSessions - Success", func(t *testing.T) {
		reqBody := `{"userId":"user123", "originalQuery":"test query"}`
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions", strings.NewReader(reqBody)), "user123")
		rec := httptest.NewRecorder()
		handler.HandleSessions(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSessions - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/agent/sessions", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessions(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessions - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/agent/sessions", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		handler.HandleSessions(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessions - Create Error", func(t *testing.T) {
		gwErr := &wisdev.AgentGateway{Store: &errorStore{}}
		hErr := NewGatewayHandler(gwErr)
		reqBody := `{"userId":"user123", "originalQuery":"test query"}`
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions", strings.NewReader(reqBody)), "user123")
		rec := httptest.NewRecorder()
		hErr.HandleSessions(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrWisdevFailed, resp.Error.Code)
	})

	t.Run("HandleSessions - Empty Query", func(t *testing.T) {
		reqBody := `{"userId":"user123", "originalQuery":"   "}`
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions", strings.NewReader(reqBody)), "user123")
		rec := httptest.NewRecorder()
		handler.HandleSessions(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
		assert.Equal(t, "query is required", resp.Error.Message)
	})

	t.Run("HandleSessionByID - Dispatch Execute - Success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Status = wisdev.SessionGeneratingTree
		session.Plan = &wisdev.PlanState{PlanID: "p1"}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/execute", nil), "u1")
		rec := httptest.NewRecorder()
		gw.Executor = wisdev.NewPlanExecutor(nil, policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil)
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSessionByID - Dispatch Execute - Session Not Found", func(t *testing.T) {
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/nosession/execute", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Execute - Missing Query Contract", func(t *testing.T) {
		session := &wisdev.AgentSession{
			SessionID: "sid-missing-query",
			UserID:    "u1",
			Status:    wisdev.SessionGeneratingTree,
			Plan:      &wisdev.PlanState{PlanID: "p1"},
			CreatedAt: wisdev.NowMillis(),
			UpdatedAt: wisdev.NowMillis(),
		}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/execute", nil), "u1")
		rec := httptest.NewRecorder()
		gw.Executor = wisdev.NewPlanExecutor(nil, policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil)
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
		assert.Equal(t, "session query is required", resp.Error.Message)
	})

	t.Run("HandleSessionByID - Dispatch Events - Stream Unsupported", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		req := withUser(httptest.NewRequest(http.MethodGet, "/agent/sessions/"+session.SessionID+"/events", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(&minimalistResponseWriter{ResponseWriter: rec}, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInternal, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Execute - Wrong Method", func(t *testing.T) {
		req := withUser(httptest.NewRequest(http.MethodGet, "/agent/sessions/sid/execute", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Cancel - Success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/cancel", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSessionByID - Dispatch Resume - Success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Status = wisdev.SessionPaused
		session.Plan = &wisdev.PlanState{}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)
		gw.Executor = wisdev.NewPlanExecutor(nil, policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil)
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSessionByID - Dispatch Resume - Missing Query Contract", func(t *testing.T) {
		session := &wisdev.AgentSession{
			SessionID: "sid-resume-missing-query",
			UserID:    "u1",
			Status:    wisdev.SessionPaused,
			Plan:      &wisdev.PlanState{},
			CreatedAt: wisdev.NowMillis(),
			UpdatedAt: wisdev.NowMillis(),
		}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/resume", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
		assert.Equal(t, "session query is required", resp.Error.Message)
	})

	t.Run("HandleSessionByID - Dispatch Abandon - Success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/"+session.SessionID+"/abandon", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSessionByID - Dispatch Cancel - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/agent/sessions/sid/cancel", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Resume - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/agent/sessions/sid/resume", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Abandon - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/agent/sessions/sid/abandon", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Cancel - Session Not Found", func(t *testing.T) {
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/nosession/cancel", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Resume - Session Not Found", func(t *testing.T) {
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/nosession/resume", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Abandon - Session Not Found", func(t *testing.T) {
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/nosession/abandon", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleSessionByID - NotFound (short path)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/agent/sessions/", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Invalid SessionID (empty)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/agent/sessions//execute", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Unknown Action", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/agent/sessions/sid/unknown", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleTools", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/agent/tools", nil)
		rec := httptest.NewRecorder()
		handler.HandleTools(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleAgentCard", func(t *testing.T) {
		gw.ADKRuntime = &wisdev.ADKRuntime{
			Config: wisdev.DefaultADKRuntimeConfig(),
		}
		req := httptest.NewRequest(http.MethodGet, "/agent/card", nil)
		req.Host = "localhost:8080"
		rec := httptest.NewRecorder()
		handler.HandleAgentCard(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		card, ok := payload["agentCard"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, "http-json", card["preferredTransport"])
		assert.Equal(t, "http://localhost:8080/agent/card", card["url"])
	})

	t.Run("HandleAgentCard WellKnown Path", func(t *testing.T) {
		gw.ADKRuntime = &wisdev.ADKRuntime{
			Config: wisdev.DefaultADKRuntimeConfig(),
		}
		req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
		req.Host = "localhost:8080"
		rec := httptest.NewRecorder()
		handler.HandleAgentCard(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var payload map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
		card, ok := payload["agentCard"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, "http://localhost:8080/.well-known/agent-card.json", card["url"])
	})
}

type flexibleStore struct {
	wisdev.SessionStore
	getFn func(string) (*wisdev.AgentSession, error)
	putFn func(*wisdev.AgentSession) error
}

func (s *flexibleStore) Get(ctx context.Context, id string) (*wisdev.AgentSession, error) {
	return s.getFn(id)
}
func (s *flexibleStore) Put(ctx context.Context, session *wisdev.AgentSession, ttl time.Duration) error {
	return s.putFn(session)
}

func TestGatewayHandlerExtra(t *testing.T) {
	session := &wisdev.AgentSession{
		SessionID:      "s1",
		UserID:         "u1",
		Query:          "sleep memory replay",
		OriginalQuery:  "sleep memory replay",
		CorrectedQuery: "sleep memory replay",
	}
	fs := &flexibleStore{
		getFn: func(id string) (*wisdev.AgentSession, error) { return session, nil },
		putFn: func(s *wisdev.AgentSession) error { return errors.New("put error") },
	}
	gw := &wisdev.AgentGateway{Store: fs, SessionTTL: 1 * time.Hour}
	gw.Executor = wisdev.NewPlanExecutor(nil, policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil)
	handler := NewGatewayHandler(gw)

	t.Run("Cancel Persist Error", func(t *testing.T) {
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/s1/cancel", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("Resume Persist Error", func(t *testing.T) {
		session.Status = wisdev.SessionPaused
		session.Plan = &wisdev.PlanState{}
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/s1/resume", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("Abandon Persist Error", func(t *testing.T) {
		session.Status = wisdev.SessionExecutingPlan
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/s1/abandon", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("Execute Persist Error", func(t *testing.T) {
		session.Plan = nil // Force BuildDefaultPlan and Put
		session.Status = wisdev.SessionGeneratingTree
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/s1/execute", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("Resume Success (no plan)", func(t *testing.T) {
		session.Status = wisdev.SessionPaused
		session.Plan = nil
		fs.putFn = func(s *wisdev.AgentSession) error { return nil }
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/s1/resume", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("Resume Success (with existing plan)", func(t *testing.T) {
		session.Status = wisdev.SessionPaused
		session.Plan = &wisdev.PlanState{}
		req := withUser(httptest.NewRequest(http.MethodPost, "/agent/sessions/s1/resume", nil), "u1")
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

type flushRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushRecorder) Flush() {
}
