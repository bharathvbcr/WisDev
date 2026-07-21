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
	assert.Equal(t, 20, quality.MaxSearchTerms)
	assert.Equal(t, 24, quality.HitsPerSearch)
	assert.Equal(t, 144, quality.MaxUniquePapers)

	guidedBalanced := ResolveSearchBudget("balanced", WisDevModeGuided)
	yoloBalanced := ResolveSearchBudget("balanced", WisDevModeYOLO)
	assert.Equal(t, guidedBalanced, yoloBalanced)
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
	assert.Equal(t, ResolveModelNameForTier(complex.PrimaryModelTier), complex.PrimaryModelName)
	assert.GreaterOrEqual(t, complex.AllocatedTokens, 72000)
	assert.GreaterOrEqual(t, complex.MaxParallelism, 4)
}

func TestBuildResearchExecutionProfile_YoloUsesGuidedStableBudgets(t *testing.T) {
	query := "systematic review of sleep and memory consolidation mechanisms"
	guided := BuildResearchExecutionProfile(context.Background(), query, "guided", "balanced", false, 0)
	yolo := BuildResearchExecutionProfile(context.Background(), query, "yolo", "balanced", false, 0)

	assert.Equal(t, WisDevModeYOLO, yolo.Mode)
	assert.Equal(t, guided.SearchBudget, yolo.SearchBudget)
	assert.Equal(t, guided.MaxIterations, yolo.MaxIterations)
	assert.Equal(t, guided.AllocatedTokens, yolo.AllocatedTokens)
	assert.Equal(t, guided.PrimaryModelTier, yolo.PrimaryModelTier)
	assert.Equal(t, guided.PrimaryModelName, yolo.PrimaryModelName)
}
