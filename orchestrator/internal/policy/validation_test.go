package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEvaluateGuardrailWithHintsUsesGoHeuristic(t *testing.T) {
	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)

	got := EvaluateGuardrailWithHints(cfg, budget, RiskMedium, false, 0, PolicyHints{
		ExecutionMode: "yolo",
	})

	assert.True(t, got.Allowed)
	assert.True(t, got.RequiresConfirmation)
	assert.Equal(t, "medium_risk_confirmation_required", got.Reason)
}

func TestValidateSandboxSnippet(t *testing.T) {
	assert.Equal(t, SandboxValidationResult{Valid: false, Reason: "empty_snippet"}, ValidateSandboxSnippet(" "))
	assert.Equal(t, SandboxValidationResult{Valid: true, Reason: "heuristic_allow"}, ValidateSandboxSnippet("print(1)"))

	got := ValidateSandboxSnippet("__import__('os')")
	assert.False(t, got.Valid)
	assert.Equal(t, "disallowed_token:__import__", got.Reason)
}

func TestValidateStructuredOutput(t *testing.T) {
	assert.Equal(t, StructuredValidationResult{Valid: false, Reason: "missing_schema_type"}, ValidateStructuredOutput("", nil))

	claim := ValidateStructuredOutput("claim_table", map[string]any{"claims": []any{"c1"}})
	assert.True(t, claim.Valid)
	assert.Equal(t, "heuristic_allow", claim.Reason)

	missingClaims := ValidateStructuredOutput("claim_evidence", map[string]any{"claims": []any{}})
	assert.False(t, missingClaims.Valid)
	assert.Equal(t, "missing_claims", missingClaims.Reason)

	missingPRISMA := ValidateStructuredOutput("prisma", map[string]any{})
	assert.False(t, missingPRISMA.Valid)
	assert.Equal(t, "missing_identified", missingPRISMA.Reason)

	rebuttal := ValidateStructuredOutput("rebuttal", map[string]any{"summary": "handled"})
	assert.True(t, rebuttal.Valid)

	reasoning := ValidateStructuredOutput("reasoning_paths", map[string]any{"branches": []any{
		map[string]any{"supportScore": 0.7},
	}})
	assert.True(t, reasoning.Valid)

	unsupported := ValidateStructuredOutput("unknown", map[string]any{})
	assert.False(t, unsupported.Valid)
	assert.Equal(t, "unsupported_schema_type", unsupported.Reason)
}
