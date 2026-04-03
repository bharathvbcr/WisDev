package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHandleOpenSearchHybrid(t *testing.T) {
	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v2/opensearch/hybrid", nil)
		rec := httptest.NewRecorder()

		HandleOpenSearchHybrid(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/opensearch/hybrid", bytes.NewBufferString(`{invalid`))
		rec := httptest.NewRecorder()

		HandleOpenSearchHybrid(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v2/opensearch/hybrid", bytes.NewBufferString(`{"query":"sleep and memory"}`))
		rec := httptest.NewRecorder()

		HandleOpenSearchHybrid(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
