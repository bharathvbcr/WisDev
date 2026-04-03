package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/paper"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

type flexibleProfiler struct {
	extractFn func(context.Context, search.Paper) (*paper.Profile, error)
}

func (f *flexibleProfiler) ExtractProfile(ctx context.Context, p search.Paper) (*paper.Profile, error) {
	return f.extractFn(ctx, p)
}

func TestPaperHandler_HandleProfile(t *testing.T) {
	fp := &flexibleProfiler{}
	h := NewPaperHandler(fp, "")

	t.Run("Degraded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/profile", nil)
		ctx := resilience.SetDegraded(req.Context(), true)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.HandleProfile(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/profile", nil)
		rec := httptest.NewRecorder()
		h.HandleProfile(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/profile", bytes.NewReader([]byte("{invalid")))
		rec := httptest.NewRecorder()
		h.HandleProfile(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("Success", func(t *testing.T) {
		p := search.Paper{ID: "p1"}
		body, _ := json.Marshal(p)
		req := httptest.NewRequest(http.MethodPost, "/profile", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		fp.extractFn = func(ctx context.Context, paperData search.Paper) (*paper.Profile, error) {
			return &paper.Profile{PaperID: "p1"}, nil
		}

		h.HandleProfile(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp struct {
			PaperID string `json:"paperId"`
		}
		json.NewDecoder(rec.Body).Decode(&resp)
		assert.Equal(t, "p1", resp.PaperID)
	})

	t.Run("Extract Error", func(t *testing.T) {
		p := search.Paper{ID: "p1"}
		body, _ := json.Marshal(p)
		req := httptest.NewRequest(http.MethodPost, "/profile", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		fp.extractFn = func(ctx context.Context, paperData search.Paper) (*paper.Profile, error) {
			return nil, errors.New("extract fail")
		}

		h.HandleProfile(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrDependencyFailed, resp.Error.Code)
	})
}
