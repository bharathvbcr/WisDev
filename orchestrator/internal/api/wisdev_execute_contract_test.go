package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeExecutionPayload(t *testing.T) {
	t.Run("canonicalizes action and metadata fields", func(t *testing.T) {
		normalized := normalizeExecutionPayload(
			map[string]any{
				"applied":              false,
				"requiresConfirmation": true,
				"nextActions":          []any{"approve", "reject_replan", "approve"},
				"data": map[string]any{
					"providers": []any{"openalex"},
					"runtimeToolCandidates": []any{
						map[string]any{"name": "research.webSearch"},
						"research.openAlex",
					},
					"structuredValidation": map[string]any{
						"valid":      true,
						"schemaType": "claim_table",
						"reason":     "ok",
					},
				},
			},
			map[string]any{
				"providers":               []any{"crossref", "openalex"},
				"sourceMix":               []any{"academic"},
				"toolInvocationPolicy":    "Always_Ask",
				"stepId":                  "step-1",
				"selectedParallelStepIds": []any{"step-1", "step-2"},
			},
			map[string]any{
				"sourcePreferences":       []any{"semantic_scholar", "crossref"},
				"selectedParallelStepIds": []any{"step-2", "step-3"},
			},
		)

		assert.Equal(t, []string{"approve", "reject_and_replan"}, normalized["nextActions"])
		assert.Equal(t, "action_required", normalized["guardrailReason"])

		data := normalized["data"].(map[string]any)
		assert.Equal(t, "go", data["controlPlane"])
		assert.Equal(t, []string{"openalex", "crossref", "semantic_scholar"}, data["providers"])
		assert.Equal(t, []string{"academic"}, data["sourceMix"])
		assert.Equal(t, "always_ask", data["toolInvocationPolicy"])
		assert.Equal(t, "step-1", data["stepId"])
		assert.Equal(t, []string{"step-1", "step-2", "step-3"}, data["selectedParallelStepIds"])
		assert.Equal(t, []string{"research.webSearch", "research.openAlex"}, data["runtimeToolCandidateNames"])
		assert.Equal(t, true, data["structuredValidationValid"])
		assert.Equal(t, "claim_table", data["structuredValidationSchemaType"])
		assert.Equal(t, "ok", data["structuredValidationReason"])
	})
}

func TestNormalizeExecuteCapabilityRequest(t *testing.T) {
	normalized := normalizeExecuteCapabilityRequest(executeCapabilityRequest{
		SessionID:     " s-1 ",
		Action:        " research.buildClaimEvidenceTable ",
		StepAction:    "confirm_and_execute",
		EditedPayload: map[string]any{"query": "edited"},
		Payload: map[string]any{
			"toolInvocationPolicy":    "Always_Ask",
			"selectedParallelStepIds": []any{"step-2"},
		},
		Context: map[string]any{
			"currentStepId":           "step-1",
			"selectedParallelStepIds": []any{"step-3"},
		},
	})

	assert.Equal(t, "s-1", normalized.SessionID)
	assert.Equal(t, "research.buildClaimEvidenceTable", normalized.Action)
	assert.Equal(t, "approve", normalized.StepAction)
	assert.Equal(t, "step-1", normalized.StepID)
	assert.Equal(t, "approve", normalized.Payload["stepAction"])
	assert.Equal(t, "always_ask", normalized.Payload["toolInvocationPolicy"])
	assert.Equal(t, true, normalized.Payload["enforceTieredControl"])
	assert.Equal(t, "step-1", normalized.Payload["stepId"])
	assert.Equal(t, "step-1", normalized.Context["currentStepId"])
	assert.Equal(t, []string{"step-2", "step-3", "step-1"}, normalized.Payload["selectedParallelStepIds"])
	assert.Equal(t, []string{"step-2", "step-3", "step-1"}, normalized.Context["selectedParallelStepIds"])
	assert.Equal(t, map[string]any{"query": "edited"}, normalized.Payload["editedPayload"])
}
