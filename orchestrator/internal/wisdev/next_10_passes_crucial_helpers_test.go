package wisdev

import (
	"context"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNext10PassesCrucialWisdevHelpers(t *testing.T) {
	t.Run("answer status maps budget gate and stop reason states", func(t *testing.T) {
		exhausted := &ResearchSessionState{DurableJob: &ResearchDurableJobState{BudgetUsed: ResearchBudgetUsage{Exhausted: true}}}
		assert.Equal(t, "budget_exhausted", ResearchAnswerStatusFromState(exhausted, nil, false, ""))
		assert.Equal(t, "verified", ResearchAnswerStatusFromState(nil, &ResearchFinalizationGate{Ready: true}, false, ""))
		assert.Equal(t, "verified", ResearchAnswerStatusFromState(nil, &ResearchFinalizationGate{}, true, ""))
		assert.Equal(t, "blocked", ResearchAnswerStatusFromState(nil, &ResearchFinalizationGate{Status: "blocked"}, false, ""))
		assert.Equal(t, "provisional", ResearchAnswerStatusFromState(nil, &ResearchFinalizationGate{Provisional: true}, false, ""))
		assert.Equal(t, "custom", ResearchAnswerStatusFromState(nil, &ResearchFinalizationGate{Status: "custom"}, false, ""))
		assert.Equal(t, "verified", ResearchAnswerStatusFromState(nil, nil, false, "verified_final"))
		assert.Equal(t, "budget_exhausted", ResearchAnswerStatusFromState(nil, nil, false, "budget_exhausted_with_open_gaps"))
		assert.Equal(t, "provisional", ResearchAnswerStatusFromState(nil, nil, false, ""))
		assert.Equal(t, "provisional", ResearchAnswerStatusFromState(nil, nil, false, "no_grounded_sources"))
	})

	t.Run("belief merge combines confidence and evidence without aliasing incoming state", func(t *testing.T) {
		dst := NewBeliefState()
		dst.AddBelief(&Belief{ID: "b1", Claim: "claim", Confidence: 0.4, SupportingEvidence: []string{"s1"}})
		src := NewBeliefState()
		src.AddBelief(&Belief{ID: "b1", Claim: "claim", Confidence: 0.9, SupportingEvidence: []string{"s2"}, ContradictingEvidence: []string{"c1"}, ProvenanceChain: []ProvenanceEntry{{Description: "p1"}}})
		src.AddBelief(&Belief{ID: "b2", Claim: "new", Confidence: 0.7, SupportingEvidence: []string{"s3"}, SourceFamilies: []string{"pubmed"}})

		mergeBeliefState(dst, src)
		assert.Equal(t, 0.9, dst.Beliefs["b1"].Confidence)
		assert.Equal(t, []string{"s1", "s2"}, dst.Beliefs["b1"].SupportingEvidence)
		assert.Equal(t, []string{"c1"}, dst.Beliefs["b1"].ContradictingEvidence)
		assert.Len(t, dst.Beliefs["b1"].ProvenanceChain, 1)
		require.NotNil(t, dst.Beliefs["b2"])
		src.Beliefs["b2"].SupportingEvidence[0] = "mutated"
		assert.Equal(t, []string{"s3"}, dst.Beliefs["b2"].SupportingEvidence)
		assert.NotPanics(t, func() { mergeBeliefState(nil, src); mergeBeliefState(dst, nil) })
	})

	t.Run("decision budget checks cover tool script and cost limits", func(t *testing.T) {
		assert.True(t, decisionCandidateExceedsBudget(PlanStep{}, policy.BudgetState{MaxToolCalls: 1, ToolCallsUsed: 1}))
		assert.True(t, decisionCandidateExceedsBudget(PlanStep{ExecutionTarget: ExecutionTargetPythonSandbox}, policy.BudgetState{MaxScriptRuns: 1, ScriptRunsUsed: 1}))
		assert.True(t, decisionCandidateExceedsBudget(PlanStep{EstimatedCostCents: 6}, policy.BudgetState{MaxCostCents: 10, CostCentsUsed: 5}))
		assert.False(t, decisionCandidateExceedsBudget(PlanStep{EstimatedCostCents: 4}, policy.BudgetState{MaxCostCents: 10, CostCentsUsed: 5}))
	})

	t.Run("gap critique helpers format prompt text and reopen coverage", func(t *testing.T) {
		assert.False(t, loopGapNeedsFollowUp(nil))
		assert.True(t, loopGapNeedsFollowUp(&LoopGapState{}))
		assert.True(t, loopGapNeedsFollowUp(&LoopGapState{Sufficient: true, MissingAspects: []string{"replication"}}))
		assert.False(t, loopGapNeedsFollowUp(&LoopGapState{Sufficient: true}))

		assert.Equal(t, "- No coverage ledger entries were recorded.", formatCoverageLedgerForPrompt(nil, 2))
		formatted := formatCoverageLedgerForPrompt([]CoverageLedgerEntry{{Category: "coverage", Status: "open", Title: "Need source", Description: "Need source"}}, 1)
		assert.Contains(t, formatted, "[coverage/open] Need source :: Need source")
		assert.Equal(t, "abcdef...", clipPromptText(" abcdefgh ", 6))

		gap := mergeDraftCritiqueIntoGapState(&LoopGapState{Sufficient: true, Reasoning: "base", Confidence: 0.9}, &LoopDraftCritique{
			NeedsRevision:      true,
			RetrievalReopened:  false,
			Reasoning:          "more evidence",
			MissingAspects:     []string{"replication"},
			MissingSourceTypes: []string{"full_text"},
			Contradictions:     []string{"conflict"},
			NextQueries:        []string{"query"},
			Confidence:         0.4,
		}, "root")
		require.NotNil(t, gap)
		assert.False(t, gap.Sufficient)
		assert.Contains(t, gap.Reasoning, "Critique: more evidence")
		assert.Equal(t, []string{"replication"}, gap.MissingAspects)
		assert.Equal(t, 0.4, gap.Confidence)
		assert.NotEmpty(t, gap.Ledger)
	})

	t.Run("quest summaries choose claims and paper fallbacks", func(t *testing.T) {
		assert.Equal(t, "", summarizeQuestHypotheses(nil))
		assert.Equal(t, "claim one; text two; query three", summarizeQuestHypotheses([]Hypothesis{
			{Claim: "claim one"}, {Text: "text two"}, {Query: "query three"}, {Claim: "claim four"},
		}))
		assert.Equal(t, "2 accepted claims; 1 rejected branches", summarizeQuestReasoning([]EvidenceFinding{{}, {}}, []QuestBranchRecord{{}}))
		lines := questFindingLines([]EvidenceFinding{{Claim: "claim one", PaperTitle: "Paper A"}, {Claim: ""}}, []Source{{Title: "fallback paper"}}, 3)
		assert.Equal(t, []string{"claim one (Paper A)", "fallback paper"}, lines)
		assert.Equal(t, []string{"fallback paper"}, questFindingLines(nil, []Source{{Title: "fallback paper"}}, 1))
	})

	t.Run("worker budgets and coverage ledgers cover role-specific branches", func(t *testing.T) {
		budgets := assignResearchWorkerSearchBudgets([]ResearchWorkerRole{
			ResearchWorkerSourceDiversifier,
			ResearchWorkerCitationGraph,
			ResearchWorkerScout,
		}, 5)
		assert.NotEmpty(t, budgets)
		assert.Empty(t, assignResearchWorkerSearchBudgets([]ResearchWorkerRole{ResearchWorkerScout}, 0))

		assert.Nil(t, buildWorkerCoverageLedger(researchWorkerExecution{Status: "running"}))
		synth := buildWorkerCoverageLedger(researchWorkerExecution{Status: "completed", Role: ResearchWorkerSynthesizer, Queries: []string{"q"}})
		require.Len(t, synth, 1)
		assert.Equal(t, "worker_synthesizer", synth[0].Category)
		verifier := buildWorkerCoverageLedger(researchWorkerExecution{Status: "completed", Role: ResearchWorkerIndependentVerifier, Queries: []string{"q"}})
		require.Len(t, verifier, 1)
		assert.Equal(t, "unverified_claim", verifier[0].ObligationType)
	})

	t.Run("execution helpers normalize nil contexts, result maps, and text snippets", func(t *testing.T) {
		assert.NotNil(t, executorContext(nil))
		ctx := context.WithValue(context.Background(), "k", "v")
		assert.Equal(t, ctx, executorContext(ctx))
		assert.NotNil(t, durableExecutionRootContext(nil))
		assert.Equal(t, map[string]any{}, ensureExecutionResultMap(nil))
		existing := map[string]any{"x": 1}
		ensured := ensureExecutionResultMap(existing)
		ensured["y"] = 2
		assert.Equal(t, 2, existing["y"])
		assert.Equal(t, "answer", firstOutcomeText(map[string]any{"a": " ", "b": "answer"}, "a", "b"))
		assert.Equal(t, "", firstOutcomeText(map[string]any{"a": "<nil>"}, "a"))
		assert.Equal(t, "one two...", truncateOutcomeText(" one   two three ", 7))
		assert.Len(t, truncateExecutionQueryPreview("x"+string(make([]byte, 130))), 120)
	})

	t.Run("citation graph coverage ledger flags empty and edgeless graphs", func(t *testing.T) {
		assert.Nil(t, buildCitationGraphCoverageLedger("root", nil))
		empty := buildCitationGraphCoverageLedger("root", &ResearchCitationGraph{})
		require.Len(t, empty, 1)
		assert.Equal(t, coverageLedgerStatusOpen, empty[0].Status)
		assert.Equal(t, "missing_citation_identity", empty[0].ObligationType)

		edgeless := buildCitationGraphCoverageLedger("root", &ResearchCitationGraph{Nodes: []ResearchCitationGraphNode{{ID: "n1"}}, ForwardQueries: []string{"forward"}})
		require.Len(t, edgeless, 1)
		assert.Equal(t, "missing_source_diversity", edgeless[0].ObligationType)
		assert.Equal(t, []string{"forward"}, edgeless[0].SupportingQueries)

		resolved := buildCitationGraphCoverageLedger("root", &ResearchCitationGraph{Nodes: []ResearchCitationGraphNode{{ID: "n1"}}, Edges: []ResearchCitationGraphEdge{{SourceID: "n1", TargetID: "n2"}}})
		require.Len(t, resolved, 1)
		assert.Equal(t, coverageLedgerStatusResolved, resolved[0].Status)
	})

	t.Run("ADK plugin lookup and subagent mapping cover nil and fallback branches", func(t *testing.T) {
		cfg, ok := (*ADKRuntime)(nil).PluginForAction(ActionResearchRetrievePapers)
		assert.False(t, ok)
		assert.Empty(t, cfg.Name)
		runtime := &ADKRuntime{toolToPlug: map[string]ADKPluginConfig{CanonicalizeWisdevAction(ActionResearchRetrievePapers): {Name: "go-native-tools"}}}
		cfg, ok = runtime.PluginForAction(ActionResearchRetrievePapers)
		require.True(t, ok)
		assert.Equal(t, "go-native-tools", cfg.Name)
		assert.Equal(t, "wisdev-reasoning", adkSubAgentForPlugin("go-native-tools"))
		assert.Equal(t, "python-researcher", adkSubAgentForPlugin("python-sandbox-tools"))
		assert.Equal(t, "", adkSubAgentForPlugin("unknown"))
	})

	t.Run("answer status wrapper and session id helpers cover passthrough branches", func(t *testing.T) {
		assert.Equal(t, "verified", researchAnswerStatus(nil, nil, false, "verified_final"))
		assert.Equal(t, "", safeAgentSessionID(nil))
		assert.Equal(t, "session", safeAgentSessionID(&AgentSession{SessionID: " session "}))
	})
}
