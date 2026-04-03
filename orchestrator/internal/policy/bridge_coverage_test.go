package policy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateSandboxSnippetHeuristic(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "false")
	t.Setenv("WISDEV_RUST_BRIDGE_BIN", "")

	tests := []struct {
		snippet string
		want    bool
	}{
		{"print('hello')", true},
		{"", false},
		{"import os; os.system('rm -rf /')", false},
		{"eval('1+1')", false},
		{"subprocess.run(['ls'])", false},
	}

	for _, tt := range tests {
		got := ValidateSandboxSnippet(tt.snippet)
		if got.Valid != tt.want {
			t.Errorf("ValidateSandboxSnippet(%q) = %v, want %v", tt.snippet, got.Valid, tt.want)
		}
	}
}

func TestValidateStructuredOutputHeuristic(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "false")
	t.Setenv("WISDEV_RUST_BRIDGE_BIN", "")

	t.Run("missing type", func(t *testing.T) {
		res := ValidateStructuredOutput("", nil)
		if res.Valid {
			t.Error("Expected invalid")
		}
	})

	t.Run("claim_table", func(t *testing.T) {
		res := ValidateStructuredOutput("claim_table", map[string]any{"claims": []any{"c1"}})
		if !res.Valid {
			t.Errorf("Expected valid, got %s", res.Reason)
		}

		res = ValidateStructuredOutput("claim_table", map[string]any{"claims": []any{}})
		if res.Valid {
			t.Error("Expected invalid for empty claims")
		}
	})

	t.Run("prisma", func(t *testing.T) {
		res := ValidateStructuredOutput("prisma", map[string]any{"identified": 10})
		if !res.Valid {
			t.Error("Expected valid")
		}

		res = ValidateStructuredOutput("prisma", map[string]any{})
		if res.Valid {
			t.Error("Expected invalid")
		}
	})

	t.Run("rebuttal", func(t *testing.T) {
		res := ValidateStructuredOutput("rebuttal", map[string]any{"summary": "test"})
		if !res.Valid {
			t.Error("Expected valid")
		}

		res = ValidateStructuredOutput("rebuttal", map[string]any{})
		if res.Valid {
			t.Error("Expected invalid")
		}
	})

	t.Run("unsupported", func(t *testing.T) {
		res := ValidateStructuredOutput("unknown", map[string]any{})
		if res.Valid {
			t.Error("Expected invalid")
		}
	})
}

// bridgeTestServer starts an httptest server and sets RUST_GATEWAY_INTERNAL_URL
// so the HTTP-based bridge routes to it during the test.
func bridgeTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("RUST_GATEWAY_INTERNAL_URL", srv.URL)
}

func TestRustBridgeCoverage(t *testing.T) {
	t.Setenv("WISDEV_RUST_REQUIRED", "true")

	t.Run("runRustBridgeCommand Success", func(t *testing.T) {
		bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok":   true,
				"data": map[string]any{"allowed": true},
			})
		})
		var res GuardrailDecision
		err := runRustBridgeCommand("policy-eval", map[string]string{}, &res)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !res.Allowed {
			t.Error("expected Allowed=true")
		}
	})

	t.Run("runRustBridgeCommand HTTP Error", func(t *testing.T) {
		bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		})
		err := runRustBridgeCommand("policy-eval", map[string]string{}, nil)
		if err == nil {
			t.Error("expected error on HTTP 500")
		}
	})

	t.Run("evaluateGuardrailViaRust Error — required → deny", func(t *testing.T) {
		bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bridge down", http.StatusServiceUnavailable)
		})
		res, ok := evaluateGuardrailViaRust(DefaultPolicyConfig(), BudgetState{}, RiskLow, false, 0)
		if !ok {
			t.Error("expected ok=true when Rust is required (error path → deny)")
		}
		if res.Allowed {
			t.Error("expected Allowed=false on bridge error when Rust is required")
		}
	})

	t.Run("evaluateGuardrailViaRust Not Required — fallthrough on error", func(t *testing.T) {
		t.Setenv("WISDEV_RUST_REQUIRED", "false")
		bridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bridge down", http.StatusServiceUnavailable)
		})
		_, ok := evaluateGuardrailViaRust(DefaultPolicyConfig(), BudgetState{}, RiskLow, false, 0)
		if ok {
			t.Error("expected ok=false when Rust is not required and bridge fails")
		}
	})
}

func TestValidateSandboxSnippetDetailed(t *testing.T) {
	// Disable Rust bridge so the heuristic path is exercised.
	t.Setenv("WISDEV_RUST_REQUIRED", "false")
	t.Setenv("WISDEV_RUST_BRIDGE_BIN", "")

	t.Run("allowed snippets", func(t *testing.T) {
		if !ValidateSandboxSnippet("import pandas as pd").Valid {
			t.Error("Failed pandas")
		}
		if !ValidateSandboxSnippet("import numpy as np").Valid {
			t.Error("Failed numpy")
		}
		if !ValidateSandboxSnippet("import matplotlib.pyplot as plt").Valid {
			t.Error("Failed matplotlib")
		}
	})

	t.Run("blocked snippets", func(t *testing.T) {
		if !ValidateSandboxSnippet("import requests").Valid {
			t.Error("Should allow requests via heuristic")
		}
		if ValidateSandboxSnippet("open('/etc/passwd')").Valid {
			t.Error("Should block open")
		}
		if ValidateSandboxSnippet("__import__('os')").Valid {
			t.Error("Should block __import__")
		}
	})
}
