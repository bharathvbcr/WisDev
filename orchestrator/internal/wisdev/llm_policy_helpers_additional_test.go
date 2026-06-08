package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmpb "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendWisdevStructuredOutputInstruction(t *testing.T) {
	assert.Equal(t, wisdevStructuredOutputSchemaInstruction, appendWisdevStructuredOutputInstruction("   "))
	assert.Equal(
		t,
		"Return JSON only.\n\n"+wisdevStructuredOutputSchemaInstruction,
		appendWisdevStructuredOutputInstruction(" Return JSON only. "),
	)
}

func TestNormalizeWisdevGeneratedText(t *testing.T) {
	trimmed, err := normalizeWisdevGeneratedText("specialist execution", &llmpb.GenerateResponse{Text: "  grounded output  "})
	require.NoError(t, err)
	assert.Equal(t, "grounded output", trimmed)

	_, err = normalizeWisdevGeneratedText("specialist execution", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned nil response")

	_, err = normalizeWisdevGeneratedText("specialist execution", &llmpb.GenerateResponse{Text: "   "})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned empty text")
}

func TestWisdevPolicyHelpersApplyExpectedPolicies(t *testing.T) {
	heavyStructured := applyWisdevHeavyStructuredPolicy(&llmpb.StructuredRequest{Prompt: "hello"})
	require.NotNil(t, heavyStructured.ThinkingBudget)
	assert.Equal(t, llm.ResolveHeavyModel(), heavyStructured.Model)
	assert.Equal(t, "priority", heavyStructured.ServiceTier)
	assert.Equal(t, "structured_high_value", heavyStructured.RequestClass)
	assert.Equal(t, int32(-1), *heavyStructured.ThinkingBudget)

	standardStructured := applyWisdevStandardStructuredPolicy(&llmpb.StructuredRequest{Prompt: "hello"})
	require.NotNil(t, standardStructured.ThinkingBudget)
	assert.Equal(t, llm.ResolveStandardModel(), standardStructured.Model)
	assert.Equal(t, "standard", standardStructured.ServiceTier)
	assert.Equal(t, "standard", standardStructured.RequestClass)
	assert.Equal(t, int32(1024), *standardStructured.ThinkingBudget)

	recoverableStructured := applyWisdevRecoverableStructuredPolicy(&llmpb.StructuredRequest{Prompt: "hello"})
	require.NotNil(t, recoverableStructured.ThinkingBudget)
	assert.Equal(t, llm.ResolveStandardModel(), recoverableStructured.Model)
	assert.Equal(t, "standard", recoverableStructured.ServiceTier)
	assert.Equal(t, "standard", recoverableStructured.RequestClass)
	assert.Equal(t, int32(1024), *recoverableStructured.ThinkingBudget)

	standardGenerate := applyWisdevStandardGeneratePolicy(&llmpb.GenerateRequest{Prompt: "hello"})
	require.NotNil(t, standardGenerate.ThinkingBudget)
	assert.Equal(t, llm.ResolveStandardModel(), standardGenerate.Model)
	assert.Equal(t, "standard", standardGenerate.ServiceTier)
	assert.Equal(t, "standard", standardGenerate.RequestClass)
	assert.Equal(t, int32(1024), *standardGenerate.ThinkingBudget)

	lightGenerate := applyWisdevLightGeneratePolicy(&llmpb.GenerateRequest{Prompt: "hello"})
	require.NotNil(t, lightGenerate.ThinkingBudget)
	assert.Equal(t, llm.ResolveLightModel(), lightGenerate.Model)
	assert.Equal(t, "standard", lightGenerate.ServiceTier)
	assert.Equal(t, "light", lightGenerate.RequestClass)
	assert.Equal(t, int32(0), *lightGenerate.ThinkingBudget)
}

func TestParseResearchComplexityRejectsInvalidJSON(t *testing.T) {
	_, err := parseResearchComplexity(`{"complexity":`)
	require.Error(t, err)
}
