package llm

import (
	"testing"
	"time"

	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeRequestClassAndTierHelpers(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want RequestClass
	}{
		{name: "normalize known light", raw: " light ", want: RequestClassLight},
		{name: "normalize standard", raw: "STANDARD", want: RequestClassStandard},
		{name: "normalize structured alias", raw: "structured", want: RequestClassStructuredHighValue},
		{name: "normalize structured high value alias", raw: "high_value_structured", want: RequestClassStructuredHighValue},
		{name: "normalize heavy", raw: "Heavy", want: RequestClassHeavy},
		{name: "normalize unknown", raw: "legacy", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeRequestClass(tc.raw))
		})
	}

	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "normalize light", raw: " light ", want: "light"},
		{name: "normalize heavy", raw: "HEAVY", want: "heavy"},
		{name: "normalize standard alias", raw: "balanced", want: "standard"},
		{name: "normalize default alias", raw: "default", want: "standard"},
		{name: "normalize unknown", raw: "custom", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeTier(tc.raw))
		})
	}

	assert.Equal(t, minProviderLatencyBudgetMs, clampLatencyBudgetMs(512))
	assert.Equal(t, maxProviderLatencyBudgetMs, clampLatencyBudgetMs(120000))
	assert.Equal(t, int(DefaultProviderTransportTimeout/time.Millisecond), clampLatencyBudgetMs(0))
	assert.Equal(t, 6000, clampLatencyBudgetMs(6000))
}

func TestInferRequestClassAndFallbackChains(t *testing.T) {
	prev := processStartTime
	t.Cleanup(func() {
		processStartTime = prev
	})
	processStartTime = time.Now().Add(-(ColdStartWindow + time.Second))

	for _, tc := range []struct {
		name string
		in   RequestPolicyInput
		want RequestClass
	}{
		{
			name: "structured + high value",
			in:   RequestPolicyInput{Structured: true, HighValue: true},
			want: RequestClassStructuredHighValue,
		},
		{
			name: "requested heavy tier",
			in:   RequestPolicyInput{RequestedTier: "heavy"},
			want: RequestClassHeavy,
		},
		{
			name: "requested light tier",
			in:   RequestPolicyInput{RequestedTier: "light"},
			want: RequestClassLight,
		},
		{
			name: "subagent task",
			in:   RequestPolicyInput{TaskType: "sub-agent"},
			want: RequestClassLight,
		},
		{
			name: "analysis task without structured",
			in:   RequestPolicyInput{TaskType: "analysis"},
			want: RequestClassStandard,
		},
		{
			name: "analysis task with structured",
			in:   RequestPolicyInput{TaskType: "analysis", Structured: true},
			want: RequestClassStructuredHighValue,
		},
		{
			name: "unknown input falls back to standard",
			in:   RequestPolicyInput{TaskType: "mystery"},
			want: RequestClassStandard,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, inferRequestClass(tc.in))
		})
	}

	t.Run("request class light forces light fallback path", func(t *testing.T) {
		policy := ResolveRequestPolicy(RequestPolicyInput{
			RequestClass:  string(RequestClassLight),
			RequestedTier: "heavy",
		})
		assert.Equal(t, RequestClassLight, policy.RequestClass)
		assert.Equal(t, "light", policy.InitialTier)
		assert.Equal(t, []string{"light", "standard"}, policy.FallbackChain)
		assert.Equal(t, RetryProfileConservative, policy.RetryProfile)
		assert.Equal(t, int32(50000), policy.LatencyBudgetMs)
		assert.Equal(t, int32(0), *policy.ThinkingBudget)
		assert.Equal(t, "standard", policy.ServiceTier)
		assert.Equal(t, DefaultProviderOuterDeadline, policy.OuterDeadline)
	})

	t.Run("structured alias resolves to structured-high-value with priority service tier", func(t *testing.T) {
		policy := ResolveRequestPolicy(RequestPolicyInput{
			RequestClass:    "structured",
			RequestedTier:   "heavy",
			LatencyBudgetMs: 30000,
		})
		assert.Equal(t, RequestClassStructuredHighValue, policy.RequestClass)
		assert.Equal(t, "heavy", policy.InitialTier)
		assert.Equal(t, []string{"heavy", "standard", "light"}, policy.FallbackChain)
		assert.Equal(t, int32(30000), policy.LatencyBudgetMs)
		assert.Equal(t, int32(-1), *policy.ThinkingBudget)
		assert.Equal(t, "priority", policy.ServiceTier)
	})

	t.Run("cold start inflates full deadline but preserves clamped budget", func(t *testing.T) {
		prev := processStartTime
		t.Cleanup(func() {
			processStartTime = prev
		})

		processStartTime = time.Now().Add(-10 * time.Second)
		policy := ResolveRequestPolicy(RequestPolicyInput{
			RequestClass:    string(RequestClassHeavy),
			LatencyBudgetMs: 120000,
		})
		assert.True(t, policy.ColdStart)
		assert.Equal(t, ColdStartOuterDeadline, policy.OuterDeadline)
		assert.Equal(t, int32(maxProviderLatencyBudgetMs), policy.LatencyBudgetMs)
	})
}

func TestApplyPolicyDefaults(t *testing.T) {
	policy := ResolveRequestPolicy(RequestPolicyInput{
		RequestClass:    "heavy",
		LatencyBudgetMs: 30000,
	})

	generateReq := ApplyGeneratePolicy(nil, policy)
	assert.NotNil(t, generateReq)
	assert.Equal(t, ResolveModelForTier(policy.InitialTier), generateReq.Model)
	assert.Equal(t, policy.ServiceTier, generateReq.ServiceTier)
	assert.Equal(t, string(policy.RequestClass), generateReq.RequestClass)
	assert.Equal(t, policy.LatencyBudgetMs, generateReq.LatencyBudgetMs)
	assert.Equal(t, policy.ThinkingBudget, generateReq.ThinkingBudget)

	structuredReq := ApplyStructuredPolicy(nil, policy)
	assert.NotNil(t, structuredReq)
	assert.Equal(t, ResolveModelForTier(policy.InitialTier), structuredReq.Model)
	assert.Equal(t, policy.ServiceTier, structuredReq.ServiceTier)
	assert.Equal(t, string(policy.RequestClass), structuredReq.RequestClass)
	assert.Equal(t, policy.LatencyBudgetMs, structuredReq.LatencyBudgetMs)
	assert.Equal(t, policy.ThinkingBudget, structuredReq.ThinkingBudget)

	// Also confirm explicit model is preserved end-to-end for both request types.
	explicit := &llmpb.GenerateRequest{Model: "custom-model"}
	explicitStructured := &llmpb.StructuredRequest{Model: "custom-structured-model"}
	assert.Equal(t, "custom-model", ApplyGeneratePolicy(explicit, policy).Model)
	assert.Equal(t, "custom-structured-model", ApplyStructuredPolicy(explicitStructured, policy).Model)
}

func TestRequestPolicyHelperBranches(t *testing.T) {
	assert.Equal(t, "light", defaultTierForClass(RequestClassLight))
	assert.Equal(t, "standard", defaultTierForClass(RequestClassStandard))
	assert.Equal(t, "heavy", defaultTierForClass(RequestClassHeavy))

	assert.Equal(t, []string{"light", "standard"}, fallbackChainFor("light", RequestClassStandard))
	assert.Equal(t, []string{"heavy", "standard", "light"}, fallbackChainFor("heavy", RequestClassStandard))
	assert.Equal(t, []string{"standard", "heavy", "light"}, fallbackChainFor("standard", RequestClassStandard))

	thinkingBudget, ok := thinkingBudgetForClass(RequestClassStructuredHighValue)
	assert.True(t, ok)
	assert.Equal(t, int32(-1), thinkingBudget)

	thinkingBudget, ok = thinkingBudgetForClass(RequestClass("legacy"))
	assert.False(t, ok)
	assert.Equal(t, int32(0), thinkingBudget)

	assert.Equal(t, "standard", serviceTierForClass(RequestClassStandard))
	assert.Equal(t, "priority", serviceTierForClass(RequestClassHeavy))

	assert.Equal(t, RetryProfileConservative, retryProfileForClass(RequestClassLight))
	assert.Equal(t, RetryProfileStandard, retryProfileForClass(RequestClassHeavy))
}
