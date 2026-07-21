package wisdev

import (
	"context"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
)

// normalizeSpaceLower trims, collapses whitespace, and lowercases a string.
func normalizeSpaceLower(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func NormalizeResearchQualityMode(raw string) string {
	switch lower := strings.ToLower(strings.TrimSpace(raw)); lower {
	case "fast":
		return "fast"
	case "quality", "high", "deep", "thorough", "rigorous":
		return "quality"
	default:
		return "balanced"
	}
}

func ResolveSearchBudget(qualityRaw string, mode WisDevMode) SearchBudget {
	qualityMode := NormalizeResearchQualityMode(qualityRaw)
	budget := SearchBudget{QualityMode: qualityMode}

	switch qualityMode {
	case "fast":
		budget.MaxSearchTerms = 2
		budget.HitsPerSearch = 4
		budget.MaxUniquePapers = 12
	case "quality":
		budget.MaxSearchTerms = 20
		budget.HitsPerSearch = 24
		budget.MaxUniquePapers = 144
	default:
		budget.MaxSearchTerms = 8
		budget.HitsPerSearch = 20
		budget.MaxUniquePapers = 80
	}

	if unleashedBudgetMode() {
		applyUnleashedSearchBudget(&budget)
	}

	return budget
}

func ResolveModelNameForTier(tier ModelTier) string {
	switch tier {
	case ModelTierHeavy:
		return llm.ResolveHeavyModel()
	case ModelTierLight:
		return llm.ResolveLightModel()
	default:
		return llm.ResolveStandardModel()
	}
}

func BuildResearchExecutionProfile(
	ctx context.Context,
	query string,
	modeRaw string,
	qualityRaw string,
	interactive bool,
	requestedIterations int,
) ResearchExecutionProfile {
	mode := NormalizeWisDevMode(modeRaw)
	serviceTier := ResolveServiceTier(mode, interactive)
	qualityMode := NormalizeResearchQualityMode(qualityRaw)
	searchBudget := ResolveSearchBudget(qualityMode, mode)

	metadata := map[string]interface{}{
		"hypothesis_count": float64(defaultHypothesisCount(qualityMode, mode)),
	}
	if qualityMode == "quality" {
		metadata["agentic_mode"] = "deep"
	} else if qualityMode == "fast" {
		metadata["agentic_mode"] = "quick"
	}

	complexity := NewComplexityAnalyzer().AnalyzeTask(ctx, query, metadata)
	primaryTier := complexity.RecommendedTier
	if interactive && primaryTier == ModelTierLight {
		primaryTier = ModelTierStandard
	}
	specialistTier := ModelTierLight
	if complexity.Score > 0.9 {
		specialistTier = ModelTierStandard
	}

	maxIterations := defaultMaxIterations(qualityMode, mode)
	if requestedIterations > 0 {
		maxIterations = requestedIterations
	}
	if maxIterations < 1 {
		maxIterations = 1
	}
	iterationCeiling := 12
	unleashed := unleashedBudgetMode()
	if unleashed {
		iterationCeiling = 24
	}
	if maxIterations > iterationCeiling {
		maxIterations = iterationCeiling
	}

	allocatedTokens := defaultAllocatedTokens(qualityMode, mode)
	if complexity.EstimatedTokens > allocatedTokens {
		allocatedTokens = complexity.EstimatedTokens
	}
	if interactive {
		allocatedTokens += 4000
	}

	maxParallelism := 2
	switch {
	case complexity.Score < 0.3:
		maxParallelism = 2
	case complexity.Score < 0.65:
		maxParallelism = 4
	default:
		maxParallelism = 6
	}

	timeoutPerAgent := 30 * time.Second
	if unleashed {
		maxParallelism += 2
		timeoutPerAgent = 90 * time.Second
	}

	return ResearchExecutionProfile{
		Mode:                mode,
		ServiceTier:         serviceTier,
		QualityMode:         qualityMode,
		SearchBudget:        searchBudget,
		PrimaryModelTier:    primaryTier,
		PrimaryModelName:    ResolveModelNameForTier(primaryTier),
		SpecialistModelTier: specialistTier,
		SpecialistModelName: ResolveModelNameForTier(specialistTier),
		MaxIterations:       maxIterations,
		AllocatedTokens:     allocatedTokens,
		MaxParallelism:      maxParallelism,
		TimeoutPerAgent:     timeoutPerAgent,
		ComplexityScore:     complexity.Score,
		EstimatedTokens:     complexity.EstimatedTokens,
	}
}

func defaultHypothesisCount(qualityMode string, mode WisDevMode) int {
	if unleashedBudgetMode() {
		switch qualityMode {
		case "fast":
			return 8
		case "quality":
			return 12
		default:
			return 10
		}
	}
	switch qualityMode {
	case "fast":
		return 4
	case "quality":
		return 8
	default:
		return 6
	}
}

func defaultMaxIterations(qualityMode string, mode WisDevMode) int {
	if unleashedBudgetMode() {
		switch qualityMode {
		case "fast":
			return 6
		case "quality":
			return 20
		default:
			return 14
		}
	}
	switch qualityMode {
	case "fast":
		return 2
	case "quality":
		return 8
	default:
		return 5
	}
}

func defaultAllocatedTokens(qualityMode string, mode WisDevMode) int {
	if unleashedBudgetMode() {
		switch qualityMode {
		case "fast":
			return 48000
		case "quality":
			return 200000
		default:
			return 120000
		}
	}
	base := 40000
	switch qualityMode {
	case "fast":
		base = 16000
	case "quality":
		base = 72000
	}
	return base
}
