package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusBadRequest, ErrInvalidParameters, "invalid params", map[string]any{"field": "query"})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{
		"ok": false,
		"error": {
			"code": "INVALID_PARAMETERS",
			"message": "invalid params",
			"status": 400,
			"details": {"field": "query"}
		}
	}`, rec.Body.String())
}

func TestWriteJSONError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusNotFound, "NOT_FOUND", "not found")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{
		"ok": false,
		"error": {
			"code": "NOT_FOUND",
			"message": "not found",
			"status": 404
		}
	}`, rec.Body.String())
}
