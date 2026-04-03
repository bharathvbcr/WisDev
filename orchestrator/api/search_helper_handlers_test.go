package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestSearchHelperHandlers(t *testing.T) {
	reg := search.NewProviderRegistry()
	h := NewSearchHandler(reg, reg, nil)

	t.Run("HandleRelatedArticles - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/related", nil)
		rec := httptest.NewRecorder()
		h.HandleRelatedArticles(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
		assert.Equal(t, http.MethodPost, resp.Error.Details["allowedMethod"])
	})

	t.Run("HandleRelatedArticles - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/related", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		h.HandleRelatedArticles(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleRelatedArticles - Missing Query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/related", strings.NewReader(`{"query":"","source_title":""}`))
		rec := httptest.NewRecorder()
		h.HandleRelatedArticles(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleQueryField - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/field", nil)
		rec := httptest.NewRecorder()
		h.HandleQueryField(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleQueryField - Missing Query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/field", strings.NewReader(`{"query":"   "}`))
		rec := httptest.NewRecorder()
		h.HandleQueryField(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleQueryIntroduction - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/introduction", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		h.HandleQueryIntroduction(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleQueryIntroduction - Missing Query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/introduction", strings.NewReader(`{"query":"","papers":[]}`))
		rec := httptest.NewRecorder()
		h.HandleQueryIntroduction(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("HandleBatchSummaries - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/summaries", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		h.HandleBatchSummaries(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleBatchSummaries - Missing Papers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/summaries", strings.NewReader(`{"papers":[]}`))
		rec := httptest.NewRecorder()
		h.HandleBatchSummaries(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})
}
