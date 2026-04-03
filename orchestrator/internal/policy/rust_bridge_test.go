package policy

import "testing"

func TestValidateSandboxSnippet_HeuristicBlock(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "false")
	result := ValidateSandboxSnippet("import os\nos.system('rm -rf /')")
	if result.Valid {
		t.Fatalf("expected sandbox validation failure for disallowed token")
	}
}

func TestValidateStructuredOutput_ClaimTable(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "false")
	okResult := ValidateStructuredOutput("claim_table", map[string]any{
		"claims": []any{map[string]any{"claim": "x"}},
	})
	if !okResult.Valid {
		t.Fatalf("expected valid claim_table payload, got reason=%s", okResult.Reason)
	}

	badResult := ValidateStructuredOutput("claim_table", map[string]any{})
	if badResult.Valid {
		t.Fatalf("expected invalid claim_table payload")
	}
}
