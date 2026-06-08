package wisdev

import (
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
)

type providerCooldownReporter interface {
	ProviderCooldownRemaining() time.Duration
}

func wisdevLLMCooldownRemaining(requester any) time.Duration {
	if requester == nil {
		return 0
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
