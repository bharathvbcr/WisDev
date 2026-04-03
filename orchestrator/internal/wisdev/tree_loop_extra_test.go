package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTreeLoop_Expansion(t *testing.T) {
	t.Run("extractVariantsFromThoughts - structured result", func(t *testing.T) {
		result := map[string]any{
			"branches": []any{
				map[string]any{
					"hypothesis": "hyp1",
					"nodes": []any{
						map[string]any{"label": "variant A", "reasoning": "reason A", "search_weight": 0.8},
					},
				},
				map[string]any{
					"hypothesis": "hyp2",
					"nodes": []any{
						map[string]any{"label": "variant B"},
					},
				},
			},
		}
		base := map[string]any{"query": "test"}
		variants := extractVariantsFromThoughts(result, base)
		assert.Len(t, variants, 2)
		assert.Equal(t, "variant A", variants[0]["label"])
		assert.Equal(t, "reason A", variants[0]["reasoning"])
		assert.Equal(t, "hyp1", variants[0]["branch_hypothesis"])
	})

	t.Run("extractVariantsFromThoughts - empty branches", func(t *testing.T) {
		result := map[string]any{"branches": []any{}}
		variants := extractVariantsFromThoughts(result, map[string]any{})
		assert.Nil(t, variants)
	})

	t.Run("extractVariantsFromThoughts - missing branches key", func(t *testing.T) {
		result := map[string]any{"other": "data"}
		variants := extractVariantsFromThoughts(result, map[string]any{})
		assert.Nil(t, variants)
	})

	t.Run("expandNodeWithLLM - execFn success", func(t *testing.T) {
		execFn := func(_ context.Context, _ string, _ map[string]any, _ *AgentSession) (map[string]any, error) {
			return map[string]any{
				"branches": []any{
					map[string]any{
						"nodes": []any{map[string]any{"label": "Try X"}},
					},
					map[string]any{
						"nodes": []any{map[string]any{"label": "Try Y"}},
					},
				},
			}, nil
		}
		node := &mctsNode{ID: 1, Payload: map[string]any{"label": "root"}}
		base := map[string]any{"query": "search", "label": "root"}
		variants, err := expandNodeWithLLM(context.Background(), execFn, nil, node, base, nil)
		assert.NoError(t, err)
		assert.NotEmpty(t, variants)
	})
}

func TestTreeLoop_Consensus(t *testing.T) {
	branches := []completedBranch{
		{BranchID: 1, ConsensusKey: "key1", Score: 0.8},
		{BranchID: 2, ConsensusKey: "key1", Score: 0.7},
		{BranchID: 3, ConsensusKey: "key2", Score: 0.9},
	}

	winner, votes, ok := selectWinnerByConsensus(branches)
	assert.True(t, ok)
	// key1 has 2 votes, key2 has 1. key1 should win.
	assert.Equal(t, "key1", winner.ConsensusKey)
	assert.Equal(t, 2, votes["key1"])
	assert.Equal(t, 1, votes["key2"])
}

func TestTreeLoop_Consensus_Empty(t *testing.T) {
	_, _, ok := selectWinnerByConsensus(nil)
	assert.False(t, ok)
}
