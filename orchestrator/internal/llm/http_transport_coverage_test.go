package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_HTTPTransport_Extra(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "secret-key")

	var structuredPayload struct {
		Prompt          string `json:"prompt"`
		Model           string `json:"model"`
		RequestClass    string `json:"requestClass"`
		RetryProfile    string `json:"retryProfile"`
		ServiceTier     string `json:"serviceTier"`
		LatencyBudgetMs int32  `json:"latencyBudgetMs"`
	}
	var embedPayload struct {
		Text            string `json:"text"`
		LatencyBudgetMs int32  `json:"latencyBudgetMs"`
	}
	var embedBatchPayload struct {
		Texts           []string `json:"texts"`
		LatencyBudgetMs int32    `json:"latencyBudgetMs"`
	}

	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/llm/embed":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&embedPayload))
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"embedding": []float32{0.1, 0.2},
				"modelUsed": "text-embedding-test",
			}))
		case "/llm/embed/batch":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&embedBatchPayload))
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"embeddings": []map[string]any{
					{"values": []float32{0.1, 0.2}},
					{"values": []float32{0.3, 0.4}},
				},
				"modelUsed": "text-embedding-test",
			}))
		case "/llm/structured-output":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&structuredPayload))
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": "{\"foo\": \"bar\"}",
				"modelUsed":  "gemini-test",
			}))

		case "/llm/generate":
			w.WriteHeader(http.StatusTeapot)
			w.Write([]byte(`{"error": "im a teapot"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
	client := NewClient()

	t.Run("Embed", func(t *testing.T) {
		resp, err := client.Embed(context.Background(), &llmpb.EmbedRequest{Text: "test", LatencyBudgetMs: 7000})
		require.NoError(t, err)
		assert.Equal(t, []float32{0.1, 0.2}, resp.Embedding)
		assert.EqualValues(t, 7000, embedPayload.LatencyBudgetMs)
	})

	t.Run("EmbedBatch", func(t *testing.T) {
		resp, err := client.EmbedBatch(context.Background(), &llmpb.EmbedBatchRequest{Texts: []string{"a", "b"}, LatencyBudgetMs: 8000})
		require.NoError(t, err)
		assert.Len(t, resp.Embeddings, 2)
		assert.Equal(t, []float32{0.1, 0.2}, resp.Embeddings[0].Values)
		assert.EqualValues(t, 8000, embedBatchPayload.LatencyBudgetMs)
	})

	t.Run("StructuredOutput", func(t *testing.T) {
		resp, err := client.StructuredOutput(context.Background(), &llmpb.StructuredRequest{
			Prompt:          "p",
			JsonSchema:      "{}",
			Model:           "gemini-test",
			RequestClass:    "structured_high_value",
			RetryProfile:    "standard",
			ServiceTier:     "priority",
			LatencyBudgetMs: 5000,
		})
		require.NoError(t, err)
		assert.Equal(t, `{"foo": "bar"}`, resp.JsonResult)
		assert.Equal(t, "p", structuredPayload.Prompt)
		assert.Equal(t, "gemini-test", structuredPayload.Model)
		assert.Equal(t, "structured_high_value", structuredPayload.RequestClass)
		assert.Equal(t, "standard", structuredPayload.RetryProfile)
		assert.Equal(t, "priority", structuredPayload.ServiceTier)
		assert.EqualValues(t, 5000, structuredPayload.LatencyBudgetMs)
	})

	t.Run("HTTP Error Decoding", func(t *testing.T) {
		_, err := client.Generate(context.Background(), &llmpb.GenerateRequest{Prompt: "err"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "im a teapot")
	})
}

func TestHTTPTransport_ErrorHelpers(t *testing.T) {
	t.Run("decodeHTTPError branches", func(t *testing.T) {
		err := decodeHTTPError(&http.Response{
			StatusCode: http.StatusTeapot,
			Body:       io.NopCloser(bytes.NewReader(nil)),
		}, "python llm")
		require.EqualError(t, err, "python llm returned 418")

		err = decodeHTTPError(&http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"code":"RATE_LIMITED","message":"too many requests"}}`))),
		}, "python llm")
		require.EqualError(t, err, "python llm returned 502: too many requests (RATE_LIMITED)")

		err = decodeHTTPError(&http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"backend rejected"}}`))),
		}, "python llm")
		require.EqualError(t, err, "python llm returned 502: backend rejected")

		err = decodeHTTPError(&http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":"bad embed request","kind":"permanent"}`))),
		}, "python llm")
		require.EqualError(t, err, "python llm returned 400: bad embed request (permanent)")

		err = decodeHTTPError(&http.Response{
			StatusCode: http.StatusGatewayTimeout,
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"detail":{"error":{"message":"deadline exceeded","code":"EMBED_TIMEOUT"}}}`))),
		}, "python llm")
		require.EqualError(t, err, "python llm returned 504: deadline exceeded (EMBED_TIMEOUT)")

		err = decodeHTTPError(&http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(bytes.NewReader([]byte(`not-json`))),
		}, "python llm")
		require.EqualError(t, err, "python llm returned 502: not-json")
	})

	t.Run("request helpers reject invalid payloads and destinations", func(t *testing.T) {
		client := &Client{httpBaseURL: "://bad-url", timeout: time.Second}
		require.Error(t, client.getJSON(context.Background(), "/llm/health", &struct{}{}))
		require.Error(t, client.postJSON(context.Background(), "/llm/generate", map[string]any{"x": "y"}, &map[string]any{}))
		_, err := client.generateStreamHTTP(context.Background(), &llmpb.GenerateRequest{Prompt: "x"})
		require.Error(t, err)

		_, err = client.newJSONRequest(context.Background(), http.MethodPost, "/llm/generate", map[string]any{"bad": make(chan int)})
		require.ErrorContains(t, err, "unsupported type: chan int")
	})

	t.Run("request helpers surface transport dial errors", func(t *testing.T) {
		client := &Client{httpBaseURL: "http://127.0.0.1:1", timeout: time.Second}
		_, err := client.generateStreamHTTP(context.Background(), &llmpb.GenerateRequest{Prompt: "x"})
		require.Error(t, err)

		_, err = client.RuntimeHealth(context.Background())
		require.Error(t, err)

		client = &Client{httpBaseURL: "http://127.0.0.1:1", transport: transportHTTPJSON, timeout: time.Second}
		_, err = client.StructuredOutput(context.Background(), &llmpb.StructuredRequest{Prompt: "x", JsonSchema: "{}"})
		require.Error(t, err)
	})

	t.Run("structured output HTTP surfaces remote status errors", func(t *testing.T) {
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/llm/structured-output", r.URL.Path)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"structured backend down"}}`))
		}))
		defer server.Close()

		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		client := NewClient()

		_, err := client.StructuredOutput(context.Background(), &llmpb.StructuredRequest{
			Prompt:     "p",
			JsonSchema: "{}",
		})
		require.EqualError(t, err, "python llm returned 502: structured backend down")
	})

	t.Run("stream HTTP helper surfaces transport errors", func(t *testing.T) {
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/llm/generate/stream", r.URL.Path)
			w.WriteHeader(http.StatusServiceUnavailable)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    "DOWN",
					"message": "service unavailable",
				},
			}))
		}))
		defer server.Close()

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		client := NewClient()

		_, err := client.generateStreamHTTP(context.Background(), &llmpb.GenerateRequest{Prompt: "x"})
		require.ErrorContains(t, err, "(DOWN)")
	})

	t.Run("embed HTTP helpers surface typed error payloads", func(t *testing.T) {
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/llm/embed":
				w.WriteHeader(http.StatusBadRequest)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"error": "bad embed request",
					"kind":  "permanent",
				}))
			case "/llm/embed/batch":
				w.WriteHeader(http.StatusGatewayTimeout)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"detail": map[string]any{
						"error": map[string]any{
							"message": "deadline exceeded",
							"code":    "EMBED_BATCH_TIMEOUT",
						},
					},
				}))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		client := NewClient()

		_, err := client.Embed(context.Background(), &llmpb.EmbedRequest{Text: "x"})
		require.EqualError(t, err, "python llm returned 400: bad embed request (permanent)")

		_, err = client.EmbedBatch(context.Background(), &llmpb.EmbedBatchRequest{Texts: []string{"a", "b"}})
		require.EqualError(t, err, "python llm returned 504: deadline exceeded (EMBED_BATCH_TIMEOUT)")
	})
}

func TestLLMHTTPStream_EdgeCases(t *testing.T) {
	t.Run("Recv handles stream errors and empty events", func(t *testing.T) {
		streamErr := newLLMHTTPStream(context.Background(), &http.Response{
			Body: io.NopCloser(strings.NewReader(`{"error":{"code":"RATE_LIMITED","message":"backoff triggered"}}` + "\n")),
		})
		_, err := streamErr.Recv()
		require.ErrorContains(t, err, "python llm stream failed: backoff triggered (RATE_LIMITED)")

		streamMsgOnly := newLLMHTTPStream(context.Background(), &http.Response{
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"service unavailable"}}` + "\n")),
		})
		_, err = streamMsgOnly.Recv()
		require.EqualError(t, err, "python llm stream failed: service unavailable")

		streamEmptyError := newLLMHTTPStream(context.Background(), &http.Response{
			Body: io.NopCloser(strings.NewReader(`{"error":{}}` + "\n")),
		})
		_, err = streamEmptyError.Recv()
		require.EqualError(t, err, "python llm stream failed")

		emptyStream := newLLMHTTPStream(context.Background(), &http.Response{
			Body: io.NopCloser(strings.NewReader(`{}` + "\n")),
		})
		_, err = emptyStream.Recv()
		require.EqualError(t, err, "python llm stream emitted an empty event")

		malformedStream := newLLMHTTPStream(context.Background(), &http.Response{
			Body: io.NopCloser(strings.NewReader(`{`)),
		})
		_, err = malformedStream.Recv()
		require.Error(t, err)
	})

	t.Run("RecvMsg validates target type and can map chunks", func(t *testing.T) {
		invalidTarget := newLLMHTTPStream(context.Background(), &http.Response{
			Body: io.NopCloser(strings.NewReader(`{"chunk":{"delta":"x","done":true,"finishReason":"stop"}}` + "\n")),
		})
		var wrong any
		err := invalidTarget.RecvMsg(&wrong)
		require.Error(t, err)

		stream := newLLMHTTPStream(context.Background(), &http.Response{
			Body: io.NopCloser(strings.NewReader(`{"chunk":{"delta":"x","done":true,"finishReason":"stop"}}` + "\n")),
		})
		var target struct {
			Value string
		}
		err = stream.RecvMsg(&target)
		require.EqualError(t, err, "python llm http stream expected *llm.GenerateChunk")

		malformedForMsg := newLLMHTTPStream(context.Background(), &http.Response{
			Body: io.NopCloser(strings.NewReader(`{`)),
		})
		var malformedChunk llmpb.GenerateChunk
		err = malformedForMsg.RecvMsg(&malformedChunk)
		require.Error(t, err)

		okStream := newLLMHTTPStream(context.Background(), &http.Response{
			Body: io.NopCloser(strings.NewReader(`{"chunk":{"delta":"y","done":true,"finishReason":"stop"}}` + "\n")),
		})
		var chunk llmpb.GenerateChunk
		err = okStream.RecvMsg(&chunk)
		require.NoError(t, err)
		require.Equal(t, "y", chunk.Delta)
		require.True(t, chunk.Done)
	})

	t.Run("decodeHTTPError handles detail without explicit code", func(t *testing.T) {
		err := decodeHTTPError(&http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"detail":{"message":"bad gateway"}}`))),
		}, "python llm")
		require.EqualError(t, err, "python llm returned 502: bad gateway")
	})

	t.Run("Header and trailer metadata are preserved", func(t *testing.T) {
		stream := newLLMHTTPStream(context.Background(), &http.Response{
			Body: io.NopCloser(strings.NewReader(`{}`)),
			Header: http.Header{
				"X-From": {"unit"},
			},
			Trailer: http.Header{
				"X-Trailer": {"done"},
			},
		})
		headers, err := stream.Header()
		require.NoError(t, err)
		require.Equal(t, []string{"unit"}, headers["x-from"])
		require.NoError(t, stream.CloseSend())
		require.Equal(t, []string{"done"}, stream.Trailer()["x-trailer"])
		require.NoError(t, stream.CloseSend())

		md := httpHeaderToMetadata(http.Header{
			"X-A": {"1", "2"},
			"":    {"bad"},
		})
		require.Equal(t, []string{"1", "2"}, md["x-a"])
		_, has := md[""]
		require.False(t, has)
	})
}
