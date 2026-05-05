package wisdev

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func disableExecutorGuardrailDeps(t *testing.T) {
	t.Helper()
	t.Setenv(allowGoCitationFallbackEnv, "true")
}

func TestPlanExecutor_RunStepWithRecovery_Extra(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	e := &PlanExecutor{
		brainCaps: NewBrainCapabilities(nil),
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			if action == "fail" {
				return nil, errors.New("fail")
			}
			return map[string]any{"success": true, "confidence": 0.95}, nil
		},
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun: true,
		},
	}
	session := &AgentSession{
		SessionID:     "s1",
		OriginalQuery: "sleep memory replay",
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			StepAttempts:     make(map[string]int),
			StepFailureCount: make(map[string]int),
			ApprovedStepIDs:  make(map[string]bool),
			CompletedStepIDs: make(map[string]bool),
			FailedStepIDs:    make(map[string]string),
		},
	}

	t.Run("Nil Context Normalized", func(t *testing.T) {
		originalPythonExecute := e.pythonExecute
		t.Cleanup(func() { e.pythonExecute = originalPythonExecute })
		e.pythonExecute = func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			require.NotNil(t, ctx)
			assert.NoError(t, ctx.Err())
			return map[string]any{"success": true, "confidence": 0.93}, nil
		}
		step := PlanStep{ID: "step_nil_context", Action: "ok", ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow}
		res := e.RunStepWithRecovery(nil, session, step, 1)
		assert.NoError(t, res.Err)
		assert.Equal(t, 0.93, res.Confidence)
	})

	t.Run("GoNative Success", func(t *testing.T) {
		step := PlanStep{
			ID:              "step_native",
			Action:          ActionResearchVerifyReasoningPaths,
			ExecutionTarget: ExecutionTargetGoNative,
			Risk:            RiskLevelLow,
			Params: map[string]any{
				"branches": []any{
					map[string]any{
						"id":                 "branch-1",
						"claim":              "supported branch",
						"supportScore":       0.9,
						"evidenceCount":      2,
						"contradictionCount": 0,
						"findings": []any{
							map[string]any{"claim": "supported branch", "source_id": "paper-1", "snippet": "primary support"},
							map[string]any{"claim": "supported branch", "source_id": "paper-2", "snippet": "replication support"},
						},
					},
				},
			},
		}
		res := e.RunStepWithRecovery(context.Background(), session, step, 1)
		assert.NoError(t, res.Err)
		assert.NotNil(t, res.Payload)
		assert.True(t, toBool(res.Payload["readyForSynthesis"]))
	})

	t.Run("GoNative SynthesizeAnswer", func(t *testing.T) {
		mockLLM := new(mockLLMServiceClient)
		lc := llm.NewClient()
		lc.SetClient(mockLLM)
		mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return strings.Contains(req.Prompt, `Synthesize a comprehensive research report for the query: "sleep memory"`) &&
				strings.Contains(req.Prompt, "ID: p1 | Title: Sleep Study")
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"sections":[{"heading":"Findings","sentences":[{"text":"sleep supports memory consolidation","evidenceIds":["p1"]}]}]}`}, nil).Once()

		originalBrainCaps := e.brainCaps
		t.Cleanup(func() { e.brainCaps = originalBrainCaps })
		e.brainCaps = NewBrainCapabilities(lc)

		step := PlanStep{
			ID:              "step_synthesis",
			Action:          " research.synthesize-answer ",
			ExecutionTarget: ExecutionTargetGoNative,
			Risk:            RiskLevelHigh,
			Params: map[string]any{
				"query": "sleep memory",
				"evidence": []any{
					map[string]any{"id": "p1", "title": "Sleep Study", "summary": "REM sleep improves consolidation."},
				},
			},
		}

		res := e.RunStepWithRecovery(context.Background(), session, step, 1)
		require.NoError(t, res.Err)
		assert.Equal(t, 0.9, res.Confidence)
		assert.Equal(t, "## Findings\n\nsleep supports memory consolidation", res.Payload["text"])
		assert.NotNil(t, res.Payload["structuredAnswer"])
		assert.Len(t, res.Sources, 1)
		mockLLM.AssertExpectations(t)
	})

	t.Run("GoNative VerifyClaimsBatch", func(t *testing.T) {
		mockLLM := new(mockLLMServiceClient)
		lc := llm.NewClient()
		lc.SetClient(mockLLM)
		mockLLM.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return strings.Contains(req.Prompt, "Rank and verify these research findings") &&
				strings.Contains(req.Prompt, "candidate claim") &&
				strings.Contains(req.Prompt, "Sleep Study") &&
				req.RequestClass == "standard" &&
				req.ServiceTier == "standard" &&
				req.GetThinkingBudget() == 1024
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"results":[{"verified":true,"score":0.88,"report":"supported"}]}`}, nil).Once()

		originalBrainCaps := e.brainCaps
		t.Cleanup(func() { e.brainCaps = originalBrainCaps })
		e.brainCaps = NewBrainCapabilities(lc)

		step := PlanStep{
			ID:              "step_batch_verify",
			Action:          ActionResearchVerifyClaimsBatch,
			ExecutionTarget: ExecutionTargetGoNative,
			Risk:            RiskLevelMedium,
			Params: map[string]any{
				"candidateOutputs": []any{map[string]any{"claim": "candidate claim"}},
				"sources":          []any{map[string]any{"id": "p1", "title": "Sleep Study"}},
			},
		}

		res := e.RunStepWithRecovery(context.Background(), session, step, 1)
		require.NoError(t, res.Err)
		assert.Equal(t, 0.9, res.Confidence)
		assert.NotEmpty(t, res.Payload["results"])
		assert.Len(t, res.Sources, 1)
		mockLLM.AssertExpectations(t)
	})

	t.Run("GoNative FullPaperRetrieve", func(t *testing.T) {
		originalParallelSearch := ParallelSearch
		t.Cleanup(func() { ParallelSearch = originalParallelSearch })
		ParallelSearch = func(ctx context.Context, rdb redis.UniversalClient, query string, opts SearchOptions) (*MultiSourceResult, error) {
			return &MultiSourceResult{
				Papers:    []Source{{ID: "paper-" + query, Title: "Paper " + query}},
				TraceID:   "trace-" + query,
				QueryUsed: query,
			}, nil
		}
		step := PlanStep{
			ID:              "step_full_paper",
			Action:          ActionResearchFullPaperRetrieve,
			ExecutionTarget: ExecutionTargetGoNative,
			Risk:            RiskLevelLow,
			Params: map[string]any{
				"query":       "sleep memory",
				"planQueries": []any{"hippocampal replay"},
				"limit":       4,
			},
		}
		res := e.RunStepWithRecovery(context.Background(), session, step, 1)
		require.NoError(t, res.Err)
		assert.Equal(t, "full_paper", res.Payload["mode"])
		assert.Equal(t, "paperBundle.v1", res.Payload["contract"])
		assert.NotEmpty(t, res.Payload["queryTrajectory"])
		assert.NotEmpty(t, res.Sources)
	})

	t.Run("PythonSandbox Validation Fail", func(t *testing.T) {
		step := PlanStep{ID: "step_sandbox", Action: "import os; os.system('rm -rf /')", ExecutionTarget: ExecutionTargetPythonSandbox, Risk: RiskLevelLow}
		res := e.RunStepWithRecovery(context.Background(), session, step, 1)
		assert.Error(t, res.Err)
		assert.Contains(t, res.Err.Error(), "GUARDRAIL_BLOCKED")
	})

	t.Run("GoNative Error", func(t *testing.T) {
		originalBrainCaps := e.brainCaps
		t.Cleanup(func() { e.brainCaps = originalBrainCaps })
		e.brainCaps = nil
		step := PlanStep{
			ID:               "step_native_err",
			Action:           ActionResearchVerifyReasoningPaths,
			ExecutionTarget:  ExecutionTargetGoNative,
			Risk:             RiskLevelLow,
			DependsOnStepIDs: []string{"hyp"},
		}
		ensurePlanArtifactState(session.Plan)
		session.Plan.CompletedStepIDs["hyp"] = true
		artifactSet, artifactErr := normalizeStepArtifacts(
			PlanStep{ID: "hyp", Action: ActionResearchGenerateHypotheses},
			map[string]any{
				"hypotheses": []any{
					map[string]any{"id": "branch-1", "claim": "unsupported branch", "status": "candidate"},
				},
			},
			nil,
		)
		if !assert.NoError(t, artifactErr) {
			return
		}
		persistStepArtifacts(session, PlanStep{ID: "hyp", Action: ActionResearchGenerateHypotheses}, artifactSet)
		res := e.RunStepWithRecovery(context.Background(), session, step, 1)
		assert.Error(t, res.Err)
	})

	t.Run("PythonCapability Success with Sources", func(t *testing.T) {
		e.pythonExecute = func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{
				"success":    true,
				"confidence": 0.99,
				"papers": []any{
					map[string]any{"id": "p1", "title": "T1"},
				},
			}, nil
		}
		step := PlanStep{ID: "step_py", Action: "search", ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow}
		res := e.RunStepWithRecovery(context.Background(), session, step, 1)
		assert.NoError(t, res.Err)
		assert.Equal(t, 0.99, res.Confidence)
		assert.Len(t, res.Sources, 1)
	})

	t.Run("Unsupported Target", func(t *testing.T) {
		step := PlanStep{ID: "step_unknown", ExecutionTarget: "unknown"}
		res := e.RunStepWithRecovery(context.Background(), session, step, 1)
		assert.Error(t, res.Err)
		assert.Contains(t, res.Err.Error(), "unsupported execution target")
	})

	t.Run("Unsupported GoNative Action", func(t *testing.T) {
		step := PlanStep{ID: "step_unknown_native", Action: "research.unknownNative", ExecutionTarget: ExecutionTargetGoNative, Risk: RiskLevelLow}
		res := e.RunStepWithRecovery(context.Background(), session, step, 1)
		require.Error(t, res.Err)
		assert.Contains(t, res.Err.Error(), "unsupported Go-native WisDev action")
	})

	t.Run("Panic Recovery", func(t *testing.T) {
		e.pythonExecute = func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			panic("python executor exploded")
		}
		step := PlanStep{ID: "step_panic", Action: "panic", ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow, MaxAttempts: 1}
		res := e.RunStepWithRecovery(context.Background(), session, step, 3)
		require.Error(t, res.Err)
		assert.Contains(t, res.Err.Error(), "step panic recovered")
	})
}

func TestPlanExecutor_HeavyModelTriage_Extra(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	e := &PlanExecutor{
		brainCaps: NewBrainCapabilities(lc),
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{"success": true}, nil
		},
		policyConfig: policy.PolicyConfig{AllowLowRiskAutoRun: true},
	}
	session := &AgentSession{
		SessionID:     "s1",
		OriginalQuery: "heavy query",
		Budget: policy.BudgetState{
			MaxToolCalls: 100,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{ApprovedStepIDs: make(map[string]bool)},
	}

	t.Run("Downgrade to Standard", func(t *testing.T) {
		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil &&
				strings.Contains(req.Prompt, "Assess the complexity") &&
				req.RequestClass == "light" &&
				req.ServiceTier == "standard" &&
				req.RetryProfile == "conservative" &&
				req.GetThinkingBudget() == 0
		})).Return(&llmv1.StructuredResponse{JsonResult: `{"complexity":"medium"}`}, nil).Once()

		step := PlanStep{ID: "s1", ModelTier: ModelTierHeavy, ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow}

		_, _, _, err := e.executeStepOnce(context.Background(), session, step, false)
		assert.NoError(t, err)
		assert.Equal(t, "medium", session.AssessedComplexity)
	})
}

func TestDegradedQuery_Extra(t *testing.T) {
	assert.Equal(t, "", degradedQuery(""))
	assert.Equal(t, "one two three", degradedQuery("one two three"))
	assert.Equal(t, "1 2 3 4 5 6", degradedQuery("1 2 3 4 5 6 7 8 9"))
}

func TestResolveModelForTier_Extra(t *testing.T) {
	budget := policy.BudgetState{MaxCostCents: 100, CostCentsUsed: 60} // 40 remaining
	assert.Equal(t, llm.ResolveStandardModel(), resolveModelForTier(ModelTierHeavy, budget))
	assert.Equal(t, llm.ResolveLightModel(), resolveModelForTier(ModelTierLight, budget))
	assert.Equal(t, llm.ResolveStandardModel(), resolveModelForTier("unknown", budget))
}

func TestPlanExecutor_ExecuteStepOnce_Confidences(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	e := &PlanExecutor{
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			confs := payload["previous_step_confidences"].(map[string]float64)
			if confs["s1"] == 0.8 {
				return map[string]any{"success": true}, nil
			}
			return nil, errors.New("conf mismatch")
		},
		policyConfig: policy.PolicyConfig{AllowLowRiskAutoRun: true},
	}
	session := &AgentSession{
		SessionID: "s1",
		Budget:    policy.BudgetState{MaxToolCalls: 10, MaxCostCents: 1000},
		Plan: &PlanState{
			PlanID:          "p1",
			StepConfidences: map[string]float64{"s1": 0.8},
			ApprovedStepIDs: make(map[string]bool),
		},
	}
	step := PlanStep{
		ID:               "s2",
		Action:           "act",
		DependsOnStepIDs: []string{"s1"},
		ExecutionTarget:  ExecutionTargetPythonCapability,
		Risk:             RiskLevelLow,
	}

	_, _, _, err := e.executeStepOnce(context.Background(), session, step, false)
	assert.NoError(t, err)
}

func TestPlanExecutor_Execute_FailedTerminal(t *testing.T) {
	e := &PlanExecutor{}
	session := &AgentSession{
		SessionID: "s1",
		Status:    SessionExecutingPlan,
		Plan: &PlanState{
			Steps:         []PlanStep{{ID: "s1"}},
			FailedStepIDs: map[string]string{"s1": "fail"},
		},
	}
	out := make(chan PlanExecutionEvent, 10)
	go e.Execute(context.Background(), session, out)

	found := false
	for ev := range out {
		if ev.Type == EventStepFailed && strings.Contains(ev.Message, "ended with failed steps") {
			found = true
		}
	}
	assert.True(t, found)
	assert.Equal(t, SessionFailed, session.Status)
}

func TestPlanExecutor_ExecuteStepOnce_PythonFail(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	e := &PlanExecutor{
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return nil, errors.New("python error")
		},
		policyConfig: policy.PolicyConfig{AllowLowRiskAutoRun: true},
	}
	session := &AgentSession{
		Budget: policy.BudgetState{MaxToolCalls: 10, MaxCostCents: 1000},
		Plan:   &PlanState{ApprovedStepIDs: make(map[string]bool)},
	}
	step := PlanStep{ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow}
	_, _, _, err := e.executeStepOnce(context.Background(), session, step, false)
	assert.Error(t, err)
	assert.Equal(t, "python error", err.Error())
}

func TestPlanExecutor_RunStepWithRecovery_Confirmation(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	e := &PlanExecutor{
		policyConfig: policy.PolicyConfig{AlwaysConfirmHighRisk: true},
	}
	session := &AgentSession{
		Budget: policy.BudgetState{MaxToolCalls: 10, MaxCostCents: 1000},
		Plan:   &PlanState{ApprovedStepIDs: make(map[string]bool)},
	}
	step := PlanStep{ID: "s1", Risk: RiskLevelHigh, ExecutionTarget: ExecutionTargetPythonCapability}

	res := e.RunStepWithRecovery(context.Background(), session, step, 1)
	assert.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "CONFIRMATION_REQUIRED")
}

func TestPlanExecutor_ExecutePersistsArtifactsForGoNativeVerification(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	e := &PlanExecutor{
		brainCaps:    NewBrainCapabilities(nil),
		policyConfig: policy.PolicyConfig{AllowLowRiskAutoRun: true},
	}
	session := &AgentSession{
		SessionID: "sess-artifacts",
		Budget:    policy.BudgetState{MaxToolCalls: 10, MaxCostCents: 1000},
		Plan: &PlanState{
			PlanID:          "plan-artifacts",
			ApprovedStepIDs: make(map[string]bool),
			StepArtifacts:   make(map[string]StepArtifactSet),
		},
	}
	step := PlanStep{
		ID:              "verify",
		Action:          ActionResearchVerifyReasoningPaths,
		ExecutionTarget: ExecutionTargetGoNative,
		Risk:            RiskLevelLow,
		Params: map[string]any{
			"branches": []any{
				map[string]any{
					"id":                 "branch-1",
					"claim":              "supported branch",
					"supportScore":       0.9,
					"evidenceCount":      2,
					"contradictionCount": 0,
					"findings": []any{
						map[string]any{"claim": "supported branch", "source_id": "paper-1", "snippet": "primary support"},
						map[string]any{"claim": "supported branch", "source_id": "paper-2", "snippet": "replication support"},
					},
				},
			},
		},
	}
	payload, sources, _, err := e.executeStepOnce(context.Background(), session, step, false)
	if !assert.NoError(t, err) {
		return
	}
	artifactSet, artifactErr := normalizeStepArtifacts(step, payload, sources)
	if !assert.NoError(t, artifactErr) {
		return
	}
	persistStepArtifacts(session, step, artifactSet)

	artifact, ok := session.Plan.StepArtifacts["verify"]
	assert.True(t, ok)
	if assert.NotNil(t, artifact.ReasoningBundle) && assert.NotNil(t, artifact.ReasoningBundle.Verification) {
		assert.True(t, artifact.ReasoningBundle.Verification.ReadyForSynthesis)
	}
}

func TestPlanExecutor_CoordinateAgentFeedback_Extra(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	e := &PlanExecutor{llmClient: lc}

	session := &AgentSession{OriginalQuery: "test"}
	outcomes := []PlanOutcome{{StepID: "s1", Success: false}}

	t.Run("REPLAN", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Mediate between agent outcomes") &&
				req.Model == llm.ResolveStandardModel() &&
				req.RequestClass == "standard" &&
				req.RetryProfile == "standard" &&
				req.ServiceTier == "standard" &&
				req.GetThinkingBudget() == 1024 &&
				req.LatencyBudgetMs > 0
		})).Return(&llmv1.GenerateResponse{Text: "REPLAN because..."}, nil).Once()
		res, err := e.CoordinateAgentFeedback(context.Background(), session, outcomes)
		assert.NoError(t, err)
		assert.Contains(t, res, "REPLAN")
	})

	t.Run("CONTINUE", func(t *testing.T) {
		msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
			return strings.Contains(req.Prompt, "Mediate between agent outcomes") &&
				req.RequestClass == "standard" &&
				req.GetThinkingBudget() == 1024
		})).Return(&llmv1.GenerateResponse{Text: "CONTINUE"}, nil).Once()
		res, err := e.CoordinateAgentFeedback(context.Background(), session, outcomes)
		assert.NoError(t, err)
		assert.Equal(t, "CONTINUE", res)
	})
}

func TestPlanExecutor_CoordinateAgentFeedback_SkipsSingleSuccessfulOutcome(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	e := &PlanExecutor{llmClient: lc}

	res, err := e.CoordinateAgentFeedback(context.Background(), &AgentSession{OriginalQuery: "test"}, []PlanOutcome{{
		StepID:      "s1",
		Action:      ActionResearchRetrievePapers,
		Success:     true,
		Summary:     "retrieved 10 papers",
		ResultCount: 10,
		Confidence:  0.86,
	}})

	assert.NoError(t, err)
	assert.Equal(t, "CONTINUE", res)
	msc.AssertNotCalled(t, "Generate", mock.Anything, mock.Anything)
}

func TestShouldApplyCoordinatorReplanRequiresActionableFailure(t *testing.T) {
	assert.False(t, shouldApplyCoordinatorReplan([]PlanOutcome{{
		StepID:      "step-01",
		Action:      ActionResearchQueryDecompose,
		Success:     true,
		ResultCount: 0,
		Degraded:    true,
	}}, "REPLAN because planning artifacts were degraded"))

	assert.True(t, shouldApplyCoordinatorReplan([]PlanOutcome{{
		StepID:  "step-02",
		Action:  ActionResearchRetrievePapers,
		Success: true,
	}}, "REPLAN because retrieval produced no papers"))

	assert.True(t, shouldApplyCoordinatorReplan([]PlanOutcome{{
		StepID:  "step-03",
		Action:  ActionResearchGenerateThoughts,
		Success: false,
		Error:   "TOOL_TIMEOUT",
	}}, "REPLAN after failure"))
}

func TestShouldApplyCoordinatorReplanSkipsTerminalFailures(t *testing.T) {
	assert.False(t, shouldApplyCoordinatorReplan([]PlanOutcome{{
		StepID:  "step-guardrail",
		Action:  ActionResearchQueryDecompose,
		Success: false,
		Error:   "GUARDRAIL_BLOCKED",
	}}, "REPLAN after terminal failure"))

	assert.False(t, shouldApplyCoordinatorReplan([]PlanOutcome{{
		StepID:  "step-budget",
		Action:  ActionResearchQueryDecompose,
		Success: false,
		Error:   "BUDGET_EXCEEDED",
	}}, "REPLAN after budget exhaustion"))
}

func TestPlanExecutor_CoordinateAgentFeedback_UsesStructuredOutcomePrompt(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	e := &PlanExecutor{llmClient: lc}

	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req != nil &&
			strings.Contains(req.Prompt, "Outcomes JSON") &&
			strings.Contains(req.Prompt, `"action":"research.retrievePapers"`) &&
			strings.Contains(req.Prompt, `"summary":"retrieved 10 papers"`) &&
			req.RequestClass == "standard" &&
			req.GetThinkingBudget() == 1024
	})).Return(&llmv1.GenerateResponse{Text: "CONTINUE"}, nil).Once()

	res, err := e.CoordinateAgentFeedback(context.Background(), &AgentSession{OriginalQuery: "test"}, []PlanOutcome{{
		StepID:      "s1",
		Action:      ActionResearchRetrievePapers,
		Success:     false,
		Error:       "TOOL_TIMEOUT",
		Summary:     "retrieved 10 papers",
		ResultCount: 10,
		Confidence:  0.42,
	}})

	assert.NoError(t, err)
	assert.Equal(t, "CONTINUE", res)
	msc.AssertExpectations(t)
}

func TestPlanExecutor_CoordinateAgentFeedback_CooldownFallback(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	e := &PlanExecutor{llmClient: lc}

	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req != nil && strings.Contains(req.Prompt, "Mediate between agent outcomes")
	})).Return(nil, errors.New("vertex text generation provider cooldown active; retry after 45s")).Once()

	res, err := e.CoordinateAgentFeedback(context.Background(), &AgentSession{OriginalQuery: "test"}, []PlanOutcome{{
		StepID:  "s1",
		Action:  ActionResearchRetrievePapers,
		Success: false,
		Error:   "TOOL_TIMEOUT",
	}})

	assert.NoError(t, err)
	assert.Equal(t, "CONTINUE", res)
	msc.AssertExpectations(t)
}

func TestPlanExecutorRetrievePapersUsesConfiguredMCPRegistry(t *testing.T) {
	registry := internalsearch.NewProviderRegistry()
	provider := &gatewaySearchProvider{name: "openalex"}
	registry.Register(provider)

	executor := NewPlanExecutor(NewToolRegistry(), policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil, registry)
	session := &AgentSession{
		OriginalQuery:  "open-source retrieval",
		CorrectedQuery: "open-source retrieval",
		Mode:           WisDevModeGuided,
		ServiceTier:    ServiceTierStandard,
	}

	result, papers, _, err := executor.executeGoNativeStep(context.Background(), session, ActionResearchRetrievePapers, map[string]any{
		"query":   "open-source retrieval",
		"limit":   100,
		"sources": []any{"openalex", "openalex"},
		"traceId": "trace-plan-mcp",
	}, false)

	require.NoError(t, err)
	assert.Len(t, papers, 1)
	assert.Equal(t, "wisdev.mcp.paper_retrieval.v1", result["contract"])
	assert.Equal(t, internalsearch.ToolSearchPapersName, result["mcpTool"])
	assert.Equal(t, "wisdev_core_mcp_tool", result["retrievalBy"])
	assert.Equal(t, "open-source retrieval", provider.query)
	assert.Equal(t, 50, provider.opts.Limit)
	assert.Equal(t, []string{"openalex"}, provider.opts.Sources)
}

func TestPlanExecutor_Execute_ConfirmationRequired(t *testing.T) {

	e := &PlanExecutor{
		maxParallelLanes: 1,
		policyConfig: policy.PolicyConfig{
			AlwaysConfirmHighRisk: true,
		},
	}
	session := &AgentSession{
		SessionID: "s1",
		Status:    SessionGeneratingTree,
		Budget: policy.BudgetState{
			MaxToolCalls: 10,
			MaxCostCents: 1000,
		},
		Plan: &PlanState{
			PlanID:          "p1",
			Steps:           []PlanStep{{ID: "s1", Action: "risky", Risk: RiskLevelHigh, ExecutionTarget: ExecutionTargetPythonCapability}},
			ApprovedStepIDs: make(map[string]bool),
		},
	}

	out := make(chan PlanExecutionEvent, 10)
	go e.Execute(context.Background(), session, out)

	confirmationFound := false
	for ev := range out {
		if ev.Type == EventConfirmationNeed {
			confirmationFound = true
		}
	}
	assert.True(t, confirmationFound)
	assert.Equal(t, SessionPaused, session.Status)
	assert.Equal(t, "s1", session.Plan.PendingApprovalStepID)
}

func TestPlanExecutor_Execute_TerminalFailureDoesNotAutomaticReplan(t *testing.T) {
	disableExecutorGuardrailDeps(t)

	e := &PlanExecutor{
		maxReplans:       2,
		maxParallelLanes: 1,
	}
	session := &AgentSession{
		SessionID: "s-terminal",
		Status:    SessionExecutingPlan,
		Plan: &PlanState{
			PlanID: "p-terminal",
			Steps: []PlanStep{
				{ID: "s1", Action: ActionResearchQueryDecompose, ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow},
				{ID: "s2", Action: ActionResearchRetrievePapers, ExecutionTarget: ExecutionTargetPythonCapability, DependsOnStepIDs: []string{"s1"}, Risk: RiskLevelLow},
			},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs: map[string]string{
				"s1": "GUARDRAIL_BLOCKED:tool_budget_exceeded",
			},
			ApprovedStepIDs: make(map[string]bool),
		},
		Budget: policy.BudgetState{MaxToolCalls: 1, MaxCostCents: 10},
	}

	out := make(chan PlanExecutionEvent, 16)
	e.Execute(context.Background(), session, out)

	planRevisedFound := false
	terminalFailureFound := false
	for ev := range out {
		if ev.Type == EventPlanRevised {
			planRevisedFound = true
		}
		if ev.Type == EventStepFailed && strings.Contains(ev.Message, "no ready steps after terminal failure") {
			terminalFailureFound = true
		}
	}

	assert.False(t, planRevisedFound)
	assert.True(t, terminalFailureFound)
	assert.Equal(t, 0, session.Plan.ReplanCount)
	assert.Equal(t, SessionFailed, session.Status)
}

func TestClassifyErrorCode_Extra(t *testing.T) {
	assert.Equal(t, "TOOL_TIMEOUT", classifyErrorCode(errors.New("timeout")))
	assert.Equal(t, "TOOL_RATE_LIMIT", classifyErrorCode(errors.New("too many requests")))
	assert.Equal(t, "TOOL_SCHEMA_INVALID", classifyErrorCode(errors.New("invalid argument")))
	assert.Equal(t, "GUARDRAIL_BLOCKED", classifyErrorCode(errors.New("safety policy")))
	assert.Equal(t, "CONFIRMATION_REQUIRED", classifyErrorCode(errors.New("approval needed")))
	assert.Equal(t, "TOOL_NOT_FOUND", classifyErrorCode(errors.New("unknown action")))
	assert.Equal(t, "BUDGET_EXCEEDED", classifyErrorCode(errors.New("cost exceeded")))
	assert.Equal(t, "AUTH_FAILED", classifyErrorCode(errors.New("unauthorized")))
	assert.Equal(t, "TOOL_EXEC_FAILED", classifyErrorCode(errors.New("random error")))
	assert.Equal(t, "", classifyErrorCode(nil))
}

func TestCoordinateAgentFeedback_Empty(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	e := &PlanExecutor{llmClient: lc}
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req != nil && req.RequestClass == "standard" && req.GetThinkingBudget() == 1024
	})).Return(&llmv1.GenerateResponse{Text: ""}, nil).Once()
	res, err := e.CoordinateAgentFeedback(context.Background(), &AgentSession{}, nil)
	assert.NoError(t, err)
	assert.Equal(t, "CONTINUE", res)
}

func TestCoordinateAgentFeedback_Error(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	e := &PlanExecutor{llmClient: lc}
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req != nil && req.RequestClass == "standard" && req.GetThinkingBudget() == 1024
	})).Return(nil, errors.New("llm fail")).Once()
	res, err := e.CoordinateAgentFeedback(context.Background(), &AgentSession{}, nil)
	assert.NoError(t, err) // implementation returns CONTINUE on error
	assert.Equal(t, "CONTINUE", res)
}

func TestPlanExecutor_Execute_ContextCancel_Wait(t *testing.T) {
	e := &PlanExecutor{maxParallelLanes: 1}
	session := &AgentSession{
		SessionID: "s1",
		Status:    SessionExecutingPlan,
		Plan: &PlanState{
			Steps:           []PlanStep{{ID: "s1", Action: "search", ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow}},
			ApprovedStepIDs: make(map[string]bool),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	out := make(chan PlanExecutionEvent, 10)

	e.Execute(ctx, session, out)

	found := false
	for ev := range out {
		if ev.Type == EventStepFailed && (ev.Message == "execution cancelled" || strings.Contains(ev.Message, "cancelled")) {
			found = true
		}
	}
	assert.True(t, found)
}

func TestPlanExecutor_Execute_NilSession(t *testing.T) {
	e := &PlanExecutor{}
	out := make(chan PlanExecutionEvent, 2)
	e.Execute(context.Background(), nil, out)

	events := make([]PlanExecutionEvent, 0, 1)
	for ev := range out {
		events = append(events, ev)
	}
	require.Len(t, events, 1)
	assert.Equal(t, EventStepFailed, events[0].Type)
	assert.Equal(t, "session is nil", events[0].Message)
}

func TestResolveSessionQuery_Extra(t *testing.T) {
	assert.Equal(t, "", resolveSessionQuery(nil))
}

func TestPlanExecutor_ExecuteStepOnce_NoPythonExecutor(t *testing.T) {

	e := &PlanExecutor{
		pythonExecute: nil,
		policyConfig:  policy.PolicyConfig{AllowLowRiskAutoRun: true},
	}
	session := &AgentSession{
		Budget: policy.BudgetState{MaxToolCalls: 10, MaxCostCents: 1000},
		Plan:   &PlanState{ApprovedStepIDs: make(map[string]bool)},
	}
	step := PlanStep{ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow}
	_, _, _, err := e.executeStepOnce(context.Background(), session, step, false)
	assert.Error(t, err)
	assert.Equal(t, "python executor not configured", err.Error())
}

func TestPlanExecutor_Execute_MaxReplansReached(t *testing.T) {
	e := &PlanExecutor{
		maxParallelLanes: 1,
		maxReplans:       0,
	}
	session := &AgentSession{
		SessionID: "s1",
		Status:    SessionExecutingPlan,
		Plan: &PlanState{
			Steps:         []PlanStep{{ID: "s1", MaxAttempts: 1, ExecutionTarget: ExecutionTargetPythonCapability}},
			FailedStepIDs: map[string]string{"s1": "permanent fail"},
			ReplanCount:   0,
		},
	}
	out := make(chan PlanExecutionEvent, 10)
	go e.Execute(context.Background(), session, out)

	failedFound := false
	for ev := range out {
		if ev.Type == EventStepFailed && strings.Contains(ev.Message, "ended with failed steps") {
			failedFound = true
		}
	}
	assert.True(t, failedFound)
}

func TestPlanExecutor_Execute_ContextCancel_Immediate(t *testing.T) {
	e := &PlanExecutor{}
	session := &AgentSession{
		SessionID: "s1",
		Status:    SessionGeneratingTree,
		Plan:      &PlanState{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan PlanExecutionEvent, 10)
	e.Execute(ctx, session, out)
	found := false
	for ev := range out {
		if ev.Type == EventStepFailed && ev.Message == "execution cancelled" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestEnsurePlanStateMaps_Nil(t *testing.T) {
	ensurePlanStateMaps(nil) // should not panic
}

func TestPlanExecutor_Execute_Replan(t *testing.T) {

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	e := &PlanExecutor{
		llmClient:        lc,
		maxReplans:       1,
		maxParallelLanes: 1,
		policyConfig:     policy.PolicyConfig{AllowLowRiskAutoRun: true},
	}
	session := &AgentSession{
		SessionID: "s1",
		Status:    SessionExecutingPlan,
		Budget:    policy.BudgetState{MaxToolCalls: 10, MaxCostCents: 1000},
		Plan: &PlanState{
			PlanID:          "p1",
			Steps:           []PlanStep{{ID: "s1", Action: "search", ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow}},
			ApprovedStepIDs: make(map[string]bool),
		},
	}

	e.pythonExecute = func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
		return map[string]any{"success": true}, nil
	}

	// First iteration: CoordinateAgentFeedback returns REPLAN
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return strings.Contains(req.Prompt, "Mediate between agent outcomes")
	})).Return(&llmv1.GenerateResponse{Text: "REPLAN because we need more data"}, nil).Once()

	// Second iteration: allStepsTerminal will be true after replan if we don't add more steps
	// but the code adds a step in applyAutomaticReplan.
	// So we need to mock CoordinateAgentFeedback again for the second iteration to CONTINUE
	msc.On("Generate", mock.Anything, mock.Anything).Return(&llmv1.GenerateResponse{Text: "CONTINUE"}, nil).Maybe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan PlanExecutionEvent, 20)
	go e.Execute(ctx, session, out)

	replanFound := false
	for ev := range out {
		if ev.Type == EventPlanRevised {
			replanFound = true
			cancel() // Stop the executor
			break
		}
	}
	assert.True(t, replanFound)
	assert.Equal(t, 1, session.Plan.ReplanCount)
}

func TestPlanExecutor_Execute_AutomaticReplanDoesNotPauseForInternalCoordination(t *testing.T) {

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)

	e := &PlanExecutor{
		llmClient:        lc,
		maxReplans:       1,
		maxParallelLanes: 1,
		policyConfig: policy.PolicyConfig{
			AllowLowRiskAutoRun:          true,
			RequireConfirmationForMedium: true,
		},
		pythonExecute: func(ctx context.Context, action string, payload map[string]any, sess *AgentSession) (map[string]any, error) {
			return map[string]any{"success": true}, nil
		},
	}
	session := &AgentSession{
		SessionID: "s1",
		Status:    SessionExecutingPlan,
		Budget:    policy.BudgetState{MaxToolCalls: 10, MaxCostCents: 1000},
		Plan: &PlanState{
			PlanID:          "p1",
			Steps:           []PlanStep{{ID: "s1", Action: "search", ExecutionTarget: ExecutionTargetPythonCapability, Risk: RiskLevelLow}},
			ApprovedStepIDs: make(map[string]bool),
		},
	}

	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req != nil && strings.Contains(req.Prompt, "Mediate between agent outcomes")
	})).Return(&llmv1.GenerateResponse{Text: "REPLAN because we need a recovery step"}, nil).Once()
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req != nil && strings.Contains(req.Prompt, "Mediate between agent outcomes")
	})).Return(&llmv1.GenerateResponse{Text: "CONTINUE"}, nil).Maybe()

	out := make(chan PlanExecutionEvent, 32)
	go e.Execute(context.Background(), session, out)

	confirmationFound := false
	completedFound := false
	for ev := range out {
		if ev.Type == EventConfirmationNeed {
			confirmationFound = true
		}
		if ev.Type == EventCompleted {
			completedFound = true
		}
	}

	assert.False(t, confirmationFound)
	assert.True(t, completedFound)
	assert.Equal(t, SessionComplete, session.Status)
	assert.True(t, session.Plan.CompletedStepIDs["coordinator_replan_1"])
}
