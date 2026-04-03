package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTopicTreeHandler(t *testing.T) {
	handler := NewTopicTreeHandler(nil)

	t.Run("HandleTopicTreeGenerate - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/topic-tree/generate", nil)
		rec := httptest.NewRecorder()
		handler.HandleTopicTreeGenerate(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
		assert.Equal(t, http.MethodPost, resp.Error.Details["allowedMethod"])
	})

	t.Run("HandleTopicTreeGenerate - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/topic-tree/generate", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		handler.HandleTopicTreeGenerate(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleTopicTreeChildren - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/topic-tree/children", nil)
		rec := httptest.NewRecorder()
		handler.HandleTopicTreeChildren(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleTopicTreeChildren - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/topic-tree/children", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		handler.HandleTopicTreeChildren(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleTopicTreeRefineQueries - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/topic-tree/refine-queries", nil)
		rec := httptest.NewRecorder()
		handler.HandleTopicTreeRefineQueries(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("HandleTopicTreeRefineQueries - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/topic-tree/refine-queries", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		handler.HandleTopicTreeRefineQueries(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("handleTopicTreeEdges - Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/topic-tree/edges", nil)
		rec := httptest.NewRecorder()
		handleTopicTreeEdges(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("handleTopicTreeEdges - Invalid Body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/topic-tree/edges", strings.NewReader(`{invalid`))
		rec := httptest.NewRecorder()
		handleTopicTreeEdges(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})
}
