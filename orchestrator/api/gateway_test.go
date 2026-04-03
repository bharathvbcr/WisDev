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

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"

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
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions", strings.NewReader(reqBody))
		rec := httptest.NewRecorder()
		handler.HandleSessions(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSessions - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessions(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessions - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions", strings.NewReader(`{invalid`))
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
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions", strings.NewReader(reqBody))
		rec := httptest.NewRecorder()
		hErr.HandleSessions(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrWisdevFailed, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Execute - Success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		session.Status = wisdev.SessionGeneratingTree
		session.Plan = &wisdev.PlanState{PlanID: "p1"}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions/"+session.SessionID+"/execute", nil)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		gw.Executor = wisdev.NewPlanExecutor(nil, policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil)
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSessionByID - Dispatch Execute - Session Not Found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions/nosession/execute", nil)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Execute - Stream Unsupported", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions/"+session.SessionID+"/execute", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(&minimalistResponseWriter{ResponseWriter: rec}, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInternal, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Execute - Wrong Method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/sid/execute", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Cancel - Success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/"+session.SessionID+"/cancel", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSessionByID - Dispatch Resume - Success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/"+session.SessionID+"/resume", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSessionByID - Dispatch Abandon - Success", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "q1")
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/"+session.SessionID+"/abandon", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("HandleSessionByID - Dispatch Cancel - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions/sid/cancel", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Resume - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions/sid/resume", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Abandon - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions/sid/abandon", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Cancel - Session Not Found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/nosession/cancel", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Resume - Session Not Found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/nosession/resume", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Dispatch Abandon - Session Not Found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/nosession/abandon", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleSessionByID - NotFound (short path)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions/", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Invalid SessionID (empty)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions//execute", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleSessionByID - Unknown Action", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions/sid/unknown", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrNotFound, resp.Error.Code)
	})

	t.Run("HandleTools", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/tools", nil)
		rec := httptest.NewRecorder()
		handler.HandleTools(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
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
	session := &wisdev.AgentSession{SessionID: "s1"}
	fs := &flexibleStore{
		getFn: func(id string) (*wisdev.AgentSession, error) { return session, nil },
		putFn: func(s *wisdev.AgentSession) error { return errors.New("put error") },
	}
	gw := &wisdev.AgentGateway{Store: fs, SessionTTL: 1 * time.Hour}
	handler := NewGatewayHandler(gw)

	t.Run("Cancel Persist Error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/s1/cancel", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("Resume Persist Error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/s1/resume", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("Abandon Persist Error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/s1/abandon", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("Execute Persist Error", func(t *testing.T) {
		session.Plan = nil // Force BuildDefaultPlan and Put
		req := httptest.NewRequest(http.MethodGet, "/v2/agent/sessions/s1/execute", nil)
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("Resume Success (no plan)", func(t *testing.T) {
		session.Plan = nil
		fs.putFn = func(s *wisdev.AgentSession) error { return nil }
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/s1/resume", nil)
		rec := httptest.NewRecorder()
		handler.HandleSessionByID(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("Resume Success (with existing plan)", func(t *testing.T) {
		session.Plan = &wisdev.PlanState{}
		req := httptest.NewRequest(http.MethodPost, "/v2/agent/sessions/s1/resume", nil)
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
