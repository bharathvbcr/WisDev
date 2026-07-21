package wisdev

import (
	"os"
	"strings"
	"time"
)

// unleashedBudgetMode reports whether the operator opted into the high-output
// "unleashed" research profile via the WISDEV_UNLEASHED environment variable.
//
// When enabled, the orchestrator's budget chokepoints relax their caps so the
// autonomous loop iterates more, searches more broadly, gathers more papers,
// runs more workers/hypotheses in parallel, and produces more elaborate output.
//
// The default (unset / "0" / "false") preserves the standard bounded behavior,
// so the test suite and production safety limits are unaffected unless an
// operator explicitly opts in. Intended for deep research runs where maximizing depth
// matters more than conserving tokens.
//
// See also the package-local gates in internal/policy and internal/llm, which
// read the same variable to lift the per-session guardrail budget and the
// default per-call output-token ceiling respectively.
func unleashedBudgetMode() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WISDEV_UNLEASHED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// unleashedTimeout scales a per-call deadline up in unleashed mode so elaborate
// generations (thinking models, large outputs, heavy sidecar synthesis) are not
// cut off mid-response. Returns the standard duration unchanged when not
// unleashed, so default latency/hang protection is preserved.
func unleashedTimeout(standard time.Duration) time.Duration {
	if unleashedBudgetMode() {
		return standard * 3
	}
	return standard
}

// unleashedMinLoopIterations returns a minimum autonomous-loop iteration floor
// in unleashed mode so the loop does not converge on the first pass (which left
// runs reporting "iterations: 0" / effectively single-shot). The floor is capped
// by maxIterations so it never exceeds the run's own ceiling. Returns 0 when not
// unleashed, preserving the default early-convergence behavior.
func unleashedMinLoopIterations(maxIterations int) int {
	if !unleashedBudgetMode() {
		return 0
	}
	floor := 5
	if maxIterations > 0 && floor > maxIterations {
		floor = maxIterations
	}
	return floor
}

// reconcileUnleashedPolicyOverride applies a config-supplied per-session policy
// override (e.g. from wisdev-adk.yaml). In unleashed mode the override may only
// RAISE the limit — a lower YAML cap cannot silently undo the unleashed policy
// from policy.DefaultPolicyConfig. Outside unleashed mode the override wins as
// before.
func reconcileUnleashedPolicyOverride(current, override int) int {
	if unleashedBudgetMode() && current > override {
		return current
	}
	return override
}

// scaleUnleashedBudget multiplies a positive budget value by mult, clamped to
// ceiling, but never below the original value. Non-positive inputs (which carry
// "unbounded"/"unset" semantics downstream) are returned unchanged.
func scaleUnleashedBudget(value, mult, ceiling int) int {
	if value <= 0 {
		return value
	}
	scaled := value * mult
	if ceiling > 0 && scaled > ceiling {
		scaled = ceiling
	}
	if scaled < value {
		return value
	}
	return scaled
}

// applyUnleashedSearchBudget lifts a resolved SearchBudget to the generous
// "unleashed" tier keyed by quality mode. Every field only ever increases, so
// callers that already requested a richer budget keep it.
func applyUnleashedSearchBudget(budget *SearchBudget) {
	if budget == nil {
		return
	}
	var maxTerms, hits, uniquePapers int
	switch budget.QualityMode {
	case "fast":
		maxTerms, hits, uniquePapers = 6, 12, 48
	case "quality":
		maxTerms, hits, uniquePapers = 48, 48, 360
	default: // balanced
		maxTerms, hits, uniquePapers = 24, 40, 200
	}
	if maxTerms > budget.MaxSearchTerms {
		budget.MaxSearchTerms = maxTerms
	}
	if hits > budget.HitsPerSearch {
		budget.HitsPerSearch = hits
	}
	if uniquePapers > budget.MaxUniquePapers {
		budget.MaxUniquePapers = uniquePapers
	}
}
