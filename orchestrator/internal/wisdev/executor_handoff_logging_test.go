package wisdev

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildStepHandoffLogArgsNormalizesCommonFields(t *testing.T) {
	session := &AgentSession{
		SessionID:      "session-1",
		Query:          "   ",
		CorrectedQuery: "graph retrieval corrected",
		Plan:           &PlanState{PlanID: "plan-1"},
	}
	step := PlanStep{
		ID:              "step-1",
		Action:          "search.academic",
		ExecutionTarget: ExecutionTargetGoNative,
	}
	ownership := stepExecutionOwnership{
		Owner:              "wisdev-worker",
		SubAgent:           "methodologist",
		OwningComponent:    "go_orchestrator",
		ResultOrigin:       "research_worker",
		ResultFusionIntent: "result_fusion",
	}

	args := buildStepHandoffLogArgs(
		"agent_query_handoff_complete",
		session,
		step,
		ownership,
		true,
		"result",
		"success",
		"result_count",
		3,
	)
	fields := logArgsToFieldMap(args)

	assert.Equal(t, "go_orchestrator", fields["service"])
	assert.Equal(t, "go", fields["runtime"])
	assert.Equal(t, "wisdev.executor", fields["component"])
	assert.Equal(t, "plan_step_handoff", fields["operation"])
	assert.Equal(t, "agent_query_handoff_complete", fields["stage"])
	assert.Equal(t, "session-1", fields["session_id"])
	assert.Equal(t, "plan-1", fields["plan_id"])
	assert.Equal(t, "step-1", fields["step_id"])
	assert.Equal(t, "search.academic", fields["action"])
	assert.Equal(t, string(ExecutionTargetGoNative), fields["execution_target"])
	assert.Equal(t, "graph retrieval corrected", fields["query_preview"])
	assert.Equal(t, len("graph retrieval corrected"), fields["query_length"])
	assert.Equal(t, true, fields["degraded"])
	assert.Equal(t, true, fields["delegated"])
	assert.Equal(t, "wisdev-worker", fields["owner"])
	assert.Equal(t, "methodologist", fields["sub_agent"])
	assert.Equal(t, "go_orchestrator", fields["owning_component"])
	assert.Equal(t, "research_worker", fields["result_origin"])
	assert.Equal(t, "result_fusion", fields["fusion_intent"])
	assert.Equal(t, "success", fields["result"])
	assert.Equal(t, 3, fields["result_count"])
}

func TestBuildStepHandoffLogArgsDropsMalformedAttrs(t *testing.T) {
	args := buildStepHandoffLogArgs(
		"",
		nil,
		PlanStep{},
		stepExecutionOwnership{},
		false,
		nil,
		"discarded",
		"",
		"blank-key",
		"valid_key",
		"valid-value",
		"dangling_key",
	)
	fields := logArgsToFieldMap(args)

	assert.Equal(t, 0, len(args)%2)
	assert.Equal(t, "unspecified", fields["stage"])
	assert.Equal(t, "", fields["query_preview"])
	assert.Equal(t, 0, fields["query_length"])
	assert.Equal(t, 3, fields["dropped_malformed_attr_count"])
	assert.Equal(t, "valid-value", fields["valid_key"])
	_, hasDangling := fields["dangling_key"]
	assert.False(t, hasDangling)
}

func logArgsToFieldMap(args []any) map[string]any {
	fields := make(map[string]any, len(args)/2)
	for idx := 0; idx+1 < len(args); idx += 2 {
		key, ok := args[idx].(string)
		if !ok {
			continue
		}
		fields[key] = args[idx+1]
	}
	return fields
}
