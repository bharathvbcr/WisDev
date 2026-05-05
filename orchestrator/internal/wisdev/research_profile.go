package wisdev

import (
	"context"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
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
		budget.MaxSearchTerms = 16
		budget.HitsPerSearch = 16
		budget.MaxUniquePapers = 96
	default:
		budget.MaxSearchTerms = 6
		budget.HitsPerSearch = 12
		budget.MaxUniquePapers = 36
	}

	if mode == WisDevModeYOLO && qualityMode != "fast" {
		budget.MaxSearchTerms += 4
		budget.HitsPerSearch += 4
		budget.MaxUniquePapers += 32
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
	if mode == WisDevModeYOLO || qualityMode == "quality" {
		metadata["agentic_mode"] = "deep"
	} else if qualityMode == "fast" {
		metadata["agentic_mode"] = "quick"
	}

	complexity := NewComplexityAnalyzer().AnalyzeTask(ctx, query, metadata)
	primaryTier := complexity.RecommendedTier
	if interactive && primaryTier == ModelTierLight {
		primaryTier = ModelTierStandard
	}
	if mode == WisDevModeYOLO && qualityMode != "fast" && complexity.Score >= 0.45 {
		primaryTier = ModelTierHeavy
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
	if maxIterations > 12 {
		maxIterations = 12
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
		TimeoutPerAgent:     30 * time.Second,
		ComplexityScore:     complexity.Score,
		EstimatedTokens:     complexity.EstimatedTokens,
	}
}

func defaultHypothesisCount(qualityMode string, mode WisDevMode) int {
	switch qualityMode {
	case "fast":
		if mode == WisDevModeYOLO {
			return 5
		}
		return 4
	case "quality":
		if mode == WisDevModeYOLO {
			return 10
		}
		return 8
	default:
		if mode == WisDevModeYOLO {
			return 8
		}
		return 6
	}
}

func defaultMaxIterations(qualityMode string, mode WisDevMode) int {
	switch qualityMode {
	case "fast":
		if mode == WisDevModeYOLO {
			return 3
		}
		return 2
	case "quality":
		if mode == WisDevModeYOLO {
			return 10
		}
		return 8
	default:
		if mode == WisDevModeYOLO {
			return 7
		}
		return 5
	}
}

func defaultAllocatedTokens(qualityMode string, mode WisDevMode) int {
	base := 40000
	switch qualityMode {
	case "fast":
		base = 16000
	case "quality":
		base = 72000
	}
	if mode == WisDevModeYOLO {
		base += 12000
	}
	return base
}
