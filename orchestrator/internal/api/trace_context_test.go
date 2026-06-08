package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequestTraceIDFromRequest_UseContextIDFirst(t *testing.T) {
	is := assert.New(t)
	req := httptest.NewRequest(http.MethodGet, "/search?traceId=query-trace", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxRequestTraceID, "ctx-trace"))
	req.Header.Set("X-Trace-Id", "header-trace")

	is.Equal("ctx-trace", requestTraceIDFromRequest(req))
}

func TestRequestTraceIDFromRequest_QueryThenTraceParent(t *testing.T) {
	is := assert.New(t)

	reqHeader := httptest.NewRequest(http.MethodGet, "/search?traceId=query-trace", nil)
	reqHeader.Header.Set("X-Trace-Id", "header-trace")
	reqHeader.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	is.Equal("header-trace", requestTraceIDFromRequest(reqHeader))

	reqQuery := httptest.NewRequest(http.MethodGet, "/search?traceId=query-trace&trace_id=legacy-trace", nil)
	is.Equal("query-trace", requestTraceIDFromRequest(reqQuery))

	reqTraceID := httptest.NewRequest(http.MethodGet, "/search?trace_id=legacy-trace", nil)
	is.Equal("legacy-trace", requestTraceIDFromRequest(reqTraceID))
}

func TestRequestTraceIDFromRequest_Traceparent(t *testing.T) {
	is := assert.New(t)
	req := httptest.NewRequest(http.MethodGet, "/search", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	is.Equal("4bf92f3577b34da6a3ce929d0e0e4736", requestTraceIDFromRequest(req))
}

func TestRequestTraceIDFromRequest_TraceparentIgnoresInvalid(t *testing.T) {
	is := assert.New(t)
	req := httptest.NewRequest(http.MethodGet, "/search", nil)
	req.Header.Set("traceparent", "00-invalid")

	is.Equal("", requestTraceIDFromRequest(req))
}
