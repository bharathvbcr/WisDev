package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveLLMProviderMode(t *testing.T) {
	t.Setenv("WISDEV_LLM_BASE_URL", "")
	t.Setenv("WISDEV_LLM_PROVIDER", "")
	assert.Equal(t, LLMProviderModeCloud, ResolveLLMProviderMode())

	t.Setenv("WISDEV_LLM_BASE_URL", "http://127.0.0.1:11434/v1")
	t.Setenv("WISDEV_LLM_PROVIDER", "")
	assert.Equal(t, LLMProviderModeHybrid, ResolveLLMProviderMode())

	t.Setenv("WISDEV_LLM_PROVIDER", "ollama")
	assert.Equal(t, LLMProviderModeLocal, ResolveLLMProviderMode())

	t.Setenv("WISDEV_LLM_PROVIDER", "cloud")
	assert.Equal(t, LLMProviderModeCloud, ResolveLLMProviderMode())

	t.Setenv("WISDEV_LLM_PROVIDER", "hybrid")
	assert.Equal(t, LLMProviderModeHybrid, ResolveLLMProviderMode())
}

func TestDescribeProviderChain(t *testing.T) {
	t.Setenv("WISDEV_LLM_PROVIDER", "hybrid")
	client := &Client{
		OpenAICompatible: NewOpenAICompatibleClient("http://127.0.0.1:11434/v1", "llama3.1", ""),
		VertexDirect:     &VertexClient{backend: "vertex_ai"},
	}
	assert.Equal(t, "hybrid:light→openai_compatible/llama3.1|heavy→vertex_ai", DescribeProviderChain(client))
	assert.Equal(t, "sidecar", DescribeProviderChain(nil))
}

func TestWireDirectProvidersCloudSkipsLocalEvenWithBaseURL(t *testing.T) {
	t.Setenv("WISDEV_LLM_BASE_URL", "http://127.0.0.1:11434/v1")
	t.Setenv("WISDEV_LLM_MODEL", "mistral")
	t.Setenv("WISDEV_LLM_PROVIDER", "cloud")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")

	client := NewClient()
	WireDirectProviders(t.Context(), client)
	assert.Nil(t, client.OpenAICompatible)
}
