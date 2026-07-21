package policy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResearchModes_Registry(t *testing.T) {
	require.Len(t, researchModeOrder, 4)
	for _, mode := range researchModeOrder {
		cfg, ok := researchModes[mode]
		require.Truef(t, ok, "missing mode %s", mode)
		assert.Equal(t, mode, cfg.Name)
		assert.NotEmpty(t, cfg.DisplayName)
		assert.NotEmpty(t, cfg.Providers)
	}
}

func TestCanAccessResearchMode(t *testing.T) {
	assert.True(t, CanAccessResearchMode(ResearchExploration, TierFree))
	assert.False(t, CanAccessResearchMode(ResearchReview, TierFree))
	assert.False(t, CanAccessResearchMode(ResearchMethodology, TierFree))
	assert.False(t, CanAccessResearchMode(ResearchGapFinding, TierFree))

	assert.True(t, CanAccessResearchMode(ResearchReview, TierPro))
	assert.True(t, CanAccessResearchMode(ResearchGapFinding, TierAdvanced))
}

func TestModeProviders_FreeFallsBackToExploration(t *testing.T) {
	pro := ModeProviders(ResearchReview, TierPro)
	assert.Equal(t, researchModes[ResearchReview].Providers, pro)

	free := ModeProviders(ResearchReview, TierFree)
	assert.Equal(t, researchModes[ResearchExploration].Providers, free)
}

func TestModeRankingWeights(t *testing.T) {
	exploration := ModeRankingWeights(ResearchExploration)
	assert.Equal(t, 0.4, exploration.Citations)
	assert.Equal(t, 0.5, exploration.Relevance)

	gap := ModeRankingWeights(ResearchGapFinding)
	assert.Greater(t, gap.Recency, 0.5)
	assert.Equal(t, 0.4, gap.CitationVelocity)
}

func TestModeUsesHyDE(t *testing.T) {
	assert.True(t, ModeUsesHyDE(ResearchExploration))
	assert.False(t, ModeUsesHyDE(ResearchReview))
}

func TestGatedResearchModes(t *testing.T) {
	free := GatedResearchModes(TierFree)
	require.Len(t, free, 1)
	assert.Equal(t, ResearchExploration, free[0].Name)

	pro := GatedResearchModes(TierPro)
	require.Len(t, pro, 4)
}

func TestHyDEPrompt_SubstitutesQuery(t *testing.T) {
	prompt := HyDEPrompt(ResearchExploration, "machine learning")
	assert.Contains(t, prompt, "machine learning")
	assert.NotContains(t, prompt, "{query}")
}

func TestGapFindingYearFilter(t *testing.T) {
	cfg := researchModes[ResearchGapFinding]
	require.NotNil(t, cfg.Filters)
	require.NotNil(t, cfg.Filters.YearRange)
	require.NotNil(t, cfg.Filters.YearRange.Min)
	assert.Equal(t, time.Now().Year()-2, *cfg.Filters.YearRange.Min)
}

func TestNormalizeResearchMode(t *testing.T) {
	mode, ok := NormalizeResearchMode("  REVIEW ")
	assert.True(t, ok)
	assert.Equal(t, ResearchReview, mode)

	mode, ok = NormalizeResearchMode("unknown")
	assert.False(t, ok)
	assert.Equal(t, ResearchExploration, mode)
}

func TestResolveSearchPriority(t *testing.T) {
	balanced := ResolveSearchPriority(ProfileBalanced, TierPro)
	assert.Equal(t, QualityBalanced, balanced.QualityMode)
	assert.Equal(t, ResearchExploration, balanced.SearchResearchMode)
	assert.Nil(t, balanced.ProviderLimit)
	assert.Equal(t, MaxSystematicReviewTabConcurrency, balanced.MaxTabConcurrency)
	assert.True(t, balanced.AllowPreferredSources)

	aiPro := ResolveSearchPriority(ProfileAIFirst, TierPro)
	assert.Equal(t, QualityQuality, aiPro.QualityMode)
	require.NotNil(t, aiPro.ProviderLimit)
	assert.Equal(t, 8, *aiPro.ProviderLimit)

	aiFree := ResolveSearchPriority(ProfileAIFirst, TierFree)
	assert.Equal(t, QualityBalanced, aiFree.QualityMode)
	require.NotNil(t, aiFree.ProviderLimit)
	assert.Equal(t, 3, *aiFree.ProviderLimit)

	cost := ResolveSearchPriority(ProfileCostOptimized, TierPro)
	assert.Equal(t, QualityFast, cost.QualityMode)
	require.NotNil(t, cost.ProviderLimit)
	assert.Equal(t, 2, *cost.ProviderLimit)
}

func TestNormalizeSearchPriorityProfile(t *testing.T) {
	p, ok := NormalizeSearchPriorityProfile("ai-first")
	assert.True(t, ok)
	assert.Equal(t, ProfileAIFirst, p)

	p, ok = NormalizeSearchPriorityProfile("cost_optimized")
	assert.True(t, ok)
	assert.Equal(t, ProfileCostOptimized, p)

	p, ok = NormalizeSearchPriorityProfile("unknown")
	assert.False(t, ok)
	assert.Equal(t, ProfileBalanced, p)
}

func TestWisDevResearchModeForScope(t *testing.T) {
	cfg := ResolveSearchPriority(ProfileBalanced, TierPro)
	assert.Equal(t, ResearchExploration, WisDevResearchModeForScope("focused", cfg))
	assert.Equal(t, ResearchReview, WisDevResearchModeForScope("broad", cfg))
}
