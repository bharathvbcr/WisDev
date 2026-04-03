package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSearchExpansionHandlers_ErrorEnvelopes(t *testing.T) {
	handler := &SearchHandler{}

	t.Run("aggressive expansion method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/expand/aggressive", nil)
		rec := httptest.NewRecorder()

		handler.HandleAggressiveExpansion(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("aggressive expansion invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/expand/aggressive", bytes.NewBufferString(`{bad`))
		rec := httptest.NewRecorder()

		handler.HandleAggressiveExpansion(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("aggressive expansion missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/expand/aggressive", bytes.NewBufferString(`{"query":"   "}`))
		rec := httptest.NewRecorder()

		handler.HandleAggressiveExpansion(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("splade expansion method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/expand/splade", nil)
		rec := httptest.NewRecorder()

		handler.HandleSPLADEExpansion(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("splade expansion invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/expand/splade", bytes.NewBufferString(`{bad`))
		rec := httptest.NewRecorder()

		handler.HandleSPLADEExpansion(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("splade expansion missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/expand/splade", bytes.NewBufferString(`{"query":"   "}`))
		rec := httptest.NewRecorder()

		handler.HandleSPLADEExpansion(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})
}
