package wisdev

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextPass2CrucialWisdevHelpers(t *testing.T) {
	t.Run("DAG executes dependencies and exposes terminal state", func(t *testing.T) {
		session := &AgentSession{
			SessionID:          "session-dag",
			ResearchScratchpad: map[string]string{"note": "kept"},
			ThoughtSignature:   "thought-1",
			Plan: &PlanState{Steps: []PlanStep{
				{ID: "collect", Action: "search"},
				{ID: "synthesize", Action: "synthesize", DependsOnStepIDs: []string{"collect"}},
			}},
		}
		var executed []string
		dag := NewResearchDAG(session, nil, func(ctx context.Context, tctx TaskContext) TaskResult {
			executed = append(executed, tctx.Step.ID)
			if tctx.Step.ID == "synthesize" {
				assert.Contains(t, tctx.UpstreamResults, "collect")
			}
			assert.Equal(t, session.ResearchScratchpad, tctx.Scratchpad)
			assert.Equal(t, "thought-1", tctx.ThoughtSignature)
			return TaskResult{ID: tctx.Step.ID, Status: TaskCompleted, Result: map[string]any{"ok": true}, Duration: time.Millisecond}
		})

		require.NoError(t, dag.Execute(t.Context()))
		assert.Equal(t, []string{"collect", "synthesize"}, executed)
		assert.True(t, dag.allTerminal())
		assert.Equal(t, TaskCompleted, dag.status["collect"])
		assert.Equal(t, TaskCompleted, dag.status["synthesize"])
		assert.Contains(t, dag.GetResults(), "collect")
	})

	t.Run("DAG terminal helper rejects pending and running tasks", func(t *testing.T) {
		dag := &ResearchDAG{status: map[string]TaskStatus{
			"done":    TaskCompleted,
			"failed":  TaskFailed,
			"skipped": TaskSkipped,
		}}
		assert.True(t, dag.allTerminal())

		dag.status["pending"] = TaskPending
		assert.False(t, dag.allTerminal())

		dag.status["pending"] = TaskRunning
		assert.False(t, dag.allTerminal())
	})

	t.Run("steering signals mutate pending queries and belief verdicts", func(t *testing.T) {
		loop := &AutonomousLoop{}
		pending := []string{"old query", "drop this query"}

		loop.applySteeringSignal(t.Context(), SteeringSignal{Type: "redirect", Queries: []string{" new query ", " "}}, &pending, nil, nil)
		assert.Equal(t, []string{"new query"}, pending)

		loop.applySteeringSignal(t.Context(), SteeringSignal{Type: "focus", Queries: []string{"done query", " fresh focus "}}, &pending, nil, []string{"done query"})
		assert.Equal(t, []string{"fresh focus", "new query"}, pending)

		pending = append(pending, "remove molecular drift")
		loop.applySteeringSignal(t.Context(), SteeringSignal{Type: "exclude", Queries: []string{"molecular"}}, &pending, nil, nil)
		assert.Equal(t, []string{"fresh focus", "new query"}, pending)

		state := NewBeliefState()
		state.AddBelief(&Belief{ID: "b1", Claim: "Aspirin reduces platelet aggregation", Confidence: 0.4, Status: BeliefStatusRefuted})
		state.AddBelief(&Belief{ID: "b2", Claim: "Ibuprofen improves sleep", Confidence: 0.8, Status: BeliefStatusActive})

		loop.applySteeringSignal(t.Context(), SteeringSignal{Type: "approve", Payload: "platelet"}, &pending, state, nil)
		assert.Equal(t, 1.0, state.Beliefs["b1"].Confidence)
		assert.Equal(t, BeliefStatusActive, state.Beliefs["b1"].Status)
		assert.True(t, state.Beliefs["b1"].Triangulated)

		loop.applySteeringSignal(t.Context(), SteeringSignal{Type: "reject", Payload: "sleep"}, &pending, state, nil)
		assert.Equal(t, 0.0, state.Beliefs["b2"].Confidence)
		assert.Equal(t, BeliefStatusRefuted, state.Beliefs["b2"].Status)

		before := append([]string(nil), pending...)
		loop.applySteeringSignal(t.Context(), SteeringSignal{}, &pending, state, nil)
		assert.Equal(t, before, pending)
	})

	t.Run("branch plan lookup is trimmed and case insensitive", func(t *testing.T) {
		plans := []ResearchBranchPlan{
			{ID: "p1", Query: "neural reranking reproducibility"},
			{ID: "p2", Query: "citation integrity"},
		}

		assert.Nil(t, findResearchBranchPlanByQuery(plans, " "))
		assert.Nil(t, findResearchBranchPlanByQuery(plans, "missing"))
		got := findResearchBranchPlanByQuery(plans, " CITATION INTEGRITY ")
		require.IsType(t, ResearchBranchPlan{}, got)
		assert.Equal(t, "p2", got.(ResearchBranchPlan).ID)
	})

	t.Run("research hardening helpers normalize open ledgers and structure snippets", func(t *testing.T) {
		assert.False(t, questHasOpenCoverageLedger(nil))
		assert.False(t, questHasOpenCoverageLedger([]CoverageLedgerEntry{{Status: coverageLedgerStatusResolved}}))
		assert.True(t, questHasOpenCoverageLedger([]CoverageLedgerEntry{{Status: coverageLedgerStatusOpen}}))

		assert.Equal(t, "", optionalAnyString(nil))
		assert.Equal(t, "", optionalAnyString("   "))
		assert.Equal(t, "42", optionalAnyString(42))
		assert.Equal(t, "caption value", optionalAnyString(" caption value "))

		snippets := extractStructureMapSnippets([]any{
			"bad-shape",
			map[string]any{"summary": "   "},
			map[string]any{"caption": " Figure 1 shows the treatment effect. "},
			map[string]any{"text": "Figure 1 shows the treatment effect."},
			map[string]any{"label": "Backup label"},
		}, 2)
		assert.Equal(t, []string{"Figure 1 shows the treatment effect.", "Backup label"}, snippets)
		assert.Equal(t, []string{"Backup label"}, extractStructureMapSnippets([]any{map[string]any{"label": "Backup label"}}, 0))
	})
}
