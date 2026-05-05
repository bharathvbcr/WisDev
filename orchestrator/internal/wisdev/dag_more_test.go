package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResearchDAG_Validate_EdgeCases(t *testing.T) {
	is := assert.New(t)
	executor := func(ctx context.Context, tctx TaskContext) TaskResult {
		return TaskResult{Status: TaskCompleted}
	}
	session := &AgentSession{
		Plan: &PlanState{
			Steps: []PlanStep{{ID: "root"}},
		},
	}

	t.Run("exceeds max width", func(t *testing.T) {
		dag := NewResearchDAG(session, nil, executor)
		dag.maxWidth = 1
		dag.steps["s1"] = PlanStep{ID: "s1"}
		dag.steps["s2"] = PlanStep{ID: "s2"}

		err := dag.Validate()
		is.Error(err)
		is.Contains(err.Error(), "exceeds safety limit of 1")
	})

	t.Run("exceeds max depth", func(t *testing.T) {
		dag := NewResearchDAG(session, nil, executor)
		dag.maxDepth = 1
		dag.steps["s1"] = PlanStep{ID: "s1"}
		dag.steps["s2"] = PlanStep{ID: "s2", DependsOnStepIDs: []string{"s1"}}

		err := dag.Validate()
		is.Error(err)
		is.Contains(err.Error(), "exceeds safety limit of 1")
	})

	t.Run("cycle detection", func(t *testing.T) {
		dag := NewResearchDAG(session, nil, executor)
		dag.maxDepth = 5
		dag.steps["s1"] = PlanStep{ID: "s1", DependsOnStepIDs: []string{"s2"}}
		dag.steps["s2"] = PlanStep{ID: "s2", DependsOnStepIDs: []string{"s1"}}

		err := dag.Validate()
		is.Error(err)
		is.Contains(err.Error(), "exceeds safety limit of 5") // cycle returns depth 1000
	})
}
