package wisdev

// Downstream event egress.
//
// WisDev already streams structured PlanExecutionEvents to attached clients in
// real time over SSE (api/gateway.go) and gRPC (wisdev/grpc.go). Those are pull
// surfaces: a consumer only sees events while it holds a live connection.
//
// DownstreamEventSink is the egress complement. It fans the same lifecycle
// events out to operator-configured HTTP endpoints (webhooks) so downstream
// systems — queues, Slack relays, CI triggers, data warehouses — can react to a
// research run even when no client is attached. This is what makes async,
// fire-and-forget research ("kick off the run, notify me when it lands")
// possible.
//
// Design constraints (AGENTS.md "every new external integration"):
//   - Non-blocking: Dispatch never stalls the research loop. A full buffer drops
//     the event and logs it (deterministic fallback) rather than back-pressuring.
//   - Explicit per-attempt timeout, bounded retries with capped exponential
//     backoff, and cancellation propagation via the sink context.
//   - Per-endpoint circuit breaker so one dead consumer cannot starve the others.
//   - Graceful, bounded drain on shutdown so a deploy does not silently drop
//     events already queued for delivery.
//   - Optional HMAC-SHA256 signing so receivers can verify authenticity
//     (see VerifyWebhookSignature for the receiver-side check).
//   - Structured stage logs at enqueue / deliver / retry / skip / failure.
//
// Configuration (env; injected from Secret Manager in deployed environments):
//
//	WISDEV_EVENT_WEBHOOK_URL               comma-separated endpoint URLs ("" => disabled)
//	WISDEV_EVENT_WEBHOOK_SECRET            HMAC-SHA256 signing secret (recommended)
//	WISDEV_EVENT_WEBHOOK_EVENTS            comma-separated event-type allowlist (default: all)
//	WISDEV_EVENT_WEBHOOK_TIMEOUT_MS        per-attempt timeout in ms (default 5000)
//	WISDEV_EVENT_WEBHOOK_MAX_RETRIES       retries after the first attempt (default 3, 0-10)
//	WISDEV_EVENT_WEBHOOK_QUEUE_SIZE        buffered delivery queue depth (default 256)
//	WISDEV_EVENT_WEBHOOK_BREAKER_THRESHOLD consecutive failures before an endpoint trips (default 5, 0 disables)
//	WISDEV_EVENT_WEBHOOK_BREAKER_COOLDOWN_MS  open-circuit cooldown in ms (default 30000)
//	WISDEV_EVENT_WEBHOOK_DRAIN_MS           bounded drain window on shutdown in ms (default 5000)
//
// Delivery request shape:
//
//	POST <url>
//	Content-Type: application/json
//	X-WisDev-Event: <event type>
//	X-WisDev-Event-Id: <stable event id>
//	X-WisDev-Trace-Id: <trace id>
//	X-WisDev-Delivery: <unique delivery id>
//	X-WisDev-Signature: sha256=<hex hmac of the raw body>   (only when a secret is set)
//	{ "delivery", "emittedAt", "source", "specVersion", "event": <PlanExecutionEvent> }

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/telemetry"
)

const (
	envEventWebhookURL              = "WISDEV_EVENT_WEBHOOK_URL"
	envEventWebhookSecret           = "WISDEV_EVENT_WEBHOOK_SECRET"
	envEventWebhookEvents           = "WISDEV_EVENT_WEBHOOK_EVENTS"
	envEventWebhookTimeoutMS        = "WISDEV_EVENT_WEBHOOK_TIMEOUT_MS"
	envEventWebhookMaxRetries       = "WISDEV_EVENT_WEBHOOK_MAX_RETRIES"
	envEventWebhookQueueSize        = "WISDEV_EVENT_WEBHOOK_QUEUE_SIZE"
	envEventWebhookBreakerThreshold = "WISDEV_EVENT_WEBHOOK_BREAKER_THRESHOLD"
	envEventWebhookBreakerCooldown  = "WISDEV_EVENT_WEBHOOK_BREAKER_COOLDOWN_MS"
	envEventWebhookDrainMS          = "WISDEV_EVENT_WEBHOOK_DRAIN_MS"

	defaultEventWebhookTimeout          = 5 * time.Second
	defaultEventWebhookMaxRetries       = 3
	defaultEventWebhookQueueSize        = 256
	defaultEventWebhookBreakerThreshold = 5
	defaultEventWebhookBreakerCooldown  = 30 * time.Second
	defaultEventWebhookDrainTimeout     = 5 * time.Second
	maxEventWebhookBodyBytes            = 64 * 1024
	maxRetryAfter                       = 15 * time.Second
	eventWebhookUserAgent               = "WisDev-EventSink/1"
	eventWebhookComponent               = "wisdev.downstream_events"
	eventWebhookSpecVersion             = "1.0"
	eventWebhookSource                  = "wisdev.orchestrator"
)

// DownstreamEventSinkConfig is the resolved configuration for the sink.
type DownstreamEventSinkConfig struct {
	URLs             []string
	Secret           string
	EventFilter      map[PlanExecutionEventType]bool // nil/empty => deliver all event types
	Timeout          time.Duration
	MaxRetries       int
	QueueSize        int
	BreakerThreshold int           // consecutive failures before an endpoint opens (0 disables)
	BreakerCooldown  time.Duration // how long an opened endpoint stays skipped
	DrainTimeout     time.Duration // bounded drain window on Close
}

// DownstreamEventSinkStats exposes counters for observability and tests.
type DownstreamEventSinkStats struct {
	Enqueued  int64
	Delivered int64
	Dropped   int64
	Failed    int64
	Skipped   int64 // deliveries skipped because the endpoint circuit was open
}

type downstreamDelivery struct {
	delivery  string
	eventType PlanExecutionEventType
	eventID   string
	traceID   string
	body      []byte
}

type downstreamEnvelope struct {
	Delivery         string             `json:"delivery"`
	EmittedAt        int64              `json:"emittedAt"`
	Source           string             `json:"source"`
	SpecVersion      string             `json:"specVersion"`
	Event            PlanExecutionEvent `json:"event"`
	PayloadTruncated bool               `json:"payloadTruncated,omitempty"`
}

// endpointBreaker is a minimal per-endpoint circuit breaker. After `threshold`
// consecutive delivery failures to an endpoint it opens for `cooldown`,
// fast-skipping that endpoint so one dead consumer cannot block delivery to the
// others (the single delivery worker would otherwise spend its whole retry
// budget on the dead endpoint). After the cooldown a single probe is allowed;
// success closes the breaker, failure re-opens it.
//
// We deliberately do NOT reuse resilience.CircuitBreaker here: its
// ShouldTripCircuitBreaker policy treats context-deadline-exceeded as
// non-trippable (correct for LLM call budgets), but a webhook timeout is the
// primary signal that a consumer endpoint is dead — exactly what must trip in
// this path. All state is owned by the sink's single delivery goroutine, so no
// locking is required.
type endpointBreaker struct {
	threshold int
	cooldown  time.Duration
	failures  map[string]int
	openUntil map[string]time.Time
}

func newEndpointBreaker(threshold int, cooldown time.Duration) *endpointBreaker {
	if threshold <= 0 {
		return nil // disabled
	}
	if cooldown <= 0 {
		cooldown = defaultEventWebhookBreakerCooldown
	}
	return &endpointBreaker{
		threshold: threshold,
		cooldown:  cooldown,
		failures:  map[string]int{},
		openUntil: map[string]time.Time{},
	}
}

// allow reports whether a delivery to endpoint may proceed now. A nil breaker is
// disabled and always allows.
func (b *endpointBreaker) allow(endpoint string, now time.Time) bool {
	if b == nil {
		return true
	}
	if until, open := b.openUntil[endpoint]; open && now.Before(until) {
		return false
	}
	return true
}

// record updates breaker state after a delivery resolves.
func (b *endpointBreaker) record(endpoint string, success bool, now time.Time) {
	if b == nil {
		return
	}
	if success {
		delete(b.failures, endpoint)
		delete(b.openUntil, endpoint)
		return
	}
	b.failures[endpoint]++
	if b.failures[endpoint] >= b.threshold {
		b.openUntil[endpoint] = now.Add(b.cooldown)
	}
}

// DownstreamEventSink delivers PlanExecutionEvents to external webhooks
// asynchronously and best-effort. A nil *DownstreamEventSink is a valid no-op,
// so callers never need to nil-check before Dispatch.
type DownstreamEventSink struct {
	cfg           DownstreamEventSinkConfig
	client        *http.Client
	queue         chan downstreamDelivery
	stopAccepting chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	closeOnce     sync.Once
	drainTimeout  time.Duration

	breaker  *endpointBreaker // owned by the delivery goroutine
	draining bool             // owned by the delivery goroutine

	enqueued  atomic.Int64
	delivered atomic.Int64
	dropped   atomic.Int64
	failed    atomic.Int64
	skipped   atomic.Int64
}

// NewDownstreamEventSink builds the sink from environment configuration. It
// returns nil when no webhook URL is configured (the common case — the feature
// is opt-in), which keeps Dispatch a cheap no-op on the research hot path.
func NewDownstreamEventSink() *DownstreamEventSink {
	return NewDownstreamEventSinkWithConfig(resolveDownstreamEventSinkConfig())
}

// NewDownstreamEventSinkWithConfig builds the sink from an explicit config.
// Returns nil when no endpoints are configured. When enabled it starts a single
// background worker that performs delivery off the caller's goroutine.
func NewDownstreamEventSinkWithConfig(cfg DownstreamEventSinkConfig) *DownstreamEventSink {
	if len(cfg.URLs) == 0 {
		return nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultEventWebhookTimeout
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultEventWebhookQueueSize
	}
	if cfg.DrainTimeout <= 0 {
		cfg.DrainTimeout = defaultEventWebhookDrainTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &DownstreamEventSink{
		cfg:           cfg,
		client:        &http.Client{Timeout: cfg.Timeout},
		queue:         make(chan downstreamDelivery, cfg.QueueSize),
		stopAccepting: make(chan struct{}),
		ctx:           ctx,
		cancel:        cancel,
		drainTimeout:  cfg.DrainTimeout,
		breaker:       newEndpointBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown),
	}
	s.wg.Add(1)
	go s.run()
	slog.Info("wisdev downstream event sink enabled",
		"component", eventWebhookComponent,
		"operation", "init",
		"endpoints", len(cfg.URLs),
		"signed", cfg.Secret != "",
		"queueSize", cfg.QueueSize,
		"timeoutMs", cfg.Timeout.Milliseconds(),
		"maxRetries", cfg.MaxRetries,
		"eventFilter", len(cfg.EventFilter) > 0,
		"breakerThreshold", cfg.BreakerThreshold,
		"drainMs", cfg.DrainTimeout.Milliseconds(),
	)
	return s
}

func resolveDownstreamEventSinkConfig() DownstreamEventSinkConfig {
	cfg := DownstreamEventSinkConfig{
		URLs:             eventSinkEnvList(envEventWebhookURL),
		Secret:           strings.TrimSpace(os.Getenv(envEventWebhookSecret)),
		Timeout:          defaultEventWebhookTimeout,
		MaxRetries:       defaultEventWebhookMaxRetries,
		QueueSize:        defaultEventWebhookQueueSize,
		BreakerThreshold: defaultEventWebhookBreakerThreshold,
		BreakerCooldown:  defaultEventWebhookBreakerCooldown,
		DrainTimeout:     defaultEventWebhookDrainTimeout,
	}
	if filter := eventSinkEnvList(envEventWebhookEvents); len(filter) > 0 {
		cfg.EventFilter = make(map[PlanExecutionEventType]bool, len(filter))
		for _, e := range filter {
			cfg.EventFilter[PlanExecutionEventType(e)] = true
		}
	}
	if ms, ok := eventSinkEnvInt(envEventWebhookTimeoutMS, 100, 120000); ok {
		cfg.Timeout = time.Duration(ms) * time.Millisecond
	}
	if n, ok := eventSinkEnvInt(envEventWebhookMaxRetries, 0, 10); ok {
		cfg.MaxRetries = n
	}
	if n, ok := eventSinkEnvInt(envEventWebhookQueueSize, 1, 8192); ok {
		cfg.QueueSize = n
	}
	if n, ok := eventSinkEnvInt(envEventWebhookBreakerThreshold, 0, 1000); ok {
		cfg.BreakerThreshold = n
	}
	if ms, ok := eventSinkEnvInt(envEventWebhookBreakerCooldown, 100, 600000); ok {
		cfg.BreakerCooldown = time.Duration(ms) * time.Millisecond
	}
	if ms, ok := eventSinkEnvInt(envEventWebhookDrainMS, 0, 120000); ok {
		cfg.DrainTimeout = time.Duration(ms) * time.Millisecond
	}
	return cfg
}

// Dispatch enqueues an event for asynchronous downstream delivery. It never
// blocks: when the buffer is full the event is dropped and logged so the
// research loop is never throttled by a slow or unreachable consumer.
func (s *DownstreamEventSink) Dispatch(event PlanExecutionEvent) {
	if s == nil {
		return
	}
	// Stop accepting once Close has begun so post-shutdown events are not lost
	// into a queue nobody is draining.
	select {
	case <-s.stopAccepting:
		return
	default:
	}
	if s.cfg.EventFilter != nil && !s.cfg.EventFilter[event.Type] {
		return
	}
	deliveryID := NewTraceID()
	body, truncated, err := buildDownstreamEnvelope(deliveryID, event)
	if err != nil {
		slog.Warn("downstream event sink: failed to encode envelope",
			"component", eventWebhookComponent,
			"operation", "dispatch",
			"eventType", string(event.Type),
			"error", err.Error(),
			"result", "encode_error",
		)
		return
	}
	delivery := downstreamDelivery{
		delivery:  deliveryID,
		eventType: event.Type,
		eventID:   firstNonEmpty(event.EventID, event.TraceID, deliveryID),
		traceID:   firstNonEmpty(event.TraceID, event.EventID),
		body:      body,
	}
	select {
	case s.queue <- delivery:
		s.enqueued.Add(1)
		slog.Debug("downstream event enqueued",
			"component", eventWebhookComponent,
			"operation", "dispatch",
			"eventType", string(event.Type),
			"eventId", delivery.eventID,
			"delivery", deliveryID,
			"payloadTruncated", truncated,
			"result", "enqueued",
		)
	default:
		s.dropped.Add(1)
		telemetry.RecordDownstreamWebhookDelivery("dropped", string(event.Type))
		slog.Warn("downstream event sink: queue full, dropping event",
			"component", eventWebhookComponent,
			"operation", "dispatch",
			"eventType", string(event.Type),
			"eventId", delivery.eventID,
			"queueSize", s.cfg.QueueSize,
			"result", "dropped",
		)
	}
}

// Close stops accepting new events, drains the queue within the bounded drain
// window (delivering what it can), then forces any still-in-flight delivery to
// abort. Safe to call more than once and safe on a nil sink.
func (s *DownstreamEventSink) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() { close(s.stopAccepting) })
	if !waitWaitGroup(&s.wg, s.drainTimeout+2*time.Second) {
		// Drain overran its window — force-abort in-flight delivery and sleeps.
		s.cancel()
	}
	s.wg.Wait()
	s.cancel() // release the context (idempotent)
	slog.Info("downstream event sink closed",
		"component", eventWebhookComponent,
		"operation", "close",
		"enqueued", s.enqueued.Load(),
		"delivered", s.delivered.Load(),
		"failed", s.failed.Load(),
		"dropped", s.dropped.Load(),
		"skipped", s.skipped.Load(),
	)
}

// Stats returns a snapshot of delivery counters.
func (s *DownstreamEventSink) Stats() DownstreamEventSinkStats {
	if s == nil {
		return DownstreamEventSinkStats{}
	}
	return DownstreamEventSinkStats{
		Enqueued:  s.enqueued.Load(),
		Delivered: s.delivered.Load(),
		Dropped:   s.dropped.Load(),
		Failed:    s.failed.Load(),
		Skipped:   s.skipped.Load(),
	}
}

// MetadataMap returns an operator-facing snapshot of sink configuration and
// delivery counters for runtime metadata. It never exposes endpoint URLs or the
// signing secret. Returns nil on a nil sink so callers can omit the field.
func (s *DownstreamEventSink) MetadataMap() map[string]any {
	if s == nil {
		return nil
	}
	st := s.Stats()
	return map[string]any{
		"enabled":   true,
		"endpoints": len(s.cfg.URLs),
		"signed":    s.cfg.Secret != "",
		"enqueued":  st.Enqueued,
		"delivered": st.Delivered,
		"failed":    st.Failed,
		"dropped":   st.Dropped,
		"skipped":   st.Skipped,
	}
}

func (s *DownstreamEventSink) run() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopAccepting:
			s.drain()
			return
		case d := <-s.queue:
			s.deliver(d)
		}
	}
}

// drain delivers whatever is already queued, bounded by drainTimeout. Past the
// deadline (or once the context is force-cancelled) remaining items are counted
// as dropped so shutdown stays bounded.
func (s *DownstreamEventSink) drain() {
	s.draining = true
	deadline := time.Now().Add(s.drainTimeout)
	for {
		select {
		case d := <-s.queue:
			if s.ctx.Err() != nil || time.Now().After(deadline) {
				s.dropped.Add(1)
				telemetry.RecordDownstreamWebhookDelivery("dropped", string(d.eventType))
				continue
			}
			s.deliver(d)
		default:
			return
		}
	}
}

func (s *DownstreamEventSink) deliver(d downstreamDelivery) {
	for _, endpoint := range s.cfg.URLs {
		s.deliverTo(endpoint, d)
	}
}

func (s *DownstreamEventSink) deliverTo(endpoint string, d downstreamDelivery) {
	if !s.breaker.allow(endpoint, time.Now()) {
		s.skipped.Add(1)
		telemetry.RecordDownstreamWebhookDelivery("skipped", string(d.eventType))
		slog.Warn("downstream event skipped: endpoint circuit open",
			"component", eventWebhookComponent,
			"operation", "deliver",
			"eventType", string(d.eventType),
			"eventId", d.eventID,
			"endpoint", redactURL(endpoint),
			"result", "skipped",
		)
		return
	}

	attempts := s.cfg.MaxRetries + 1
	if s.draining {
		attempts = 1 // shutdown drain gets a single best-effort attempt
	}
	var lastErr error
	var lastStatus int
	for attempt := 1; attempt <= attempts; attempt++ {
		if s.ctx.Err() != nil {
			return // forced shutdown — abort without counting an endpoint failure
		}

		start := time.Now()
		status, retryAfter, err := s.postOnce(endpoint, d)
		latency := time.Since(start)

		if err == nil && status >= 200 && status < 300 {
			s.delivered.Add(1)
			s.breaker.record(endpoint, true, time.Now())
			telemetry.RecordDownstreamWebhookDelivery("delivered", string(d.eventType))
			slog.Info("downstream event delivered",
				"component", eventWebhookComponent,
				"operation", "deliver",
				"eventType", string(d.eventType),
				"eventId", d.eventID,
				"delivery", d.delivery,
				"endpoint", redactURL(endpoint),
				"status", status,
				"attempt", attempt,
				"latencyMs", latency.Milliseconds(),
				"result", "ok",
			)
			return
		}

		lastErr = err
		lastStatus = status

		// A non-retryable client rejection (4xx other than 408/429) will not
		// succeed on retry — fail fast rather than burn the budget.
		if err == nil && status >= 400 && status < 500 &&
			status != http.StatusRequestTimeout && status != http.StatusTooManyRequests {
			s.failed.Add(1)
			s.breaker.record(endpoint, false, time.Now())
			telemetry.RecordDownstreamWebhookDelivery("rejected", string(d.eventType))
			slog.Warn("downstream event rejected by consumer",
				"component", eventWebhookComponent,
				"operation", "deliver",
				"eventType", string(d.eventType),
				"eventId", d.eventID,
				"endpoint", redactURL(endpoint),
				"status", status,
				"attempt", attempt,
				"latencyMs", latency.Milliseconds(),
				"result", "rejected",
			)
			return
		}

		if attempt < attempts {
			backoff := eventSinkBackoff(attempt)
			retryAfterHonored := false
			if retryAfter > 0 {
				// Respect the consumer's Retry-After (already clamped) instead of
				// our exponential backoff when it asked us to wait.
				backoff = retryAfter
				retryAfterHonored = true
			}
			slog.Warn("downstream event delivery failed, retrying",
				"component", eventWebhookComponent,
				"operation", "deliver",
				"eventType", string(d.eventType),
				"eventId", d.eventID,
				"endpoint", redactURL(endpoint),
				"status", status,
				"attempt", attempt,
				"error", errString(err),
				"backoffMs", backoff.Milliseconds(),
				"retryAfterHonored", retryAfterHonored,
				"result", "retry",
			)
			if !s.sleep(backoff) {
				return // context cancelled during backoff — treat as shutdown abort
			}
		}
	}

	if s.ctx.Err() != nil {
		return // aborted by shutdown, not a genuine endpoint failure
	}
	s.failed.Add(1)
	s.breaker.record(endpoint, false, time.Now())
	telemetry.RecordDownstreamWebhookDelivery("failed", string(d.eventType))
	slog.Error("downstream event delivery exhausted retries",
		"component", eventWebhookComponent,
		"operation", "deliver",
		"eventType", string(d.eventType),
		"eventId", d.eventID,
		"endpoint", redactURL(endpoint),
		"status", lastStatus,
		"attempts", attempts,
		"error", errString(lastErr),
		"result", "failed",
	)
}

func (s *DownstreamEventSink) postOnce(endpoint string, d downstreamDelivery) (int, time.Duration, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(d.body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", eventWebhookUserAgent)
	req.Header.Set("X-WisDev-Event", string(d.eventType))
	req.Header.Set("X-WisDev-Event-Id", d.eventID)
	req.Header.Set("X-WisDev-Delivery", d.delivery)
	if d.traceID != "" {
		req.Header.Set("X-WisDev-Trace-Id", d.traceID)
	}
	if s.cfg.Secret != "" {
		req.Header.Set("X-WisDev-Signature", "sha256="+signPayload(s.cfg.Secret, d.body))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection can be reused by keep-alive.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

// parseRetryAfter parses a Retry-After header (delta-seconds or HTTP-date) into
// a positive duration, clamped to maxRetryAfter so a hostile or broken consumer
// cannot stall the single delivery worker. Returns 0 when absent or unparseable.
func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.Atoi(header); err == nil {
		d = time.Duration(secs) * time.Second
	} else if when, err := http.ParseTime(header); err == nil {
		d = time.Until(when)
	} else {
		return 0
	}
	if d <= 0 {
		return 0
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// sleep waits for d, returning false if the sink context is cancelled first.
func (s *DownstreamEventSink) sleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func buildDownstreamEnvelope(deliveryID string, event PlanExecutionEvent) ([]byte, bool, error) {
	env := downstreamEnvelope{
		Delivery:    deliveryID,
		EmittedAt:   NowMillis(),
		Source:      eventWebhookSource,
		SpecVersion: eventWebhookSpecVersion,
		Event:       event,
	}
	body, err := json.Marshal(env)
	if err != nil {
		return nil, false, err
	}
	if len(body) <= maxEventWebhookBodyBytes {
		return body, false, nil
	}
	// Defense-in-depth: an oversized payload (large titles/snippets) is trimmed
	// to lifecycle metadata. Consumers still get the signal and can pull the
	// full result through the API.
	env.Event.Payload = nil
	env.PayloadTruncated = true
	body, err = json.Marshal(env)
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}

func signPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhookSignature reports whether sigHeader authenticates body under
// secret. sigHeader is the raw X-WisDev-Signature value ("sha256=<hex>").
// Comparison is constant-time. Webhook consumers (and the OSS agent's own
// inbound tests) use this to confirm a delivery genuinely came from WisDev.
func VerifyWebhookSignature(secret string, body []byte, sigHeader string) bool {
	if secret == "" || sigHeader == "" {
		return false
	}
	expected := "sha256=" + signPayload(secret, body)
	return hmac.Equal([]byte(expected), []byte(sigHeader))
}

// eventSinkBackoff returns capped exponential backoff: 200ms, 400ms, 800ms ...
// up to a 5s ceiling.
func eventSinkBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := (200 * time.Millisecond) << (attempt - 1)
	if d > 5*time.Second || d <= 0 {
		return 5 * time.Second
	}
	return d
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "invalid-url"
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.Path
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// waitWaitGroup waits up to d for wg, returning true if it finished in time.
func waitWaitGroup(wg *sync.WaitGroup, d time.Duration) bool {
	if wg == nil {
		return true // nothing to wait on (e.g. reflective zero-value coverage call)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func eventSinkEnvList(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func eventSinkEnvInt(name string, min, max int) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, false
	}
	return n, true
}
