package wisdev

import (
	"encoding/json"
	"log/slog"
)

// StreamEventType enumerates all possible thinking-stream event types.
type StreamEventType string

const (
	EventThoughtGenerated  StreamEventType = "thought_generated"
	EventBranchPruned      StreamEventType = "branch_pruned"
	EventVerifierScored    StreamEventType = "verifier_scored"
	EventIterationComplete StreamEventType = "iteration_complete"
	EventExpansionFailed   StreamEventType = "expansion_failed"
	EventConsensusSelected StreamEventType = "consensus_selected"
)

// StreamEvent is the canonical structure for all SSE events emitted by the MCTS loop
// and the iterative research pipeline. All fields are optional; omitempty keeps payloads small.
type StreamEvent struct {
	Type            StreamEventType `json:"type"`
	TraceID         string          `json:"trace_id,omitempty"`
	Iteration       int             `json:"iteration,omitempty"`
	NodeID          int             `json:"node_id,omitempty"`
	Depth           int             `json:"depth,omitempty"`
	Hypothesis      string          `json:"hypothesis,omitempty"`
	Label           string          `json:"label,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	SimilarityScore float64         `json:"similarity_score,omitempty"`
	Score           float64         `json:"score,omitempty"`
	VerifierScore   float64         `json:"verifier_score,omitempty"`
	CoverageScore   float64         `json:"coverage_score,omitempty"`
	PRMReward       float64         `json:"prm_reward,omitempty"`
	NewPapers       int             `json:"new_papers,omitempty"`
	TotalPapers     int             `json:"total_papers,omitempty"`
	BranchID        int             `json:"branch_id,omitempty"`
	ErrorMsg        string          `json:"error_msg,omitempty"`
}

// emitEvent serialises evt to JSON and calls streamFn. Safe to call with nil streamFn.
func emitEvent(streamFn func(map[string]any), evt StreamEvent) {
	if streamFn == nil {
		return
	}
	b, err := json.Marshal(evt)
	if err != nil {
		slog.Warn("emitEvent: failed to marshal stream event", "type", evt.Type, "error", err)
		return
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		slog.Warn("emitEvent: failed to unmarshal event to map", "error", err)
		return
	}
	streamFn(m)
}
