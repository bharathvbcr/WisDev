package llm

import (
	"testing"

	llmpb "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
	"github.com/stretchr/testify/assert"
)

func TestHybridStructuredOutputRouting(t *testing.T) {
	t.Setenv("WISDEV_LLM_PROVIDER", "hybrid")
	t.Setenv("WISDEV_LLM_BASE_URL", "http://127.0.0.1:11434/v1")

	light := &llmpb.StructuredRequest{RequestClass: "light"}
	standard := &llmpb.StructuredRequest{RequestClass: "standard"}
	heavy := &llmpb.StructuredRequest{RequestClass: "heavy"}
	highValue := &llmpb.StructuredRequest{RequestClass: "structured_high_value"}

	assert.True(t, HybridStructuredOutputPrefersLocal(light))
	assert.True(t, HybridStructuredOutputPrefersLocal(standard))
	assert.False(t, HybridStructuredOutputPrefersCloud(light))
	assert.False(t, HybridStructuredOutputPrefersCloud(standard))

	assert.True(t, HybridStructuredOutputPrefersCloud(heavy))
	assert.True(t, HybridStructuredOutputPrefersCloud(highValue))
	assert.False(t, HybridStructuredOutputPrefersLocal(heavy))

	t.Setenv("WISDEV_LLM_PROVIDER", "cloud")
	assert.False(t, HybridStructuredOutputPrefersCloud(heavy))
	assert.False(t, HybridStructuredOutputPrefersLocal(light))
}

func TestResolveStructuredRequestClassDefaultsToStandard(t *testing.T) {
	assert.Equal(t, RequestClassStandard, resolveStructuredRequestClass(nil))
	assert.Equal(t, RequestClassStandard, resolveStructuredRequestClass(&llmpb.StructuredRequest{}))
	assert.Equal(t, RequestClassHeavy, resolveStructuredRequestClass(&llmpb.StructuredRequest{RequestClass: "heavy"}))
}
