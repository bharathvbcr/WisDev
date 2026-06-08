package llm

import (
	"testing"

	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func TestApplyGeneratePolicy(t *testing.T) {
	policy := ResolveRequestPolicy(RequestPolicyInput{
		RequestedTier:   "light",
		TaskType:        "light",
		LatencyBudgetMs: 12000,
	})
	req := ApplyGeneratePolicy(&llmpb.GenerateRequest{Prompt: "hello"}, policy)
	if req.Model != ResolveLightModel() {
		t.Fatalf("expected light model, got %q", req.Model)
	}
	if req.ServiceTier != "standard" {
		t.Fatalf("expected service tier standard, got %q", req.ServiceTier)
	}
	if req.RequestClass != string(RequestClassLight) {
		t.Fatalf("expected request class light, got %q", req.RequestClass)
	}
	if req.ThinkingBudget == nil || *req.ThinkingBudget != 0 {
		t.Fatalf("expected thinking budget 0, got %#v", req.ThinkingBudget)
	}
}

func TestApplyStructuredPolicyPreservesExplicitModel(t *testing.T) {
	policy := ResolveRequestPolicy(RequestPolicyInput{
		RequestedTier:   "standard",
		RequestClass:    string(RequestClassStructuredHighValue),
		Structured:      true,
		HighValue:       true,
		LatencyBudgetMs: 30000,
	})
	req := ApplyStructuredPolicy(&llmpb.StructuredRequest{
		Prompt: "hello",
		Model:  "custom-model",
	}, policy)
	if req.Model != "custom-model" {
		t.Fatalf("expected explicit model to be preserved, got %q", req.Model)
	}
	if req.ServiceTier != "priority" {
		t.Fatalf("expected service tier priority, got %q", req.ServiceTier)
	}
	if req.RequestClass != string(RequestClassStructuredHighValue) {
		t.Fatalf("expected structured high value class, got %q", req.RequestClass)
	}
	if req.ThinkingBudget == nil || *req.ThinkingBudget != -1 {
		t.Fatalf("expected dynamic thinking budget -1, got %#v", req.ThinkingBudget)
	}
}
