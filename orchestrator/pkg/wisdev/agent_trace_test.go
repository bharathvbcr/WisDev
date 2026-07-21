package wisdev

import (
	"testing"

	internal "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

func TestFromInternalReasoningTraceMapsFields(t *testing.T) {
	steps := fromInternalReasoningTrace([]internal.ReasoningTraceEntry{
		{
			Timestamp:    1717000000000,
			Phase:        " planning ",
			Decision:     " cot_plan_summary ",
			Reasoning:    " Decompose the question into retrieval branches. ",
			Alternatives: []string{"single broad query", "exhaustive crawl"},
		},
		{
			Phase:    "retrieval",
			Decision: "react_action_retrieve",
		},
	})
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(steps))
	}
	first := steps[0]
	if first.Timestamp != 1717000000000 {
		t.Fatalf("timestamp not mapped: %d", first.Timestamp)
	}
	if first.Phase != "planning" || first.Decision != "cot_plan_summary" {
		t.Fatalf("phase/decision not trimmed+mapped: %q %q", first.Phase, first.Decision)
	}
	if first.Reasoning != "Decompose the question into retrieval branches." {
		t.Fatalf("reasoning not trimmed: %q", first.Reasoning)
	}
	if len(first.Alternatives) != 2 || first.Alternatives[1] != "exhaustive crawl" {
		t.Fatalf("alternatives not mapped: %#v", first.Alternatives)
	}
	if steps[1].Phase != "retrieval" || steps[1].Decision != "react_action_retrieve" {
		t.Fatalf("second step not mapped: %#v", steps[1])
	}
}

func TestFromInternalReasoningTraceEmpty(t *testing.T) {
	if got := fromInternalReasoningTrace(nil); got != nil {
		t.Fatalf("expected nil for empty trace, got %#v", got)
	}
}
