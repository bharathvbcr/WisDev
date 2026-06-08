package llm

import (
	"strings"

	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

// ApplyGeneratePolicy populates a GenerateRequest with the canonical LLM
// request policy. Explicit model overrides are preserved; otherwise the model
// is derived from the policy tier.
func ApplyGeneratePolicy(req *llmpb.GenerateRequest, policy RequestPolicy) *llmpb.GenerateRequest {
	if req == nil {
		req = &llmpb.GenerateRequest{}
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = ResolveModelForTier(policy.InitialTier)
	}
	req.ServiceTier = policy.ServiceTier
	req.RetryProfile = string(policy.RetryProfile)
	req.RequestClass = string(policy.RequestClass)
	req.LatencyBudgetMs = policy.LatencyBudgetMs
	if policy.ThinkingBudget != nil {
		req.ThinkingBudget = policy.ThinkingBudget
	}
	return req
}

// ApplyStructuredPolicy populates a StructuredRequest with the canonical LLM
// request policy. Explicit model overrides are preserved; otherwise the model
// is derived from the policy tier.
func ApplyStructuredPolicy(req *llmpb.StructuredRequest, policy RequestPolicy) *llmpb.StructuredRequest {
	if req == nil {
		req = &llmpb.StructuredRequest{}
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = ResolveModelForTier(policy.InitialTier)
	}
	req.ServiceTier = policy.ServiceTier
	req.RetryProfile = string(policy.RetryProfile)
	req.RequestClass = string(policy.RequestClass)
	req.LatencyBudgetMs = policy.LatencyBudgetMs
	if policy.ThinkingBudget != nil {
		req.ThinkingBudget = policy.ThinkingBudget
	}
	return req
}
