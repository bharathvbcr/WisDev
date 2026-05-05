package llm

import (
	"strings"
	"time"
)

type RequestClass string

const (
	RequestClassLight               RequestClass = "light"
	RequestClassStandard            RequestClass = "standard"
	RequestClassStructuredHighValue RequestClass = "structured_high_value"
	RequestClassHeavy               RequestClass = "heavy"
)

type RetryProfile string

const (
	RetryProfileConservative RetryProfile = "conservative"
	RetryProfileStandard     RetryProfile = "standard"
)

const (
	// DefaultProviderTransportTimeout is the per-attempt HTTP/gRPC backstop for
	// the transport layer. The caller's context deadline fires first in normal
	// operation; this guards against callers that forget to set a deadline.
	DefaultProviderTransportTimeout = 50 * time.Second

	// DefaultProviderOuterDeadline is the context deadline set on the full
	// request lifecycle (all fallback tiers combined).
	DefaultProviderOuterDeadline = 55 * time.Second

	// ColdStartOuterDeadline inflates the outer deadline during the cold-start
	// window to absorb ADC token acquisition, SDK warm-up, and first-connection
	// latency that can add 5–15 s on the first few requests after boot.
	ColdStartOuterDeadline = 90 * time.Second

	minProviderLatencyBudgetMs = 4_000
	maxProviderLatencyBudgetMs = 50_000

)

type RequestPolicyInput struct {
	RequestedTier   string
	TaskType        string
	RequestClass    string
	LatencyBudgetMs int
	Structured      bool
	HighValue       bool
}

type RequestPolicy struct {
	RequestClass     RequestClass
	InitialTier      string
	FallbackChain    []string
	ThinkingBudget   *int32
	ServiceTier      string
	RetryProfile     RetryProfile
	LatencyBudgetMs  int32
	TransportTimeout time.Duration
	OuterDeadline    time.Duration
	// ColdStart is true when this policy was resolved during the cold-start
	// window. Callers can log this to correlate elevated latency with boot state.
	ColdStart bool
}

func ResolveRequestPolicy(input RequestPolicyInput) RequestPolicy {
	class := normalizeRequestClass(input.RequestClass)
	if class == "" {
		class = inferRequestClass(input)
	}

	initialTier := normalizeTier(input.RequestedTier)
	if initialTier == "" {
		initialTier = defaultTierForClass(class)
	}
	if class == RequestClassLight {
		initialTier = "light"
	}

	fallbackChain := fallbackChainFor(initialTier, class)
	coldStart := IsColdStartWindow()

	// During cold start, we keep the default request path forgiving by
	// preserving the standard 90-second outer deadline, but we no longer
	// inflate caller-specified latency budgets. Explicit budgets should remain
	// deliberate and bounded by the normal clamp.
	rawBudget := input.LatencyBudgetMs
	latencyBudgetMs := clampLatencyBudgetMs(rawBudget)

	// Inflate the outer deadline during cold start to match the extended budget.
	outerDeadline := DefaultProviderOuterDeadline
	if coldStart {
		outerDeadline = ColdStartOuterDeadline
	}

	policy := RequestPolicy{
		RequestClass:     class,
		InitialTier:      initialTier,
		FallbackChain:    fallbackChain,
		ServiceTier:      serviceTierForClass(class),
		RetryProfile:     retryProfileForClass(class),
		LatencyBudgetMs:  int32(latencyBudgetMs),
		TransportTimeout: DefaultProviderTransportTimeout,
		OuterDeadline:    outerDeadline,
		ColdStart:        coldStart,
	}

	if budget, ok := thinkingBudgetForClass(class); ok {
		policy.ThinkingBudget = &budget
	}

	return policy
}

func normalizeRequestClass(raw string) RequestClass {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(RequestClassLight):
		return RequestClassLight
	case string(RequestClassStandard):
		return RequestClassStandard
	case string(RequestClassStructuredHighValue), "structured", "high_value_structured":
		return RequestClassStructuredHighValue
	case string(RequestClassHeavy):
		return RequestClassHeavy
	default:
		return ""
	}
}

func inferRequestClass(input RequestPolicyInput) RequestClass {
	if input.Structured && input.HighValue {
		return RequestClassStructuredHighValue
	}

	switch normalizeTier(input.RequestedTier) {
	case "light":
		return RequestClassLight
	case "heavy":
		return RequestClassHeavy
	}

	taskType := strings.ToLower(strings.TrimSpace(input.TaskType))
	switch taskType {
	case "sub-agent", "subagent", "light":
		return RequestClassLight
	case "structured", "analysis", "synthesis", "reasoning":
		if input.Structured {
			return RequestClassStructuredHighValue
		}
	}

	if input.Structured && input.HighValue {
		return RequestClassStructuredHighValue
	}
	return RequestClassStandard
}

func normalizeTier(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "light":
		return "light"
	case "heavy":
		return "heavy"
	case "balanced", "standard", "default":
		return "standard"
	default:
		return ""
	}
}

func defaultTierForClass(class RequestClass) string {
	switch class {
	case RequestClassLight:
		return "light"
	case RequestClassHeavy:
		return "heavy"
	default:
		return "standard"
	}
}

func fallbackChainFor(initialTier string, class RequestClass) []string {
	switch {
	case class == RequestClassLight || initialTier == "light":
		return []string{"light", "standard"}
	case initialTier == "heavy":
		return []string{"heavy", "standard", "light"}
	default:
		return []string{"standard", "heavy", "light"}
	}
}

func thinkingBudgetForClass(class RequestClass) (int32, bool) {
	switch class {
	case RequestClassLight:
		return 0, true
	case RequestClassStandard:
		return 1024, true
	case RequestClassStructuredHighValue:
		return -1, true
	case RequestClassHeavy:
		return 8192, true
	default:
		return 0, false
	}
}

func serviceTierForClass(class RequestClass) string {
	switch class {
	case RequestClassStructuredHighValue, RequestClassHeavy:
		return "priority"
	default:
		return "standard"
	}
}

func retryProfileForClass(class RequestClass) RetryProfile {
	if class == RequestClassLight {
		return RetryProfileConservative
	}
	return RetryProfileStandard
}

func clampLatencyBudgetMs(value int) int {
	if value <= 0 {
		return int(DefaultProviderTransportTimeout / time.Millisecond)
	}
	if value < minProviderLatencyBudgetMs {
		return minProviderLatencyBudgetMs
	}
	if value > maxProviderLatencyBudgetMs {
		return maxProviderLatencyBudgetMs
	}
	return value
}
