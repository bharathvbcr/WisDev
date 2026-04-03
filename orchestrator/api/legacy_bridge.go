package api

import (
	"encoding/json"
	"net/http"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	"time"
)

func writeV2Envelope(w http.ResponseWriter, payloadKey string, payload any) string {
	traceID := wisdev.NewTraceID()
	return writeV2EnvelopeWithTraceID(w, traceID, payloadKey, payload)
}

func writeV2EnvelopeWithTraceID(w http.ResponseWriter, traceID string, payloadKey string, payload any) string {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(buildV2EnvelopeBody(traceID, payloadKey, payload))
	return traceID
}

func buildV2EnvelopeBody(traceID string, payloadKey string, payload any) map[string]any {
	body := map[string]any{
		"ok":        true,
		"traceId":   traceID,
		"createdAt": time.Now().UnixMilli(),
	}
	if payload != nil {
		body[payloadKey] = payload
		// For backward compatibility with some consumers that expect "result"
		body["result"] = payload
	}
	return body
}

func writeV2EnvelopeStatus(w http.ResponseWriter, statusCode int, payloadKey string, payload any) string {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	traceID := wisdev.NewTraceID()
	_ = json.NewEncoder(w).Encode(buildV2EnvelopeBody(traceID, payloadKey, payload))
	return traceID
}
