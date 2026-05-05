package llm

import "testing"

func TestResolveRequestPolicy(t *testing.T) {
	t.Run("light request stays off heavy tier", func(t *testing.T) {
		policy := ResolveRequestPolicy(RequestPolicyInput{
			RequestedTier: "light",
			TaskType:      "sub-agent",
		})
		if policy.RequestClass != RequestClassLight {
			t.Fatalf("expected light request class, got %q", policy.RequestClass)
		}
		if policy.InitialTier != "light" {
			t.Fatalf("expected light initial tier, got %q", policy.InitialTier)
		}
		if got, want := policy.FallbackChain, []string{"light", "standard"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("unexpected fallback chain: %#v", got)
		}
		if policy.ThinkingBudget == nil || *policy.ThinkingBudget != 0 {
			t.Fatalf("expected thinking budget 0, got %#v", policy.ThinkingBudget)
		}
		if policy.ServiceTier != "standard" {
			t.Fatalf("expected standard service tier, got %q", policy.ServiceTier)
		}
	})

	t.Run("standard request escalates through heavy and light", func(t *testing.T) {
		policy := ResolveRequestPolicy(RequestPolicyInput{})
		if policy.RequestClass != RequestClassStandard {
			t.Fatalf("expected standard request class, got %q", policy.RequestClass)
		}
		if policy.InitialTier != "standard" {
			t.Fatalf("expected standard initial tier, got %q", policy.InitialTier)
		}
		if got, want := policy.FallbackChain, []string{"standard", "heavy", "light"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("unexpected fallback chain: %#v", got)
		}
		if policy.ThinkingBudget == nil || *policy.ThinkingBudget != 1024 {
			t.Fatalf("expected thinking budget 1024, got %#v", policy.ThinkingBudget)
		}
		if policy.RetryProfile != RetryProfileStandard {
			t.Fatalf("expected standard retry profile, got %q", policy.RetryProfile)
		}
		if policy.LatencyBudgetMs != 50000 {
			t.Fatalf("expected default latency budget 50000, got %d", policy.LatencyBudgetMs)
		}
	})

	t.Run("structured high value uses dynamic thinking and priority tier", func(t *testing.T) {
		policy := ResolveRequestPolicy(RequestPolicyInput{
			Structured:   true,
			HighValue:    true,
			RequestClass: "structured_high_value",
		})
		if policy.RequestClass != RequestClassStructuredHighValue {
			t.Fatalf("expected structured high value request class, got %q", policy.RequestClass)
		}
		if policy.ServiceTier != "priority" {
			t.Fatalf("expected priority service tier, got %q", policy.ServiceTier)
		}
		if policy.ThinkingBudget == nil || *policy.ThinkingBudget != -1 {
			t.Fatalf("expected thinking budget -1, got %#v", policy.ThinkingBudget)
		}
	})

	t.Run("heavy request clamps explicit latency budget", func(t *testing.T) {
		policy := ResolveRequestPolicy(RequestPolicyInput{
			RequestedTier:   "heavy",
			LatencyBudgetMs: 120000,
		})
		if policy.InitialTier != "heavy" {
			t.Fatalf("expected heavy initial tier, got %q", policy.InitialTier)
		}
		if policy.ServiceTier != "priority" {
			t.Fatalf("expected priority service tier, got %q", policy.ServiceTier)
		}
		if policy.ThinkingBudget == nil || *policy.ThinkingBudget != 8192 {
			t.Fatalf("expected thinking budget 8192, got %#v", policy.ThinkingBudget)
		}
		if policy.LatencyBudgetMs != 50000 {
			t.Fatalf("expected clamped latency budget 50000, got %d", policy.LatencyBudgetMs)
		}
	})
}
