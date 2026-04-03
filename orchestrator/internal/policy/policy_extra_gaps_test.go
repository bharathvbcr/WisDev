package policy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ==========================================
// RustRequired — additional branches (75.0%)
// ==========================================

// TestGaps_RustRequired_TrueValues covers the "1", "true", "yes" branches.
func TestGaps_RustRequired_TrueValues(t *testing.T) {
	for _, val := range []string{"1", "true", "yes", "TRUE", "YES"} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("WISDEV_RUST_REQUIRED", val)
			if !RustRequired() {
				t.Errorf("RustRequired() = false for %q, want true", val)
			}
		})
	}
}

// TestGaps_RustRequired_FalseValues covers "false", "0", "no" paths.
func TestGaps_RustRequired_FalseValues(t *testing.T) {
	for _, val := range []string{"false", "0", "no", "FALSE", "NO"} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("WISDEV_RUST_REQUIRED", val)
			if RustRequired() {
				t.Errorf("RustRequired() = true for %q, want false", val)
			}
		})
	}
}

// TestGaps_RustRequired_EmptyDefaultsTrue covers the empty string → true path.
func TestGaps_RustRequired_EmptyDefaultsTrue(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "")
	if !RustRequired() {
		t.Error("RustRequired() = false for empty env, want true (default)")
	}
}

// ==========================================
// evaluateGuardrailHeuristic — missing branches (81.2%)
// ==========================================

// TestGaps_Heuristic_HighRiskNoConfirmation covers AlwaysConfirmHighRisk=false path.
func TestGaps_Heuristic_HighRiskNoConfirmation(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.AlwaysConfirmHighRisk = false
	budget := NewBudgetState(cfg)
	got := evaluateGuardrailHeuristic(cfg, budget, RiskHigh, false, 0)
	if !got.Allowed {
		t.Error("expected Allowed=true for high risk when AlwaysConfirmHighRisk=false")
	}
	if got.RequiresConfirmation {
		t.Error("expected RequiresConfirmation=false when AlwaysConfirmHighRisk=false")
	}
	if got.Reason != "high_risk_allowed" {
		t.Errorf("expected reason=high_risk_allowed, got %q", got.Reason)
	}
}

// TestGaps_Heuristic_MediumRiskNoConfirmation covers RequireConfirmationForMedium=false path.
func TestGaps_Heuristic_MediumRiskNoConfirmation(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.RequireConfirmationForMedium = false
	budget := NewBudgetState(cfg)
	got := evaluateGuardrailHeuristic(cfg, budget, RiskMedium, false, 0)
	if !got.Allowed {
		t.Error("expected Allowed=true for medium risk")
	}
	if got.RequiresConfirmation {
		t.Error("expected RequiresConfirmation=false when RequireConfirmationForMedium=false")
	}
	if got.Reason != "medium_risk_allowed" {
		t.Errorf("expected reason=medium_risk_allowed, got %q", got.Reason)
	}
}

// TestGaps_Heuristic_LowRiskManualOnly covers AllowLowRiskAutoRun=false path.
func TestGaps_Heuristic_LowRiskManualOnly(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.AllowLowRiskAutoRun = false
	budget := NewBudgetState(cfg)
	got := evaluateGuardrailHeuristic(cfg, budget, RiskLow, false, 0)
	if !got.Allowed {
		t.Error("expected Allowed=true for low risk even with manual-only mode")
	}
	if !got.RequiresConfirmation {
		t.Error("expected RequiresConfirmation=true when AllowLowRiskAutoRun=false")
	}
	if got.Reason != "low_risk_manual_only" {
		t.Errorf("expected reason=low_risk_manual_only, got %q", got.Reason)
	}
}

// TestGaps_Heuristic_MediumRiskWithConfirmation covers RequireConfirmationForMedium=true path.
func TestGaps_Heuristic_MediumRiskWithConfirmation(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.RequireConfirmationForMedium = true
	budget := NewBudgetState(cfg)
	got := evaluateGuardrailHeuristic(cfg, budget, RiskMedium, false, 0)
	if !got.Allowed || !got.RequiresConfirmation {
		t.Errorf("expected Allowed=true, RequiresConfirmation=true; got %+v", got)
	}
}

// ==========================================
// EvaluateGuardrail — dispatcher paths (66.7%)
// ==========================================

// TestGaps_EvaluateGuardrail_RustRequiredDisabled covers the path where
// WISDEV_RUST_REQUIRED=true but bridge binary is missing → blocked.
func TestGaps_EvaluateGuardrail_RustRequiredUnavailable(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "true")
	// Point bridge at a server that always returns 503 so the HTTP call fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bridge unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("RUST_GATEWAY_INTERNAL_URL", srv.URL)

	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)

	// evaluateGuardrailViaRust should return (blocked, true) when required but bridge fails.
	got := EvaluateGuardrail(cfg, budget, RiskLow, false, 0)
	if got.Allowed {
		t.Errorf("expected Allowed=false when Rust is required but bridge fails; got Reason=%s", got.Reason)
	}
}

// TestGaps_EvaluateGuardrail_RustNotRequired_FallsThrough covers the path where
// WISDEV_RUST_REQUIRED=false so the heuristic is used regardless of bridge result.
func TestGaps_EvaluateGuardrail_RustNotRequired_FallsThrough(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "false")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bridge unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("RUST_GATEWAY_INTERNAL_URL", srv.URL)

	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)

	got := EvaluateGuardrail(cfg, budget, RiskLow, false, 0)
	if !got.Allowed {
		t.Errorf("expected heuristic to allow low risk; got Reason=%s", got.Reason)
	}
}

// ==========================================
// runRustBridgeCommand — additional branches
// ==========================================

// TestGaps_RunRustBridge_HTTPError covers the non-200 status path.
func TestGaps_RunRustBridge_HTTPError(t *testing.T) {
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bridge error message", http.StatusInternalServerError)
	})

	err := runRustBridgeCommand("policy-eval", map[string]string{}, nil)
	if err == nil {
		t.Error("expected error from HTTP 500")
	}
}

// TestGaps_RunRustBridge_NilOut covers the out==nil path (no JSON unmarshaling).
func TestGaps_RunRustBridge_NilOut(t *testing.T) {
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": nil})
	})

	// out=nil means don't unmarshal — should succeed without error.
	err := runRustBridgeCommand("policy-eval", map[string]string{}, nil)
	if err != nil {
		t.Errorf("unexpected error with nil out: %v", err)
	}
}

// ==========================================
// evaluateGuardrailViaRust — success & empty reason
// ==========================================

// TestGaps_EvaluateGuardrailViaRust_SuccessEmptyReason covers the success path
// with an empty reason string (triggers default reason fill-in).
func TestGaps_EvaluateGuardrailViaRust_SuccessEmptyReason(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "true")
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]any{
				"allowed":              true,
				"requiresConfirmation": false,
				"reason":               "",
			},
		})
	})

	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)
	got, ok := evaluateGuardrailViaRust(cfg, budget, RiskLow, false, 0)
	if !ok {
		t.Error("expected ok=true on successful Rust bridge call")
	}
	if !got.Allowed {
		t.Errorf("expected Allowed=true, got Reason=%s", got.Reason)
	}
	// Empty reason from Rust → filled with "rust_policy_default"
	if got.Reason != "rust_policy_default" {
		t.Errorf("expected reason=rust_policy_default for empty Rust reason, got %q", got.Reason)
	}
}

// TestGaps_EvaluateGuardrailViaRust_SuccessNonEmptyReason covers the path where
// Rust returns a non-empty reason (no fill-in).
func TestGaps_EvaluateGuardrailViaRust_SuccessNonEmptyReason(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "true")
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]any{
				"allowed":              true,
				"requiresConfirmation": true,
				"reason":               "policy_confirmed",
			},
		})
	})

	cfg := DefaultPolicyConfig()
	budget := NewBudgetState(cfg)
	got, ok := evaluateGuardrailViaRust(cfg, budget, RiskLow, false, 0)
	if !ok {
		t.Error("expected ok=true")
	}
	if got.Reason != "policy_confirmed" {
		t.Errorf("expected reason=policy_confirmed, got %q", got.Reason)
	}
}

// ==========================================
// ValidateSandboxSnippet — bridge-enabled paths
// ==========================================

// TestGaps_ValidateSandboxSnippet_BridgeEnabled_Success covers the bridge-enabled
// path where runRustBridgeCommand succeeds.
func TestGaps_ValidateSandboxSnippet_BridgeEnabled_Success(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "true")
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"data": map[string]any{"valid": true, "reason": "rust_allow"},
		})
	})

	got := ValidateSandboxSnippet("print('hello')")
	if !got.Valid {
		t.Errorf("expected Valid=true from bridge, got Reason=%s", got.Reason)
	}
}

// TestGaps_ValidateSandboxSnippet_BridgeEnabled_FailsRustRequired covers the path
// where the bridge is enabled but HTTP call fails AND RustRequired=true.
func TestGaps_ValidateSandboxSnippet_BridgeEnabled_FailsRustRequired(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "true")
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bridge down", http.StatusServiceUnavailable)
	})

	got := ValidateSandboxSnippet("some snippet")
	if got.Valid {
		t.Error("expected Valid=false when bridge fails and Rust is required")
	}
	if got.Reason != "rust_sandbox_unavailable" {
		t.Errorf("expected reason=rust_sandbox_unavailable, got %q", got.Reason)
	}
}

// TestGaps_ValidateSandboxSnippet_BridgeEnabled_FailsNotRequired covers the path
// where bridge fails AND RustRequired=false → heuristic fallback.
func TestGaps_ValidateSandboxSnippet_BridgeEnabled_FailsNotRequired(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "false")
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bridge down", http.StatusServiceUnavailable)
	})

	// A safe snippet → heuristic should allow it.
	got := ValidateSandboxSnippet("print('hello world')")
	if !got.Valid {
		t.Errorf("expected Valid=true via heuristic fallback, got Reason=%s", got.Reason)
	}
}

// ==========================================
// ValidateStructuredOutput — bridge-enabled paths
// ==========================================

// TestGaps_ValidateStructuredOutput_BridgeEnabled_Success covers the bridge-enabled
// success path.
func TestGaps_ValidateStructuredOutput_BridgeEnabled_Success(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "true")
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]any{
				"valid":      true,
				"reason":     "rust_valid",
				"normalized": map[string]any{"claims": []any{"c1"}},
			},
		})
	})

	got := ValidateStructuredOutput("claim_table", map[string]any{"claims": []any{"c1"}})
	if !got.Valid {
		t.Errorf("expected Valid=true from bridge, got Reason=%s", got.Reason)
	}
}

func TestGaps_RunRustBridge_EnvelopeDecode(t *testing.T) {
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"data": map[string]any{"valid": true, "reason": "rust_allow"},
		})
	})

	var out RustSandboxValidationResult
	if err := runRustBridgeCommand("sandbox-validate", map[string]string{"snippet": "print(1)"}, &out); err != nil {
		t.Fatalf("unexpected error decoding envelope: %v", err)
	}
	if !out.Valid || out.Reason != "rust_allow" {
		t.Fatalf("unexpected envelope payload: %+v", out)
	}
}

// TestGaps_ValidateStructuredOutput_BridgeEnabled_NilNormalized covers the
// `out.Normalized == nil → out.Normalized = {}` fill-in branch.
func TestGaps_ValidateStructuredOutput_BridgeEnabled_NilNormalized(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "true")
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// normalized is null/missing → triggers nil → {} fill-in
		json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"data": map[string]any{"valid": true, "reason": "rust_ok"},
		})
	})

	got := ValidateStructuredOutput("prisma", map[string]any{"identified": 5})
	if !got.Valid {
		t.Errorf("expected Valid=true, got Reason=%s", got.Reason)
	}
	if got.Normalized == nil {
		t.Error("expected Normalized to be filled in as empty map when Rust returns null")
	}
}

// TestGaps_ValidateStructuredOutput_BridgeEnabled_FailsRustRequired covers the path
// where bridge HTTP call fails and RustRequired=true → blocked.
func TestGaps_ValidateStructuredOutput_BridgeEnabled_FailsRustRequired(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "true")
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bridge down", http.StatusServiceUnavailable)
	})

	got := ValidateStructuredOutput("claim_table", map[string]any{"claims": []any{"c1"}})
	if got.Valid {
		t.Error("expected Valid=false when bridge fails and Rust is required")
	}
	if got.Reason != "rust_structured_validator_unavailable" {
		t.Errorf("expected rust_structured_validator_unavailable, got %q", got.Reason)
	}
}

// TestGaps_ValidateStructuredOutput_BridgeEnabled_FailsNotRequired covers the fallback
// path when bridge fails and RustRequired=false → heuristic.
func TestGaps_ValidateStructuredOutput_BridgeEnabled_FailsNotRequired(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "false")
	bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bridge down", http.StatusServiceUnavailable)
	})

	// Heuristic: claim_table with valid claims → valid
	got := ValidateStructuredOutput("claim_table", map[string]any{"claims": []any{"c1"}})
	if !got.Valid {
		t.Errorf("expected heuristic to pass claim_table, got Reason=%s", got.Reason)
	}
}

// ==========================================
// toString — non-string value (83.3%)
// ==========================================

// TestGaps_ToString_NonStringValue covers the !ok branch where value is not a string.
func TestGaps_ToString_NonStringValue(t *testing.T) {
	// Pass a non-string value — toString should return "".
	result := toString(42)
	if result != "" {
		t.Errorf("toString(42) = %q, want %q", result, "")
	}
}

// TestGaps_ToString_SliceValue covers a slice value (not a string).
func TestGaps_ToString_SliceValue(t *testing.T) {
	result := toString([]string{"a", "b"})
	if result != "" {
		t.Errorf("toString([]string{}) = %q, want %q", result, "")
	}
}

// TestGaps_ToString_StringValue covers the happy path.
func TestGaps_ToString_StringValue(t *testing.T) {
	result := toString("hello world")
	if result != "hello world" {
		t.Errorf("toString(%q) = %q, want %q", "hello world", result, "hello world")
	}
}
