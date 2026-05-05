package llm

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestClient_WithoutVertexDirect(t *testing.T) {
	mockClient := &mockLLMServiceClient{}
	c := &Client{
		grpcAddr:    "127.0.0.1:50051",
		httpBaseURL: "http://127.0.0.1:8080",
		transport:   transportGRPC,
		timeout:     2 * time.Second,
		client:      mockClient,
		VertexDirect: &VertexClient{
			client: new(mockGenAIModels),
		},
	}

	without := c.WithoutVertexDirect()
	assert.NotNil(t, without)
	assert.Nil(t, without.VertexDirect)
	assert.Equal(t, c.grpcAddr, without.grpcAddr)
	assert.Equal(t, c.httpBaseURL, without.httpBaseURL)
	assert.Equal(t, c.transport, without.transport)
	assert.Equal(t, c.timeout, without.timeout)
	assert.Same(t, mockClient, without.client)
}

func TestClient_WithoutVertexDirectNilReceiver(t *testing.T) {
	var c *Client
	assert.Nil(t, c.WithoutVertexDirect())
}

func TestClient_CloseNoOpWhenNoConnection(t *testing.T) {
	c := &Client{}
	require.NoError(t, c.Close())
}

func TestClient_RuntimeHealthNilClient(t *testing.T) {
	var c *Client
	_, err := c.RuntimeHealth(context.Background())
	require.Error(t, err)
	require.Equal(t, errNilClient, err)
}

func TestClient_IsColdStartWindowAndUptime(t *testing.T) {
	prev := processStartTime
	t.Cleanup(func() {
		processStartTime = prev
	})

	processStartTime = time.Now().Add(-10 * time.Second)
	assert.True(t, IsColdStartWindow())
	assert.Greater(t, ProcessUptimeMs(), int64(0))

	processStartTime = time.Now().Add(-(ColdStartWindow + time.Second))
	assert.False(t, IsColdStartWindow())
	assert.Greater(t, ProcessUptimeMs(), int64(0))
}

func TestClient_StructuredOutput_FallbackPathError(t *testing.T) {
	mockModels := new(mockGenAIModels)
	mockModels.On(
		"GenerateContent",
		mock.Anything,
		"gemini-2.5-flash-lite",
		mock.Anything,
		mock.Anything,
	).Return(nil, errors.New("serviceClass parameter is not supported in Vertex AI")).Once()

	sidecar := new(mockLLMServiceClient)
	sidecar.On("StructuredOutput", mock.Anything, mock.Anything).
		Return(nil, errors.New("downstream unavailable")).Once()

	client := &Client{
		transport:    transportGRPC,
		client:       sidecar,
		VertexDirect: &VertexClient{client: mockModels},
	}

	_, err := client.StructuredOutput(context.Background(), &llmpb.StructuredRequest{
		Prompt:       "who are you?",
		Model:        "gemini-2.5-flash-lite",
		JsonSchema:   `{"type":"object","properties":{"answer":{"type":"string"}}}`,
		RequestClass: "structured_high_value",
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "vertex direct structured output failed")
	require.ErrorContains(t, err, "sidecar fallback failed")
	mockModels.AssertExpectations(t)
	sidecar.AssertExpectations(t)
}

func TestClient_ResolveTransport(t *testing.T) {
	t.Run("grpc/proxy precedence and HTTPS fallback", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", transportGRPC)
		assert.Equal(t, transportGRPC, resolveTransport("http://127.0.0.1:1234"))
		assert.Equal(t, transportGRPC, resolveTransport("http://127.0.0.1:1234/health"))

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", transportHTTPJSON)
		assert.Equal(t, transportHTTPJSON, resolveTransport("http://127.0.0.1:1234"))

		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "unknown")
		assert.Equal(t, transportGRPC, resolveTransport("http://127.0.0.1:1234"))
		assert.Equal(t, transportHTTPJSON, resolveTransport("https://example.com"))
	})

	t.Run("empty env value chooses inference path", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "")
		assert.Equal(t, transportGRPC, resolveTransport("http://127.0.0.1:1234"))
		assert.Equal(t, transportHTTPJSON, resolveTransport("https://example.com"))
	})
}

func TestClient_NewClientFallsBackToStackConfigBaseURL(t *testing.T) {
	origOverlays := stackconfig.Manifest.Overlays
	t.Cleanup(func() {
		stackconfig.Manifest.Overlays = origOverlays
	})
	stackconfig.Manifest.Overlays = map[string]stackconfig.ManifestOverlay{}
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", "")
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "")

	client := NewClientWithTimeout(250 * time.Millisecond)

	assert.Equal(t, stackconfig.ResolveBaseURL("python_sidecar"), client.httpBaseURL)
	assert.Equal(t, 250*time.Millisecond, client.timeout)
}

func TestClient_NewClientOIDCAudienceBranch(t *testing.T) {
	origOverlays := stackconfig.Manifest.Overlays
	t.Cleanup(func() {
		stackconfig.Manifest.Overlays = origOverlays
	})
	stackconfig.Manifest.Overlays = map[string]stackconfig.ManifestOverlay{}
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", "")
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "")
	t.Setenv("GOOGLE_OIDC_AUDIENCE", "https://example.com/llm")

	client := NewClientWithTimeout(500 * time.Millisecond)
	assert.NotNil(t, client)
}

func TestClient_WithTimeout(t *testing.T) {
	mockClient := &mockLLMServiceClient{}
	base := &Client{
		grpcAddr:    "127.0.0.1:50051",
		httpBaseURL: "http://127.0.0.1:8080",
		transport:   transportGRPC,
		timeout:     5 * time.Second,
		client:      mockClient,
		VertexDirect: &VertexClient{
			client: new(mockGenAIModels),
		},
	}

	t.Run("returns cloned client with updated timeout", func(t *testing.T) {
		cloned := base.WithTimeout(2 * time.Second)
		require.NotNil(t, cloned)
		assert.Equal(t, base.grpcAddr, cloned.grpcAddr)
		assert.Equal(t, base.httpBaseURL, cloned.httpBaseURL)
		assert.Equal(t, base.transport, cloned.transport)
		assert.Same(t, mockClient, cloned.client)
		assert.Equal(t, base.VertexDirect, cloned.VertexDirect)
		assert.Equal(t, 2*time.Second, cloned.timeout)
	})

	t.Run("nil client returns nil", func(t *testing.T) {
		var nilClient *Client
		assert.Nil(t, nilClient.WithTimeout(3*time.Second))
	})
}

func TestClient_NewClientAndSetClient(t *testing.T) {
	t.Run("new client trims explicit HTTP URL and applies test timeout", func(t *testing.T) {
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", transportHTTPJSON)
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", "http://127.0.0.1:8080/")
		client := NewClient()

		assert.Equal(t, "http://127.0.0.1:8080", client.httpBaseURL)
		assert.Equal(t, transportHTTPJSON, client.transport)
		assert.Equal(t, 500*time.Millisecond, client.timeout)
	})

	t.Run("set client updates transport and stores handle", func(t *testing.T) {
		mvc := &mockLLMServiceClient{}
		client := &Client{
			transport: transportHTTPJSON,
			client:    nil,
		}
		client.SetClient(mvc)
		assert.Same(t, mvc, client.client)
		assert.Equal(t, transportGRPC, client.transport)
	})
}

func TestClient_ensureClientFailurePath(t *testing.T) {
	// Empty grpc target should fail fast with nil-client configuration error.
	client := &Client{
		grpcAddr:  "",
		transport: transportGRPC,
	}
	err := client.ensureClient(context.Background())
	assert.Equal(t, errNilClient, err)

	client = &Client{
		grpcAddr:  "   ",
		transport: transportGRPC,
	}
	err = client.ensureClient(context.Background())
	assert.Equal(t, errNilClient, err)
}

func TestClient_TransportName(t *testing.T) {
	t.Run("nil transport returns empty name", func(t *testing.T) {
		var c *Client
		assert.Equal(t, "", c.TransportName())
	})

	t.Run("non-empty transport value is normalized", func(t *testing.T) {
		c := &Client{transport: "  http-json  "}
		assert.Equal(t, "http-json", c.TransportName())
	})
}

func TestClient_ensureClient(t *testing.T) {
	t.Run("http transport bypasses grpc lazy client creation", func(t *testing.T) {
		c := &Client{
			transport:   transportHTTPJSON,
			grpcAddr:    "   ",
			httpBaseURL: "http://127.0.0.1:8080",
		}
		require.NoError(t, c.ensureClient(context.Background()))
		assert.Nil(t, c.client)
	})

	t.Run("http transport with whitespace still bypasses grpc client creation", func(t *testing.T) {
		c := &Client{
			transport:   "  " + transportHTTPJSON + "  ",
			grpcAddr:    "",
			httpBaseURL: "http://127.0.0.1:8080",
		}
		require.NoError(t, c.ensureClient(context.Background()))
		assert.Nil(t, c.client)
	})

	t.Run("grpc transport initializes and reuses client", func(t *testing.T) {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer lis.Close()

		serverURL := lis.Addr().String()

		// grpc.NewClient validates target format and creates lazy connection; no
		// registration is required for this unit test.
		c := &Client{
			grpcAddr:  serverURL,
			transport: transportGRPC,
		}

		require.NoError(t, c.ensureClient(context.Background()))
		require.NotNil(t, c.client)

		// Second call should be a no-op when client is already initialized.
		require.NoError(t, c.ensureClient(context.Background()))
		assert.NotNil(t, c.client)
	})

	t.Run("reuses injected client without grpc address", func(t *testing.T) {
		mockSvc := new(mockLLMServiceClient)
		client := &Client{
			grpcAddr:  "",
			transport: transportGRPC,
		}
		client.SetClient(mockSvc)

		require.NoError(t, client.ensureClient(context.Background()))
		assert.Same(t, mockSvc, client.client)
	})
}

func TestClient_WarmUpProbeAndRetryNilAndCanceledPaths(t *testing.T) {
	var nilClient *Client
	err := nilClient.WarmUpProbe(context.Background())
	require.ErrorIs(t, err, errNilClient)

	c := &Client{}
	err = c.WarmUpProbe(context.Background())
	require.Error(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, c.WarmUpWithRetry(ctx, 0), context.Canceled)
}

func TestClient_GrpcEnsureClientFailuresAndWarmUpSuccess(t *testing.T) {
	failing := &Client{
		transport: transportGRPC,
	}

	_, err := failing.Generate(context.Background(), &llmpb.GenerateRequest{Prompt: "hello"})
	require.ErrorIs(t, err, errNilClient)

	_, err = failing.GenerateStream(context.Background(), &llmpb.GenerateRequest{Prompt: "hello"})
	require.ErrorIs(t, err, errNilClient)

	_, err = failing.StructuredOutput(context.Background(), &llmpb.StructuredRequest{
		Prompt:     "hello",
		JsonSchema: `{"type":"object"}`,
	})
	require.ErrorIs(t, err, errNilClient)

	_, err = failing.Embed(context.Background(), &llmpb.EmbedRequest{Text: "hello"})
	require.ErrorIs(t, err, errNilClient)

	_, err = failing.EmbedBatch(context.Background(), &llmpb.EmbedBatchRequest{Texts: []string{"hello"}})
	require.ErrorIs(t, err, errNilClient)

	healthClient := &Client{
		transport: transportGRPC,
		grpcAddr:  "127.0.0.1:50051",
	}
	mockSvc := new(mockLLMServiceClient)
	mockSvc.On("Health", mock.Anything, mock.Anything).Return(&llmpb.HealthResponse{Ok: true, Version: "v1"}, nil).Once()
	healthClient.SetClient(mockSvc)

	require.NoError(t, healthClient.WarmUpWithRetry(context.Background(), 1))
	mockSvc.AssertExpectations(t)
}

func TestClient_GrpcGeneratePath(t *testing.T) {
	mockSvc := new(mockLLMServiceClient)
	client := &Client{
		transport: transportGRPC,
	}
	client.SetClient(mockSvc)

	mockSvc.On("Generate", mock.Anything, mock.Anything).
		Return(&llmpb.GenerateResponse{Text: "grpc-ok"}, nil).Once()

	resp, err := client.Generate(context.Background(), &llmpb.GenerateRequest{Prompt: "hello"})
	require.NoError(t, err)
	require.Equal(t, "grpc-ok", resp.Text)
	mockSvc.AssertExpectations(t)
}

func TestClient_NilReceiverMethodBranches(t *testing.T) {
	var c *Client

	_, err := c.Generate(context.Background(), &llmpb.GenerateRequest{})
	require.ErrorIs(t, err, errNilClient)

	_, err = c.GenerateStream(context.Background(), &llmpb.GenerateRequest{})
	require.ErrorIs(t, err, errNilClient)

	_, err = c.StructuredOutput(context.Background(), &llmpb.StructuredRequest{JsonSchema: `{"type":"object"}`})
	require.ErrorIs(t, err, errNilClient)

	_, err = c.Embed(context.Background(), &llmpb.EmbedRequest{})
	require.ErrorIs(t, err, errNilClient)

	_, err = c.EmbedBatch(context.Background(), &llmpb.EmbedBatchRequest{})
	require.ErrorIs(t, err, errNilClient)

	_, err = c.Health(context.Background())
	require.ErrorIs(t, err, errNilClient)
}

func TestClient_RuntimeHealthHTTPFailure(t *testing.T) {
	c := &Client{transport: transportHTTPJSON}
	_, err := c.RuntimeHealth(context.Background())
	require.Error(t, err)
}
