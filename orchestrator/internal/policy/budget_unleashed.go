package policy

import (
	"os"
	"strings"
)

// unleashedPolicyMode reports whether the operator opted into the high-output
// "unleashed" profile via the WISDEV_UNLEASHED environment variable. When
// enabled, DefaultPolicyConfig lifts the per-session guardrail budget (tool
// calls, script runs, and spend ceiling) so long, elaborate research sessions
// are not cut short. The default (unset) preserves the conservative limits, so
// tests and production safety behavior are unaffected unless explicitly opted
// in. Mirrors the gate in internal/wisdev and internal/llm, which read the same
// variable.
func unleashedPolicyMode() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WISDEV_UNLEASHED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
