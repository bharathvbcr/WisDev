package policy

// Search-priority policy: Go-owned port of frontend/config/searchPriority.ts
// (+ profile → quality-mode mapping previously in featureTierRuntime.ts).
//
// CANONICAL OWNER: Go. Profile → quality mode, research mode, provider limit,
// and tab concurrency live here. The frontend selects a profile preference
// (env / UI) and FETCHes the resolved config via GET /api/config/quality-modes
// (?profile=) or GET /api/config/search-priority.

import "strings"

// SearchPriorityProfile is the deployment/search-cost preference knob.
type SearchPriorityProfile string

const (
	ProfileAIFirst       SearchPriorityProfile = "ai_first"
	ProfileBalanced      SearchPriorityProfile = "balanced"
	ProfileCostOptimized SearchPriorityProfile = "cost_optimized"
)

// MaxSystematicReviewTabConcurrency is the server-owned tab concurrency cap
// (aligned with systematic_review_search.go and FE searchConcurrency.ts).
const MaxSystematicReviewTabConcurrency = 2

// SearchPriorityConfig is the resolved per-profile search policy.
type SearchPriorityConfig struct {
	Profile              SearchPriorityProfile `json:"profile"`
	QualityMode          QualityMode           `json:"qualityMode"`
	SearchResearchMode   ResearchMode          `json:"searchResearchMode"`
	WisDevFocusedMode    ResearchMode          `json:"wisdevFocusedMode"`
	WisDevBroadMode      ResearchMode          `json:"wisdevBroadMode"`
	ProviderLimit        *int                  `json:"providerLimit,omitempty"`
	MaxTabConcurrency    int                   `json:"maxTabConcurrency"`
	AllowPreferredSources bool                 `json:"allowPreferredSources"`
	EnforceQualityMode   bool                  `json:"enforceQualityMode"`
}

var searchPriorityProfiles = map[SearchPriorityProfile]SearchPriorityConfig{
	ProfileAIFirst: {
		Profile:               ProfileAIFirst,
		QualityMode:           QualityQuality,
		SearchResearchMode:    ResearchReview,
		WisDevFocusedMode:     ResearchExploration,
		WisDevBroadMode:       ResearchReview,
		ProviderLimit:         intPtr(8),
		MaxTabConcurrency:     MaxSystematicReviewTabConcurrency,
		AllowPreferredSources: false,
		EnforceQualityMode:    true,
	},
	ProfileBalanced: {
		Profile:               ProfileBalanced,
		QualityMode:           QualityBalanced,
		SearchResearchMode:    ResearchExploration,
		WisDevFocusedMode:     ResearchExploration,
		WisDevBroadMode:       ResearchReview,
		ProviderLimit:         nil,
		MaxTabConcurrency:     MaxSystematicReviewTabConcurrency,
		AllowPreferredSources: true,
		EnforceQualityMode:    false,
	},
	ProfileCostOptimized: {
		Profile:               ProfileCostOptimized,
		QualityMode:           QualityFast,
		SearchResearchMode:    ResearchExploration,
		WisDevFocusedMode:     ResearchExploration,
		WisDevBroadMode:       ResearchExploration,
		ProviderLimit:         intPtr(2),
		MaxTabConcurrency:     MaxSystematicReviewTabConcurrency,
		AllowPreferredSources: true,
		EnforceQualityMode:    true,
	},
}

// NormalizeSearchPriorityProfile canonicalizes profile input.
// Unknown/empty → balanced.
func NormalizeSearchPriorityProfile(raw string) (SearchPriorityProfile, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "balanced":
		return ProfileBalanced, true
	case "cost", "cost_optimized", "cost-optimized":
		return ProfileCostOptimized, true
	case "ai_first", "ai-first":
		return ProfileAIFirst, true
	default:
		return ProfileBalanced, false
	}
}

// ResolveSearchPriority returns the tier-aware config for a profile.
// AI-first on free tier downgrades quality mode and provider limit
// (matches FE getSearchPriorityConfig free-tier override).
func ResolveSearchPriority(profile SearchPriorityProfile, tier SubscriptionTier) SearchPriorityConfig {
	base, ok := searchPriorityProfiles[profile]
	if !ok {
		base = searchPriorityProfiles[ProfileBalanced]
	}
	out := cloneSearchPriorityConfig(base)

	if profile == ProfileAIFirst && tier == TierFree {
		out.QualityMode = QualityBalanced
		limit := 3
		out.ProviderLimit = &limit
	}
	return out
}

// WisDevResearchModeForScope picks focused vs broad WisDev research mode.
func WisDevResearchModeForScope(scope string, cfg SearchPriorityConfig) ResearchMode {
	if strings.EqualFold(strings.TrimSpace(scope), "focused") {
		return cfg.WisDevFocusedMode
	}
	return cfg.WisDevBroadMode
}

// DefaultQualityModeForProfile returns the profile's default quality mode
// (with free-tier AI-first downgrade).
func DefaultQualityModeForProfile(profile SearchPriorityProfile, tier SubscriptionTier) QualityMode {
	return ResolveSearchPriority(profile, tier).QualityMode
}

func cloneSearchPriorityConfig(cfg SearchPriorityConfig) SearchPriorityConfig {
	out := cfg
	if cfg.ProviderLimit != nil {
		v := *cfg.ProviderLimit
		out.ProviderLimit = &v
	}
	return out
}

func intPtr(v int) *int { return &v }
