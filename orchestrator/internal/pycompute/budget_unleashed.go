package pycompute

import (
	"os"
	"strings"
	"time"
)

// unleashedSidecarMode reports whether the operator opted into the high-output
// "unleashed" profile via WISDEV_UNLEASHED. When enabled, the Python sidecar
// HTTP client deadline is extended so heavy synthesis calls (final draft,
// hypothesis generation, claim/citation verification on thinking models) are not
// cut off mid-response. Default (unset) preserves the standard timeout. Mirrors
// the gates in internal/wisdev, internal/policy, and internal/llm.
func unleashedSidecarMode() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WISDEV_UNLEASHED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// unleashedSidecarTimeout scales the sidecar HTTP client deadline up in
// unleashed mode, leaving it unchanged otherwise.
func unleashedSidecarTimeout(standard time.Duration) time.Duration {
	if unleashedSidecarMode() {
		return standard * 3
	}
	return standard
}

// unleashedSidecarBreakerFailures raises the sidecar circuit-breaker failure
// tolerance in unleashed mode so a few transient blips during a long, high-volume
// run do not short-circuit Python capabilities. The sidecar is a local trusted
// process, so a higher threshold carries no external rate-limit risk.
func unleashedSidecarBreakerFailures(standard int) int {
	if unleashedSidecarMode() {
		return standard * 2
	}
	return standard
}
