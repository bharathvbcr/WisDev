package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseBoolValue(t *testing.T) {
	tests := []struct {
		raw          string
		defaultValue bool
		expected     bool
	}{
		{"true", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"on", false, true},
		{"false", true, false},
		{"0", true, false},
		{"no", true, false},
		{"off", true, false},
		{"", true, true},
		{"", false, false},
		{"maybe", true, true},
		{"maybe", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			assert.Equal(t, tt.expected, parseBoolValue(tt.raw, tt.defaultValue))
		})
	}
}

func TestParseIntValue(t *testing.T) {
	tests := []struct {
		raw          string
		defaultValue int
		expected     int
	}{
		{"10", 5, 10},
		{"0", 5, 5},
		{"-1", 5, 5},
		{"abc", 5, 5},
		{"", 5, 5},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			assert.Equal(t, tt.expected, parseIntValue(tt.raw, tt.defaultValue))
		})
	}
}

func TestParseOptionalIntValue(t *testing.T) {
	tests := []struct {
		raw      string
		expected int
	}{
		{"10", 10},
		{"0", 0},
		{"-1", 0},
		{"abc", 0},
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			assert.Equal(t, tt.expected, parseOptionalIntValue(tt.raw))
		})
	}
}

func TestReadJSONBody(t *testing.T) {
	type testStruct struct {
		Name string `json:"name"`
	}

	t.Run("valid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"test"}`))
		var v testStruct
		err := readJSONBody(req, &v)
		assert.NoError(t, err)
		assert.Equal(t, "test", v.Name)
	})

	t.Run("nil body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Body = nil
		var v testStruct
		err := readJSONBody(req, &v)
		assert.NoError(t, err)
	})

	t.Run("invalid json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{invalid`))
		var v testStruct
		err := readJSONBody(req, &v)
		assert.Error(t, err)
	})
}

func TestWriteJSONResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	data := map[string]string{"foo": "bar"}
	writeJSONResponse(rec, http.StatusCreated, data)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"foo":"bar"}`, rec.Body.String())
}
