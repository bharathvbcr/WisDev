package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestHypothesisService_UsesDefaultModelAndParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/llm/structured-output", r.URL.Path)

		var payload struct {
			Prompt          string `json:"prompt"`
			Model           string `json:"model"`
			RequestClass    string `json:"requestClass"`
			RetryProfile    string `json:"retryProfile"`
			ServiceTier     string `json:"serviceTier"`
			LatencyBudgetMs int32  `json:"latencyBudgetMs"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Contains(t, payload.Prompt, "query: sleep and memory")
		assertWisdevStructuredPromptHygiene(t, payload.Prompt)
		require.Equal(t, llm.ResolveStandardModel(), payload.Model)
		assert.Equal(t, "standard", payload.RequestClass)
		assert.Equal(t, "standard", payload.RetryProfile)
		assert.Equal(t, "standard", payload.ServiceTier)
		assert.Greater(t, payload.LatencyBudgetMs, int32(0))

		body := map[string]any{"jsonResult": `[{"claim":"c1","falsifiabilityCondition":"if A and B","confidenceThreshold":0.71}]`}
		require.NoError(t, json.NewEncoder(w).Encode(body))
	}))
	t.Cleanup(server.Close)

	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)
	client := llm.NewClient()

	service := NewHypothesisService(client)
	hypos, err := service.ProposeHypotheses(context.Background(), "query: sleep and memory", "understand", "")
	require.NoError(t, err)
	require.Len(t, hypos, 1)
	assert.Equal(t, "c1", hypos[0].Claim)
}

func TestHypothesisService_ExplicitModelPropagated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/llm/structured-output", r.URL.Path)
		var payload struct {
			Model           string `json:"model"`
			RequestClass    string `json:"requestClass"`
			RetryProfile    string `json:"retryProfile"`
			ServiceTier     string `json:"serviceTier"`
			LatencyBudgetMs int32  `json:"latencyBudgetMs"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "custom-model", payload.Model)
		assert.Equal(t, "standard", payload.RequestClass)
		assert.Equal(t, "standard", payload.RetryProfile)
		assert.Equal(t, "standard", payload.ServiceTier)
		assert.Greater(t, payload.LatencyBudgetMs, int32(0))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"jsonResult": `[]`}))
	}))
	t.Cleanup(server.Close)
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

	service := NewHypothesisService(llm.NewClient())
	hypos, err := service.ProposeHypotheses(context.Background(), "q", "i", "custom-model")
	require.NoError(t, err)
	assert.Empty(t, hypos)
}

func TestHypothesisService_CooldownFallback(t *testing.T) {
	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	service := NewHypothesisService(client)

	msc.On("StructuredOutput", mock.Anything, mock.Anything).
		Return(nil, errors.New("vertex structured output provider cooldown active; retry after 45s")).Once()

	hypos, err := service.ProposeHypotheses(context.Background(), "sleep and memory", "understand", "")

	require.NoError(t, err)
	require.NotEmpty(t, hypos)
	assert.Contains(t, hypos[0].Claim, "sleep and memory")
	msc.AssertExpectations(t)
}

func TestHypothesisService_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"jsonResult": `{"broken":`}))
	}))
	t.Cleanup(server.Close)
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

	service := NewHypothesisService(llm.NewClient())
	_, err := service.ProposeHypotheses(context.Background(), "q", "i", "model")
	require.Error(t, err)
}
