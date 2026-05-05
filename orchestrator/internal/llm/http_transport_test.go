package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestClient_HTTPTransport(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "secret-key")

	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "secret-key", r.Header.Get("X-Internal-Service-Key"))

		switch r.URL.Path {
		case "/llm/generate":
			require.Equal(t, http.MethodPost, r.Method)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"text":         "generated",
				"modelUsed":    "gemini-test",
				"inputTokens":  3,
				"outputTokens": 4,
				"finishReason": "stop",
				"latencyMs":    12,
			}))
		case "/llm/generate/stream":
			require.Equal(t, http.MethodPost, r.Method)
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte("{\"chunk\":{\"delta\":\"alpha\",\"done\":false,\"finishReason\":\"\"}}\n"))
			_, _ = w.Write([]byte("{\"chunk\":{\"delta\":\"\",\"done\":true,\"finishReason\":\"stop\"}}\n"))
		case "/llm/health":
			require.Equal(t, http.MethodGet, r.Method)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"ok":              true,
				"version":         "1.2.3",
				"modelsAvailable": []string{"gemini-test"},
				"error":           "",
			}))
		case "/health":
			require.Equal(t, http.MethodGet, r.Method)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"service":   "python_sidecar",
				"status":    "ok",
				"transport": "http-json+grpc-protobuf",
				"dependencies": []map[string]any{
					{
						"name":      "gemini_runtime",
						"transport": "vertex-sdk-or-proxy",
						"status":    "configured",
						"source":    "native",
						"detail":    "",
					},
					{
						"name":      "grpc_sidecar",
						"transport": "grpc-protobuf",
						"status":    "ok",
					},
				},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

	client := NewClient()

	resp, err := client.Generate(context.Background(), &llmpb.GenerateRequest{Prompt: "hello"})
	require.NoError(t, err)
	require.Equal(t, "generated", resp.Text)
	require.Equal(t, "gemini-test", resp.ModelUsed)

	stream, err := client.GenerateStream(context.Background(), &llmpb.GenerateRequest{Prompt: "hello"})
	require.NoError(t, err)

	chunk, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "alpha", chunk.Delta)
	require.False(t, chunk.Done)

	chunk, err = stream.Recv()
	require.NoError(t, err)
	require.True(t, chunk.Done)
	require.Equal(t, "stop", chunk.FinishReason)

	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	health, err := client.Health(context.Background())
	require.NoError(t, err)
	require.True(t, health.Ok)
	require.Equal(t, "1.2.3", health.Version)

	runtimeHealth, err := client.RuntimeHealth(context.Background())
	require.NoError(t, err)
	require.Equal(t, "python_sidecar", runtimeHealth.Service)
	require.Equal(t, "ok", runtimeHealth.Status)
	require.Equal(t, "http-json+grpc-protobuf", runtimeHealth.Transport)
	require.Len(t, runtimeHealth.Dependencies, 2)
	require.Equal(t, "gemini_runtime", runtimeHealth.Dependencies[0].Name)
	require.Equal(t, "native", runtimeHealth.Dependencies[0].Source)
}

func TestClientHTTPTransportCorrelatesTraceAcrossGoAndSidecarRequest(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))

	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, traceID.String(), r.Header.Get("X-Trace-Id"))
		var body generateHTTPRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, traceID.String(), body.Metadata["trace_id"])
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"text":         "trace ok",
			"modelUsed":    "gemini-test",
			"finishReason": "stop",
		}))
	}))
	defer server.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

	client := NewClient()
	resp, err := client.Generate(ctx, &llmpb.GenerateRequest{
		Prompt:   "hello",
		Metadata: map[string]string{"trace_id": traceID.String()},
	})
	require.NoError(t, err)
	require.Equal(t, "trace ok", resp.Text)
}

func TestClientHTTPTimeoutFor(t *testing.T) {
	client := &Client{timeout: 10 * time.Second}

	t.Run("uses the smaller backstop when context deadline is longer", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(25*time.Second))
		defer cancel()

		timeout := client.httpTimeoutFor(ctx)
		require.Equal(t, 10*time.Second, timeout)
	})

	t.Run("uses context deadline when it is tighter than the client backstop", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(2*time.Second))
		defer cancel()

		timeout := client.httpTimeoutFor(ctx)
		require.Greater(t, timeout, time.Second)
		require.Less(t, timeout, client.timeout)
	})

	t.Run("falls back to client timeout without deadline", func(t *testing.T) {
		require.Equal(t, 10*time.Second, client.httpTimeoutFor(context.Background()))
	})
}
