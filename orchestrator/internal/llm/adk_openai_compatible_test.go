package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestADKOpenAICompatibleModelGenerateContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Equal(t, "llama3.1", payload["model"])
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"Local ADK answer"}}],
			"usage":{"prompt_tokens":12,"completion_tokens":4}
		}`))
	}))
	defer server.Close()

	client := NewOpenAICompatibleClient(server.URL+"/v1", "llama3.1", "")
	model, err := NewADKOpenAICompatibleModel(client)
	require.NoError(t, err)

	req := &adkmodel.LLMRequest{
		Contents: genai.Text("What is meniscus repair?"),
	}
	var (
		gotResp *adkmodel.LLMResponse
		gotErr  error
	)
	for resp, err := range model.GenerateContent(context.Background(), req, false) {
		gotResp = resp
		gotErr = err
		break
	}
	require.NoError(t, gotErr)
	require.NotNil(t, gotResp)
	require.NotNil(t, gotResp.Content)
	assert.Equal(t, "Local ADK answer", genaiContentText(gotResp.Content))
}

func TestADKOpenAICompatibleModelMapsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"wisdevSearchPapers","arguments":"{\"query\":\"ACL\"}"}}]}}],
			"usage":{"prompt_tokens":20,"completion_tokens":8}
		}`))
	}))
	defer server.Close()

	client := NewOpenAICompatibleClient(server.URL+"/v1", "llama3.1", "")
	model, err := NewADKOpenAICompatibleModel(client)
	require.NoError(t, err)

	req := &adkmodel.LLMRequest{
		Contents: genai.Text("search ACL papers"),
		Config: &genai.GenerateContentConfig{
			Tools: []*genai.Tool{{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name:        "wisdevSearchPapers",
					Description: "Search academic papers",
				}},
			}},
		},
	}
	var gotResp *adkmodel.LLMResponse
	for resp, err := range model.GenerateContent(context.Background(), req, false) {
		require.NoError(t, err)
		gotResp = resp
		break
	}
	require.NotNil(t, gotResp)
	require.NotEmpty(t, gotResp.Content.Parts)
	require.NotNil(t, gotResp.Content.Parts[0].FunctionCall)
	assert.Equal(t, "wisdevSearchPapers", gotResp.Content.Parts[0].FunctionCall.Name)
}

func TestLocalLLMProviderForced(t *testing.T) {
	t.Setenv("WISDEV_LLM_PROVIDER", "ollama")
	assert.True(t, LocalLLMProviderForced())
	t.Setenv("WISDEV_LLM_PROVIDER", "vertex")
	assert.False(t, LocalLLMProviderForced())
}
