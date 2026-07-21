package wisdev

import (
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
)

type providerCooldownReporter interface {
	ProviderCooldownRemaining() time.Duration
}

type providerModelCooldownReporter interface {
	ProviderCooldownRemainingForModel(model string) time.Duration
}

// wisdevLLMCooldownRemaining reports the provider cooldown for the requester.
// When a non-empty model is supplied (optional variadic to keep existing call
// sites source-compatible) and the requester supports per-model cooldowns,
// only that model's cooldown (plus any model-agnostic cooldown) is reported;
// otherwise the conservative process-wide aggregate is used.
func wisdevLLMCooldownRemaining(requester any, model ...string) time.Duration {
	if requester == nil {
		return 0
	}
	if len(model) > 0 && strings.TrimSpace(model[0]) != "" {
		if reporter, ok := requester.(providerModelCooldownReporter); ok {
			return reporter.ProviderCooldownRemainingForModel(model[0])
		}
	}
	reporter, ok := requester.(providerCooldownReporter)
	if !ok {
		return 0
	}
	return reporter.ProviderCooldownRemaining()
}

func wisdevLLMCallIsCoolingDown(err error) bool {
	return llm.IsProviderRateLimitError(err)
}
