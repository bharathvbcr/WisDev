package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeSubscriptionTier(t *testing.T) {
	cases := []struct {
		in     string
		want   SubscriptionTier
		wantOK bool
	}{
		{"free", TierFree, true},
		{"PRO", TierPro, true},
		{" advanced ", TierAdvanced, true},
		{"", TierFree, false},
		{"enterprise", TierFree, false}, // unknown -> least privilege
		{"gold", TierFree, false},
	}
	for _, c := range cases {
		got, ok := NormalizeSubscriptionTier(c.in)
		assert.Equalf(t, c.want, got, "tier for %q", c.in)
		assert.Equalf(t, c.wantOK, ok, "ok for %q", c.in)
	}
}

func TestSubscriptionTierAtLeast(t *testing.T) {
	assert.True(t, SubscriptionTierAtLeast(TierPro, TierPro))
	assert.True(t, SubscriptionTierAtLeast(TierAdvanced, TierPro))
	assert.False(t, SubscriptionTierAtLeast(TierFree, TierPro))
	assert.True(t, SubscriptionTierAtLeast(TierFree, TierFree))
	assert.False(t, SubscriptionTierAtLeast(TierPro, TierAdvanced))
}

func TestNormalizeModelTier_BalancedLegacyAlias(t *testing.T) {
	// balanced is a legacy alias for the canonical "standard" model tier.
	assert.Equal(t, "standard", NormalizeModelTier("balanced"))
	assert.Equal(t, "standard", NormalizeModelTier("BALANCED"))
	assert.Equal(t, "standard", NormalizeModelTier("standard"))
	assert.Equal(t, "light", NormalizeModelTier("light"))
	assert.Equal(t, "heavy", NormalizeModelTier("heavy"))
	// unknown model tier defaults to standard (never silently "heavy").
	assert.Equal(t, "standard", NormalizeModelTier("ultra"))
}

func TestQualityModeAvailabilityMatrix(t *testing.T) {
	type row struct {
		mode      QualityMode
		free      bool
		pro       bool
		advanced  bool
	}
	rows := []row{
		{QualityFast, true, true, true},
		{QualityBalanced, true, true, true},
		{QualityQuality, false, true, true}, // requires >= pro
	}
	for _, r := range rows {
		assert.Equalf(t, r.free, IsQualityModeAvailable(r.mode, TierFree), "%s@free", r.mode)
		assert.Equalf(t, r.pro, IsQualityModeAvailable(r.mode, TierPro), "%s@pro", r.mode)
		assert.Equalf(t, r.advanced, IsQualityModeAvailable(r.mode, TierAdvanced), "%s@advanced", r.mode)
	}
	// Unknown mode is never available.
	assert.False(t, IsQualityModeAvailable(QualityMode("ludicrous"), TierAdvanced))
}

func TestGatedQualityModes(t *testing.T) {
	free := GatedQualityModes(TierFree)
	require.Len(t, free, 2)
	assert.Equal(t, QualityFast, free[0].Name)     // ordered lightest->heaviest
	assert.Equal(t, QualityBalanced, free[1].Name)

	pro := GatedQualityModes(TierPro)
	require.Len(t, pro, 3)
	assert.Equal(t, QualityQuality, pro[2].Name)

	adv := GatedQualityModes(TierAdvanced)
	require.Len(t, adv, 3)

	// Ported feature values must be preserved exactly (spot-check the extremes).
	assert.Equal(t, 50, free[0].Features.MaxPapersPerSearch)
	assert.Equal(t, 1, free[0].Features.APITierAccess)
	assert.Equal(t, "brief", free[0].Features.AISummaryDepth)
	assert.Equal(t, 200, pro[2].Features.MaxPapersPerSearch)
	assert.Equal(t, 4, pro[2].Features.APITierAccess)
	assert.True(t, pro[2].Features.AllowProviderSelection)
}

func TestQualityModeByName_StandardAliasesBalanced(t *testing.T) {
	cfg, ok := QualityModeByName("standard")
	require.True(t, ok)
	assert.Equal(t, QualityBalanced, cfg.Name)

	cfg, ok = QualityModeByName("  Balanced ")
	require.True(t, ok)
	assert.Equal(t, QualityBalanced, cfg.Name)

	_, ok = QualityModeByName("nonexistent")
	assert.False(t, ok)
}

func TestDefaultQualityMode(t *testing.T) {
	for _, tier := range []SubscriptionTier{TierFree, TierPro, TierAdvanced} {
		assert.Equal(t, QualityBalanced, DefaultQualityMode(tier))
	}
}

func TestProviderTierGating(t *testing.T) {
	// Exact-tier counts (parity with getProvidersByTier).
	assert.Len(t, ProvidersAtTier(1), 3)
	assert.Len(t, ProvidersAtTier(2), 4)
	assert.Len(t, ProvidersAtTier(3), 10)
	assert.Len(t, ProvidersAtTier(4), 7)

	// Cumulative access counts.
	assert.Len(t, ProvidersUpToTier(1), 3)
	assert.Len(t, ProvidersUpToTier(2), 7)
	assert.Len(t, ProvidersUpToTier(3), 17)
	assert.Len(t, ProvidersUpToTier(4), 24)

	// Tier-1 core providers are always present and ordered first.
	ids := ProviderIDs(ProvidersUpToTier(1))
	assert.Equal(t, []string{"opensearch_hybrid", "semanticscholar", "openalex"}, ids)
}

func TestProvidersForSubscription(t *testing.T) {
	// Default quality mode is Balanced (APITierAccess=3) for every tier.
	assert.Len(t, ProvidersForSubscription(TierFree), 17)
	assert.Len(t, ProvidersForSubscription(TierPro), 17)
	assert.Len(t, ProvidersForSubscription(TierAdvanced), 17)
}
