package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

const testSteeringUserID = "steering-user"

func registerWisDevRoutesWithSteeringSession(t *testing.T, sessionID string) (*http.ServeMux, *wisdev.AgentGateway) {
	t.Helper()
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	gateway := &wisdev.AgentGateway{
		Journal:    wisdev.NewRuntimeJournal(nil),
		StateStore: wisdev.NewRuntimeStateStore(nil, nil),
	}
	require.NoError(t, gateway.StateStore.PersistAgentSessionMutation(sessionID, testSteeringUserID, map[string]any{
		"sessionId": sessionID,
		"userId":    testSteeringUserID,
	}, wisdev.RuntimeJournalEntry{EventType: "test_session_seed", Summary: "seed steering session"}))
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gateway, nil, nil)
	return mux, gateway
}

func TestRegisterWisDevRoutes(t *testing.T) {
	mux := http.NewServeMux()
	// All route registration functions are called within RegisterWisDevRoutes.
	// We pass nil for most because we just want to verify registration completes.
	RegisterWisDevRoutes(mux, nil, nil, nil)

	// Since mux doesn't expose a list of routes easily in standard lib without
	// complex trickery, and most registerXRoutes functions add handlers to mux,
	// we just verify it didn't panic.
	assert.NotNil(t, mux)
}

func TestRegisterWisDevRoutesMountsHypothesisHandler(t *testing.T) {
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/wisdev/hypothesis/quest_1/list", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var resp APIError
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, ErrServiceUnavailable, resp.Error.Code)
}

func TestRegisterWisDevRoutesMountsToolSearchWithoutGateway(t *testing.T) {
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/tool-search", strings.NewReader(`{"query":"research"}`))
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	toolSearch, ok := resp["toolSearch"].(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "research", toolSearch["query"])
	assert.NotNil(t, toolSearch["tools"])
}

func TestRegisterWisDevRoutesMountsSteeringEndpoint(t *testing.T) {
	sessionID := "route-session"
	mux, _ := registerWisDevRoutesWithSteeringSession(t, sessionID)
	ch, unregister := wisdev.RegisterSteeringChannel(sessionID)
	defer unregister()

	req := httptest.NewRequest(http.MethodPost, "/wisdev/steering", strings.NewReader(`{"sessionId":"route-session","type":"focus","payload":"REM sleep","queries":["REM sleep memory"]}`))
	req = withTestUserID(req, testSteeringUserID)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	select {
	case signal := <-ch:
		assert.Equal(t, "focus", signal.Type)
		assert.Equal(t, "REM sleep", signal.Payload)
		assert.Equal(t, []string{"REM sleep memory"}, signal.Queries)
	default:
		t.Fatal("expected steering signal")
	}
}

func TestRegisterWisDevRoutesQueuesSteeringWithoutActiveLoop(t *testing.T) {
	sessionID := "route-session-queued"
	mux, _ := registerWisDevRoutesWithSteeringSession(t, sessionID)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/steering", strings.NewReader(`{"sessionId":"route-session-queued","type":"redirect","payload":"NREM spindles","queries":["NREM spindle replay"]}`))
	req = withTestUserID(req, testSteeringUserID)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	var resp map[string]any
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "queued", resp["delivery"])

	ch, unregister := wisdev.RegisterSteeringChannel("route-session-queued")
	defer unregister()
	select {
	case signal := <-ch:
		assert.Equal(t, "redirect", signal.Type)
		assert.Equal(t, "NREM spindles", signal.Payload)
		assert.Equal(t, []string{"NREM spindle replay"}, signal.Queries)
	default:
		t.Fatal("expected queued steering signal")
	}
}

func TestRegisterWisDevRoutesJournalsSteeringSignal(t *testing.T) {
	sessionID := "route-session-journal-" + time.Now().Format("150405.000000000")
	mux, gateway := registerWisDevRoutesWithSteeringSession(t, sessionID)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/steering", strings.NewReader(`{"sessionId":"`+sessionID+`","type":"focus","payload":"REM sleep"}`))
	req = withTestUserID(req, testSteeringUserID)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	entries := gateway.Journal.ReadSession(sessionID, 10)
	if assert.Len(t, entries, 1) {
		assert.Equal(t, "wisdev_steering_signal", entries[0].EventType)
		assert.Equal(t, testSteeringUserID, entries[0].UserID)
		assert.Equal(t, "queued", entries[0].Metadata["delivery"])
	}
}

func TestRegisterWisDevRoutesAcceptsSteeringWebSocket(t *testing.T) {
	sessionID := "route-session-ws"
	mux, _ := registerWisDevRoutesWithSteeringSession(t, sessionID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, withTestUserID(r, testSteeringUserID))
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/wisdev/steering/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	assert.NoError(t, err)
	defer conn.Close()

	ch, unregister := wisdev.RegisterSteeringChannel(sessionID)
	defer unregister()

	assert.NoError(t, conn.WriteJSON(map[string]any{
		"sessionId": sessionID,
		"type":      "approve",
		"payload":   "continue",
	}))
	var resp map[string]any
	assert.NoError(t, conn.ReadJSON(&resp))
	assert.Equal(t, "accepted", resp["status"])
	assert.Equal(t, "delivered", resp["delivery"])

	select {
	case signal := <-ch:
		assert.Equal(t, "approve", signal.Type)
		assert.Equal(t, "continue", signal.Payload)
	default:
		t.Fatal("expected websocket steering signal")
	}
}

func TestRegisterWisDevRoutesRejectsCrossOriginSteering(t *testing.T) {
	mux, _ := registerWisDevRoutesWithSteeringSession(t, "route-session-origin")

	req := httptest.NewRequest(http.MethodPost, "/wisdev/steering", strings.NewReader(`{"sessionId":"route-session-origin","type":"focus"}`))
	req = withTestUserID(req, testSteeringUserID)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRegisterWisDevRoutesRejectsUnauthorizedSteering(t *testing.T) {
	mux, _ := registerWisDevRoutesWithSteeringSession(t, "route-session-auth")

	req := httptest.NewRequest(http.MethodPost, "/wisdev/steering", strings.NewReader(`{"sessionId":"route-session-auth","type":"focus"}`))
	req = withTestUserID(req, "other-user")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSteeringAdmissionLimiterRateLimitsByClient(t *testing.T) {
	limiter := newSteeringAdmissionLimiter(2, time.Minute)
	now := time.Now()
	if !limiter.allow("127.0.0.1", now) || !limiter.allow("127.0.0.1", now.Add(time.Second)) {
		t.Fatal("expected first two steering requests to be admitted")
	}
	if limiter.allow("127.0.0.1", now.Add(2*time.Second)) {
		t.Fatal("expected third steering request in window to be rate limited")
	}
	if !limiter.allow("127.0.0.1", now.Add(2*time.Minute)) {
		t.Fatal("expected limiter to reopen after window")
	}
}

func TestConvertWisdevSourcesToSearchPapers(t *testing.T) {
	is := assert.New(t)

	t.Run("nil sources", func(t *testing.T) {
		res := convertWisdevSourcesToSearchPapers(nil)
		is.Len(res, 0)
	})

	t.Run("conversion", func(t *testing.T) {
		sources := []wisdev.Source{
			{ID: "id1", Title: "T1", Summary: "S1", Year: 2024},
		}
		res := convertWisdevSourcesToSearchPapers(sources)
		is.Len(res, 1)
		is.Equal("id1", res[0].ID)
		is.Equal("T1", res[0].Title)
		is.Equal("S1", res[0].Abstract)
		is.Equal(2024, res[0].Year)
	})
}
