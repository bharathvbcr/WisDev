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

// describeLLMBackendLive resolves the backend label against the running
// inference server so status surfaces show the model actually loaded
// (e.g. Ollama's live model) instead of the configured default.
func describeLLMBackendLive(ctx context.Context, client *llm.Client) string {
	return llm.DescribeProviderChainLive(ctx, client)
}

// refreshLLMBackendLive updates the TUI backend label in the background with
// the model actually loaded on the local inference server. Re-run on each
// research start so a server launched after the TUI is still picked up.
func (s *tuiState) refreshLLMBackendLive(client *llm.Client) {
	if s == nil || client == nil || s.offlineMode {
		return
	}
	go func() {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer probeCancel()
		if live := describeLLMBackendLive(probeCtx, client); live != "" && live != s.llmBackend {
			s.llmBackend = live
			s.notifyRunUpdate()
		}
	}()
}

func withGlobalResearchLLMClient(client *llm.Client, fn func() error) error {
	previous := internal.GlobalLLMClient
	internal.GlobalLLMClient = client
	defer func() { internal.GlobalLLMClient = previous }()
	return fn()
}
