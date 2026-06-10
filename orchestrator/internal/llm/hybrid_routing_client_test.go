package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

func TestStructuredOutputHybridCloudFirstUsesVertexForHeavy(t *testing.T) {
	t.Setenv("WISDEV_LLM_PROVIDER", "hybrid")
	t.Setenv("WISDEV_LLM_BASE_URL", "http://127.0.0.1:11434/v1")

	localCalled := false
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		http.Error(w, "should not be primary", http.StatusInternalServerError)
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
		Prompt:       "synthesize evidence",
		JsonSchema:   `{"type":"object","properties":{"ok":{"type":"boolean"}}}`,
		RequestClass: "structured_high_value",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.GetJsonResult(), `"ok":true`)
	assert.False(t, localCalled, "hybrid heavy calls should prefer cloud before local")
	mockModels.AssertExpectations(t)
}

func TestStructuredOutputHybridLocalFirstUsesOllamaForLight(t *testing.T) {
	t.Setenv("WISDEV_LLM_PROVIDER", "hybrid")
	t.Setenv("WISDEV_LLM_BASE_URL", "http://127.0.0.1:11434/v1")

	vertexCalled := false
	mockModels := new(mockGenAIModels)
	mockModels.On("GenerateContent", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			vertexCalled = true
		}).
		Return(&genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: `{"ok":true}`}}}},
			},
		}, nil).Maybe()

	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"ok\":true}"}}],
			"usage":{"prompt_tokens":1,"completion_tokens":1}
		}`))
	}))
	defer openAI.Close()

	client := &Client{
		OpenAICompatible: NewOpenAICompatibleClient(openAI.URL+"/v1", "llama3.1", ""),
		VertexDirect:     &VertexClient{client: mockModels, backend: "vertex_ai"},
	}

	resp, err := client.StructuredOutput(context.Background(), &llmv1.StructuredRequest{
		Prompt:       "classify query",
		JsonSchema:   `{"type":"object","properties":{"ok":{"type":"boolean"}}}`,
		RequestClass: "light",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.GetJsonResult(), `"ok":true`)
	assert.False(t, vertexCalled, "hybrid light calls should prefer local before cloud")
}
