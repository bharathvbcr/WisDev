package wisdev

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func TestMCPStatusIncludesLLMDirect(t *testing.T) {
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"llama3.1"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer openAI.Close()

	llmClient := llm.NewClient()
	llmClient.OpenAICompatible = llm.NewOpenAICompatibleClient(openAI.URL+"/v1", "llama3.1", "")

	gw := &AgentGateway{
		SearchRegistry: search.BuildRegistry(),
		LLMClient:      llmClient,
	}
	status := gw.MCPStatus()
	llmDirect, ok := status["llmDirect"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, llmDirect["configured"])
	assert.Equal(t, true, llmDirect["healthy"])
}

func TestMCPStatusHTTPEndpointIncludesLLMDirect(t *testing.T) {
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"llama3.1"}]}`))
	}))
	defer openAI.Close()

	llmClient := llm.NewClient()
	llmClient.OpenAICompatible = llm.NewOpenAICompatibleClient(openAI.URL+"/v1", "llama3.1", "")
	gw := &AgentGateway{
		SearchRegistry: search.BuildRegistry(),
		LLMClient:      llmClient,
	}

	mux := http.NewServeMux()
	gw.RegisterMCPRoutes(mux, MCPRouteConfig{})

	req := httptest.NewRequest(http.MethodGet, "/wisdev/mcp/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	llmDirect, ok := payload["llmDirect"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, llmDirect["configured"])
}
