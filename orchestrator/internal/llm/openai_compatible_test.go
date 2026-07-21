package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeOpenAICompatibleBaseURL(t *testing.T) {
	openAI, root, style := normalizeOpenAICompatibleBaseURL("http://127.0.0.1:11434/v1")
	assert.Equal(t, "http://127.0.0.1:11434/v1", openAI)
	assert.Equal(t, "http://127.0.0.1:11434", root)
	assert.Equal(t, openAIAPIStyle, style)

	openAI, root, style = normalizeOpenAICompatibleBaseURL("http://127.0.0.1:11434")
	assert.Equal(t, "http://127.0.0.1:11434/v1", openAI)
	assert.Equal(t, "http://127.0.0.1:11434", root)
	assert.Equal(t, ollamaNativeAPIStyle, style)

	openAI, root, style = normalizeOpenAICompatibleBaseURL("http://localhost:8080")
	assert.Equal(t, "http://localhost:8080/v1", openAI)
	assert.Equal(t, "http://localhost:8080", root)
	assert.Equal(t, openAIAPIStyle, style)
}

func TestOpenAICompatibleClientStructuredOutputOpenAI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Equal(t, "llama3.1", payload["model"])
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"corrected_query\":\"meniscus reconstruction strategies\"}"}}],
			"usage":{"prompt_tokens":12,"completion_tokens":8}
		}`))
	}))
	defer server.Close()

	client := NewOpenAICompatibleClient(server.URL+"/v1", "llama3.1", "")
	result, inTok, outTok, err := client.generateStructuredWithTokens(
		context.Background(),
		"gemini-2.0-flash",
		"fix this query",
		"you are helpful",
		`{"type":"object","properties":{"corrected_query":{"type":"string"}}}`,
		0.2,
		256,
		"",
		nil,
		"query_prep",
		"",
	)
	require.NoError(t, err)
	assert.Contains(t, result, "meniscus reconstruction strategies")
	assert.Equal(t, int32(12), inTok)
	assert.Equal(t, int32(8), outTok)
}

func TestOpenAICompatibleClientStructuredOutputOllamaNative(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/chat", r.URL.Path)
		_, _ = w.Write([]byte(`{
			"message":{"content":"{\"domain\":\"medicine\"}"},
			"prompt_eval_count":5,
			"eval_count":3
		}`))
	}))
	defer server.Close()

	client := &OpenAICompatibleClient{
		openAIBaseURL: server.URL + "/v1",
		ollamaRootURL: server.URL,
		apiStyle:      ollamaNativeAPIStyle,
		defaultModel:  "llama3.1",
		httpClient:    server.Client(),
	}
	result, inTok, outTok, err := client.generateStructuredWithTokens(
		context.Background(),
		"",
		"classify domain",
		"",
		`{"type":"object","properties":{"domain":{"type":"string"}}}`,
		0.2,
		128,
		"",
		nil,
		"domain",
		"",
	)
	require.NoError(t, err)
	assert.Contains(t, result, "medicine")
	assert.Equal(t, int32(5), inTok)
	assert.Equal(t, int32(3), outTok)
}

func TestClientStructuredOutputUsesOpenAICompatible(t *testing.T) {
	t.Setenv("WISDEV_LLM_PROVIDER", "ollama")
	t.Setenv("WISDEV_LLM_BASE_URL", "http://127.0.0.1:11434/v1")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"ok\":true}"}}],
			"usage":{"prompt_tokens":1,"completion_tokens":1}
		}`))
	}))
	defer server.Close()

	client := &Client{
		OpenAICompatible: NewOpenAICompatibleClient(server.URL+"/v1", "llama3.1", ""),
	}
	resp, err := client.StructuredOutput(context.Background(), &llmv1.StructuredRequest{
		Prompt:     "test",
		JsonSchema: `{"type":"object","properties":{"ok":{"type":"boolean"}}}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.GetJsonResult(), `"ok":true`)
	assert.Equal(t, "llama3.1", resp.GetModelUsed())
}

func TestWireDirectProvidersPrefersLocalBaseURL(t *testing.T) {
	t.Setenv("WISDEV_LLM_BASE_URL", "http://127.0.0.1:11434/v1")
	t.Setenv("WISDEV_LLM_MODEL", "mistral")
	t.Setenv("WISDEV_LLM_PROVIDER", "ollama")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")

	client := NewClient()
	WireDirectProviders(context.Background(), client)
	require.NotNil(t, client.OpenAICompatible)
	assert.Equal(t, "mistral", client.OpenAICompatible.DefaultModel())
	assert.Nil(t, client.VertexDirect)
}

func TestResolveOpenAICompatibleConfigProviderVertex(t *testing.T) {
	t.Setenv("WISDEV_LLM_BASE_URL", "http://127.0.0.1:11434/v1")
	t.Setenv("WISDEV_LLM_PROVIDER", "vertex")
	_, _, _, ok := ResolveOpenAICompatibleConfig()
	assert.False(t, ok)
}

func TestOpenAICompatibleHealthCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewOpenAICompatibleClient(server.URL+"/v1", "llama3.1", "")
	require.NoError(t, client.HealthCheck(context.Background()))
}

func TestResolveLocalModelForTierUsesEnvOverrides(t *testing.T) {
	t.Setenv("WISDEV_LLM_MODEL", "llama3.1")
	t.Setenv("WISDEV_LLM_MODEL_LIGHT", "tinyllama")
	t.Setenv("WISDEV_LLM_MODEL_HEAVY", "qwen2.5:7b")

	assert.Equal(t, "tinyllama", ResolveLocalModelForTier("light"))
	assert.Equal(t, "llama3.1", ResolveLocalModelForTier("standard"))
	assert.Equal(t, "qwen2.5:7b", ResolveLocalModelForTier("heavy"))

	client := NewOpenAICompatibleClient("http://127.0.0.1:11434/v1", "llama3.1", "")
	assert.Equal(t, "tinyllama", client.resolveModel("gemini-2.5-flash-lite"))
}

func TestModelAvailableOllamaNative(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3.1:latest"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &OpenAICompatibleClient{
		openAIBaseURL: server.URL + "/v1",
		ollamaRootURL: server.URL,
		apiStyle:      ollamaNativeAPIStyle,
		defaultModel:  "llama3.1",
		httpClient:    server.Client(),
	}
	available, resolved := client.ModelAvailable(context.Background())
	assert.True(t, available)
	assert.Contains(t, resolved, "llama3.1")
}

func TestLooksLikeManagedGeminiModel(t *testing.T) {
	assert.True(t, looksLikeManagedGeminiModel("gemini-2.0-flash"))
	assert.False(t, looksLikeManagedGeminiModel("llama3.1"))
}

func TestNewOpenAICompatibleClientFromEnv(t *testing.T) {
	t.Setenv("WISDEV_LLM_BASE_URL", "http://example.test/v1")
	t.Setenv("WISDEV_LLM_MODEL", "qwen2.5")
	client := NewOpenAICompatibleClientFromEnv()
	require.NotNil(t, client)
	assert.Equal(t, "qwen2.5", client.DefaultModel())
}

func TestWireDirectProvidersLocalOnlyProviderWithoutBaseURL(t *testing.T) {
	t.Setenv("WISDEV_LLM_BASE_URL", "")
	t.Setenv("WISDEV_LLM_PROVIDER", "ollama")
	client := NewClient()
	WireDirectProviders(context.Background(), client)
	require.NotNil(t, client.OpenAICompatible)
	assert.True(t, strings.Contains(client.OpenAICompatible.CredentialSource(), "11434") || client.OpenAICompatible.BackendName() == "openai_compatible")
}

