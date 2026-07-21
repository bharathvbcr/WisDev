package wisdev

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type capturedRequest struct {
	headers http.Header
	body    []byte
}

func newCaptureServer(t *testing.T, status int) (*httptest.Server, <-chan capturedRequest) {
	t.Helper()
	received := make(chan capturedRequest, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- capturedRequest{headers: r.Header.Clone(), body: body}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, received
}

func waitForStat(t *testing.T, get func() int64, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if get() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for stat to reach %d, last=%d", want, get())
}

func sampleEvent() PlanExecutionEvent {
	return PlanExecutionEvent{
		Type:      EventCompleted,
		EventID:   "evt-1",
		TraceID:   "trace-1",
		SessionID: "sess-1",
		Message:   "research complete",
		Payload:   map[string]any{"papers": 12},
	}
}

func TestDownstreamEventSinkDisabledWithoutURL(t *testing.T) {
	if sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{}); sink != nil {
		t.Fatalf("expected nil sink when no URLs configured, got %#v", sink)
	}
	// A nil sink must be a safe no-op so callers never have to guard it.
	var nilSink *DownstreamEventSink
	nilSink.Dispatch(sampleEvent())
	nilSink.Close()
	if stats := nilSink.Stats(); stats != (DownstreamEventSinkStats{}) {
		t.Fatalf("expected zero stats from nil sink, got %#v", stats)
	}
}

func TestDownstreamEventSinkDeliversSignedEnvelope(t *testing.T) {
	srv, received := newCaptureServer(t, http.StatusOK)
	const secret = "shhh-secret"
	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:    []string{srv.URL},
		Secret:  secret,
		Timeout: 2 * time.Second,
		QueueSize: 8,
	})
	if sink == nil {
		t.Fatal("expected enabled sink")
	}
	defer sink.Close()

	sink.Dispatch(sampleEvent())

	var got capturedRequest
	select {
	case got = <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("no webhook delivery received")
	}

	if ev := got.headers.Get("X-WisDev-Event"); ev != string(EventCompleted) {
		t.Errorf("X-WisDev-Event = %q, want %q", ev, EventCompleted)
	}
	if id := got.headers.Get("X-WisDev-Event-Id"); id != "evt-1" {
		t.Errorf("X-WisDev-Event-Id = %q, want evt-1", id)
	}
	if got.headers.Get("X-WisDev-Delivery") == "" {
		t.Error("missing X-WisDev-Delivery header")
	}
	if tid := got.headers.Get("X-WisDev-Trace-Id"); tid != "trace-1" {
		t.Errorf("X-WisDev-Trace-Id = %q, want trace-1", tid)
	}
	if ct := got.headers.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	// HMAC signature must be computed over the exact raw body.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(got.body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig := got.headers.Get("X-WisDev-Signature"); sig != want {
		t.Errorf("signature = %q, want %q", sig, want)
	}

	var env downstreamEnvelope
	if err := json.Unmarshal(got.body, &env); err != nil {
		t.Fatalf("envelope did not decode: %v", err)
	}
	if env.SpecVersion != eventWebhookSpecVersion {
		t.Errorf("specVersion = %q", env.SpecVersion)
	}
	if env.Source != eventWebhookSource {
		t.Errorf("source = %q", env.Source)
	}
	if env.Delivery == "" {
		t.Error("envelope delivery id empty")
	}
	if env.Event.Type != EventCompleted || env.Event.Message != "research complete" {
		t.Errorf("event not preserved: %+v", env.Event)
	}

	waitForStat(t, func() int64 { return sink.Stats().Delivered }, 1, time.Second)
}

func TestDownstreamEventSinkUnsignedWhenNoSecret(t *testing.T) {
	srv, received := newCaptureServer(t, http.StatusOK)
	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{URLs: []string{srv.URL}})
	defer sink.Close()

	sink.Dispatch(sampleEvent())
	select {
	case got := <-received:
		if sig := got.headers.Get("X-WisDev-Signature"); sig != "" {
			t.Errorf("expected no signature without secret, got %q", sig)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery received")
	}
}

func TestDownstreamEventSinkEventFilter(t *testing.T) {
	srv, received := newCaptureServer(t, http.StatusOK)
	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:        []string{srv.URL},
		EventFilter: map[PlanExecutionEventType]bool{EventCompleted: true},
	})
	defer sink.Close()

	// Filtered out — should never reach the endpoint.
	sink.Dispatch(PlanExecutionEvent{Type: EventProgress, EventID: "p1"})
	// Allowed.
	sink.Dispatch(PlanExecutionEvent{Type: EventCompleted, EventID: "c1"})

	select {
	case got := <-received:
		if id := got.headers.Get("X-WisDev-Event-Id"); id != "c1" {
			t.Errorf("delivered wrong event %q, expected only c1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("allowed event not delivered")
	}

	// Nothing else should arrive.
	select {
	case got := <-received:
		t.Errorf("unexpected second delivery: %s", got.headers.Get("X-WisDev-Event-Id"))
	case <-time.After(150 * time.Millisecond):
	}
	if dropped := sink.Stats().Dropped; dropped != 0 {
		t.Errorf("filtered events should not count as dropped, got %d", dropped)
	}
}

func TestDownstreamEventSinkRetriesThenSucceeds(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError) // transient
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:       []string{srv.URL},
		MaxRetries: 2,
		Timeout:    time.Second,
	})
	defer sink.Close()

	sink.Dispatch(sampleEvent())
	waitForStat(t, func() int64 { return sink.Stats().Delivered }, 1, 3*time.Second)
	if n := calls.Load(); n != 2 {
		t.Errorf("expected 2 attempts (1 fail + 1 success), got %d", n)
	}
}

func TestDownstreamEventSinkNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:       []string{srv.URL},
		MaxRetries: 3,
		Timeout:    time.Second,
	})
	defer sink.Close()

	sink.Dispatch(sampleEvent())
	waitForStat(t, func() int64 { return sink.Stats().Failed }, 1, 2*time.Second)
	// 400 is a permanent rejection — must not be retried.
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 attempt on 4xx, got %d", n)
	}
}

func TestDownstreamEventSinkExhaustsRetries(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:       []string{srv.URL},
		MaxRetries: 1, // => 2 attempts total
		Timeout:    time.Second,
	})
	defer sink.Close()

	sink.Dispatch(sampleEvent())
	waitForStat(t, func() int64 { return sink.Stats().Failed }, 1, 3*time.Second)
	if n := calls.Load(); n != 2 {
		t.Errorf("expected 2 attempts before giving up, got %d", n)
	}
}

func TestDownstreamEventSinkTimeoutCountsAsFailure(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // outlive the client timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)

	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:       []string{srv.URL},
		MaxRetries: 0,
		Timeout:    100 * time.Millisecond,
	})
	defer sink.Close()

	sink.Dispatch(sampleEvent())
	waitForStat(t, func() int64 { return sink.Stats().Failed }, 1, 2*time.Second)
}

func TestDownstreamEventSinkDropsWhenQueueFull(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release // keep the single worker busy
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:      []string{srv.URL},
		QueueSize: 1,
		Timeout:   2 * time.Second,
	})
	defer sink.Close()

	sink.Dispatch(sampleEvent())     // taken by worker, blocks in handler
	<-started                        // worker is now in-flight; queue is empty
	sink.Dispatch(sampleEvent())     // fills the 1-deep queue
	sink.Dispatch(sampleEvent())     // queue full -> dropped
	sink.Dispatch(sampleEvent())     // dropped

	waitForStat(t, func() int64 { return sink.Stats().Dropped }, 1, time.Second)
	close(release)
}

func TestBuildDownstreamEnvelopeTruncatesOversizePayload(t *testing.T) {
	huge := strings.Repeat("x", maxEventWebhookBodyBytes+1024)
	event := PlanExecutionEvent{
		Type:    EventPaperFound,
		EventID: "big",
		Payload: map[string]any{"blob": huge},
	}
	body, truncated, err := buildDownstreamEnvelope("d1", event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !truncated {
		t.Fatal("expected oversize payload to be truncated")
	}
	var env downstreamEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.PayloadTruncated {
		t.Error("envelope should flag payloadTruncated")
	}
	if env.Event.Payload != nil {
		t.Error("payload should be dropped when truncated")
	}
	if env.Event.Type != EventPaperFound {
		t.Error("lifecycle metadata must survive truncation")
	}
}

func TestBuildDownstreamEnvelopeKeepsSmallPayload(t *testing.T) {
	body, truncated, err := buildDownstreamEnvelope("d2", sampleEvent())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Fatal("small payload must not be truncated")
	}
	var env downstreamEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := env.Event.Payload["papers"].(float64); got != 12 {
		t.Errorf("payload not preserved: %+v", env.Event.Payload)
	}
}

func TestDownstreamEventSinkConfigClampsFromEnv(t *testing.T) {
	t.Setenv(envEventWebhookURL, " https://a.example/hook , https://b.example/hook ")
	t.Setenv(envEventWebhookSecret, "  topsecret  ")
	t.Setenv(envEventWebhookEvents, "completed, step_failed")
	t.Setenv(envEventWebhookTimeoutMS, "1500")
	t.Setenv(envEventWebhookMaxRetries, "5")
	t.Setenv(envEventWebhookQueueSize, "64")

	cfg := resolveDownstreamEventSinkConfig()
	if len(cfg.URLs) != 2 || cfg.URLs[0] != "https://a.example/hook" || cfg.URLs[1] != "https://b.example/hook" {
		t.Errorf("URLs not parsed/trimmed: %#v", cfg.URLs)
	}
	if cfg.Secret != "topsecret" {
		t.Errorf("secret not trimmed: %q", cfg.Secret)
	}
	if !cfg.EventFilter[EventCompleted] || !cfg.EventFilter[EventStepFailed] || cfg.EventFilter[EventProgress] {
		t.Errorf("event filter wrong: %#v", cfg.EventFilter)
	}
	if cfg.Timeout != 1500*time.Millisecond {
		t.Errorf("timeout = %v", cfg.Timeout)
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("maxRetries = %d", cfg.MaxRetries)
	}
	if cfg.QueueSize != 64 {
		t.Errorf("queueSize = %d", cfg.QueueSize)
	}
}

func TestDownstreamEventSinkConfigRejectsOutOfRange(t *testing.T) {
	t.Setenv(envEventWebhookTimeoutMS, "5") // below floor -> ignored
	t.Setenv(envEventWebhookMaxRetries, "99") // above ceiling -> ignored
	cfg := resolveDownstreamEventSinkConfig()
	if cfg.Timeout != defaultEventWebhookTimeout {
		t.Errorf("out-of-range timeout should fall back to default, got %v", cfg.Timeout)
	}
	if cfg.MaxRetries != defaultEventWebhookMaxRetries {
		t.Errorf("out-of-range retries should fall back to default, got %d", cfg.MaxRetries)
	}
}

func TestEventSinkBackoffIsCapped(t *testing.T) {
	if got := eventSinkBackoff(1); got != 200*time.Millisecond {
		t.Errorf("attempt 1 backoff = %v", got)
	}
	if got := eventSinkBackoff(2); got != 400*time.Millisecond {
		t.Errorf("attempt 2 backoff = %v", got)
	}
	if got := eventSinkBackoff(100); got != 5*time.Second {
		t.Errorf("large attempt should cap at 5s, got %v", got)
	}
}

func TestDownstreamEventSinkCloseIsIdempotent(t *testing.T) {
	srv, _ := newCaptureServer(t, http.StatusOK)
	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{URLs: []string{srv.URL}})
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sink.Close()
		}()
	}
	wg.Wait()
}

func TestRedactURLStripsQueryAndCredentials(t *testing.T) {
	got := redactURL("https://user:pass@hooks.example.com/path?token=abc123")
	if strings.Contains(got, "token") || strings.Contains(got, "pass") {
		t.Errorf("redactURL leaked sensitive parts: %q", got)
	}
	if got != "https://hooks.example.com/path" {
		t.Errorf("redactURL = %q", got)
	}
}

// TestAppendExecutionEventDispatchesToSink proves the producer-funnel wiring:
// every event journaled by appendExecutionEvent is also teed to the downstream
// sink, with the session id and funnel-enriched payload carried through.
func TestAppendExecutionEventDispatchesToSink(t *testing.T) {
	srv, received := newCaptureServer(t, http.StatusOK)
	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:    []string{srv.URL},
		Timeout: 2 * time.Second,
	})
	defer sink.Close()

	// appendExecutionEvent only reads Journal + EventSink, so a minimal gateway
	// is enough to exercise the tee without standing up the full runtime.
	gateway := &AgentGateway{
		Journal:   newIsolatedRuntimeJournal(t),
		EventSink: sink,
	}
	session := &AgentSession{
		SessionID: "sess-wire",
		UserID:    "u1",
		Status:    SessionComplete,
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}

	appendExecutionEvent(gateway, session, PlanExecutionEvent{
		Type:      EventCompleted,
		TraceID:   "trace-wire",
		Message:   "done",
		CreatedAt: NowMillis(),
	})

	select {
	case got := <-received:
		if ev := got.headers.Get("X-WisDev-Event"); ev != string(EventCompleted) {
			t.Errorf("X-WisDev-Event = %q", ev)
		}
		var env downstreamEnvelope
		if err := json.Unmarshal(got.body, &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if env.Event.Type != EventCompleted {
			t.Errorf("event type = %q", env.Event.Type)
		}
		if env.Event.SessionID != "sess-wire" {
			t.Errorf("sessionId not propagated by funnel: %q", env.Event.SessionID)
		}
		if env.Event.Payload["eventId"] == nil {
			t.Error("expected funnel-enriched eventId in payload")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("appendExecutionEvent did not dispatch to the downstream sink")
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	const secret = "topsecret"
	sig := "sha256=" + signPayload(secret, body)

	if !VerifyWebhookSignature(secret, body, sig) {
		t.Error("valid signature rejected")
	}
	if VerifyWebhookSignature("wrong", body, sig) {
		t.Error("wrong secret accepted")
	}
	if VerifyWebhookSignature(secret, []byte(`{"hello":"tampered"}`), sig) {
		t.Error("tampered body accepted")
	}
	if VerifyWebhookSignature("", body, sig) {
		t.Error("empty secret accepted")
	}
	if VerifyWebhookSignature(secret, body, "") {
		t.Error("empty signature accepted")
	}
}

func TestEndpointBreakerOpensAndRecovers(t *testing.T) {
	b := newEndpointBreaker(2, 30*time.Second)
	base := time.Unix(1_000_000, 0)
	ep := "https://x.example/hook"

	if !b.allow(ep, base) {
		t.Fatal("breaker should start closed")
	}
	b.record(ep, false, base)
	if !b.allow(ep, base) {
		t.Fatal("one failure should not open (threshold 2)")
	}
	b.record(ep, false, base)
	if b.allow(ep, base) {
		t.Fatal("breaker should open at threshold")
	}
	if b.allow(ep, base.Add(29*time.Second)) {
		t.Fatal("breaker should stay open during cooldown")
	}
	after := base.Add(31 * time.Second)
	if !b.allow(ep, after) {
		t.Fatal("breaker should allow a probe after cooldown")
	}
	b.record(ep, false, after) // failed probe re-opens
	if b.allow(ep, after) {
		t.Fatal("failed probe should re-open the breaker")
	}
	later := after.Add(31 * time.Second)
	if !b.allow(ep, later) {
		t.Fatal("breaker should allow a probe after the second cooldown")
	}
	b.record(ep, true, later) // success closes and clears
	if !b.allow(ep, later.Add(time.Hour)) {
		t.Fatal("breaker should be closed after a successful probe")
	}
}

func TestEndpointBreakerDisabled(t *testing.T) {
	if b := newEndpointBreaker(0, time.Second); b != nil {
		t.Fatal("threshold 0 should disable the breaker (nil)")
	}
	var nilBreaker *endpointBreaker
	if !nilBreaker.allow("x", time.Unix(0, 0)) {
		t.Error("nil breaker must allow")
	}
	nilBreaker.record("x", false, time.Unix(0, 0)) // must not panic
}

func TestDownstreamEventSinkSkipsOpenEndpoint(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusInternalServerError) // permanently dead
	}))
	defer srv.Close()

	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:             []string{srv.URL},
		MaxRetries:       0,
		Timeout:          time.Second,
		BreakerThreshold: 2,
		BreakerCooldown:  time.Hour, // stays open for the duration of the test
	})
	defer sink.Close()

	for i := 0; i < 6; i++ {
		sink.Dispatch(sampleEvent())
	}
	// After 2 failures the circuit opens and the rest are skipped.
	waitForStat(t, func() int64 { return sink.Stats().Skipped }, 1, 3*time.Second)
	waitForStat(t, func() int64 { return sink.Stats().Failed }, 2, 3*time.Second)
	if n := calls.Load(); n > 3 {
		t.Errorf("open circuit should stop hammering the dead endpoint, got %d calls", n)
	}
}

func TestDownstreamEventSinkDrainsQueueOnClose(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release // hold the worker on the first delivery
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:         []string{srv.URL},
		MaxRetries:   0,
		Timeout:      2 * time.Second,
		QueueSize:    8,
		DrainTimeout: 2 * time.Second,
	})

	sink.Dispatch(sampleEvent()) // taken by worker, blocks in handler
	<-started
	sink.Dispatch(sampleEvent()) // queued
	sink.Dispatch(sampleEvent()) // queued
	close(release)               // let the first delivery complete

	sink.Close() // must drain the two queued events rather than drop them

	if got := sink.Stats(); got.Delivered != 3 || got.Dropped != 0 {
		t.Errorf("graceful close should deliver all 3 with 0 dropped, got %+v", got)
	}
}

func TestDownstreamEventSinkConfigBreakerAndDrainFromEnv(t *testing.T) {
	t.Setenv(envEventWebhookBreakerThreshold, "9")
	t.Setenv(envEventWebhookBreakerCooldown, "12000")
	t.Setenv(envEventWebhookDrainMS, "1500")
	cfg := resolveDownstreamEventSinkConfig()
	if cfg.BreakerThreshold != 9 {
		t.Errorf("breakerThreshold = %d", cfg.BreakerThreshold)
	}
	if cfg.BreakerCooldown != 12*time.Second {
		t.Errorf("breakerCooldown = %v", cfg.BreakerCooldown)
	}
	if cfg.DrainTimeout != 1500*time.Millisecond {
		t.Errorf("drainTimeout = %v", cfg.DrainTimeout)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("empty = %v, want 0", d)
	}
	if d := parseRetryAfter("garbage"); d != 0 {
		t.Errorf("garbage = %v, want 0", d)
	}
	if d := parseRetryAfter("0"); d != 0 {
		t.Errorf("0 = %v, want 0", d)
	}
	if d := parseRetryAfter("2"); d != 2*time.Second {
		t.Errorf("2 = %v, want 2s", d)
	}
	if d := parseRetryAfter("3600"); d != maxRetryAfter {
		t.Errorf("3600 = %v, want %v (clamped)", d, maxRetryAfter)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(past); d != 0 {
		t.Errorf("past date = %v, want 0", d)
	}
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(future); d <= 0 || d > maxRetryAfter {
		t.Errorf("future date = %v, want within (0, %v]", d, maxRetryAfter)
	}
}

func TestDownstreamEventSinkHonorsRetryAfter(t *testing.T) {
	var calls, firstNano, secondNano atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			firstNano.Store(time.Now().UnixNano())
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		secondNano.Store(time.Now().UnixNano())
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{
		URLs:       []string{srv.URL},
		MaxRetries: 2,
		Timeout:    2 * time.Second,
	})
	defer sink.Close()

	sink.Dispatch(sampleEvent())
	waitForStat(t, func() int64 { return sink.Stats().Delivered }, 1, 4*time.Second)
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 attempts, got %d", n)
	}
	// The retry waited ~1s (Retry-After) rather than the 200ms default backoff.
	if gap := time.Duration(secondNano.Load() - firstNano.Load()); gap < 700*time.Millisecond {
		t.Errorf("retry gap %v too short — Retry-After not honored", gap)
	}
}

func TestDownstreamEventSinkMetadataMap(t *testing.T) {
	var nilSink *DownstreamEventSink
	if nilSink.MetadataMap() != nil {
		t.Error("nil sink MetadataMap should be nil")
	}

	srv, _ := newCaptureServer(t, http.StatusOK)
	sink := NewDownstreamEventSinkWithConfig(DownstreamEventSinkConfig{URLs: []string{srv.URL}})
	defer sink.Close()

	sink.Dispatch(sampleEvent())
	waitForStat(t, func() int64 { return sink.Stats().Delivered }, 1, 2*time.Second)

	m := sink.MetadataMap()
	if enabled, _ := m["enabled"].(bool); !enabled {
		t.Errorf("enabled = %v", m["enabled"])
	}
	if n, _ := m["endpoints"].(int); n != 1 {
		t.Errorf("endpoints = %v", m["endpoints"])
	}
	if signed, _ := m["signed"].(bool); signed {
		t.Errorf("signed = %v, want false", m["signed"])
	}
	if d, _ := m["delivered"].(int64); d < 1 {
		t.Errorf("delivered = %v", m["delivered"])
	}
	for k := range m {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "url") || strings.Contains(lk, "secret") {
			t.Errorf("metadata leaks sensitive key %q", k)
		}
	}
}
