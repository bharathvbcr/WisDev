package wisdev

import (
	"encoding/json"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolRegistry(t *testing.T) {
	r := NewToolRegistry()

	// Test List
	tools := r.List()
	assert.NotEmpty(t, tools)

	// Test Get
	tool, err := r.Get("research.retrievePapers")
	assert.NoError(t, err)
	assert.Equal(t, "research.retrievePapers", tool.Name)

	// Test Get not found
	_, err = r.Get("unknown")
	assert.ErrorIs(t, err, errToolNotFound)

	// Test Register
	r.Register(ToolDefinition{
		Name: "custom.tool",
		Risk: RiskLevelHigh,
	})
	custom, err := r.Get("custom.tool")
	assert.NoError(t, err)
	assert.Equal(t, ModelTierHeavy, custom.ModelTier)
}

func TestToolRegistry_RetrievePapersParameterSchema(t *testing.T) {
	r := NewToolRegistry()
	tool, err := r.Get("research.retrievePapers")
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(tool.ParameterSchema, &schema))
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)

	assert.JSONEq(t, string(search.SearchPapersToolSchema), string(tool.ParameterSchema))

	for _, key := range []string{"query", "limit", "domain", "sources", "retrievalStrategies", "stage2Rerank", "pageIndexRerank", "traceId", "yearFrom", "yearTo", "skipCache", "qualitySort"} {
		_, ok := properties[key]
		assert.True(t, ok, "missing schema key %s", key)
	}
}

func TestToolRegistry_CanonicalizesLegacyAliases(t *testing.T) {
	r := NewToolRegistry()

	tool, err := r.Get("research.searchPapers")
	require.NoError(t, err)
	assert.Equal(t, ActionResearchRetrievePapers, tool.Name)

	assert.Equal(t, ActionResearchSynthesizeAnswer, CanonicalizeWisdevAction("research.synthesize-answer"))
	assert.Equal(t, ActionResearchEvaluateEvidence, CanonicalizeWisdevAction("research.evaluate-evidence"))

	evaluateTool, err := r.Get(" research.evaluate-evidence ")
	require.NoError(t, err)
	assert.Equal(t, ActionResearchEvaluateEvidence, evaluateTool.Name)
	assert.Equal(t, ExecutionTargetPythonCapability, evaluateTool.ExecutionTarget)
}

func TestToolRegistry_CoordinateReplanIsLowRisk(t *testing.T) {
	r := NewToolRegistry()

	tool, err := r.Get("research.coordinateReplan")
	require.NoError(t, err)
	assert.Equal(t, RiskLevelLow, tool.Risk)
}

func TestBuildDeepAgentsCapabilities_UsesCanonicalActions(t *testing.T) {
	caps := BuildDeepAgentsCapabilities(NewToolRegistry())

	assert.Equal(t, "go-control-plane", caps.Backend)
	assert.True(t, caps.ToolsEnabled)
	assert.Contains(t, caps.WisdevActions, ActionResearchRetrievePapers)
	assert.Contains(t, caps.WisdevActions, ActionResearchSynthesizeAnswer)
	assert.Contains(t, caps.WisdevActions, ActionResearchEvaluateEvidence)
	assert.Contains(t, caps.WisdevActions, ActionResearchVerifyClaimsBatch)
	assert.NotContains(t, caps.WisdevActions, "research.searchPapers")
	assert.Equal(t, caps.WisdevActions, caps.AllowlistedTools)
	assert.Contains(t, caps.SensitiveWisdevActions, ActionResearchSynthesizeAnswer)
	assert.True(t, caps.PolicyByMode["guided"].RequireHumanConfirmation)
	assert.NotContains(t, caps.PolicyByMode["guided"].AllowlistedTools, ActionResearchSynthesizeAnswer)
	assert.False(t, caps.PolicyByMode["yolo"].RequireHumanConfirmation)
	assert.Contains(t, caps.PolicyByMode["yolo"].AllowlistedTools, ActionResearchSynthesizeAnswer)
}
