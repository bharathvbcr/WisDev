package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

type internalOpsDBStub struct {
	execCalls []string
}

func (s *internalOpsDBStub) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.execCalls = append(s.execCalls, sql)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (s *internalOpsDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (s *internalOpsDBStub) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (s *internalOpsDBStub) Begin(context.Context) (pgx.Tx, error)                   { return nil, nil }
func (s *internalOpsDBStub) Ping(context.Context) error                              { return nil }
func (s *internalOpsDBStub) Close()                                                  {}

func TestInternalOpsHandlerHandleAccountDelete(t *testing.T) {
	db := &internalOpsDBStub{}
	journal := wisdev.NewRuntimeJournal(nil)
	handler := NewInternalOpsHandler(db, journal)

	body := map[string]any{
		"userId": "user-123",
		"email":  "person@example.com",
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/internal/account/delete", bytes.NewReader(encoded))
	rec := httptest.NewRecorder()

	handler.HandleAccountDelete(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.GreaterOrEqual(t, len(db.execCalls), 2)

	var response map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(t, true, response["ok"])
	assert.Equal(t, true, response["success"])
	assert.Equal(t, "deletion_recorded", response["status"])
	assert.Equal(t, false, response["already_processed"])
}

func TestInternalOpsHandlerHandleAccountDeleteAcceptsRustGatewayPayload(t *testing.T) {
	db := &internalOpsDBStub{}
	handler := NewInternalOpsHandler(db, nil)

	body := map[string]any{
		"user_id":         "user-456",
		"email":           "rust@example.com",
		"idempotency_key": "account-delete:user-456:rust@example.com",
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/internal/account/delete", bytes.NewReader(encoded))
	req.Header.Set("X-Idempotency-Key", "header-should-not-win")
	rec := httptest.NewRecorder()

	handler.HandleAccountDelete(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(t, true, response["success"])
	assert.Equal(t, "user-456", response["userId"])
	assert.Equal(t, false, response["already_processed"])
}

func TestInternalOpsHandlerHandleStripeBillingSync(t *testing.T) {
	db := &internalOpsDBStub{}
	handler := NewInternalOpsHandler(db, nil)

	body := map[string]any{
		"user_id":                "user-123",
		"tier":                   "pro",
		"event_type":             "checkout.session.completed",
		"stripe_event_id":        "evt_123",
		"stripe_subscription_id": "sub_123",
		"customer_email":         "person@example.com",
		"customerId":             "cus_123",
		"status":                 "active",
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/internal/billing/stripe/webhook", bytes.NewReader(encoded))
	rec := httptest.NewRecorder()

	handler.HandleStripeBillingSync(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.GreaterOrEqual(t, len(db.execCalls), 2)

	var response map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(t, true, response["ok"])
	assert.Equal(t, true, response["success"])
	assert.Equal(t, "pro", response["tier"])
	assert.Equal(t, false, response["already_processed"])
}

func TestRouterInternalBillingSubscriptionRouteRequiresServiceKey(t *testing.T) {
	router := NewRouter(ServerConfig{Version: "test"})

	body := map[string]any{
		"user_id":         "user-123",
		"tier":            "pro",
		"event_type":      "checkout.session.completed",
		"stripe_event_id": "evt_123",
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/internal/billing/subscription", bytes.NewReader(encoded))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRouterInternalBillingSubscriptionRouteAcceptsServiceKey(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{
		Version: "test",
		DB:      &internalOpsDBStub{},
	})

	body := map[string]any{
		"user_id":         "user-123",
		"tier":            "pro",
		"event_type":      "checkout.session.completed",
		"stripe_event_id": "evt_123",
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/internal/billing/subscription", bytes.NewReader(encoded))
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(t, true, response["success"])
	assert.Equal(t, "checkout.session.completed", response["eventType"])
}

func TestRouterInternalAccountDeleteRouteAcceptsServiceKeyWithoutLLMClient(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{
		Version: "test",
		DB:      &internalOpsDBStub{},
	})

	body := map[string]any{
		"user_id": "user-123",
		"email":   "person@example.com",
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/internal/account/delete", bytes.NewReader(encoded))
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(t, true, response["success"])
	assert.Equal(t, "user-123", response["userId"])
}

func TestRouterInternalAccountDeleteRouteRequiresServiceKey(t *testing.T) {
	router := NewRouter(ServerConfig{Version: "test"})

	body := map[string]any{
		"user_id": "user-123",
		"email":   "person@example.com",
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/internal/account/delete", bytes.NewReader(encoded))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRouterInternalAccountDeleteRouteAcceptsServiceKey(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	router := NewRouter(ServerConfig{
		Version: "test",
		DB:      &internalOpsDBStub{},
	})

	body := map[string]any{
		"user_id": "user-123",
		"email":   "person@example.com",
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/internal/account/delete", bytes.NewReader(encoded))
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(t, true, response["success"])
	assert.Equal(t, "user-123", response["userId"])
}
