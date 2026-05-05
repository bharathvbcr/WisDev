package wisdev

import "testing"

func TestEmitEventIncludesCanonicalAndLegacyTraceKeys(t *testing.T) {
	var got map[string]any
	emitEvent(func(event map[string]any) {
		got = event
	}, StreamEvent{
		Type:      EventThoughtGenerated,
		TraceID:   "trace-stream-1",
		Iteration: 2,
		NodeID:    7,
	})

	if got == nil {
		t.Fatal("expected emitted event payload")
	}
	if got["traceId"] != "trace-stream-1" {
		t.Fatalf("expected traceId trace-stream-1, got %#v", got["traceId"])
	}
	if got["trace_id"] != "trace-stream-1" {
		t.Fatalf("expected trace_id trace-stream-1, got %#v", got["trace_id"])
	}
	if got["type"] != string(EventThoughtGenerated) {
		t.Fatalf("expected type %q, got %#v", EventThoughtGenerated, got["type"])
	}
}

func TestEmitEventOmitsTraceKeysWhenEmpty(t *testing.T) {
	var got map[string]any
	emitEvent(func(event map[string]any) {
		got = event
	}, StreamEvent{
		Type:      EventIterationComplete,
		Iteration: 1,
	})

	if got == nil {
		t.Fatal("expected emitted event payload")
	}
	if _, ok := got["traceId"]; ok {
		t.Fatalf("did not expect traceId when trace is empty: %#v", got["traceId"])
	}
	if _, ok := got["trace_id"]; ok {
		t.Fatalf("did not expect trace_id when trace is empty: %#v", got["trace_id"])
	}
}
