package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPlannerService_DecomposeTask(t *testing.T) {
	t.Run("requiresQuery", func(t *testing.T) {
		service := NewPlannerService(nil)
		_, err := service.DecomposeTask(context.Background(), "", "domain", "")
		require.Error(t, err)
	})

	t.Run("returnsParsedTasksAndDefaultModel", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/llm/structured-output", r.URL.Path)
			require.Equal(t, http.MethodPost, r.Method)

			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			assertWisdevStructuredPromptHygiene(t, payload["prompt"].(string))
			assert.Equal(t, llm.ResolveStandardModel(), payload["model"])
			assert.Equal(t, "standard", payload["requestClass"])
			assert.Equal(t, "standard", payload["serviceTier"])
			assert.Equal(t, float64(1024), payload["thinkingBudget"])
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `[{"id":"t1","name":"Collect papers","action":"search","dependsOnIds":[]} ]`,
			}))
		}))
		t.Cleanup(server.Close)
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

		service := NewPlannerService(llm.NewClient())
		tasks, err := service.DecomposeTask(context.Background(), "sleep and memory", "cs", "")
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		assert.Equal(t, "t1", tasks[0].ID)
	})

	t.Run("parseFailure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"json_result": `{"broken":`,
			}))
		}))
		t.Cleanup(server.Close)
		t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
		t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

		service := NewPlannerService(llm.NewClient())
		_, err := service.DecomposeTask(context.Background(), "q", "d", "standard")
		require.Error(t, err)
	})
}

func TestPlannerService_CoordinateReplanAndFollowup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/llm/structured-output":
		default:
			http.NotFound(w, r)
			return
		}

		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assertWisdevStructuredPromptHygiene(t, payload["prompt"].(string))
		if strings.Contains(payload["prompt"].(string), "Assess the complexity of this research query") {
			assert.Equal(t, "light", payload["requestClass"])
			assert.Equal(t, "standard", payload["serviceTier"])
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `{"complexity":"high"}`,
			}))
			return
		}
		assert.Equal(t, "standard", payload["requestClass"])
		assert.Equal(t, "standard", payload["serviceTier"])
		if strings.Contains(payload["prompt"].(string), "Analyze this query") {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `{"isAmbiguous": false, "question": ""}`,
			}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `[{"id":"r1","name":"replan","action":"rerun"}]`,
		}))
	}))
	t.Cleanup(server.Close)

	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", server.URL)

	service := NewPlannerService(llm.NewClient())
	replans, err := service.CoordinateReplan(context.Background(), "step-1", "timeout", map[string]any{"x": 1}, "")
	require.NoError(t, err)
	require.Len(t, replans, 1)
	assert.Equal(t, "r1", replans[0].ID)

	complexity, err := service.AssessResearchComplexity(context.Background(), "what is sleep learning?")
	require.NoError(t, err)
	assert.Equal(t, "high", complexity)

	followup, err := service.AskFollowUpIfAmbiguous(context.Background(), "need scope", "")
	require.NoError(t, err)
	assert.NotNil(t, followup)
}

func TestPlannerService_CooldownFallbacks(t *testing.T) {
	msc := &mockLLMServiceClient{}
	client := llm.NewClient()
	client.SetClient(msc)
	service := NewPlannerService(client)
	cooldownErr := errors.New("vertex structured output provider cooldown active; retry after 45s")

	msc.On("StructuredOutput", mock.Anything, mock.Anything).
		Return(nil, cooldownErr).Times(4)

	tasks, err := service.DecomposeTask(context.Background(), "sleep memory consolidation", "neuroscience", "")
	require.NoError(t, err)
	require.NotEmpty(t, tasks)
	assert.NotEmpty(t, tasks[0].ID)

	replans, err := service.CoordinateReplan(context.Background(), "step-1", "timeout", map[string]any{"attempt": 1}, "")
	require.NoError(t, err)
	require.NotEmpty(t, replans)
	assert.Contains(t, replans[0].DependsOnIDs, "step-1")

	complexity, err := service.AssessResearchComplexity(context.Background(), "sleep")
	require.NoError(t, err)
	assert.Equal(t, "low", complexity)

	followup, err := service.AskFollowUpIfAmbiguous(context.Background(), "need scope", "")
	require.NoError(t, err)
	assert.Equal(t, false, followup["isAmbiguous"])
	assert.Equal(t, true, followup["degraded"])

	msc.AssertExpectations(t)
}

func TestPlannerService_NilClient(t *testing.T) {
	service := NewPlannerService(nil)
	_, err := service.AssessResearchComplexity(context.Background(), "q")
	require.Error(t, err)
}

func TestParseResearchComplexity(t *testing.T) {
	complexity, err := parseResearchComplexity(`{"complexity":" HIGH "}`)
	require.NoError(t, err)
	assert.Equal(t, "high", complexity)

	_, err = parseResearchComplexity(`{"complexity":"unclear"}`)
	require.Error(t, err)
}
