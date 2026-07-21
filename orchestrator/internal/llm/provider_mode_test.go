package llm

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	assert.Equal(t, "hybrid:light→ollama/llama3.1|heavy→vertex_ai", DescribeProviderChain(client))
	assert.Equal(t, "sidecar", DescribeProviderChain(nil))

	generic := &Client{
		OpenAICompatible: NewOpenAICompatibleClient("http://10.0.0.5:8000/v1", "mistral", ""),
		VertexDirect:     &VertexClient{backend: "vertex_ai"},
	}
	assert.Equal(t, "hybrid:light→openai_compatible/mistral|heavy→vertex_ai", DescribeProviderChain(generic))
}

func TestDescribeProviderChainLiveUsesLoadedOllamaModel(t *testing.T) {
	t.Setenv("WISDEV_LLM_PROVIDER", "hybrid")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3.1:8b-instruct-q4_K_M"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := &Client{
		OpenAICompatible: &OpenAICompatibleClient{
			openAIBaseURL: srv.URL + "/v1",
			ollamaRootURL: srv.URL,
			apiStyle:      ollamaNativeAPIStyle,
			defaultModel:  "llama3.1",
			httpClient:    srv.Client(),
		},
		VertexDirect: &VertexClient{backend: "vertex_ai"},
	}
	assert.Equal(t,
		"hybrid:light→ollama/llama3.1:8b-instruct-q4_K_M (live)|heavy→vertex_ai",
		DescribeProviderChainLive(t.Context(), client))
}

func TestDescribeProviderChainLiveFallsBackWhenServerDown(t *testing.T) {
	t.Setenv("WISDEV_LLM_PROVIDER", "hybrid")
	client := &Client{
		OpenAICompatible: &OpenAICompatibleClient{
			openAIBaseURL: "http://127.0.0.1:1/v1",
			ollamaRootURL: "http://127.0.0.1:1",
			apiStyle:      ollamaNativeAPIStyle,
			defaultModel:  "llama3.1",
			httpClient:    &http.Client{Timeout: 200 * time.Millisecond},
		},
		VertexDirect: &VertexClient{backend: "vertex_ai"},
	}
	assert.Equal(t, "hybrid:light→ollama/llama3.1|heavy→vertex_ai", DescribeProviderChainLive(t.Context(), client))
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
