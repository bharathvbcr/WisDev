package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const (
	transportGRPC     = "grpc"
	transportHTTPJSON = "http-json"
)

var (
	errNilClient                      = errors.New("llm client is not configured")
	errStructuredOutputSchemaRequired = errors.New("structured output requires json_schema")
	errStructuredProviderCoolingDown  = errors.New("vertex structured output provider cooldown active")
	recoverableStructuredSlots        = make(chan struct{}, 1)
	recoverableStructuredPaceMu       sync.Mutex
	recoverableStructuredLastStart    time.Time
)

const recoverableStructuredMinSpacing = 2 * time.Second

// Client wraps the Go -> Python LLM client. It uses gRPC for local/container
// sidecar hops and authenticated HTTP JSON for remote overlays such as Cloud
// Run where a second cross-service gRPC port is not available.
// When VertexDirect is set, StructuredOutput calls bypass the Python sidecar
// entirely and use the native Gemini SDK for proper controlled generation.
type Client struct {
	grpcAddr    string
	httpBaseURL string
	transport   string
	timeout     time.Duration

	mu     sync.Mutex
	conn   *grpc.ClientConn
	client llmpb.LLMServiceClient

	tokenSource  oauth2.TokenSource
	VertexDirect *VertexClient // optional: bypasses Python sidecar for structured output
}

type RuntimeDependency struct {
	Name      string
	Transport string
	Status    string
	Source    string
	Detail    string
}

type RuntimeHealth struct {
	Service      string
	Status       string
	Transport    string
	Dependencies []RuntimeDependency
}

// NewClient creates a new LLM sidecar client.
// In test binaries (testing.Testing() == true) the HTTP backstop timeout is
// capped at 500 ms so that tests that don't mock the sidecar fail quickly
// rather than blocking for the full production timeout.
func NewClient() *Client {
	timeout := 10 * time.Second
	if testing.Testing() {
		timeout = 500 * time.Millisecond
	}
	return newClient(timeout)
}

// NewClientWithTimeout creates a client with an explicit http.Client backstop
// timeout. Use this when you need a specific timeout regardless of test mode.
func NewClientWithTimeout(timeout time.Duration) *Client {
	return newClient(timeout)
}

func newClient(timeout time.Duration) *Client {
	httpBaseURL := strings.TrimRight(stackconfig.ResolveEnv("PYTHON_SIDECAR_HTTP_URL"), "/")
	if httpBaseURL == "" {
		httpBaseURL = strings.TrimRight(stackconfig.ResolveBaseURL("python_sidecar"), "/")
	}

	c := &Client{
		grpcAddr:    stackconfig.ResolveGRPCTarget("python_sidecar"),
		httpBaseURL: httpBaseURL,
		transport:   resolveTransport(httpBaseURL),
		// Per-request http.Client backstop. In normal operation the caller's
		// context-owned request policy fires first; this guards against callers
		// that forget to set a deadline.
		timeout: timeout,
	}

	// Production mode: prefer service-to-service OIDC when configured.
	if aud := os.Getenv("GOOGLE_OIDC_AUDIENCE"); aud != "" {
		ts, err := idtoken.NewTokenSource(context.Background(), aud)
		if err != nil {
			slog.Warn("llm client: OIDC token source init failed; falling back to unauthenticated requests",
				"audience", aud, "error", err)
		} else {
			c.tokenSource = ts
		}
	}

	return c
}

// WithoutVertexDirect returns a lightweight Client view that always routes
// StructuredOutput through the Python sidecar (gRPC or HTTP), bypassing
// VertexDirect. This is used for LLM calls inside goroutines where the Python
// sidecar path is preferred because:
//   - gRPC cancellation propagates correctly from the goroutine's context
//   - The sidecar uses the already-working Cloud Function proxy
//   - VertexDirect can block in oauth2.TokenSource.Token() which is not
//     context-aware, potentially holding the goroutine indefinitely
//
// The returned client is a new instance that re-establishes the sidecar
// connection lazily on first use (fast for local loopback targets).
func (c *Client) WithoutVertexDirect() *Client {
	clone := c.clone()
	if clone == nil {
		return nil
	}
	// VertexDirect deliberately not set — routes through sidecar.
	clone.VertexDirect = nil
	return clone
}

// WithTimeout returns a lightweight Client view with the same routing/config
// but a different per-request HTTP backstop timeout. This keeps call-site
// budgets explicit for latency-sensitive flows without mutating the shared
// client instance.
func (c *Client) WithTimeout(timeout time.Duration) *Client {
	clone := c.clone()
	if clone == nil {
		return nil
	}
	clone.timeout = timeout
	return clone
}

func (c *Client) clone() *Client {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return &Client{
		grpcAddr:     c.grpcAddr,
		httpBaseURL:  c.httpBaseURL,
		transport:    c.transport,
		timeout:      c.timeout,
		conn:         c.conn,
		client:       c.client,
		tokenSource:  c.tokenSource,
		VertexDirect: c.VertexDirect,
	}
}

func resolveTransport(httpBaseURL string) string {
	switch strings.ToLower(strings.TrimSpace(stackconfig.ResolveEnv("PYTHON_SIDECAR_LLM_TRANSPORT"))) {
	case transportHTTPJSON, "http", "http_json":
		return transportHTTPJSON
	case transportGRPC, "grpc-protobuf", "":
		// Fall through to inference below for the empty string case.
	default:
		// Invalid explicit values degrade to inference instead of breaking boot.
	}

	// Remote HTTPS sidecars cannot expose a separate cross-service gRPC port on
	// Cloud Run, so default them to authenticated HTTP even if the transport env
	// was omitted during deployment.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(httpBaseURL)), "https://") {
		return transportHTTPJSON
	}
	return transportGRPC
}

func (c *Client) useHTTPTransport() bool {
	return strings.EqualFold(strings.TrimSpace(c.transport), transportHTTPJSON)
}

func (c *Client) TransportName() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.transport)
}

// ProviderCooldownRemaining reports a known direct-provider cooldown for
// callers that can avoid optional fan-out and use deterministic fallbacks.
func (c *Client) ProviderCooldownRemaining() time.Duration {
	if c == nil || c.VertexDirect == nil {
		return 0
	}
	return VertexProviderRateLimitRemaining()
}

func IsProviderRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	raw := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(raw, "rate limited") ||
		strings.Contains(raw, "provider cooldown active") ||
		strings.Contains(raw, "cooldown active") ||
		strings.Contains(raw, "rate_limit") ||
		strings.Contains(raw, "resource exhausted") ||
		strings.Contains(raw, "resource_exhausted") ||
		strings.Contains(raw, "429") ||
		strings.Contains(raw, " error 429") ||
		strings.Contains(raw, "429,")
}

func (c *Client) ensureClient(ctx context.Context) error {
	if c == nil {
		return errNilClient
	}
	if c.useHTTPTransport() {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return nil
	}
	if strings.TrimSpace(c.grpcAddr) == "" {
		return errNilClient
	}

	// grpc.NewClient creates a lazy client — no connection is established until
	// the first RPC call. This keeps startup fast and surfaces connectivity
	// issues on the actual request path where the caller's context applies.
	conn, err := grpc.NewClient(
		c.grpcAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	c.conn = conn
	c.client = llmpb.NewLLMServiceClient(conn)
	return nil
}

func (c *Client) injectMetadata(ctx context.Context, reqMetadata map[string]string) context.Context {
	md := metadata.New(make(map[string]string))

	if traceID := telemetry.TraceIDFrom(ctx); traceID != "" {
		md.Set("trace_id", traceID)
	}
	md.Set("x-contract-version", "v3")
	md.Set("x-caller-service", "go_orchestrator")

	if c.tokenSource != nil {
		token, err := c.tokenSource.Token()
		if err == nil {
			md.Set("authorization", "Bearer "+token.AccessToken)
		}
	}

	if key := stackconfig.ResolveInternalServiceKey(); key != "" {
		md.Set("internal_service_key", key)
	}

	for k, v := range reqMetadata {
		md.Set(k, v)
	}

	return metadata.NewOutgoingContext(ctx, md)
}

func (c *Client) applyHTTPHeaders(ctx context.Context, headers http.Header) {
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Contract-Version", "v3")
	headers.Set("X-Caller-Service", "go_orchestrator")

	if traceID := telemetry.TraceIDFrom(ctx); traceID != "" {
		headers.Set("X-Trace-Id", traceID)
	}

	if c.tokenSource != nil {
		token, err := c.tokenSource.Token()
		if err == nil {
			headers.Set("Authorization", "Bearer "+token.AccessToken)
		}
	}

	if key := stackconfig.ResolveInternalServiceKey(); key != "" {
		headers.Set("X-Internal-Service-Key", key)
	}

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(headers))
}

// Generate produces a text completion.
func (c *Client) Generate(ctx context.Context, req *llmpb.GenerateRequest) (*llmpb.GenerateResponse, error) {
	start := time.Now()
	if c == nil {
		telemetry.RecordLLMRequest("generate", req.GetModel(), errNilClient, time.Since(start))
		return nil, errNilClient
	}
	coldStart := IsColdStartWindow()
	if c.useHTTPTransport() {
		resp, err := c.generateHTTP(ctx, req)
		telemetry.RecordLLMBudgetRequest("generate", req.GetModel(), err, time.Since(start), int64(req.GetLatencyBudgetMs()), coldStart)
		return resp, err
	}

	ctx = c.injectMetadata(ctx, req.Metadata)
	if err := c.ensureClient(ctx); err != nil {
		telemetry.RecordLLMBudgetRequest("generate", req.GetModel(), err, time.Since(start), int64(req.GetLatencyBudgetMs()), coldStart)
		return nil, err
	}
	resp, err := c.client.Generate(ctx, req)
	telemetry.RecordLLMBudgetRequest("generate", req.GetModel(), err, time.Since(start), int64(req.GetLatencyBudgetMs()), coldStart)
	return resp, err
}

// GenerateStream streams a text completion.
func (c *Client) GenerateStream(ctx context.Context, req *llmpb.GenerateRequest) (llmpb.LLMService_GenerateStreamClient, error) {
	if c == nil {
		return nil, errNilClient
	}
	if c.useHTTPTransport() {
		return c.generateStreamHTTP(ctx, req)
	}

	ctx = c.injectMetadata(ctx, req.Metadata)
	if err := c.ensureClient(ctx); err != nil {
		return nil, err
	}
	return c.client.GenerateStream(ctx, req)
}

func (c *Client) structuredOutputViaSidecar(ctx context.Context, req *llmpb.StructuredRequest) (*llmpb.StructuredResponse, error) {
	if c.useHTTPTransport() {
		return c.structuredOutputHTTP(ctx, req)
	}

	ctx = c.injectMetadata(ctx, req.Metadata)
	if err := c.ensureClient(ctx); err != nil {
		return nil, err
	}
	return c.client.StructuredOutput(ctx, req)
}

func shouldFallbackStructuredOutputToSidecar(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "parameter is not supported in vertex ai") ||
		strings.Contains(message, "extra inputs are not permitted") ||
		strings.Contains(message, "unexpected keyword argument")
}

func isRecoverableStructuredRequest(req *llmpb.StructuredRequest) bool {
	if req == nil {
		return false
	}
	requestClass := strings.ToLower(strings.TrimSpace(req.GetRequestClass()))
	if requestClass != "standard" {
		return false
	}
	tier := strings.ToLower(strings.TrimSpace(req.GetServiceTier()))
	return tier == "" || tier == "standard"
}

func acquireRecoverableStructuredSlot(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case recoverableStructuredSlots <- struct{}{}:
		return func() { <-recoverableStructuredSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func paceRecoverableStructuredOutput(ctx context.Context, now time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	recoverableStructuredPaceMu.Lock()
	defer recoverableStructuredPaceMu.Unlock()

	if !recoverableStructuredLastStart.IsZero() {
		nextAllowed := recoverableStructuredLastStart.Add(recoverableStructuredMinSpacing)
		if delay := nextAllowed.Sub(now); delay > 0 {
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay+250*time.Millisecond {
				return context.DeadlineExceeded
			}
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return ctx.Err()
			}
		}
	}
	recoverableStructuredLastStart = time.Now()
	return nil
}

func (c *Client) guardRecoverableStructuredOutput(ctx context.Context, req *llmpb.StructuredRequest) (func(), error) {
	if c == nil || c.VertexDirect == nil || !isRecoverableStructuredRequest(req) {
		return func() {}, nil
	}
	if remaining := c.ProviderCooldownRemaining(); remaining > 0 {
		return nil, fmt.Errorf("%w; retry after %s", errStructuredProviderCoolingDown, remaining.Round(time.Millisecond))
	}
	release, err := acquireRecoverableStructuredSlot(ctx)
	if err != nil {
		return nil, err
	}
	if err := paceRecoverableStructuredOutput(ctx, time.Now()); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// StructuredOutput generates a JSON response from a schema.
// When VertexDirect is configured it calls the Gemini SDK natively with
// response_mime_type + response_json_schema (official controlled generation),
// which guarantees schema-constrained output. Falls back to the Python sidecar
// when VertexDirect is nil.
func (c *Client) StructuredOutput(ctx context.Context, req *llmpb.StructuredRequest) (*llmpb.StructuredResponse, error) {
	start := time.Now()
	if c == nil {
		telemetry.RecordLLMRequest("structured", req.GetModel(), errNilClient, time.Since(start))
		return nil, errNilClient
	}
	if req == nil || strings.TrimSpace(req.GetJsonSchema()) == "" {
		err := errStructuredOutputSchemaRequired
		telemetry.RecordLLMRequest("structured", "", err, time.Since(start))
		return nil, err
	}
	coldStart := IsColdStartWindow()

	// Prefer native Gemini SDK path for proper controlled generation.
	// When the transport is explicitly http-json (sidecar mode), skip VertexDirect
	// so the request routes through the HTTP sidecar for lower latency.
	if c.VertexDirect != nil && !c.useHTTPTransport() {
		release, guardErr := c.guardRecoverableStructuredOutput(ctx, req)
		if guardErr != nil {
			slog.Warn("llm recoverable structured output skipped before provider call",
				"component", "llm.client",
				"operation", "structured_output",
				"stage", "recoverable_backpressure",
				"model", req.GetModel(),
				"request_class", req.GetRequestClass(),
				"service_tier", req.GetServiceTier(),
				"error", guardErr.Error(),
			)
			telemetry.RecordLLMBudgetRequest("structured_direct_backpressure", req.GetModel(), guardErr, time.Since(start), int64(req.GetLatencyBudgetMs()), coldStart)
			return nil, guardErr
		}
		defer release()
		resp, err := c.structuredOutputDirect(ctx, req)
		if err != nil && shouldFallbackStructuredOutputToSidecar(err) {
			slog.Warn("llm structured output direct compatibility fallback",
				"component", "llm.client",
				"operation", "structured_output",
				"stage", "vertex_direct_fallback",
				"model", req.GetModel(),
				"transport", c.TransportName(),
				"error", err.Error(),
				"reason", "vertex_direct_unsupported_parameter",
			)
			sidecarResp, sidecarErr := c.structuredOutputViaSidecar(ctx, req)
			if sidecarErr == nil {
				slog.Info("llm structured output sidecar fallback succeeded",
					"component", "llm.client",
					"operation", "structured_output",
					"stage", "vertex_direct_fallback_succeeded",
					"model", req.GetModel(),
					"transport", c.TransportName(),
				)
				telemetry.RecordLLMBudgetRequest("structured_direct_sidecar_fallback", req.GetModel(), nil, time.Since(start), int64(req.GetLatencyBudgetMs()), coldStart)
				return sidecarResp, nil
			}
			err = fmt.Errorf("vertex direct structured output failed: %w; sidecar fallback failed: %v", err, sidecarErr)
		}
		telemetry.RecordLLMBudgetRequest("structured_direct", req.GetModel(), err, time.Since(start), int64(req.GetLatencyBudgetMs()), coldStart)
		return resp, err
	}

	resp, err := c.structuredOutputViaSidecar(ctx, req)
	telemetry.RecordLLMBudgetRequest("structured", req.GetModel(), err, time.Since(start), int64(req.GetLatencyBudgetMs()), coldStart)
	return resp, err
}

// structuredOutputDirect calls Gemini natively via VertexClient, using
// response_mime_type="application/json" and response_json_schema so the
// model is constrained at token level. This is the correct path for all
// environments that use the Vertex AI Cloud Function proxy, which otherwise
// strips the schema and falls back to free-form generation.
// Uses the token-aware internal helper to report accurate usage counters.
func (c *Client) structuredOutputDirect(ctx context.Context, req *llmpb.StructuredRequest) (*llmpb.StructuredResponse, error) {
	start := time.Now()
	result, inputTokens, outputTokens, err := c.VertexDirect.generateStructuredWithTokens(
		ctx,
		req.GetModel(),
		req.GetPrompt(),
		req.GetSystemPrompt(),
		req.GetJsonSchema(),
		req.GetTemperature(),
		req.GetMaxTokens(),
		req.GetServiceTier(),
		req.ThinkingBudget,
		req.GetRequestClass(),
		req.GetRetryProfile(),
	)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		return nil, err
	}
	// Fall back to estimate if the API didn't return usage metadata.
	if inputTokens == 0 {
		inputTokens = int32(len(req.GetPrompt()) / 4)
	}
	if outputTokens == 0 {
		outputTokens = int32(len(result) / 4)
	}
	return &llmpb.StructuredResponse{
		JsonResult:   result,
		ModelUsed:    req.GetModel(),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		SchemaValid:  true,
		LatencyMs:    latencyMs,
	}, nil
}

// Embed generates a single embedding.
func (c *Client) Embed(ctx context.Context, req *llmpb.EmbedRequest) (*llmpb.EmbedResponse, error) {
	start := time.Now()
	if c == nil {
		telemetry.RecordLLMRequest("embed", req.GetModel(), errNilClient, time.Since(start))
		return nil, errNilClient
	}
	if c.useHTTPTransport() {
		resp, err := c.embedHTTP(ctx, req)
		telemetry.RecordLLMRequest("embed", req.GetModel(), err, time.Since(start))
		return resp, err
	}

	ctx = c.injectMetadata(ctx, req.Metadata)
	if err := c.ensureClient(ctx); err != nil {
		telemetry.RecordLLMRequest("embed", req.GetModel(), err, time.Since(start))
		return nil, err
	}
	resp, err := c.client.Embed(ctx, req)
	telemetry.RecordLLMRequest("embed", req.GetModel(), err, time.Since(start))
	return resp, err
}

// EmbedBatch generates multiple embeddings.
func (c *Client) EmbedBatch(ctx context.Context, req *llmpb.EmbedBatchRequest) (*llmpb.EmbedBatchResponse, error) {
	start := time.Now()
	if c == nil {
		telemetry.RecordLLMRequest("embed_batch", req.GetModel(), errNilClient, time.Since(start))
		return nil, errNilClient
	}
	if c.useHTTPTransport() {
		resp, err := c.embedBatchHTTP(ctx, req)
		telemetry.RecordLLMRequest("embed_batch", req.GetModel(), err, time.Since(start))
		return resp, err
	}

	ctx = c.injectMetadata(ctx, req.Metadata)
	if err := c.ensureClient(ctx); err != nil {
		telemetry.RecordLLMRequest("embed_batch", req.GetModel(), err, time.Since(start))
		return nil, err
	}
	resp, err := c.client.EmbedBatch(ctx, req)
	telemetry.RecordLLMRequest("embed_batch", req.GetModel(), err, time.Since(start))
	return resp, err
}

// Health checks the sidecar status.
func (c *Client) Health(ctx context.Context) (*llmpb.HealthResponse, error) {
	if c == nil {
		return nil, errNilClient
	}
	if c.useHTTPTransport() {
		return c.healthHTTP(ctx)
	}
	if err := c.ensureClient(ctx); err != nil {
		return nil, err
	}
	return c.client.Health(ctx, &llmpb.HealthRequest{})
}

func (c *Client) RuntimeHealth(ctx context.Context) (*RuntimeHealth, error) {
	if c == nil {
		return nil, errNilClient
	}
	resp, err := c.runtimeHealthHTTP(ctx)
	if err != nil {
		return nil, err
	}
	dependencies := make([]RuntimeDependency, 0, len(resp.Dependencies))
	for _, dep := range resp.Dependencies {
		dependencies = append(dependencies, RuntimeDependency{
			Name:      dep.Name,
			Transport: dep.Transport,
			Status:    dep.Status,
			Source:    dep.Source,
			Detail:    dep.Detail,
		})
	}
	return &RuntimeHealth{
		Service:      resp.Service,
		Status:       resp.Status,
		Transport:    resp.Transport,
		Dependencies: dependencies,
	}, nil
}

// SetClient sets the underlying gRPC client. Primarily for testing.
func (c *Client) SetClient(client llmpb.LLMServiceClient) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transport = transportGRPC
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

// ── Cold-start warm-up ──────────────────────────────────────────────────────

var processStartTime = time.Now()

const (
	// ColdStartWindow is the window after process boot during which the sidecar
	// may still be initializing (loading models, acquiring credentials, etc.).
	ColdStartWindow = 90 * time.Second
	// WarmUpProbeTimeout is the max time to wait for a single warm-up health probe.
	WarmUpProbeTimeout = 3 * time.Second
)

// IsColdStartWindow returns true if the process is within the cold-start window.
func IsColdStartWindow() bool {
	return time.Since(processStartTime) < ColdStartWindow
}

// ProcessUptimeMs returns milliseconds since process boot.
func ProcessUptimeMs() int64 {
	return time.Since(processStartTime).Milliseconds()
}

// WarmUpProbe sends a health check to the sidecar to force connection
// establishment and any lazy initialization. Call this during server startup
// (after the sidecar is expected to be ready) to absorb cold-start latency
// before real user requests arrive.
func (c *Client) WarmUpProbe(ctx context.Context) error {
	return c.warmUpProbe(ctx, true)
}

func (c *Client) warmUpProbe(ctx context.Context, warnOnFailure bool) error {
	if c == nil {
		return errNilClient
	}
	probeCtx, cancel := context.WithTimeout(ctx, WarmUpProbeTimeout)
	defer cancel()

	start := time.Now()
	resp, err := c.Health(probeCtx)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		attrs := []any{
			"component", "llm.client",
			"operation", "warm_up_probe",
			"stage", "probe_failed",
			"transport", c.TransportName(),
			"latency_ms", latencyMs,
			"error", err.Error(),
			"cold_start_window", IsColdStartWindow(),
			"process_uptime_ms", ProcessUptimeMs(),
		}
		if warnOnFailure {
			slog.Warn("llm sidecar warm-up probe failed", attrs...)
		} else {
			slog.Info("llm sidecar warm-up probe failed; retry pending", attrs...)
		}
		return err
	}

	status := "healthy"
	if !resp.GetOk() {
		status = "degraded"
	}
	slog.Info("llm sidecar warm-up probe completed",
		"component", "llm.client",
		"operation", "warm_up_probe",
		"stage", "probe_complete",
		"transport", c.TransportName(),
		"latency_ms", latencyMs,
		"sidecar_status", status,
		"sidecar_version", resp.GetVersion(),
		"cold_start_window", IsColdStartWindow(),
		"process_uptime_ms", ProcessUptimeMs(),
	)
	return nil
}

// WarmUpWithRetry attempts the warm-up probe up to maxAttempts times with
// exponential backoff. This is designed to be called during server startup
// where the sidecar may need a few seconds to become ready.
func (c *Client) WarmUpWithRetry(ctx context.Context, maxAttempts int) error {
	if c == nil {
		return errNilClient
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := c.warmUpProbe(ctx, attempt == maxAttempts-1)
		if err == nil {
			return nil
		}
		if attempt < maxAttempts-1 {
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			slog.Info("llm sidecar warm-up retrying",
				"component", "llm.client",
				"operation", "warm_up_retry",
				"attempt", attempt+1,
				"max_attempts", maxAttempts,
				"backoff_ms", backoff.Milliseconds(),
			)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	slog.Warn("llm sidecar warm-up exhausted all attempts; proceeding with cold sidecar",
		"component", "llm.client",
		"operation", "warm_up_exhausted",
		"max_attempts", maxAttempts,
		"process_uptime_ms", ProcessUptimeMs(),
	)
	return nil // Don't block startup; proceed with potentially cold sidecar
}
