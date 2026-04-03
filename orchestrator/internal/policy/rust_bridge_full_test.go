package policy

import (
	"os"
	"testing"
)

func TestEvaluateGuardrailViaRust_Fallbacks(t *testing.T) {
	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)

	// Case 1: Bridge disabled, Rust NOT required -> should return false (fall back to heuristic)
	os.Unsetenv("WISDEV_RUST_BRIDGE_BIN")
	os.Setenv("WISDEV_RUST_REQUIRED", "false")
	_, ok := evaluateGuardrailViaRust(cfg, budget, RiskLow, false, 0)
	if ok {
		t.Error("Should not have used Rust when disabled and not required")
	}

	// Case 2: Bridge disabled, Rust IS required -> should return true with error reason
	os.Setenv("WISDEV_RUST_REQUIRED", "true")
	dec, ok := evaluateGuardrailViaRust(cfg, budget, RiskLow, false, 0)
	if !ok || dec.Allowed || dec.Reason != "rust_policy_unavailable" {
		t.Errorf("Expected blocked by rust_policy_unavailable, got %v, %v", dec, ok)
	}
}

func TestValidateSandboxSnippet_RustRequired(t *testing.T) {
	os.Unsetenv("WISDEV_RUST_BRIDGE_BIN")
	os.Setenv("WISDEV_RUST_REQUIRED", "true")
	defer os.Unsetenv("WISDEV_RUST_REQUIRED")

	res := ValidateSandboxSnippet("print(1)")
	if res.Valid || res.Reason != "rust_sandbox_unavailable" {
		t.Errorf("Expected rust_sandbox_unavailable when Rust is required, got %v", res)
	}
}

func TestValidateStructuredOutput_RustRequired(t *testing.T) {
	os.Unsetenv("WISDEV_RUST_BRIDGE_BIN")
	os.Setenv("WISDEV_RUST_REQUIRED", "true")

	res := ValidateStructuredOutput("prisma", nil)
	if res.Valid || res.Reason != "rust_structured_validator_unavailable" {
		t.Errorf("Expected rust_structured_validator_unavailable, got %v", res)
	}
}
