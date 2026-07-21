package policy

// Quality-mode, subscription-tier, and provider-tier policy.
//
// CANONICAL OWNER: Go. This file is the backend-owned port of the policy that
// previously lived in the browser:
//   - frontend/lib/featureTiers.ts   (QUALITY_MODES, tier gating)
//   - frontend/lib/apiProviders.ts   (API_PROVIDERS tiers, getProvidersByTier)
//
// Migration: "Thin the frontend, consolidate orchestration in Go" — Phase 1.
// The frontend must FETCH this policy via GET /api/config/quality-modes and
// GET /api/config/providers instead of computing it locally. Concrete model IDs
// are NOT resolved here; model-tier routing lives in internal/llm and reads the
// canonical scholar_models.json (see internal/llm.ResolveModelForTier). Keeping
// model resolution out of this package avoids an import cycle and preserves a
// single canonical owner for model IDs.

import "strings"

// SubscriptionTier is the billing tier that gates quality modes and provider access.
type SubscriptionTier string

const (
	TierFree     SubscriptionTier = "free"
	TierPro      SubscriptionTier = "pro"
	TierAdvanced SubscriptionTier = "advanced"
)

// tierRank orders subscription tiers for gating comparisons; a higher rank grants
// access to everything at or below it. Mirrors the tierRank map in
// frontend/lib/featureTiers.ts.
var tierRank = map[SubscriptionTier]int{
	TierFree:     0,
	TierPro:      1,
	TierAdvanced: 2,
}

// NormalizeSubscriptionTier canonicalizes arbitrary/legacy tier input, defaulting
// unknown values to the free tier (least privilege). ok reports whether the input
// was a recognized tier so callers can distinguish "defaulted" from "explicit free".
func NormalizeSubscriptionTier(raw string) (tier SubscriptionTier, ok bool) {
	switch SubscriptionTier(strings.ToLower(strings.TrimSpace(raw))) {
	case TierFree:
		return TierFree, true
	case TierPro:
		return TierPro, true
	case TierAdvanced:
		return TierAdvanced, true
	default:
		return TierFree, false
	}
}

// SubscriptionTierAtLeast reports whether tier meets or exceeds required.
// Used by API handlers for server-side subscription gating.
func SubscriptionTierAtLeast(tier, required SubscriptionTier) bool {
	return tierRank[tier] >= tierRank[required]
}

// QualityMode is the search/synthesis quality profile selected per request.
type QualityMode string

const (
	QualityFast     QualityMode = "fast"
	QualityBalanced QualityMode = "balanced"
	QualityQuality  QualityMode = "quality"
)

// qualityModeOrder is the canonical lightest->heaviest ordering used when
// returning gated mode lists.
var qualityModeOrder = []QualityMode{QualityFast, QualityBalanced, QualityQuality}

// NormalizeModelTier canonicalizes model-tier names. "balanced" is a legacy alias
// for the canonical "standard" model tier (mirrors
// frontend/config/aiModels.ts resolveGenerationModelId and
// internal/llm.ResolveModelForTier). Unknown values default to "standard".
// This resolves NAMES only; concrete model IDs come from internal/llm.
func NormalizeModelTier(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "balanced", "standard":
		return "standard"
	case "light":
		return "light"
	case "heavy":
		return "heavy"
	default:
		return "standard"
	}
}

// QualityModeFeatures mirrors the per-mode feature block in
// frontend/lib/featureTiers.ts (QualityModeConfig.features).
type QualityModeFeatures struct {
	RelevanceVerification  bool   `json:"relevanceVerification"`
	RelevanceMode          string `json:"relevanceMode"` // none | batch | deep
	SnowballExpansion      bool   `json:"snowballExpansion"`
	SnowballDepth          int    `json:"snowballDepth"`
	MaxPapersPerSearch     int    `json:"maxPapersPerSearch"`
	AISummaryDepth         string `json:"aiSummaryDepth"` // brief | standard | detailed
	APITierAccess          int    `json:"apiTierAccess"`  // 1..4 (max provider tier unlocked)
	MaxAPICalls            int    `json:"maxApiCalls"`
	ResultsPerAPI          int    `json:"resultsPerApi"`
	AllowProviderSelection bool   `json:"allowProviderSelection"`
}

// QualityModeConfig mirrors QualityModeConfig in frontend/lib/featureTiers.ts.
type QualityModeConfig struct {
	Name         QualityMode         `json:"name"`
	DisplayName  string              `json:"displayName"`
	Emoji        string              `json:"emoji"`
	Description  string              `json:"description"`
	LatencyMs    string              `json:"latencyMs"`
	Features     QualityModeFeatures `json:"features"`
	RequiredTier SubscriptionTier    `json:"requiredTier"`
}

// qualityModes is the canonical registry, ported value-for-value from
// QUALITY_MODES in frontend/lib/featureTiers.ts.
var qualityModes = map[QualityMode]QualityModeConfig{
	QualityFast: {
		Name:        QualityFast,
		DisplayName: "Fast",
		Emoji:       "⚡",
		Description: "Quick results, core sources only",
		LatencyMs:   "~3-5s",
		Features: QualityModeFeatures{
			RelevanceVerification:  false,
			RelevanceMode:          "none",
			SnowballExpansion:      false,
			SnowballDepth:          0,
			MaxPapersPerSearch:     50,
			AISummaryDepth:         "brief",
			APITierAccess:          1,
			MaxAPICalls:            2,
			ResultsPerAPI:          25,
			AllowProviderSelection: false,
		},
		RequiredTier: TierFree,
	},
	QualityBalanced: {
		Name:        QualityBalanced,
		DisplayName: "Balanced",
		Emoji:       "⚖️",
		Description: "Good coverage with domain sources",
		LatencyMs:   "~10-20s",
		Features: QualityModeFeatures{
			RelevanceVerification:  true,
			RelevanceMode:          "batch",
			SnowballExpansion:      true,
			SnowballDepth:          3,
			MaxPapersPerSearch:     100,
			AISummaryDepth:         "standard",
			APITierAccess:          3,
			MaxAPICalls:            7,
			ResultsPerAPI:          40,
			AllowProviderSelection: false,
		},
		RequiredTier: TierFree,
	},
	QualityQuality: {
		Name:        QualityQuality,
		DisplayName: "Quality",
		Emoji:       "🎯",
		Description: "All sources, maximum coverage",
		LatencyMs:   "~25-45s",
		Features: QualityModeFeatures{
			RelevanceVerification:  true,
			RelevanceMode:          "deep",
			SnowballExpansion:      true,
			SnowballDepth:          5,
			MaxPapersPerSearch:     200,
			AISummaryDepth:         "detailed",
			APITierAccess:          4,
			MaxAPICalls:            12,
			ResultsPerAPI:          50,
			AllowProviderSelection: true,
		},
		RequiredTier: TierPro,
	},
}

// QualityModeByName returns the config for a quality-mode name, accepting the
// legacy "standard" alias for "balanced". ok is false for unknown modes.
func QualityModeByName(raw string) (cfg QualityModeConfig, ok bool) {
	name := QualityMode(strings.ToLower(strings.TrimSpace(raw)))
	if name == "standard" { // legacy alias
		name = QualityBalanced
	}
	cfg, ok = qualityModes[name]
	return cfg, ok
}

// IsQualityModeAvailable reports whether a mode is unlocked for a subscription tier.
// Mirrors isQualityModeAvailable in frontend/lib/featureTiers.ts.
func IsQualityModeAvailable(mode QualityMode, tier SubscriptionTier) bool {
	cfg, ok := qualityModes[mode]
	if !ok {
		return false
	}
	return tierRank[tier] >= tierRank[cfg.RequiredTier]
}

// GatedQualityModes returns the quality-mode configs available to a subscription
// tier, ordered lightest->heaviest. This is the payload for
// GET /api/config/quality-modes.
func GatedQualityModes(tier SubscriptionTier) []QualityModeConfig {
	out := make([]QualityModeConfig, 0, len(qualityModeOrder))
	for _, m := range qualityModeOrder {
		if IsQualityModeAvailable(m, tier) {
			out = append(out, qualityModes[m])
		}
	}
	return out
}

// DefaultQualityMode returns the default mode for a tier. Currently all tiers
// default to Balanced (canonical alias for the "standard" model tier); mirrors
// getDefaultQualityMode in frontend/lib/featureTiers.ts.
func DefaultQualityMode(_ SubscriptionTier) QualityMode {
	return QualityBalanced
}
