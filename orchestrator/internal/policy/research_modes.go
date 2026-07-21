package policy

// Research-mode policy: Go-owned port of frontend/config/researchModes.ts.
//
// CANONICAL OWNER: Go. Provider sets, ranking weights, HyDE flags/prompts, tier
// gating, and filters live here. The frontend must FETCH display + policy via
// GET /api/config/research-modes instead of inventing local authority.
//
// Ranking consumers (paper_rank / ScoreQuality) read ModeRankingWeights so
// search scoring and the config API share one source of truth.

import (
	"strings"
	"time"
)

// ResearchMode is a researcher-lifecycle stage that reconfigures providers and ranking.
type ResearchMode string

const (
	ResearchExploration  ResearchMode = "exploration"
	ResearchReview       ResearchMode = "review"
	ResearchMethodology  ResearchMode = "methodology"
	ResearchGapFinding   ResearchMode = "gap_finding"
)

// researchModeOrder is the stable list order for API payloads.
var researchModeOrder = []ResearchMode{
	ResearchExploration,
	ResearchReview,
	ResearchMethodology,
	ResearchGapFinding,
}

// RetrievalStrategy describes how a mode prefers to retrieve papers.
type RetrievalStrategy string

const (
	RetrievalDiversity    RetrievalStrategy = "diversity"
	RetrievalSnowball     RetrievalStrategy = "snowball"
	RetrievalKeywordMatch RetrievalStrategy = "keyword_match"
	RetrievalSemantic     RetrievalStrategy = "semantic"
)

// RankingWeights are relative signal weights for preference scoring.
// Sparse modes may omit some signals (zero). Consumers should treat missing
// JSON fields as 0.
type RankingWeights struct {
	Citations         float64 `json:"citations,omitempty"`
	Recency           float64 `json:"recency,omitempty"`
	Relevance         float64 `json:"relevance,omitempty"`
	VenuePrestige     float64 `json:"venuePrestige,omitempty"`
	HasCode           float64 `json:"hasCode,omitempty"`
	HasPdf            float64 `json:"hasPdf,omitempty"`
	CitationVelocity  float64 `json:"citationVelocity,omitempty"`
}

// YearRange is an optional inclusive publication-year window.
type YearRange struct {
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`
}

// ResearchModeFilters are optional hard filters applied for a mode.
type ResearchModeFilters struct {
	PaperTypes        []string   `json:"paperTypes,omitempty"`
	YearRange         *YearRange `json:"yearRange,omitempty"`
	RequirePeerReview bool       `json:"requirePeerReview,omitempty"`
}

// ResearchModeConfig mirrors ResearchModeConfig in frontend/config/researchModes.ts.
type ResearchModeConfig struct {
	Name              ResearchMode         `json:"name"`
	DisplayName       string               `json:"displayName"`
	Description       string               `json:"description"`
	Providers         []string             `json:"providers"`
	RetrievalStrategy RetrievalStrategy    `json:"retrievalStrategy"`
	RankingWeights    RankingWeights       `json:"rankingWeights"`
	UseHyDE           bool                 `json:"useHyDE"`
	Filters           *ResearchModeFilters `json:"filters,omitempty"`
	AIHooks           []string             `json:"aiHooks,omitempty"`
	RequiredTier      SubscriptionTier     `json:"requiredTier"`
}

// researchModes is the canonical registry, ported value-for-value from
// RESEARCH_MODES in frontend/config/researchModes.ts.
var researchModes = map[ResearchMode]ResearchModeConfig{
	ResearchExploration: {
		Name:              ResearchExploration,
		DisplayName:       "Exploration",
		Description:       "Discover papers across a broad topic area. Best for starting new research.",
		Providers:         []string{"semanticscholar", "openalex", "base", "core"},
		RetrievalStrategy: RetrievalDiversity,
		RankingWeights: RankingWeights{
			Citations: 0.4,
			Recency:   0.1,
			Relevance: 0.5,
		},
		UseHyDE:      true,
		RequiredTier: TierFree,
	},
	ResearchReview: {
		Name:              ResearchReview,
		DisplayName:       "Literature Review",
		Description:       "Find comprehensive, authoritative papers. Best for systematic reviews.",
		Providers:         []string{"semanticscholar", "openCitations", "crossref", "openalex"},
		RetrievalStrategy: RetrievalSnowball,
		RankingWeights: RankingWeights{
			Citations:     0.3,
			VenuePrestige: 0.3,
			Relevance:     0.4,
		},
		UseHyDE: false,
		Filters: &ResearchModeFilters{
			PaperTypes: []string{"review", "meta-analysis", "systematic-review"},
		},
		RequiredTier: TierPro,
	},
	ResearchMethodology: {
		Name:              ResearchMethodology,
		DisplayName:       "Methodology",
		Description:       "Find papers with methods, code, and implementation details.",
		Providers:         []string{"paperswithcode", "arxiv", "semanticscholar", "core"},
		RetrievalStrategy: RetrievalKeywordMatch,
		RankingWeights: RankingWeights{
			HasCode: 0.3,
			HasPdf:  0.3,
			Recency: 0.4,
		},
		UseHyDE:      false,
		AIHooks:      []string{"extract_methods"},
		RequiredTier: TierPro,
	},
	ResearchGapFinding: {
		Name:              ResearchGapFinding,
		DisplayName:       "Gap Finding",
		Description:       "Discover underexplored topics and recent preprints.",
		Providers:         []string{"arxiv", "biorxiv", "core", "base"},
		RetrievalStrategy: RetrievalSemantic,
		RankingWeights: RankingWeights{
			Recency:          0.6,
			CitationVelocity: 0.4,
		},
		UseHyDE: false,
		Filters: &ResearchModeFilters{
			YearRange: &YearRange{Min: intPtr(time.Now().Year() - 2)},
		},
		RequiredTier: TierPro,
	},
}

// hydePrompts are mode-specific HyDE templates (query placeholder: {query}).
var hydePrompts = map[ResearchMode]string{
	ResearchExploration: `Write a research paper abstract that provides a comprehensive overview of findings for: "{query}"

The abstract should cover:
- The main research question or problem
- Key methodologies or approaches used
- Primary findings or results
- Implications for the field`,

	ResearchReview: `Write an abstract for a systematic review paper synthesizing the literature on: "{query}"

The abstract should include:
- The scope and objectives of the review
- Search strategy and inclusion criteria
- Summary of included studies
- Key themes and conclusions`,

	ResearchMethodology: `Write a methodology section describing technical implementation details for solving: "{query}"

The section should cover:
- Technical approach and algorithms
- Implementation details
- Evaluation metrics
- Reproducibility information`,

	ResearchGapFinding: `Write an abstract identifying unexplored research gaps and novel directions for: "{query}"

The abstract should highlight:
- Current state of research
- Identified gaps or limitations
- Proposed novel directions
- Potential impact of addressing these gaps`,
}

// NormalizeResearchMode canonicalizes mode input. Unknown/empty → exploration.
func NormalizeResearchMode(raw string) (ResearchMode, bool) {
	switch ResearchMode(strings.ToLower(strings.TrimSpace(raw))) {
	case ResearchExploration:
		return ResearchExploration, true
	case ResearchReview:
		return ResearchReview, true
	case ResearchMethodology:
		return ResearchMethodology, true
	case ResearchGapFinding:
		return ResearchGapFinding, true
	default:
		return ResearchExploration, false
	}
}

// ResearchModeByName returns the config for a mode name.
func ResearchModeByName(raw string) (cfg ResearchModeConfig, ok bool) {
	mode, known := NormalizeResearchMode(raw)
	if !known && strings.TrimSpace(raw) != "" {
		return ResearchModeConfig{}, false
	}
	cfg, ok = researchModes[mode]
	return cfg, ok
}

// CanAccessResearchMode reports whether tier may use mode.
func CanAccessResearchMode(mode ResearchMode, tier SubscriptionTier) bool {
	cfg, ok := researchModes[mode]
	if !ok {
		return false
	}
	return SubscriptionTierAtLeast(tier, cfg.RequiredTier)
}

// GatedResearchModes returns configs unlocked for tier, in stable order.
func GatedResearchModes(tier SubscriptionTier) []ResearchModeConfig {
	out := make([]ResearchModeConfig, 0, len(researchModeOrder))
	for _, m := range researchModeOrder {
		if CanAccessResearchMode(m, tier) {
			cfg := researchModes[m]
			out = append(out, cloneResearchModeConfig(cfg))
		}
	}
	return out
}

// DefaultResearchMode is always exploration (matches FE getDefaultMode).
func DefaultResearchMode(_ SubscriptionTier) ResearchMode {
	return ResearchExploration
}

// ModeProviders returns providers for mode, falling back to exploration when
// the tier cannot access the requested mode.
func ModeProviders(mode ResearchMode, tier SubscriptionTier) []string {
	if !CanAccessResearchMode(mode, tier) {
		return append([]string(nil), researchModes[ResearchExploration].Providers...)
	}
	return append([]string(nil), researchModes[mode].Providers...)
}

// ModeRankingWeights returns ranking weights for mode (exploration if unknown).
func ModeRankingWeights(mode ResearchMode) RankingWeights {
	cfg, ok := researchModes[mode]
	if !ok {
		return researchModes[ResearchExploration].RankingWeights
	}
	return cfg.RankingWeights
}

// ModeUsesHyDE reports whether mode enables HyDE.
func ModeUsesHyDE(mode ResearchMode) bool {
	cfg, ok := researchModes[mode]
	if !ok {
		return false
	}
	return cfg.UseHyDE
}

// HyDEPrompt returns the HyDE template for mode with {query} substituted.
func HyDEPrompt(mode ResearchMode, query string) string {
	tpl, ok := hydePrompts[mode]
	if !ok {
		tpl = hydePrompts[ResearchExploration]
	}
	return strings.ReplaceAll(tpl, "{query}", query)
}

func cloneResearchModeConfig(cfg ResearchModeConfig) ResearchModeConfig {
	out := cfg
	out.Providers = append([]string(nil), cfg.Providers...)
	out.AIHooks = append([]string(nil), cfg.AIHooks...)
	if cfg.Filters != nil {
		f := *cfg.Filters
		f.PaperTypes = append([]string(nil), cfg.Filters.PaperTypes...)
		if cfg.Filters.YearRange != nil {
			yr := *cfg.Filters.YearRange
			if cfg.Filters.YearRange.Min != nil {
				v := *cfg.Filters.YearRange.Min
				yr.Min = &v
			}
			if cfg.Filters.YearRange.Max != nil {
				v := *cfg.Filters.YearRange.Max
				yr.Max = &v
			}
			f.YearRange = &yr
		}
		out.Filters = &f
	}
	return out
}
