package api

import (
	"context"
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

func TestWriteErrorCtx_IncludesTraceID(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := context.WithValue(context.Background(), ctxRequestTraceID, "ctx-trace-001")

	WriteErrorCtx(ctx, rec, http.StatusBadRequest, ErrInvalidParameters, "bad params", map[string]any{"field": "query"})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{
		"ok": false,
		"traceId": "ctx-trace-001",
		"error": {
			"code": "INVALID_PARAMETERS",
			"message": "bad params",
			"status": 400,
			"details": {"field": "query"}
		}
	}`, rec.Body.String())
}
