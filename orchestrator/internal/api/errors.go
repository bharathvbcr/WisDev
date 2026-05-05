package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// ErrorCode is a machine-readable error string.
type ErrorCode string

const (
	ErrInternal           ErrorCode = "INTERNAL_ERROR"
	ErrBadRequest         ErrorCode = "BAD_REQUEST"
	ErrUnauthorized       ErrorCode = "UNAUTHORIZED"
	ErrForbidden          ErrorCode = "FORBIDDEN"
	ErrNotFound           ErrorCode = "NOT_FOUND"
	ErrRateLimit          ErrorCode = "RATE_LIMIT_EXCEEDED"
	ErrServiceUnavailable ErrorCode = "SERVICE_UNAVAILABLE"
	ErrDependencyFailed   ErrorCode = "DEPENDENCY_FAILED"
	ErrSearchFailed       ErrorCode = "SEARCH_FAILED"
	ErrRagFailed          ErrorCode = "RAG_FAILED"
	ErrWisdevFailed       ErrorCode = "WISDEV_FAILED"
	ErrInvalidParameters  ErrorCode = "INVALID_PARAMETERS"
	ErrConcurrencyLimit   ErrorCode = "CONCURRENCY_LIMIT_REACHED"
	ErrConflict           ErrorCode = "CONFLICT"
)

// APIError is the canonical error response for all Go API endpoints.
type APIError struct {
	OK      bool        `json:"ok"`
	Error   ErrorDetail `json:"error"`
	TraceID string      `json:"traceId,omitempty"`
}

type ErrorDetail struct {
	Code    ErrorCode      `json:"code"`
	Message string         `json:"message"`
	Status  int            `json:"status"`
	Details map[string]any `json:"details,omitempty"`
}

// WriteError sends a structured API error response.
// Deprecated: Use WriteErrorCtx to include Trace ID from the request context.
func WriteError(w http.ResponseWriter, status int, code ErrorCode, message string, details map[string]any) {
	WriteErrorCtx(context.Background(), w, status, code, message, details)
}

// WriteErrorCtx sends a structured API error response including the Trace ID from context.
func WriteErrorCtx(ctx context.Context, w http.ResponseWriter, status int, code ErrorCode, message string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	resp := APIError{
		OK: false,
		Error: ErrorDetail{
			Code:    code,
			Message: message,
			Status:  status,
			Details: details,
		},
		TraceID: requestTraceIDFromContext(ctx),
	}

	_ = json.NewEncoder(w).Encode(resp)
}

// Legacy writeJSONError for compatibility while we refactor.
func writeJSONError(w http.ResponseWriter, status int, code string, message string) {
	WriteErrorCtx(context.Background(), w, status, ErrorCode(code), message, nil)
}
