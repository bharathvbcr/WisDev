package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type fixedTokenSource struct {
	token *oauth2.Token
	err   error
}

func (f fixedTokenSource) Token() (*oauth2.Token, error) {
	return f.token, f.err
}

func TestResolveTransport(t *testing.T) {
	t.Run("honors explicit aliases and infers defaults", func(t *testing.T) {
		cases := []struct {
			name     string
			baseURL  string
			env      string
			expected string
		}{
			{name: "http alias", baseURL: "http://python-sidecar", env: "http", expected: transportHTTPJSON},
			{name: "grpc alias", baseURL: "http://python-sidecar", env: "grpc-protobuf", expected: transportGRPC},
			{name: "grpc explicit", baseURL: "https://python-sidecar", env: "grpc", expected: transportHTTPJSON},
			{name: "invalid value with https default", baseURL: "https://python-sidecar", env: "invalid", expected: transportHTTPJSON},
			{name: "inferred grpc for plain http", baseURL: "http://python-sidecar", env: "", expected: transportGRPC},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", tc.env)
				assert.Equal(t, tc.expected, resolveTransport(tc.baseURL))
			})
		}
	})
}

func TestClient_WithTimeoutAndTransportName(t *testing.T) {
	t.Run("returns cloned client with timeout override", func(t *testing.T) {
		mockClient := &mockLLMServiceClient{}
		base := &Client{
			grpcAddr:     "localhost:50051",
			httpBaseURL:  "https://python-sidecar.local",
			transport:    transportGRPC,
			timeout:      2 * time.Second,
			client:       mockClient,
			VertexDirect: &VertexClient{backend: "mock"},
		}

		next := base.WithTimeout(500 * time.Millisecond)
		require.NotNil(t, next)
		assert.NotSame(t, base, next)
		assert.Equal(t, base.grpcAddr, next.grpcAddr)
		assert.Equal(t, base.httpBaseURL, next.httpBaseURL)
		assert.Equal(t, base.transport, next.transport)
		assert.Same(t, mockClient, next.client)
		assert.Equal(t, base.VertexDirect, next.VertexDirect)
		assert.Equal(t, 500*time.Millisecond, next.timeout)
	})

	t.Run("nil client", func(t *testing.T) {
		var c *Client
		assert.Nil(t, c.WithTimeout(time.Second))
	})

	t.Run("transport name trims whitespace and handles nil", func(t *testing.T) {
		c := &Client{transport: "  " + transportHTTPJSON + "  "}
		assert.Equal(t, transportHTTPJSON, c.TransportName())

		var nilClient *Client
		assert.Empty(t, nilClient.TransportName())
	})
}

func TestNewClientWithTimeout(t *testing.T) {
	client := NewClientWithTimeout(123 * time.Millisecond)
	assert.Equal(t, 123*time.Millisecond, client.timeout)
}

func TestClient_MetadataAndHeaders(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     false,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

	t.Run("injectMetadata includes token, internal key, and base fields", func(t *testing.T) {
		c := &Client{
			tokenSource: fixedTokenSource{token: &oauth2.Token{AccessToken: "token"}},
		}
		t.Setenv("INTERNAL_SERVICE_KEY", "internal-key")

		mdCtx := c.injectMetadata(ctx, map[string]string{"foo": "bar"})
		md, ok := metadata.FromOutgoingContext(mdCtx)
		require.True(t, ok)
		assert.Equal(t, []string{"v3"}, md.Get("x-contract-version"))
		assert.Equal(t, []string{"go_orchestrator"}, md.Get("x-caller-service"))
		assert.Equal(t, []string{"Bearer token"}, md.Get("authorization"))
		assert.Equal(t, []string{"internal-key"}, md.Get("internal_service_key"))
		assert.Equal(t, []string{"bar"}, md.Get("foo"))
		assert.Equal(t, []string{traceID.String()}, md.Get("trace_id"))
	})

	t.Run("injectMetadata silently skips missing token", func(t *testing.T) {
		c := &Client{
			tokenSource: fixedTokenSource{err: errors.New("token fail")},
		}
		mdCtx := c.injectMetadata(context.Background(), nil)
		md, ok := metadata.FromOutgoingContext(mdCtx)
		require.True(t, ok)
		assert.Empty(t, md.Get("authorization"))
		assert.Empty(t, md.Get("trace_id"))
		assert.Equal(t, []string{"v3"}, md.Get("x-contract-version"))
		assert.Equal(t, []string{"go_orchestrator"}, md.Get("x-caller-service"))
	})

	t.Run("applyHTTPHeaders injects contract headers and trace id", func(t *testing.T) {
		c := &Client{
			tokenSource: fixedTokenSource{token: &oauth2.Token{AccessToken: "token"}},
		}
		t.Setenv("INTERNAL_SERVICE_KEY", "internal-key")
		headers := http.Header{}

		c.applyHTTPHeaders(ctx, headers)
		assert.Equal(t, "application/json", headers.Get("Content-Type"))
		assert.Equal(t, "v3", headers.Get("X-Contract-Version"))
		assert.Equal(t, "go_orchestrator", headers.Get("X-Caller-Service"))
		assert.Equal(t, "internal-key", headers.Get("X-Internal-Service-Key"))
		assert.Equal(t, "Bearer token", headers.Get("Authorization"))
		assert.Equal(t, traceID.String(), headers.Get("X-Trace-Id"))
	})

	t.Run("metadata and headers use local overlay internal key", func(t *testing.T) {
		t.Setenv("ENDPOINTS_MANIFEST_ENV", "local")
		t.Setenv("INTERNAL_SERVICE_KEY", "")
		c := &Client{}

		mdCtx := c.injectMetadata(context.Background(), nil)
		md, ok := metadata.FromOutgoingContext(mdCtx)
		require.True(t, ok)
		assert.Equal(t, []string{"dev-internal-key"}, md.Get("internal_service_key"))

		headers := http.Header{}
		c.applyHTTPHeaders(context.Background(), headers)
		assert.Equal(t, "dev-internal-key", headers.Get("X-Internal-Service-Key"))
	})

	t.Run("applyHTTPHeaders tolerates token failures", func(t *testing.T) {
		c := &Client{
			tokenSource: fixedTokenSource{err: errors.New("token fail")},
		}
		headers := http.Header{}
		c.applyHTTPHeaders(context.Background(), headers)
		assert.Equal(t, "application/json", headers.Get("Content-Type"))
		assert.Equal(t, "", headers.Get("Authorization"))
		assert.Equal(t, "v3", headers.Get("X-Contract-Version"))
	})
}

func TestClient_WarmUpProbeAndRetry(t *testing.T) {
	t.Run("warm-up probe accepts healthy and degraded payloads", func(t *testing.T) {
		var calls int
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/llm/health", r.URL.Path)
			calls++
			payload := map[string]any{"ok": true}
			if calls == 2 {
				payload["ok"] = false
				payload["version"] = "1.2.3"
				require.NoError(t, json.NewEncoder(w).Encode(payload))
				return
			}
			payload["version"] = "1.2.4"
			require.NoError(t, json.NewEncoder(w).Encode(payload))
		}))
		defer server.Close()

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		client := NewClient()
		require.NoError(t, client.WarmUpProbe(context.Background()))
		require.NoError(t, client.WarmUpProbe(context.Background()))
		assert.Equal(t, 2, calls)
	})

	t.Run("warm-up probe returns error for HTTP failures", func(t *testing.T) {
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		client := NewClient()
		err := client.WarmUpProbe(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "python llm returned")
	})

	t.Run("warm-up retry exhausts attempts", func(t *testing.T) {
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/llm/health", r.URL.Path)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		client := NewClient()

		start := time.Now()
		err := client.WarmUpWithRetry(context.Background(), 2)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, time.Since(start), time.Second)
	})

	t.Run("warm-up retry cancels during backoff", func(t *testing.T) {
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/llm/health", r.URL.Path)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		client := NewClient()

		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		err := client.WarmUpWithRetry(ctx, 2)
		require.Error(t, err)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("nil client returns nil-client error", func(t *testing.T) {
		var c *Client
		assert.Equal(t, errNilClient, c.WarmUpProbe(context.Background()))
		assert.Equal(t, errNilClient, c.WarmUpWithRetry(context.Background(), 1))
	})
}

func TestRuntimeHealthHTTPError(t *testing.T) {
	t.Run("runtime health success", func(t *testing.T) {
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/health", r.URL.Path)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"service":   "python_sidecar",
				"status":    "ok",
				"transport": "http-json",
			}))
		}))
		defer server.Close()

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		client := NewClient()

		resp, err := client.runtimeHealthHTTP(context.Background())
		require.NoError(t, err)
		require.Equal(t, "python_sidecar", resp.Service)
		require.Equal(t, "ok", resp.Status)
	})

	t.Run("runtime health error", func(t *testing.T) {
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/health", r.URL.Path)
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		client := NewClient()

		_, err := client.runtimeHealthHTTP(context.Background())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "python llm returned")
	})
}

func TestClient_Close(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	skipIfLoopbackProviderUnavailable(t, err)
	require.NoError(t, err)
	server := grpc.NewServer()
	go func() {
		if err := server.Serve(ln); err != nil && err != grpc.ErrServerStopped {
			t.Logf("grpc server exited: %v", err)
		}
	}()
	defer server.Stop()
	defer ln.Close()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)

	c := &Client{conn: conn}
	assert.NoError(t, c.Close())
}

func TestClient_RuntimeHealth(t *testing.T) {
	t.Run("nil client returns error", func(t *testing.T) {
		var c *Client
		_, err := c.RuntimeHealth(context.Background())
		assert.Equal(t, errNilClient, err)
	})

	t.Run("wraps runtime health payload", func(t *testing.T) {
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/health", r.URL.Path)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"service":   "python_sidecar",
				"status":    "ok",
				"transport": "http-json",
				"dependencies": []map[string]any{
					{
						"name":   "test_dependency",
						"status": "configured",
					},
				},
			}))
		}))
		defer server.Close()

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		client := NewClient()

		resp, err := client.RuntimeHealth(context.Background())
		require.NoError(t, err)
		require.Equal(t, "python_sidecar", resp.Service)
		require.Equal(t, "http-json", resp.Transport)
		require.Len(t, resp.Dependencies, 1)
		require.Equal(t, "test_dependency", resp.Dependencies[0].Name)
	})

	t.Run("preserves degraded model status from healthy sidecar transport", func(t *testing.T) {
		server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/health", r.URL.Path)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"service":   "python_sidecar",
				"status":    "degraded",
				"transport": "http-json+grpc-protobuf",
				"dependencies": []map[string]any{
					{
						"name":      "grpc_sidecar",
						"transport": "grpc-protobuf",
						"status":    "models_unavailable",
					},
				},
			}))
		}))
		defer server.Close()

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
		client := NewClient()

		resp, err := client.RuntimeHealth(context.Background())
		require.NoError(t, err)
		require.Equal(t, "degraded", resp.Status)
		require.Len(t, resp.Dependencies, 1)
		require.Equal(t, "grpc_sidecar", resp.Dependencies[0].Name)
		require.Equal(t, "models_unavailable", resp.Dependencies[0].Status)
	})
}

func TestClient_WarmUpWithRetry_DefaultAttemptLimit(t *testing.T) {
	var calls int
	server := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/llm/health", r.URL.Path)
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
	client := NewClient()

	ctx, cancel := context.WithTimeout(context.Background(), 2200*time.Millisecond)
	defer cancel()
	err := client.WarmUpWithRetry(ctx, 0)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Greater(t, calls, 0)
}
