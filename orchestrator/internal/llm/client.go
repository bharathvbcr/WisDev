package llm

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/telemetry"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"

	"google.golang.org/api/idtoken"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

var errNilClient = errors.New("llm client is not configured")

// Client wraps the gRPC client for the Python LLM sidecar.
type Client struct {
	addr    string
	timeout time.Duration

	mu     sync.Mutex
	conn   *grpc.ClientConn
	client llmv1.LLMServiceClient

	// OIDC support
	tokenSource oauth2.TokenSource
}

// NewClient creates a new LLM sidecar client.
func NewClient() *Client {
	addr := os.Getenv("LLM_SIDECAR_ADDR")
	if addr == "" {
		// Fallback to legacy env var if new one is missing
		addr = os.Getenv("WISDEV_PYTHON_GRPC_ADDR")
	}
	if addr == "" {
		addr = "localhost:50051"
	}

	c := &Client{
		addr:    addr,
		timeout: 60 * time.Second,
	}

	// Initialize OIDC token source if audience is provided (production mode)
	if aud := os.Getenv("GOOGLE_OIDC_AUDIENCE"); aud != "" {
		ts, err := idtoken.NewTokenSource(context.Background(), aud)
		if err == nil {
			c.tokenSource = ts
		}
	}

	return c
}

func (c *Client) ensureClient(ctx context.Context) error {
	if c == nil {
		return errNilClient
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return nil
	}

	// Dial the sidecar. We do NOT use grpc.WithBlock() here: blocking until
	// the sidecar is reachable would hang forever in test environments (no
	// sidecar running) and delay server startup in production. Instead, the
	// connection is established lazily and errors surface on the first RPC
	// call, where the caller's request context deadline/cancellation applies.
	conn, err := grpc.DialContext(
		ctx,
		c.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	c.conn = conn
	c.client = llmv1.NewLLMServiceClient(conn)
	return nil
}

func (c *Client) injectMetadata(ctx context.Context, reqMetadata map[string]string) context.Context {
	md := metadata.New(make(map[string]string))
	
	// Add trace ID
	if traceID := telemetry.TraceIDFrom(ctx); traceID != "" {
		md.Set("trace_id", traceID)
	}

	// 1. Production: Use Google OIDC ID Token if configured
	if c.tokenSource != nil {
		token, err := c.tokenSource.Token()
		if err == nil {
			md.Set("authorization", "Bearer "+token.AccessToken)
		}
	}

	// 2. Legacy/Internal: Propagate internal service key
	if key := os.Getenv("INTERNAL_SERVICE_KEY"); key != "" {
		md.Set("internal_service_key", key)
	}

	// Add any provided metadata
	for k, v := range reqMetadata {
		md.Set(k, v)
	}

	return metadata.NewOutgoingContext(ctx, md)
}

// Generate produces a text completion.
func (c *Client) Generate(ctx context.Context, req *llmv1.GenerateRequest) (*llmv1.GenerateResponse, error) {
	start := time.Now()
	ctx = c.injectMetadata(ctx, req.Metadata)
	if err := c.ensureClient(ctx); err != nil {
		telemetry.RecordLLMRequest("generate", req.Model, err, time.Since(start))
		return nil, err
	}
	resp, err := c.client.Generate(ctx, req)
	telemetry.RecordLLMRequest("generate", req.Model, err, time.Since(start))
	return resp, err
}

// GenerateStream streams a text completion.
func (c *Client) GenerateStream(ctx context.Context, req *llmv1.GenerateRequest) (llmv1.LLMService_GenerateStreamClient, error) {
	ctx = c.injectMetadata(ctx, req.Metadata)
	if err := c.ensureClient(ctx); err != nil {
		return nil, err
	}
	return c.client.GenerateStream(ctx, req)
}

// StructuredOutput generates a JSON response from a schema.
func (c *Client) StructuredOutput(ctx context.Context, req *llmv1.StructuredRequest) (*llmv1.StructuredResponse, error) {
	start := time.Now()
	ctx = c.injectMetadata(ctx, req.Metadata)
	if err := c.ensureClient(ctx); err != nil {
		telemetry.RecordLLMRequest("structured", req.Model, err, time.Since(start))
		return nil, err
	}
	resp, err := c.client.StructuredOutput(ctx, req)
	telemetry.RecordLLMRequest("structured", req.Model, err, time.Since(start))
	return resp, err
}

// Embed generates a single embedding.
func (c *Client) Embed(ctx context.Context, req *llmv1.EmbedRequest) (*llmv1.EmbedResponse, error) {
	start := time.Now()
	ctx = c.injectMetadata(ctx, req.Metadata)
	if err := c.ensureClient(ctx); err != nil {
		telemetry.RecordLLMRequest("embed", req.Model, err, time.Since(start))
		return nil, err
	}
	resp, err := c.client.Embed(ctx, req)
	telemetry.RecordLLMRequest("embed", req.Model, err, time.Since(start))
	return resp, err
}

// EmbedBatch generates multiple embeddings.
func (c *Client) EmbedBatch(ctx context.Context, req *llmv1.EmbedBatchRequest) (*llmv1.EmbedBatchResponse, error) {
	start := time.Now()
	ctx = c.injectMetadata(ctx, req.Metadata)
	if err := c.ensureClient(ctx); err != nil {
		telemetry.RecordLLMRequest("embed_batch", req.Model, err, time.Since(start))
		return nil, err
	}
	resp, err := c.client.EmbedBatch(ctx, req)
	telemetry.RecordLLMRequest("embed_batch", req.Model, err, time.Since(start))
	return resp, err
}

// Health checks the sidecar status.
func (c *Client) Health(ctx context.Context) (*llmv1.HealthResponse, error) {
	if err := c.ensureClient(ctx); err != nil {
		return nil, err
	}
	return c.client.Health(ctx, &llmv1.HealthRequest{})
}

// SetClient sets the underlying gRPC client. Primarily for testing.
func (c *Client) SetClient(client llmv1.LLMServiceClient) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.client = client
}

// Close closes the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
