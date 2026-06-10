package llm

import (
	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

// HybridStructuredOutputPrefersCloud reports whether hybrid mode should route a
// structured-output call to the cloud backend first (heavy / high-value work).
func HybridStructuredOutputPrefersCloud(req *llmpb.StructuredRequest) bool {
	if ResolveLLMProviderMode() != LLMProviderModeHybrid {
		return false
	}
	switch resolveStructuredRequestClass(req) {
	case RequestClassHeavy, RequestClassStructuredHighValue:
		return true
	default:
		return false
	}
}

// HybridStructuredOutputPrefersLocal reports whether hybrid mode should route a
// structured-output call to the local OpenAI-compatible backend first.
func HybridStructuredOutputPrefersLocal(req *llmpb.StructuredRequest) bool {
	if ResolveLLMProviderMode() != LLMProviderModeHybrid {
		return false
	}
	return !HybridStructuredOutputPrefersCloud(req)
}

func resolveStructuredRequestClass(req *llmpb.StructuredRequest) RequestClass {
	if req == nil {
		return RequestClassStandard
	}
	if class := normalizeRequestClass(req.GetRequestClass()); class != "" {
		return class
	}
	return inferRequestClass(RequestPolicyInput{
		RequestedTier: req.GetServiceTier(),
		Structured:    true,
	})
}
