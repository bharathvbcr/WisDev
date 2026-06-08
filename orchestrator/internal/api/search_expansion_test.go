package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
)

func TestSearchExpansionHandlers_ErrorEnvelopes(t *testing.T) {
	handler := &SearchHandler{}

	t.Run("aggressive expansion method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/expand/aggressive", nil)
		rec := httptest.NewRecorder()

		handler.HandleAggressiveExpansion(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("aggressive expansion invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/expand/aggressive", bytes.NewBufferString(`{bad`))
		rec := httptest.NewRecorder()

		handler.HandleAggressiveExpansion(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("aggressive expansion missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/expand/aggressive", bytes.NewBufferString(`{"query":"   "}`))
		rec := httptest.NewRecorder()

		handler.HandleAggressiveExpansion(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("splade expansion method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/expand/splade", nil)
		rec := httptest.NewRecorder()

		handler.HandleSPLADEExpansion(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("splade expansion invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/expand/splade", bytes.NewBufferString(`{bad`))
		rec := httptest.NewRecorder()

		handler.HandleSPLADEExpansion(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("splade expansion missing query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/expand/splade", bytes.NewBufferString(`{"query":"   "}`))
		rec := httptest.NewRecorder()

		handler.HandleSPLADEExpansion(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})
}

func TestSearchExpansionHandlers_AggressiveExpansionSuccess_Defaults(t *testing.T) {
	handler := &SearchHandler{}

	req := httptest.NewRequest(
		http.MethodPost,
		"/expand/aggressive",
		bytes.NewBufferString(`{"query":"diabetes prevention","max_variations":0}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleAggressiveExpansion(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp wisdev.AggressiveExpansionResponse
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "diabetes prevention", resp.Original)
	assert.NotEmpty(t, resp.Variations)
	assert.Greater(t, len(resp.Metadata.Strategies), 0)
	assert.GreaterOrEqual(t, resp.LatencyMs, int64(0))
}

func TestSearchExpansionHandlers_AggressiveExpansionSuccess_WithCustomOptions(t *testing.T) {
	handler := &SearchHandler{}

	req := httptest.NewRequest(
		http.MethodPost,
		"/expand/aggressive",
		bytes.NewBufferString(`{"query":"artificial intelligence","max_variations":4,"include_mesh":false,"include_abbreviations":true,"include_temporal":true,"target_apis":["semantic_scholar","openalex"]}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleAggressiveExpansion(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp wisdev.AggressiveExpansionResponse
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "artificial intelligence", resp.Original)
	assert.LessOrEqual(t, len(resp.Variations), 4)
	assert.GreaterOrEqual(t, resp.LatencyMs, int64(0))
}

func TestSearchExpansionHandlers_SPLADEExpansionSuccess(t *testing.T) {
	handler := &SearchHandler{}

	req := httptest.NewRequest(
		http.MethodPost,
		"/expand/splade",
		bytes.NewBufferString(`{"query":"query expansion for papers"}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleSPLADEExpansion(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp wisdev.SPLADEExpansionResponse
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "query expansion for papers", resp.Original)
	assert.GreaterOrEqual(t, resp.LatencyMs, int64(0))
}
