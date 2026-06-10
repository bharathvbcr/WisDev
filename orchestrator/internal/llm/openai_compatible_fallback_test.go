package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

func TestStructuredOutputOpenAICompatibleFallsBackToSidecar(t *testing.T) {
	t.Setenv("WISDEV_LLM_PROVIDER", "ollama")
	t.Setenv("WISDEV_LLM_BASE_URL", "http://127.0.0.1:11434/v1")

	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer openAI.Close()

	sidecar := &mockLLMServiceClient{}
	sidecar.On("StructuredOutput", mock.Anything, mock.Anything).
		Return(&llmv1.StructuredResponse{JsonResult: `{"ok":true}`}, nil).Once()

	client := &Client{
		OpenAICompatible: NewOpenAICompatibleClient(openAI.URL+"/v1", "llama3.1", ""),
	}
	client.SetClient(sidecar)

	resp, err := client.StructuredOutput(context.Background(), &llmv1.StructuredRequest{
		Prompt:     "test",
		JsonSchema: `{"type":"object","properties":{"ok":{"type":"boolean"}}}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.GetJsonResult(), `"ok":true`)
	sidecar.AssertExpectations(t)
}

func TestDirectProviderStatusOpenAIHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer server.Close()

	client := &Client{
		OpenAICompatible: NewOpenAICompatibleClient(server.URL+"/v1", "llama3.1", ""),
	}
	status := client.DirectProviderStatus(context.Background())
	assert.Equal(t, true, status["configured"])
	assert.Equal(t, true, status["healthy"])
	assert.Equal(t, "llama3.1", status["model"])
}

func TestStructuredOutputOpenAICompatibleFallsBackToVertex(t *testing.T) {
	t.Setenv("WISDEV_LLM_PROVIDER", "hybrid")
	t.Setenv("WISDEV_LLM_BASE_URL", "http://127.0.0.1:11434/v1")

	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "connection refused", http.StatusServiceUnavailable)
	}))
	defer openAI.Close()

	mockModels := new(mockGenAIModels)
	mockModels.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: `{"ok":true}`}}}},
			},
		}, nil).Once()

	client := &Client{
		OpenAICompatible: NewOpenAICompatibleClient(openAI.URL+"/v1", "llama3.1", ""),
		VertexDirect:     &VertexClient{client: mockModels, backend: "vertex_ai"},
	}

	resp, err := client.StructuredOutput(context.Background(), &llmv1.StructuredRequest{
		Prompt:     "test",
		JsonSchema: `{"type":"object","properties":{"ok":{"type":"boolean"}}}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.GetJsonResult(), `"ok":true`)
	mockModels.AssertExpectations(t)
}
