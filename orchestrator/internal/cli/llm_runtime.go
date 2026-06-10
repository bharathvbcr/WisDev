package cli

import (
	"context"
	"log/slog"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	internal "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

func resolveResearchLLMClient() *llm.Client {
	client := llm.NewClient()
	initCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	llm.WireDirectProviders(initCtx, client)
	slog.Info("wisdev cli: research LLM providers wired",
		"mode", llm.ResolveLLMProviderMode(),
		"chain", llm.DescribeProviderChain(client),
	)
	return client
}

func describeLLMBackend(client *llm.Client) string {
	return llm.DescribeProviderChain(client)
}

func withGlobalResearchLLMClient(client *llm.Client, fn func() error) error {
	previous := internal.GlobalLLMClient
	internal.GlobalLLMClient = client
	defer func() { internal.GlobalLLMClient = previous }()
	return fn()
}
