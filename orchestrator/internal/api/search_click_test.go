package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

type clickTestDB struct {
	execErr  error
	execSQL  string
	execArgs []any
}

func (db *clickTestDB) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	db.execSQL = sql
	db.execArgs = append([]any(nil), arguments...)
	return pgconn.CommandTag{}, db.execErr
}

func (db *clickTestDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (db *clickTestDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return nil
}

func TestHandleRecordClick(t *testing.T) {
	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/search/click", nil)
		rec := httptest.NewRecorder()

		NewSearchHandler(search.NewProviderRegistry(), search.NewProviderRegistry(), nil).HandleRecordClick(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/search/click", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()

		NewSearchHandler(search.NewProviderRegistry(), search.NewProviderRegistry(), nil).HandleRecordClick(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("missing query or paper id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/search/click", strings.NewReader(`{"query":"   ","paperId":"","provider":"", "rank":0}`))
		rec := httptest.NewRecorder()

		NewSearchHandler(search.NewProviderRegistry(), search.NewProviderRegistry(), nil).HandleRecordClick(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("success", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		db := &clickTestDB{}
		reg.SetDB(db)
		h := NewSearchHandler(reg, reg, nil)

		actorID := "11111111-1111-1111-1111-111111111111"
		body := `{"query":"Quantum   Computing","paperId":"p1","provider":"mock","rank":1,"userId":"22222222-2222-2222-2222-222222222222"}`
		req := httptest.NewRequest(http.MethodPost, "/search/click", strings.NewReader(body))
		req = withTestUserID(req, actorID)
		rec := httptest.NewRecorder()

		h.HandleRecordClick(rec, req)

		assert.Equal(t, http.StatusAccepted, rec.Code)
		assert.Equal(t, actorID, db.execArgs[0])
		assert.Equal(t, "quantum computing", db.execArgs[1])
		assert.Equal(t, "p1", db.execArgs[2])
		assert.Equal(t, "mock", db.execArgs[3])
		assert.Equal(t, 1, db.execArgs[4])
	})

	t.Run("logs db error but still accepts", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		reg.SetDB(&clickTestDB{execErr: errors.New("db unavailable")})
		h := NewSearchHandler(reg, reg, nil)

		body := `{"query":"quantum computing","paperId":"p1","provider":"mock","rank":1,"userId":"11111111-1111-1111-1111-111111111111"}`
		req := httptest.NewRequest(http.MethodPost, "/search/click", strings.NewReader(body))
		req = withTestUserID(req, "11111111-1111-1111-1111-111111111111")
		rec := httptest.NewRecorder()

		h.HandleRecordClick(rec, req)

		assert.Equal(t, http.StatusAccepted, rec.Code)
	})

	t.Run("defaults provider and rank", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		db := &clickTestDB{}
		reg.SetDB(db)
		h := NewSearchHandler(reg, reg, nil)

		body := `{"query":"quantum computing","paperId":"p1","provider":"  ","rank":0,"userId":"11111111-1111-1111-1111-111111111111"}`
		req := httptest.NewRequest(http.MethodPost, "/search/click", strings.NewReader(body))
		req = withTestUserID(req, "11111111-1111-1111-1111-111111111111")
		rec := httptest.NewRecorder()

		h.HandleRecordClick(rec, req)

		assert.Equal(t, http.StatusAccepted, rec.Code)
		assert.Equal(t, "unknown", db.execArgs[3])
		assert.Equal(t, 1, db.execArgs[4])
	})

	t.Run("ignores body user for anonymous caller", func(t *testing.T) {
		reg := search.NewProviderRegistry()
		db := &clickTestDB{}
		reg.SetDB(db)
		h := NewSearchHandler(reg, reg, nil)

		body := `{"query":"quantum computing","paperId":"p1","provider":"mock","rank":1,"userId":"33333333-3333-3333-3333-333333333333"}`
		req := httptest.NewRequest(http.MethodPost, "/search/click", strings.NewReader(body))
		rec := httptest.NewRecorder()

		h.HandleRecordClick(rec, req)

		assert.Equal(t, http.StatusAccepted, rec.Code)
		assert.Nil(t, db.execArgs[0])
	})
}
