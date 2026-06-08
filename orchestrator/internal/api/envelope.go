package api

import (
	"encoding/json"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	"net/http"
	"strings"
	"time"
)

func writeEnvelope(w http.ResponseWriter, payloadKey string, payload any) string {
	traceID := wisdev.NewTraceID()
	return writeEnvelopeWithTraceID(w, traceID, payloadKey, payload)
}

func writeEnvelopeWithTraceID(w http.ResponseWriter, traceID string, payloadKey string, payload any) string {
	w.Header().Set("Content-Type", "application/json")
	if trace := strings.TrimSpace(traceID); trace != "" {
		w.Header().Set("X-Trace-Id", trace)
	}
	_ = json.NewEncoder(w).Encode(buildEnvelopeBody(traceID, payloadKey, payload))
	return traceID
}

func buildEnvelopeBody(traceID string, payloadKey string, payload any) map[string]any {
	body := map[string]any{
		"ok":        true,
		"traceId":   traceID,
		"createdAt": time.Now().UnixMilli(),
	}
	if payload != nil {
		body[payloadKey] = payload
	}
	return body
}

func writeEnvelopeStatus(w http.ResponseWriter, statusCode int, payloadKey string, payload any) string {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	traceID := wisdev.NewTraceID()
	if trace := strings.TrimSpace(traceID); trace != "" {
		w.Header().Set("X-Trace-Id", trace)
	}
	_ = json.NewEncoder(w).Encode(buildEnvelopeBody(traceID, payloadKey, payload))
	return traceID
}
