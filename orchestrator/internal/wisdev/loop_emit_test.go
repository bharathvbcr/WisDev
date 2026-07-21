package wisdev

import (
	"context"
	"testing"
)

func TestEmitLoopStageUsesContextEmitter(t *testing.T) {
	var seen []PlanExecutionEvent
	ctx := WithLoopProgress(context.Background(), &LoopProgressEmitter{
		Emit: func(event PlanExecutionEvent) { seen = append(seen, event) },
		Req:  LoopRequest{ProjectID: "sess-1", TraceID: "trace-1"},
	})

	EmitLoopStage(ctx, "loop_iteration", "iteration 1", map[string]any{"iteration": 1})
	if len(seen) != 1 {
		t.Fatalf("expected 1 event, got %d", len(seen))
	}
	if seen[0].Payload["stage"] != "loop_iteration" {
		t.Fatalf("stage = %v", seen[0].Payload["stage"])
	}
	if seen[0].SessionID != "sess-1" {
		t.Fatalf("session = %q", seen[0].SessionID)
	}
}

func TestEmitLoopDegradedMarksPayload(t *testing.T) {
	var seen []PlanExecutionEvent
	ctx := WithLoopProgress(context.Background(), &LoopProgressEmitter{
		Emit: func(event PlanExecutionEvent) { seen = append(seen, event) },
		Req:  LoopRequest{},
	})

	EmitLoopDegraded(ctx, "evaluate_sufficiency", "using heuristic fallback", nil)
	if len(seen) != 1 {
		t.Fatalf("expected 1 event, got %d", len(seen))
	}
	if seen[0].Payload["degraded"] != true {
		t.Fatalf("degraded = %v", seen[0].Payload["degraded"])
	}
	if seen[0].Payload["fallback"] != "heuristic" {
		t.Fatalf("fallback = %v", seen[0].Payload["fallback"])
	}
}

func TestEmitLoopStageNoopsWithoutEmitter(t *testing.T) {
	EmitLoopStage(context.Background(), "loop_started", "noop", nil)
	EmitLoopDegraded(context.Background(), "noop", "noop", nil)
}
