package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/require"
)

func TestGatewayYoloGoNativeActionsAreExposedThroughHTTPBoundary(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "test-internal-key")

	gateway := &wisdev.AgentGateway{
		Store:      wisdev.NewInMemorySessionStore(),
		Registry:   wisdev.NewToolRegistry(),
		SessionTTL: 1 * time.Hour,
	}
	gateway.Executor = wisdev.NewPlanExecutor(gateway.Registry, policy.DefaultPolicyConfig(), nil, nil, nil, nil, nil)

	router := NewRouter(ServerConfig{
		Version:      "test",
		AgentGateway: gateway,
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	client := server.Client()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/agent/tools", nil)
	require.NoError(t, err)
	req.Header.Set("X-Internal-Service-Key", "test-internal-key")
	req.Header.Set("X-User-Id", "internal-service")
	toolsResp, err := client.Do(req)
	require.NoError(t, err)
	defer toolsResp.Body.Close()
	require.Equal(t, http.StatusOK, toolsResp.StatusCode)

	var toolsPayload struct {
		Tools []wisdev.ToolDefinition `json:"tools"`
	}
	require.NoError(t, json.NewDecoder(toolsResp.Body).Decode(&toolsPayload))
	toolsByName := map[string]wisdev.ToolDefinition{}
	for _, tool := range toolsPayload.Tools {
		toolsByName[tool.Name] = tool
	}
	for _, action := range []string{
		wisdev.ActionResearchRetrievePapers,
		wisdev.ActionResearchResolveCanonicalCitations,
		wisdev.ActionResearchVerifyReasoningPaths,
		wisdev.ActionResearchVerifyClaimsBatch,
		wisdev.ActionResearchSynthesizeAnswer,
	} {
		tool, ok := toolsByName[action]
		require.True(t, ok, "missing Go-native tool from /agent/tools: %s", action)
		require.Equal(t, wisdev.ExecutionTargetGoNative, tool.ExecutionTarget, "execution target drift for %s", action)
	}

	createBody := []byte(`{"userId":"smoke-user","originalQuery":"graph neural scaling laws citation verification"}`)
	createReq, err := http.NewRequest(http.MethodPost, server.URL+"/agent/sessions", bytes.NewReader(createBody))
	require.NoError(t, err)
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Internal-Service-Key", "test-internal-key")
	createReq.Header.Set("X-User-Id", "smoke-user")
	createResp, err := client.Do(createReq)
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	var createPayload struct {
		Session *wisdev.AgentSession `json:"session"`
	}
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&createPayload))
	require.NotNil(t, createPayload.Session)
	createPayload.Session.Mode = wisdev.WisDevModeYOLO
	createPayload.Session.Status = wisdev.SessionGeneratingTree
	createPayload.Session.Plan = wisdev.BuildDefaultPlan(createPayload.Session)
	require.NoError(t, gateway.Store.Put(context.Background(), createPayload.Session, gateway.SessionTTL))

	executeReq, err := http.NewRequest(http.MethodPost, server.URL+"/agent/sessions/"+createPayload.Session.SessionID+"/execute", bytes.NewReader([]byte(`{}`)))
	require.NoError(t, err)
	executeReq.Header.Set("Content-Type", "application/json")
	executeReq.Header.Set("X-Internal-Service-Key", "test-internal-key")
	executeReq.Header.Set("X-User-Id", "smoke-user")
	executeResp, err := client.Do(executeReq)
	require.NoError(t, err)
	defer executeResp.Body.Close()
	require.Equal(t, http.StatusOK, executeResp.StatusCode)

	var executePayload struct {
		OK          bool   `json:"ok"`
		SessionID   string `json:"sessionId"`
		ExecutionID string `json:"executionId"`
		Status      string `json:"status"`
	}
	require.NoError(t, json.NewDecoder(executeResp.Body).Decode(&executePayload))
	require.True(t, executePayload.OK)
	require.Equal(t, createPayload.Session.SessionID, executePayload.SessionID)
	require.NotEmpty(t, executePayload.ExecutionID)
	require.NotEmpty(t, executePayload.Status)
}
