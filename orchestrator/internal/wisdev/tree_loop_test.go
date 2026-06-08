package wisdev

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloneMap(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
	}{
		{"nil map", nil},
		{"empty map", map[string]any{}},
		{"simple map", map[string]any{"key": "value", "num": float64(123)}},
		{"nested map", map[string]any{"outer": map[string]any{"inner": true}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloned := cloneMap(tt.input)
			if tt.input == nil {
				assert.NotNil(t, cloned)
				assert.Empty(t, cloned)
			} else {
				assert.Equal(t, tt.input, cloned)
				// Ensure it's a deep copy by mutating the original if nested
				if tt.name == "nested map" {
					tt.input["outer"].(map[string]any)["inner"] = false
					assert.True(t, cloned["outer"].(map[string]any)["inner"].(bool))
				}
			}
		})
	}
}

func TestExtractVariantsFromThoughts(t *testing.T) {
	basePayload := map[string]any{"root": "data"}

	t.Run("empty or invalid", func(t *testing.T) {
		assert.Nil(t, extractVariantsFromThoughts(nil, basePayload))
		assert.Nil(t, extractVariantsFromThoughts(map[string]any{}, basePayload))
		assert.Nil(t, extractVariantsFromThoughts(map[string]any{"branches": "not a slice"}, basePayload))
	})

	t.Run("valid branches", func(t *testing.T) {
		input := map[string]any{
			"branches": []any{
				map[string]any{
					"hypothesis": "hypo1",
					"nodes": []any{
						map[string]any{
							"label":         "label1",
							"reasoning":     "reason1",
							"search_weight": 0.5,
						},
					},
				},
				map[string]any{
					"nodes": []any{
						map[string]any{
							"label": "label2",
						},
					},
				},
			},
		}

		variants := extractVariantsFromThoughts(input, basePayload)
		assert.Len(t, variants, 2)
		assert.Equal(t, "label1", variants[0]["label"])
		assert.Equal(t, "hypo1", variants[0]["branch_hypothesis"])
		assert.Equal(t, "reason1", variants[0]["reasoning"])
		assert.Equal(t, 0.5, variants[0]["search_weight"])
		assert.Equal(t, "data", variants[0]["root"])
		assert.Equal(t, "label2", variants[1]["label"])
	})

	t.Run("legacy thoughts fallback", func(t *testing.T) {
		input := map[string]any{
			"thoughts":   "legacy thought",
			"confidence": 0.72,
		}

		variants := extractVariantsFromThoughts(input, basePayload)
		assert.Len(t, variants, 1)
		assert.Equal(t, "legacy thought", variants[0]["label"])
		assert.Equal(t, "legacy thought", variants[0]["reasoning"])
		assert.Equal(t, 0.72, variants[0]["search_weight"])
		assert.Equal(t, "data", variants[0]["root"])
	})
}

func TestRunProgrammaticTreeLoop_StreamingAndBranches(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{SessionID: "stream-session"}

	streamCount := 0
	streamLabels := make([]string, 0)
	streamFn := func(event map[string]any) {
		streamCount++
		if event["type"] == string(EventThoughtGenerated) {
			streamLabels = append(streamLabels, AsOptionalString(event["label"]))
		}
	}

	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		if action == "research.generateThoughts" {
			return map[string]any{
				"branches": []any{
					map[string]any{
						"hypothesis": "branch hypothesis",
						"nodes": []any{
							map[string]any{
								"label":         "branch-one",
								"reasoning":     "canonical branch",
								"search_weight": 0.8,
							},
						},
					},
				},
				"confidence": 0.8,
			}, nil
		}
		return map[string]any{
			"confidence": 0.8,
			"summary":    "Branch result",
		}, nil
	}

	result := RunProgrammaticTreeLoop(ctx, mockExec, session, "test.action", map[string]any{"query": "test"}, 3, streamFn)

	assert.True(t, result.Completed)
	assert.Greater(t, streamCount, 0)
	assert.Contains(t, streamLabels, "branch-one")
}

func TestRunProgrammaticTreeLoop_StreamingAndLegacyThoughts(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{SessionID: "stream-session"}

	streamCount := 0
	streamLabels := make([]string, 0)
	streamFn := func(event map[string]any) {
		streamCount++
		if event["type"] == string(EventThoughtGenerated) {
			streamLabels = append(streamLabels, AsOptionalString(event["label"]))
		}
	}

	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		if action == "research.generateThoughts" {
			return map[string]any{
				"thoughts":   "thought1",
				"confidence": 0.8,
			}, nil
		}
		return map[string]any{
			"confidence": 0.8,
			"summary":    "Branch result",
		}, nil
	}

	result := RunProgrammaticTreeLoop(ctx, mockExec, session, "test.action", map[string]any{"query": "test"}, 3, streamFn)

	assert.True(t, result.Completed)
	assert.Greater(t, streamCount, 0)
	assert.Contains(t, streamLabels, "thought1")
}

func TestRunProgrammaticTreeLoop_Stagnation(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{SessionID: "stagnation-session"}

	// Mock executor that returns the same score to trigger stagnation
	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		return map[string]any{
			"confidence": 0.7,
			"summary":    "Consistent result",
		}, nil
	}

	result := RunProgrammaticTreeLoop(ctx, mockExec, session, "test.action", map[string]any{"query": "test"}, 8, nil)

	// Should stop due to stagnation (threshold 3, min iterations 3)
	assert.Less(t, len(result.Iterations), 8)
}

func TestRunProgrammaticTreeLoop_LowRewardWithoutStreamDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{SessionID: "low-reward-session"}

	lowRewardOutput := map[string]any{
		"confidence":      0.05,
		"grounding_ratio": 0.0,
		"summary":         "",
	}
	score, _ := scoreBranchResult(lowRewardOutput)
	if score >= 0.45 {
		t.Fatalf("expected low reward score below escalation threshold, got %v", score)
	}

	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		return cloneMap(lowRewardOutput), nil
	}

	assert.NotPanics(t, func() {
		result := RunProgrammaticTreeLoop(ctx, mockExec, session, "test.action", map[string]any{"query": "test"}, 1, nil)
		assert.Len(t, result.Iterations, 1)
		assert.Equal(t, "completed", result.Iterations[0].Status)
	})
}

func TestAnyToString(t *testing.T) {
	assert.Equal(t, "hello", anyToString("hello"))
	assert.Equal(t, "123", anyToString(123))
	assert.Equal(t, "true", anyToString(true))
	assert.Equal(t, "[]", anyToString([]int{}))
}

func TestExtractConfidenceScore(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected float64
	}{
		{"nil", nil, 0},
		{"direct confidence", map[string]any{"confidence": 0.8}, 0.8},
		{"report overall", map[string]any{"confidence_report": map[string]any{"overall_confidence": 0.9}}, 0.9},
		{"report calibrated", map[string]any{"confidence_report": map[string]any{"calibrated_score": 0.7}}, 0.7},
		{"clamp high", map[string]any{"confidence": 1.5}, 1.0},
		{"clamp low", map[string]any{"confidence": -0.5}, 0.0},
		{"default fallback", map[string]any{}, 0.55},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, extractConfidenceScore(tt.input))
		})
	}
}

func TestExtractGroundingScore(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected float64
	}{
		{"nil", nil, 0},
		{"direct ratio", map[string]any{"grounding_ratio": 0.8}, 0.8},
		{"report ratio", map[string]any{"confidence_report": map[string]any{"grounding_ratio": 0.9}}, 0.9},
		{"fallback", map[string]any{}, 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, extractGroundingScore(tt.input))
		})
	}
}

func TestOutputConsensusKey(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected string
	}{
		{"nil", nil, "empty"},
		{"summary", map[string]any{"summary": "Test Summary"}, "test summary"},
		{"final_answer", map[string]any{"final_answer": "42"}, "42"},
		{"reasoning", map[string]any{"reasoning": "Because reasons"}, "because reasons"},
		{"combined", map[string]any{"summary": "S", "final_answer": "A"}, "s | a"},
		{"json fallback", map[string]any{"foo": "bar"}, "{\"foo\":\"bar\"}"},
		{"long text truncation", map[string]any{"summary": string(make([]byte, 200))}, string(make([]byte, 180))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := outputConsensusKey(tt.input)
			if tt.name == "json fallback" {
				assert.Contains(t, key, "foo")
			} else if tt.name == "long text truncation" {
				assert.Len(t, key, 180)
			} else {
				assert.Equal(t, tt.expected, key)
			}
		})
	}
}

func TestRunProgrammaticTreeLoop_Basic(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{SessionID: "test-session"}

	// Simple mock executor that always succeeds
	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		if action == "research.verifyClaims" {
			return map[string]any{"score": 0.9}, nil
		}
		return map[string]any{
			"confidence": 0.85,
			"summary":    "Success result",
		}, nil
	}

	result := RunProgrammaticTreeLoop(ctx, mockExec, session, "test.action", map[string]any{"query": "test"}, 2, nil)

	assert.True(t, result.Completed)
	assert.Greater(t, len(result.Iterations), 0)
	assert.NotNil(t, result.Final)
	assert.Equal(t, 0.85, result.BestConfidence)
	assert.NotEmpty(t, result.BranchArtifacts)
	assert.NotNil(t, session.MemoryTiers)
	assert.NotEmpty(t, session.MemoryTiers.ArtifactMemory)
	assert.Equal(t, "branch_summary", result.BranchArtifacts[0].Type)
}

func TestRunProgrammaticTreeLoop_PersistsUniqueBranchArtifacts(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{SessionID: "artifact-session"}

	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		if action == "research.verifyClaims" {
			return map[string]any{"score": 0.8}, nil
		}
		return map[string]any{
			"confidence":   0.82,
			"summary":      "Stable branch output",
			"final_answer": "Stable branch output",
		}, nil
	}

	first := RunProgrammaticTreeLoop(ctx, mockExec, session, "test.action", map[string]any{"query": "test"}, 2, nil)
	firstCount := len(session.MemoryTiers.ArtifactMemory)
	second := RunProgrammaticTreeLoop(ctx, mockExec, session, "test.action", map[string]any{"query": "test"}, 2, nil)

	assert.NotEmpty(t, first.BranchArtifacts)
	assert.NotEmpty(t, second.BranchArtifacts)
	assert.NotNil(t, session.MemoryTiers)
	assert.Greater(t, firstCount, 0)
	assert.Len(t, session.MemoryTiers.ArtifactMemory, firstCount)
}

func TestRunProgrammaticTreeLoop_Failure(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{SessionID: "test-session"}

	// Mock executor that always fails
	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		return nil, errors.New("execution failed")
	}

	result := RunProgrammaticTreeLoop(ctx, mockExec, session, "test.action", map[string]any{"query": "test"}, 2, nil)

	assert.False(t, result.Completed)
	assert.Equal(t, "failed", result.Final["status"])
}

func TestRunProgrammaticTreeLoop_EarlyStop(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{SessionID: "test-session"}

	// Mock executor that returns high confidence to trigger early stop
	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		return map[string]any{
			"confidence": 0.99,
			"summary":    "High confidence result",
		}, nil
	}

	result := RunProgrammaticTreeLoop(ctx, mockExec, session, "test.action", map[string]any{"query": "test"}, 5, nil)

	// Should stop before 5 iterations (minIterationsBeforeStop is 3)
	assert.LessOrEqual(t, len(result.Iterations), 4) // 3 completions + 1 early_stop event
	assert.True(t, result.Completed)
}

func TestTreeLoopIterationsToHTTP(t *testing.T) {
	events := []treeLoopIteration{
		{Iteration: 1, BranchID: 1, Status: "completed", Success: true, Score: 0.8, Confidence: 0.9, Reason: "ok", Output: map[string]any{"tasks": []any{"a"}}},
		{Iteration: 2, BranchID: 2, Status: "failed", Success: false, Reason: "error"},
	}

	httpEvents := TreeLoopIterationsToHTTP(events)
	assert.Len(t, httpEvents, 2)
	assert.Equal(t, 1, httpEvents[0]["iteration"])
	assert.Equal(t, 0.8, httpEvents[0]["score"])
	assert.Equal(t, "completed", httpEvents[0]["status"])
	assert.Equal(t, true, httpEvents[0]["success"])
	assert.Equal(t, map[string]any{"tasks": []any{"a"}}, httpEvents[0]["output"])
	assert.Equal(t, "error", httpEvents[1]["reason"])
}

func TestExpandNodeWithLLM(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{}
	node := &mctsNode{ID: 1, Depth: 0, Payload: map[string]any{"label": "root"}}
	basePayload := map[string]any{"query": "test query", "domain": "science"}

	t.Run("success", func(t *testing.T) {
		mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
			return map[string]any{
				"branches": []any{
					map[string]any{
						"nodes": []any{
							map[string]any{"label": "new thought"},
						},
					},
				},
			}, nil
		}
		variants, err := expandNodeWithLLM(ctx, mockExec, session, node, basePayload, nil)
		assert.NoError(t, err)
		assert.Len(t, variants, 1)
		assert.Equal(t, "new thought", variants[0]["label"])
	})

	t.Run("fallback on error", func(t *testing.T) {
		mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
			return nil, errors.New("llm error")
		}
		variants, err := expandNodeWithLLM(ctx, mockExec, session, node, basePayload, nil)
		assert.NoError(t, err)     // Should not return error, should fallback
		assert.Len(t, variants, 2) // deriveBranchVariants returns 2 variants
	})

	t.Run("provider cooldown skips retry storm", func(t *testing.T) {
		attempts := 0
		mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
			attempts++
			return nil, errors.New("vertex structured output provider cooldown active; retry after 45s")
		}
		variants, err := expandNodeWithLLM(ctx, mockExec, session, node, basePayload, nil)
		assert.NoError(t, err)
		assert.Len(t, variants, 2)
		assert.Equal(t, 1, attempts)
	})
}

func TestResolveLLMExpandTimeoutUsesProductionDefaultAndEnvOverride(t *testing.T) {
	t.Setenv("WISDEV_TREE_LLM_EXPAND_TIMEOUT", "")
	assert.Equal(t, defaultLLMExpandTimeout, resolveLLMExpandTimeout())

	t.Setenv("WISDEV_TREE_LLM_EXPAND_TIMEOUT", "60s")
	assert.Equal(t, 60*time.Second, resolveLLMExpandTimeout())

	t.Setenv("WISDEV_TREE_LLM_EXPAND_TIMEOUT", "30")
	assert.Equal(t, 30*time.Second, resolveLLMExpandTimeout())
}

func TestRankVariantsWithBatchVerifierAnnotatesAndSorts(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{}
	variants := []map[string]any{
		{"label": "weak"},
		{"label": "strong"},
	}
	calls := 0
	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		calls++
		assert.Equal(t, ActionResearchVerifyClaimsBatch, action)
		assert.Equal(t, "mcts_branch_prune", payload["mode"])
		assert.NotNil(t, ctx)
		return map[string]any{
			"results": []any{
				map[string]any{"score": 0.2},
				map[string]any{"score": 0.9},
			},
		}, nil
	}

	ranked := rankVariantsWithBatchVerifier(ctx, mockExec, session, variants, map[string]any{"query": "sleep memory"})

	assert.Equal(t, 1, calls)
	assert.Equal(t, "strong", ranked[0]["label"])
	assert.Equal(t, 0.9, ranked[0]["verifier_score"])
	assert.Equal(t, "weak", ranked[1]["label"])
}

func TestRankVariantsWithBatchVerifierForwardsAvailableSources(t *testing.T) {
	resetMCTSBatchVerifierCooldownForTests()
	t.Cleanup(resetMCTSBatchVerifierCooldownForTests)
	ctx := context.Background()
	session := &AgentSession{}
	variants := []map[string]any{{"label": "candidate"}, {"label": "fallback"}}
	calls := 0
	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		calls++
		papers, ok := payload["papers"].([]any)
		require.True(t, ok)
		require.Len(t, papers, 1)
		paper, ok := papers[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "paper-1", paper["id"])
		return map[string]any{"results": []any{map[string]any{"score": 0.7}, map[string]any{"score": 0.1}}}, nil
	}

	ranked := rankVariantsWithBatchVerifier(ctx, mockExec, session, variants, map[string]any{
		"query": "sleep memory",
		"canonicalSources": []Source{
			{ID: "paper-1", Title: "Sleep Study"},
		},
	})

	assert.Equal(t, 1, calls)
	assert.Equal(t, 0.7, ranked[0]["verifier_score"])
}

func TestRankVariantsWithBatchVerifierOpensCooldownOnRateLimit(t *testing.T) {
	resetMCTSBatchVerifierCooldownForTests()
	t.Cleanup(resetMCTSBatchVerifierCooldownForTests)

	ctx := context.Background()
	session := &AgentSession{}
	variants := []map[string]any{{"label": "candidate"}, {"label": "fallback"}}
	calls := 0
	rateLimitedExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		calls++
		return nil, errors.New("vertex RESOURCE_EXHAUSTED 429 quota exceeded")
	}

	ranked := rankVariantsWithBatchVerifier(ctx, rateLimitedExec, session, variants, map[string]any{"query": "sleep memory"})

	assert.Equal(t, variants, ranked)
	assert.Equal(t, 1, calls)
	assert.Greater(t, mctsBatchVerifierCooldownRemaining(time.Now()), time.Duration(0))

	skippedExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		calls++
		return map[string]any{"results": []any{map[string]any{"score": 0.9}}}, nil
	}
	ranked = rankVariantsWithBatchVerifier(ctx, skippedExec, session, variants, map[string]any{"query": "sleep memory"})

	assert.Equal(t, variants, ranked)
	assert.Equal(t, 1, calls, "cooldown should skip the optional verifier call")
}

func TestRankVariantsWithBatchVerifierDoesNotCooldownGenericErrors(t *testing.T) {
	resetMCTSBatchVerifierCooldownForTests()
	t.Cleanup(resetMCTSBatchVerifierCooldownForTests)

	ctx := context.Background()
	session := &AgentSession{}
	variants := []map[string]any{{"label": "candidate"}, {"label": "fallback"}}
	calls := 0
	mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		calls++
		return nil, errors.New("schema validation failed")
	}

	ranked := rankVariantsWithBatchVerifier(ctx, mockExec, session, variants, map[string]any{"query": "sleep memory"})

	assert.Equal(t, variants, ranked)
	assert.Equal(t, 1, calls)
	assert.Equal(t, time.Duration(0), mctsBatchVerifierCooldownRemaining(time.Now()))
}

func TestMCTSTreeLogic(t *testing.T) {
	child1 := &mctsNode{ID: 2, Depth: 1, ParentID: 1}

	t.Run("uctScore", func(t *testing.T) {
		assert.True(t, math.IsInf(uctScore(child1, 0), 1))
		child1.Visits = 1
		child1.Value = 0.8
		assert.Greater(t, uctScore(child1, 10), 0.8)
	})

	t.Run("selectNodeByUCT", func(t *testing.T) {
		c1 := &mctsNode{ID: 10, Visits: 0}
		c2 := &mctsNode{ID: 11, Visits: 0}
		selected := selectNodeByUCT([]*mctsNode{c1, c2}, 0)
		assert.Equal(t, 10, selected.ID)

		c1.Visits = 1
		c1.Value = 1.0
		selected = selectNodeByUCT([]*mctsNode{c1, c2}, 1)
		assert.Equal(t, 11, selected.ID)
	})

	t.Run("backpropagate", func(t *testing.T) {
		r := &mctsNode{ID: 1, Visits: 0, Value: 0}
		c := &mctsNode{ID: 2, ParentID: 1, Visits: 0, Value: 0}
		state := map[int]*mctsNode{1: r, 2: c}
		backpropagate(state, 2, 0.9)
		assert.Equal(t, 1, c.Visits)
		assert.Equal(t, 0.9, c.Value)
		assert.Equal(t, 1, r.Visits)
		assert.Equal(t, 0.9, r.Value)
	})
}

func TestMaybeVerifierScore(t *testing.T) {
	ctx := context.Background()
	session := &AgentSession{}
	output := map[string]any{"data": "test"}
	basePayload := map[string]any{"verifierAction": "custom.verify"}

	t.Run("success direct score", func(t *testing.T) {
		mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
			assert.Equal(t, "custom.verify", action)
			return map[string]any{"score": 0.95}, nil
		}
		score := maybeVerifierScore(ctx, mockExec, session, output, basePayload)
		assert.Equal(t, 0.95, score)
	})

	t.Run("success report score", func(t *testing.T) {
		mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
			return map[string]any{"confidence_report": map[string]any{"calibrated_score": 0.8}}, nil
		}
		score := maybeVerifierScore(ctx, mockExec, session, output, basePayload)
		assert.Equal(t, 0.8, score)
	})

	t.Run("error returns zero", func(t *testing.T) {
		mockExec := func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
			return nil, errors.New("verify error")
		}
		score := maybeVerifierScore(ctx, mockExec, session, output, basePayload)
		assert.Equal(t, 0.0, score)
	})
}

func TestSelectWinnerByConsensus(t *testing.T) {
	tests := []struct {
		name       string
		candidates []completedBranch
		wantID     int
	}{
		{
			name: "majority winner",
			candidates: []completedBranch{
				{BranchID: 1, ConsensusKey: "A", Score: 0.8},
				{BranchID: 2, ConsensusKey: "B", Score: 0.7},
				{BranchID: 3, ConsensusKey: "A", Score: 0.6},
			},
			wantID: 1,
		},
		{
			name: "tie resolved by verifier",
			candidates: []completedBranch{
				{BranchID: 1, ConsensusKey: "A", Score: 0.8, Verifier: 0.5},
				{BranchID: 2, ConsensusKey: "B", Score: 0.7, Verifier: 0.9},
			},
			wantID: 2,
		},
		{
			name: "tie resolved by score",
			candidates: []completedBranch{
				{BranchID: 1, ConsensusKey: "A", Score: 0.9, Verifier: 0.5},
				{BranchID: 2, ConsensusKey: "B", Score: 0.7, Verifier: 0.5},
			},
			wantID: 1,
		},
		{
			name:       "empty candidates",
			candidates: []completedBranch{},
			wantID:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			winner, _, ok := selectWinnerByConsensus(tt.candidates)
			if tt.wantID == 0 {
				assert.False(t, ok)
			} else {
				assert.True(t, ok)
				assert.Equal(t, tt.wantID, winner.BranchID)
			}
		})
	}
}

func TestResolveExpandTimeout_DepthProportional(t *testing.T) {
	base := resolveLLMExpandTimeout()

	tests := []struct {
		depth    int
		expected time.Duration
	}{
		{0, base},
		{1, base},
		{2, base + 15*time.Second},
		{3, base + 30*time.Second},
		{4, base + 45*time.Second},
	}

	for _, tt := range tests {
		got := resolveExpandTimeout(tt.depth)
		assert.Equal(t, tt.expected, got, "depth=%d", tt.depth)
	}
}
