package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCloneAnyMap(t *testing.T) {
	t.Run("nil map", func(t *testing.T) {
		assert.Nil(t, cloneAnyMap(nil))
	})

	t.Run("valid map", func(t *testing.T) {
		m := map[string]any{"a": 1, "b": "2"}
		clone := cloneAnyMap(m)
		assert.Equal(t, m, clone)
		clone["a"] = 10
		assert.Equal(t, 1, m["a"]) // Verify deep copy (at top level)
	})
}

func TestDecodeStrictJSONBody(t *testing.T) {
	type testStruct struct {
		Name string `json:"name"`
	}

	t.Run("valid body", func(t *testing.T) {
		r := strings.NewReader(`{"name":"test"}`)
		var v testStruct
		err := decodeStrictJSONBody(r, &v)
		assert.NoError(t, err)
		assert.Equal(t, "test", v.Name)
	})

	t.Run("extra content", func(t *testing.T) {
		r := strings.NewReader(`{"name":"test"}{"extra":true}`)
		var v testStruct
		err := decodeStrictJSONBody(r, &v)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected extra JSON content")
	})

	t.Run("malformed extra content", func(t *testing.T) {
		r := strings.NewReader(`{"name":"test"} {invalid`)
		var v testStruct
		err := decodeStrictJSONBody(r, &v)
		assert.Error(t, err)
		assert.NotContains(t, err.Error(), "unexpected extra JSON content")
	})

	t.Run("invalid json", func(t *testing.T) {
		r := strings.NewReader(`{invalid`)
		var v testStruct
		err := decodeStrictJSONBody(r, &v)
		assert.Error(t, err)
	})
}

func TestMapPythonErrorToHTTP(t *testing.T) {
	tests := []struct {
		err      error
		expected int
		code     string
	}{
		{errors.New("not found"), http.StatusNotFound, "NOT_FOUND"},
		{errors.New("unauthorized"), http.StatusUnauthorized, "UNAUTHORIZED"},
		{errors.New("permission denied"), http.StatusUnauthorized, "UNAUTHORIZED"},
		{errors.New("invalid request"), http.StatusBadRequest, "INVALID_REQUEST"},
		{errors.New("bad request"), http.StatusBadRequest, "INVALID_REQUEST"},
		{errors.New("random error"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			status, code := mapPythonErrorToHTTP(tt.err)
			assert.Equal(t, tt.expected, status)
			assert.Equal(t, tt.code, code)
		})
	}
}

func TestNewTraceID(t *testing.T) {
	id := newTraceID()
	assert.NotEmpty(t, id)
}
