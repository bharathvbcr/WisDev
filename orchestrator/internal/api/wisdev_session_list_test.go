package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWisDevSessionListRoute(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())

	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		StateStore:   wisdev.NewRuntimeStateStore(nil, journal),
		Journal:      journal,
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	createSession := func(userID string, query string) {
		req := httptest.NewRequest(
			http.MethodPost,
			"/wisdev/session/initialize",
			strings.NewReader(`{"userId":"`+userID+`","originalQuery":"`+query+`"}`),
		)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	createSession("u1", "sleep and memory")
	createSession("u1", "graph retrieval")
	createSession("u2", "other user query")

	t.Run("returns backend-owned sessions for the caller", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/session/list", strings.NewReader(`{"userId":"u1","limit":5}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var body map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
		sessions := sliceAnyMap(body["sessions"])
		assert.GreaterOrEqual(t, len(sessions), 2)

		foundQueries := map[string]bool{}
		for _, session := range sessions {
			assert.Equal(t, "u1", wisdev.AsOptionalString(session["userId"]))
			assert.NotEmpty(t, wisdev.AsOptionalString(session["sessionId"]))
			foundQueries[wisdev.AsOptionalString(session["originalQuery"])] = true
		}
		assert.True(t, foundQueries["sleep and memory"])
		assert.True(t, foundQueries["graph retrieval"])
		assert.False(t, foundQueries["other user query"])
	})

	t.Run("rejects owner mismatch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/session/list", strings.NewReader(`{"userId":"u1","limit":5}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u2"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrUnauthorized, resp.Error.Code)
	})

	t.Run("supports GET query params and respects limit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/session/list?userId=u1&limit=1", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var body map[string]any
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
		sessions := sliceAnyMap(body["sessions"])
		assert.Len(t, sessions, 1)
		assert.Equal(t, "u1", wisdev.AsOptionalString(sessions[0]["userId"]))
	})
}

func TestWisDevSessionListRoute_WithoutStateStore(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		Journal:      wisdev.NewRuntimeJournal(nil),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	assert.NoError(t, gw.Store.Put(context.Background(), &wisdev.AgentSession{
		SessionID:      "sess_1",
		UserID:         "u1",
		OriginalQuery:  "canonical query",
		CorrectedQuery: "canonical query",
		Status:         wisdev.SessionQuestioning,
		Mode:           wisdev.WisDevModeGuided,
		ServiceTier:    wisdev.ServiceTierPriority,
		UpdatedAt:      time.Now().UnixMilli(),
	}, time.Hour))

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/wisdev/session/list?userId=u1&limit=5", nil)
	req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	sessions := sliceAnyMap(body["sessions"])
	assert.Len(t, sessions, 1)
	assert.Equal(t, "sess_1", wisdev.AsOptionalString(sessions[0]["sessionId"]))
	assert.Equal(t, "canonical query", wisdev.AsOptionalString(sessions[0]["originalQuery"]))
	assert.Equal(t, "guided", wisdev.AsOptionalString(sessions[0]["mode"]))
}

func TestWisDevSessionListRoute_MethodAndAuthGuards(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:        nil,
		Journal:      wisdev.NewRuntimeJournal(nil),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	methodNotAllowed := httptest.NewRequest(http.MethodPut, "/wisdev/session/list", strings.NewReader("{}"))
	methodNotAllowed = methodNotAllowed.WithContext(context.WithValue(methodNotAllowed.Context(), contextKey("user_id"), "u1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, methodNotAllowed)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	var methodErr APIError
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&methodErr))
	assert.Equal(t, ErrBadRequest, methodErr.Error.Code)

	withoutStore := &wisdev.AgentGateway{
		Store:        nil,
		Journal:      wisdev.NewRuntimeJournal(nil),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	mux = http.NewServeMux()
	RegisterWisDevRoutes(mux, withoutStore, nil, nil)

	serviceUnavailable := httptest.NewRequest(http.MethodGet, "/wisdev/session/list?userId=u1", nil)
	serviceUnavailable = serviceUnavailable.WithContext(context.WithValue(serviceUnavailable.Context(), contextKey("user_id"), "u1"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, serviceUnavailable)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var unavailableErr APIError
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&unavailableErr))
	assert.Equal(t, ErrServiceUnavailable, unavailableErr.Error.Code)
}

func TestWisDevSessionListRoute_InvalidBodyAndLimits(t *testing.T) {
	store := wisdev.NewInMemorySessionStore()
	for i := 0; i < 12; i++ {
		session := &wisdev.AgentSession{
			SessionID:      "sess_" + strconv.Itoa(i),
			UserID:         "u1",
			OriginalQuery:  "query " + strconv.Itoa(i),
			CorrectedQuery: "query " + strconv.Itoa(i),
			Status:         wisdev.SessionQuestioning,
			Mode:           wisdev.WisDevModeGuided,
			UpdatedAt:      time.Now().Add(time.Duration(i) * time.Minute).UnixMilli(),
		}
		require.NoError(t, store.Put(context.Background(), session, time.Hour))
	}
	gw := &wisdev.AgentGateway{
		Store:        store,
		Journal:      wisdev.NewRuntimeJournal(nil),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	badBody := httptest.NewRequest(http.MethodPost, "/wisdev/session/list", strings.NewReader(`{"userId":"u1","limit":`))
	badBody = badBody.WithContext(context.WithValue(badBody.Context(), contextKey("user_id"), "u1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, badBody)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	badBodyResp := map[string]any{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&badBodyResp))
	var badBodyError APIError
	require.NoError(t, mapToAPIError(badBodyResp, &badBodyError))
	assert.Equal(t, ErrBadRequest, badBodyError.Error.Code)

	defaultLimit := httptest.NewRequest(http.MethodGet, "/wisdev/session/list?userId=u1", nil)
	defaultLimit = defaultLimit.WithContext(context.WithValue(defaultLimit.Context(), contextKey("user_id"), "u1"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, defaultLimit)
	assert.Equal(t, http.StatusOK, rec.Code)

	var defaultLimitBody map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&defaultLimitBody))
	defaultSessions := sliceAnyMap(defaultLimitBody["sessions"])
	assert.Len(t, defaultSessions, 10)
}

func TestWisDevSessionListRoute_CapsLimitAndDedupesSessions(t *testing.T) {
	store := &duplicateSessionStore{
		sessions: []*wisdev.AgentSession{
			{
				SessionID:      "dup-session",
				UserID:         "u1",
				OriginalQuery:  "session one",
				CorrectedQuery: "session one",
				Status:         wisdev.SessionQuestioning,
				Mode:           wisdev.WisDevModeGuided,
			},
			{
				SessionID:      "dup-session",
				UserID:         "u1",
				OriginalQuery:  "session two",
				CorrectedQuery: "session two",
				Status:         wisdev.SessionQuestioning,
				Mode:           wisdev.WisDevModeGuided,
			},
		},
	}
	gw := &wisdev.AgentGateway{
		Store:        store,
		Journal:      wisdev.NewRuntimeJournal(nil),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/wisdev/session/list?userId=u1&limit=200", nil)
	req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
	assert.Len(t, sliceAnyMap(payload["sessions"]), 1)
}

func TestWisDevSessionListRoute_CapsLargeLimitToMax(t *testing.T) {
	store := wisdev.NewInMemorySessionStore()
	for i := 0; i < 120; i++ {
		session := &wisdev.AgentSession{
			SessionID:      "sess_large_" + strconv.Itoa(i),
			UserID:         "u2",
			OriginalQuery:  "query " + strconv.Itoa(i),
			CorrectedQuery: "query " + strconv.Itoa(i),
			Status:         wisdev.SessionQuestioning,
			Mode:           wisdev.WisDevModeGuided,
			UpdatedAt:      time.Now().Add(time.Duration(i) * time.Minute).UnixMilli(),
		}
		require.NoError(t, store.Put(context.Background(), session, time.Hour))
	}
	gw := &wisdev.AgentGateway{
		Store:        store,
		Journal:      wisdev.NewRuntimeJournal(nil),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	limited := httptest.NewRequest(http.MethodGet, "/wisdev/session/list?userId=u2&limit=200", nil)
	limited = limited.WithContext(context.WithValue(limited.Context(), contextKey("user_id"), "u2"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, limited)
	assert.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
	assert.Len(t, sliceAnyMap(payload["sessions"]), 100)
}

func mapToAPIError(body map[string]any, out *APIError) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

type duplicateSessionStore struct {
	sessions []*wisdev.AgentSession
}

func (d *duplicateSessionStore) Get(_ context.Context, sessionID string) (*wisdev.AgentSession, error) {
	for _, session := range d.sessions {
		if session.SessionID == sessionID {
			return session, nil
		}
	}
	return nil, assert.AnError
}

func (d *duplicateSessionStore) Put(_ context.Context, _ *wisdev.AgentSession, _ time.Duration) error {
	return nil
}

func (d *duplicateSessionStore) Delete(_ context.Context, _ string) error {
	return nil
}

func (d *duplicateSessionStore) List(_ context.Context, userID string) ([]*wisdev.AgentSession, error) {
	out := make([]*wisdev.AgentSession, 0, len(d.sessions))
	for _, session := range d.sessions {
		if session.UserID == userID {
			out = append(out, session)
		}
	}
	return out, nil
}
