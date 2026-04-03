package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTreeLoop_Scores(t *testing.T) {
	t.Run("extractConfidenceScore", func(t *testing.T) {
		assert.Equal(t, 0.8, extractConfidenceScore(map[string]any{"confidence": 0.8}))
		assert.Equal(t, 0.9, extractConfidenceScore(map[string]any{
			"confidence_report": map[string]any{"overall_confidence": 0.9},
		}))
		assert.Equal(t, 0.55, extractConfidenceScore(map[string]any{}))
		assert.Equal(t, 0.0, extractConfidenceScore(nil))
	})

	t.Run("extractGroundingScore", func(t *testing.T) {
		assert.Equal(t, 0.7, extractGroundingScore(map[string]any{"grounding_ratio": 0.7}))
		assert.Equal(t, 0.5, extractGroundingScore(map[string]any{}))
		assert.Equal(t, 0.0, extractGroundingScore(nil))
	})

	t.Run("scoreBranchResult", func(t *testing.T) {
		s, c := scoreBranchResult(map[string]any{"confidence": 1.0, "grounding_ratio": 1.0})
		assert.Equal(t, 1.0, s)
		assert.Equal(t, 1.0, c)
	})

	t.Run("uctScore", func(t *testing.T) {
		node := &mctsNode{Visits: 10, Value: 5.0}
		s := uctScore(node, 20)
		assert.Greater(t, s, 0.0)
		
		node0 := &mctsNode{Visits: 0}
		s0 := uctScore(node0, 10)
		assert.Greater(t, s0, 1e8)
	})
}

func TestTreeLoop_Helpers(t *testing.T) {
	t.Run("anyToString", func(t *testing.T) {
		assert.Equal(t, "hello", anyToString("hello"))
		assert.Equal(t, "123", anyToString(123))
		assert.Equal(t, "<nil>", anyToString(nil))
	})

	t.Run("cloneMap", func(t *testing.T) {
		orig := map[string]any{"a": float64(1)}
		cloned := cloneMap(orig)
		assert.Equal(t, orig, cloned)
		cloned["a"] = float64(2)
		assert.NotEqual(t, orig["a"], cloned["a"])
	})
	
	t.Run("outputConsensusKey", func(t *testing.T) {
		assert.Equal(t, "summary_text", outputConsensusKey(map[string]any{"summary": "summary_text"}))
		assert.Equal(t, "empty", outputConsensusKey(nil))
	})
}

func TestTreeLoop_NodeSelection(t *testing.T) {
	nodes := []*mctsNode{
		{ID: 1, Visits: 10, Value: 1.0},
		{ID: 2, Visits: 1, Value: 0.1},
	}
	// Node 2 has fewer visits, higher UCT exploration bonus
	selected := selectNodeByUCT(nodes, 11)
	assert.Equal(t, 2, selected.ID)
}

func TestTreeLoop_RunProgrammatic(t *testing.T) {
	session := &AgentSession{SessionID: "tree_test"}
	mockExec := func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
		return map[string]any{
			"summary": "done",
			"confidence": 0.9,
			"grounding_ratio": 0.8,
		}, nil
	}

	t.Run("One iteration success", func(t *testing.T) {
		res := RunProgrammaticTreeLoop(context.Background(), mockExec, session, "search", map[string]any{"q": "test"}, 1, nil)
		assert.True(t, res.Completed)
		assert.Len(t, res.Iterations, 1)
		assert.Equal(t, 0.9, res.BestConfidence)
		assert.Equal(t, "done", res.Final["summary"])
	})
}

func TestTreeLoop_HTTP(t *testing.T) {
	t.Run("TreeLoopIterationsToHTTP", func(t *testing.T) {
		iterations := []treeLoopIteration{
			{Iteration: 1, BranchID: 1, Success: true, Status: "ok", Score: 0.8, Confidence: 0.9, Reason: "good"},
		}
		res := TreeLoopIterationsToHTTP(iterations)
		assert.Len(t, res, 1)
		assert.Equal(t, 1, res[0]["iteration"])
		assert.Equal(t, "ok", res[0]["status"])
		assert.Equal(t, 0.8, res[0]["score"])
		assert.Equal(t, "good", res[0]["reason"])
	})
}
