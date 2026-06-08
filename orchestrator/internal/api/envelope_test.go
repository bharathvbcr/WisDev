package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildEnvelopeBody_UsesExplicitPayloadKeyOnly(t *testing.T) {
	payload := map[string]any{"sessionId": "s1"}

	body := buildEnvelopeBody("trace-1", "session", payload)

	assert.Equal(t, true, body["ok"])
	assert.Equal(t, "trace-1", body["traceId"])
	assert.Equal(t, payload, body["session"])
	_, hasResult := body["result"]
	assert.False(t, hasResult)
}

func TestBuildEnvelopeBody_PreservesCanonicalResultPayload(t *testing.T) {
	payload := map[string]any{"ok": true}

	body := buildEnvelopeBody("trace-2", "result", payload)

	assert.Equal(t, payload, body["result"])
}

func TestWriteEnvelopeWithTraceID_SetsTraceHeader(t *testing.T) {
	rec := httptest.NewRecorder()

	traceID := writeEnvelopeWithTraceID(rec, "trace-helper-1", "result", map[string]any{"ok": true})

	assert.Equal(t, "trace-helper-1", traceID)
	assert.Equal(t, "trace-helper-1", rec.Header().Get("X-Trace-Id"))

	var body map[string]any
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "trace-helper-1", body["traceId"])
}

func TestWriteEnvelopeStatus_SetsTraceHeader(t *testing.T) {
	rec := httptest.NewRecorder()

	traceID := writeEnvelopeStatus(rec, http.StatusAccepted, "result", map[string]any{"ok": true})

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, traceID, rec.Header().Get("X-Trace-Id"))

	var body map[string]any
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, traceID, body["traceId"])
}
