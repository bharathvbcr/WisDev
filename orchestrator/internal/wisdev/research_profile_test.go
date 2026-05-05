package wisdev

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeResearchQualityMode(t *testing.T) {
	assert.Equal(t, "quality", NormalizeResearchQualityMode("high"))
	assert.Equal(t, "quality", NormalizeResearchQualityMode("rigorous"))
	assert.Equal(t, "fast", NormalizeResearchQualityMode("fast"))
	assert.Equal(t, "balanced", NormalizeResearchQualityMode("unknown"))
}

func TestResolveSearchBudget(t *testing.T) {
	quality := ResolveSearchBudget("quality", WisDevModeGuided)
	assert.Equal(t, 16, quality.MaxSearchTerms)
	assert.Equal(t, 16, quality.HitsPerSearch)
	assert.Equal(t, 96, quality.MaxUniquePapers)

	yolo := ResolveSearchBudget("balanced", WisDevModeYOLO)
	assert.Equal(t, 10, yolo.MaxSearchTerms)
	assert.Equal(t, 16, yolo.HitsPerSearch)
	assert.Equal(t, 68, yolo.MaxUniquePapers)
}

func TestBuildResearchExecutionProfile(t *testing.T) {
	profile := BuildResearchExecutionProfile(context.Background(), "sleep and memory", "guided", "fast", true, 0)
	assert.Equal(t, WisDevModeGuided, profile.Mode)
	assert.Equal(t, ServiceTierPriority, profile.ServiceTier)
	assert.Equal(t, ModelTierStandard, profile.PrimaryModelTier)
	assert.Equal(t, ResolveModelNameForTier(ModelTierStandard), profile.PrimaryModelName)
	assert.Equal(t, 2, profile.MaxIterations)

	complex := BuildResearchExecutionProfile(
		context.Background(),
		"quantum reinforcement learning versus traditional methods for treatment efficacy and safety comparison",
		"yolo",
		"quality",
		false,
		0,
	)
	assert.Equal(t, WisDevModeYOLO, complex.Mode)
	assert.Equal(t, ServiceTierFlex, complex.ServiceTier)
	assert.Equal(t, ModelTierHeavy, complex.PrimaryModelTier)
	assert.Equal(t, ResolveModelNameForTier(ModelTierHeavy), complex.PrimaryModelName)
	assert.GreaterOrEqual(t, complex.AllocatedTokens, 72000)
	assert.GreaterOrEqual(t, complex.MaxParallelism, 4)
}
