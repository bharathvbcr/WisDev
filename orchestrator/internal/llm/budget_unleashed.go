package llm

import (
	"os"
	"strings"
)

// unleashedOutputMode reports whether the operator opted into the high-output
// "unleashed" profile via the WISDEV_UNLEASHED environment variable. When
// enabled, the per-call default output-token ceiling is raised so generations
// that rely on the fallback budget can produce more elaborate responses. It
// only lifts the zero-value fallback — callers that pass an explicit token
// budget keep it, so intentionally small structured/classification calls are
// unaffected. The default (unset) preserves the conservative limits. Mirrors
// the gate in internal/wisdev and internal/policy.
func unleashedOutputMode() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WISDEV_UNLEASHED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// defaultOutputTokens returns the fallback output-token ceiling for calls that
// did not specify one. The standard value is supplied by the caller; the
// unleashed profile raises it to allow longer completions.
func defaultOutputTokens(standard int32) int32 {
	if unleashedOutputMode() {
		const unleashedFallback int32 = 4096
		if unleashedFallback > standard {
			return unleashedFallback
		}
	}
	return standard
}

// unleashedStructuredTokenFloor is a modest minimum output-token budget applied
// to explicit structured calls in unleashed mode. It sits just above the 256
// thinking-disable threshold, giving intentionally-small structured/extraction
// calls a little more room without ballooning latency or cost.
const unleashedStructuredTokenFloor int32 = 512

// liftStructuredOutputTokens raises an explicit structured output-token budget
// to unleashedStructuredTokenFloor in unleashed mode. Non-positive budgets
// (which carry default/fallback semantics elsewhere) and budgets already at or
// above the floor are returned unchanged.
func liftStructuredOutputTokens(maxTokens int32) int32 {
	if unleashedOutputMode() && maxTokens > 0 && maxTokens < unleashedStructuredTokenFloor {
		return unleashedStructuredTokenFloor
	}
	return maxTokens
}

// structuredThinkingDisableCeiling returns the upper output-token bound below
// which structured calls keep model "thinking" minimized. Unleashed mode raises
// it to the structured floor so the slightly larger budget does not flip
// thinking on and risk truncating the JSON payload.
func structuredThinkingDisableCeiling(standard int32) int32 {
	if unleashedOutputMode() && unleashedStructuredTokenFloor > standard {
		return unleashedStructuredTokenFloor
	}
	return standard
}
