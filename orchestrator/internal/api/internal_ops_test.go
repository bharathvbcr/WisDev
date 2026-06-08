package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

type internalOpsDBStub struct {
	execCalls []string
	execFn    func(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (s *internalOpsDBStub) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.execCalls = append(s.execCalls, sql)
	if s.execFn != nil {
		return s.execFn(context.Background(), sql)
	}
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

func TestInternalOpsHandlerHandleAccountDeleteValidationAndPersistence(t *testing.T) {
	t.Run("method not allowed", func(t *testing.T) {
		handler := NewInternalOpsHandler(&internalOpsDBStub{}, nil)
		req := httptest.NewRequest(http.MethodGet, "/internal/account/delete", nil)
		rec := httptest.NewRecorder()

		handler.HandleAccountDelete(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("invalid body", func(t *testing.T) {
		handler := NewInternalOpsHandler(&internalOpsDBStub{}, nil)
		req := httptest.NewRequest(http.MethodPost, "/internal/account/delete", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()

		handler.HandleAccountDelete(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("missing user id", func(t *testing.T) {
		handler := NewInternalOpsHandler(&internalOpsDBStub{}, nil)
		req := httptest.NewRequest(http.MethodPost, "/internal/account/delete", strings.NewReader(`{"email":"person@example.com"}`))
		rec := httptest.NewRecorder()

		handler.HandleAccountDelete(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("persist error surfaces as internal error", func(t *testing.T) {
		db := &internalOpsDBStub{}
		calls := 0
		db.execFn = func(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
			calls++
			if calls == 1 {
				return pgconn.NewCommandTag("CREATE TABLE"), nil
			}
			return pgconn.CommandTag{}, errors.New("persist failed")
		}
		handler := NewInternalOpsHandler(db, nil)
		req := httptest.NewRequest(http.MethodPost, "/internal/account/delete", strings.NewReader(`{"userId":"user-123"}`))
		rec := httptest.NewRecorder()

		handler.HandleAccountDelete(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("already deleted remains idempotent", func(t *testing.T) {
		db := &internalOpsDBStub{}
		calls := 0
		db.execFn = func(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
			calls++
			switch calls {
			case 1:
				return pgconn.NewCommandTag("CREATE TABLE"), nil
			default:
				return pgconn.NewCommandTag("INSERT 0 0"), nil
			}
		}
		handler := NewInternalOpsHandler(db, nil)
		req := httptest.NewRequest(http.MethodPost, "/internal/account/delete", strings.NewReader(`{"userId":"user-123","reason":"cleanup"}`))
		rec := httptest.NewRecorder()

		handler.HandleAccountDelete(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var response map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		assert.Equal(t, true, response["already_processed"])
		assert.Equal(t, "already_deleted", response["status"])
	})
}

func TestInternalOpsHandlerHandleAccountDeleteAcceptsTrustedPayload(t *testing.T) {
	db := &internalOpsDBStub{}
	handler := NewInternalOpsHandler(db, nil)

	body := map[string]any{
		"user_id":         "user-456",
		"email":           "trusted@example.com",
		"idempotency_key": "account-delete:user-456:trusted@example.com",
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

func TestInternalOpsPersistenceHelpersWithNilDB(t *testing.T) {
	handler := NewInternalOpsHandler(nil, nil)

	alreadyDeleted, persisted, err := handler.persistAccountDeletion(context.Background(), accountDeleteRequest{UserID: "user-1"})
	assert.NoError(t, err)
	assert.False(t, alreadyDeleted)
	assert.False(t, persisted)
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
