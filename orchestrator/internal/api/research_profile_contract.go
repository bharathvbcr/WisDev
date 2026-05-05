package api

import (
	"math"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

const (
	maxProfileDomains         = 5
	maxProfileStudyTypes      = 10
	maxProfileExclusions      = 20
	maxProfileEvidenceQuality = 10
	maxProfileOutputFocus     = 10
	maxProfileSubtopics       = 50
	maxProfilePatterns        = 20
	maxProfileTrendSamples    = 20
)

func defaultRuntimeResearchProfile(userID string) map[string]any {
	return map[string]any{
		"userId":                   userID,
		"preferredDomains":         []string{},
		"preferredStudyTypes":      []string{},
		"preferredEvidenceQuality": []string{},
		"preferredOutputFocus":     []string{},
		"typicalScope":             "balanced",
		"commonExclusions":         []string{},
		"expertiseLevel":           "intermediate",
		"expertiseTrend":           []int{},
		"avgSessionDuration":       0,
		"completionRate":           0.0,
		"abandonmentRate":          0.0,
		"totalSessions":            0,
		"successfulSubtopics":      []string{},
		"rewritePatterns":          []string{},
		"lastUpdated":              time.Now().UnixMilli(),
	}
}

func summarizeRuntimeResearchProfile(agentGateway *wisdev.AgentGateway, userID string) map[string]any {
	if agentGateway == nil || agentGateway.Journal == nil {
		return map[string]any{"found": false}
	}

	summary := cloneAnyMap(agentGateway.Journal.SummarizeResearchProfile(userID))
	if len(summary) == 0 {
		return map[string]any{"found": false}
	}

	profile := asAnyMap(summary["profile"])
	if len(profile) == 0 {
		summary["found"] = false
		return summary
	}

	profile["userId"] = userID
	if _, ok := profile["lastUpdated"]; !ok {
		profile["lastUpdated"] = time.Now().UnixMilli()
	}
	summary["found"] = true
	summary["profile"] = profile
	return summary
}

func buildUpdatedRuntimeResearchProfile(
	userID string,
	conversation map[string]any,
	existing map[string]any,
) map[string]any {
	profile := cloneAnyMap(defaultRuntimeResearchProfile(userID))
	for key, value := range existing {
		profile[key] = value
	}
	profile["userId"] = userID

	prevTotal := intValue(profile["totalSessions"])
	totalSessions := prevTotal + 1
	profile["totalSessions"] = totalSessions

	domain := strings.TrimSpace(firstNonEmptyString(
		wisdev.AsOptionalString(conversation["detectedDomain"]),
		wisdev.AsOptionalString(conversation["domain"]),
	))
	profile["preferredDomains"] = mergeUniqueStrings(
		[]string{domain},
		stringSliceValue(profile["preferredDomains"]),
		maxProfileDomains,
	)

	studyTypes := answerValuesFromConversation(conversation, "q5_study_types")
	profile["preferredStudyTypes"] = mergeUniqueStrings(
		studyTypes,
		stringSliceValue(profile["preferredStudyTypes"]),
		maxProfileStudyTypes,
	)

	scope := strings.TrimSpace(firstNonEmptyString(
		firstString(answerValuesFromConversation(conversation, "q2_scope")),
		wisdev.AsOptionalString(conversation["typicalScope"]),
		wisdev.AsOptionalString(profile["typicalScope"]),
		"balanced",
	))
	profile["typicalScope"] = scope

	exclusions := answerValuesFromConversation(conversation, "q6_exclusions")
	profile["commonExclusions"] = mergeUniqueStrings(
		exclusions,
		stringSliceValue(profile["commonExclusions"]),
		maxProfileExclusions,
	)

	evidenceQuality := answerValuesFromConversation(conversation, "q7_evidence_quality")
	profile["preferredEvidenceQuality"] = mergeUniqueStrings(
		evidenceQuality,
		stringSliceValue(profile["preferredEvidenceQuality"]),
		maxProfileEvidenceQuality,
	)

	outputFocus := answerValuesFromConversation(conversation, "q8_output_focus")
	profile["preferredOutputFocus"] = mergeUniqueStrings(
		outputFocus,
		stringSliceValue(profile["preferredOutputFocus"]),
		maxProfileOutputFocus,
	)

	expertiseLevel := normalizeExpertiseLevel(firstNonEmptyString(
		wisdev.AsOptionalString(conversation["expertiseLevel"]),
		wisdev.AsOptionalString(profile["expertiseLevel"]),
	))
	profile["expertiseLevel"] = expertiseLevel
	trend := append(intSliceValue(profile["expertiseTrend"]), expertiseScore(expertiseLevel))
	if len(trend) > maxProfileTrendSamples {
		trend = trend[len(trend)-maxProfileTrendSamples:]
	}
	profile["expertiseTrend"] = trend

	successfulSubtopics := stringSliceValue(profile["successfulSubtopics"])
	successfulSubtopics = mergeUniqueStrings(
		stringSliceValue(conversation["refinementsTaken"]),
		successfulSubtopics,
		maxProfileSubtopics,
	)
	successfulSubtopics = mergeUniqueStrings(
		stringSliceValue(conversation["rewritePatterns"]),
		successfulSubtopics,
		maxProfileSubtopics,
	)
	profile["successfulSubtopics"] = successfulSubtopics

	rewritePatterns := mergeUniqueStrings(
		stringSliceValue(conversation["rewritePatterns"]),
		stringSliceValue(profile["rewritePatterns"]),
		maxProfilePatterns,
	)
	if len(rewritePatterns) == 0 {
		rewritePatterns = mergeUniqueStrings(
			stringSliceValue(conversation["refinementsTaken"]),
			rewritePatterns,
			maxProfilePatterns,
		)
	}
	profile["rewritePatterns"] = rewritePatterns

	previousAvgMinutes := floatValue(profile["avgSessionDuration"])
	currentMinutes := float64(intValue(conversation["totalTimeMs"])) / 60000.0
	if currentMinutes > 0 {
		profile["avgSessionDuration"] = weightedAverage(previousAvgMinutes, currentMinutes, prevTotal, totalSessions)
	}

	prevCompleted := boundedRateCount(floatValue(profile["completionRate"]), prevTotal)
	prevAbandoned := boundedRateCount(floatValue(profile["abandonmentRate"]), prevTotal)
	switch normalizeConversationStatus(conversation["status"]) {
	case "complete", "completed":
		prevCompleted++
	case "abandoned":
		prevAbandoned++
	}
	profile["completionRate"] = safeRate(prevCompleted, totalSessions)
	profile["abandonmentRate"] = safeRate(prevAbandoned, totalSessions)
	profile["lastUpdated"] = time.Now().UnixMilli()

	return profile
}

func asAnyMap(value any) map[string]any {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return cloneAnyMap(record)
}

func answerValuesFromConversation(conversation map[string]any, questionID string) []string {
	answers := asAnyMap(conversation["answers"])
	if len(answers) == 0 {
		return nil
	}
	answer := asAnyMap(answers[questionID])
	if len(answer) == 0 {
		return nil
	}
	return stringSliceValue(answer["values"])
}

func stringSliceValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(wisdev.AsOptionalString(item)); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		if trimmed := strings.TrimSpace(wisdev.AsOptionalString(value)); trimmed != "" {
			return []string{trimmed}
		}
		return nil
	}
}

func intSliceValue(value any) []int {
	switch typed := value.(type) {
	case []int:
		return append([]int(nil), typed...)
	case []any:
		out := make([]int, 0, len(typed))
		for _, item := range typed {
			out = append(out, intValue(item))
		}
		return out
	default:
		if value == nil {
			return nil
		}
		return []int{intValue(value)}
	}
}

func mergeUniqueStrings(primary []string, secondary []string, limit int) []string {
	out := make([]string, 0, len(primary)+len(secondary))
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	appendUnique := func(items []string) {
		for _, item := range items {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			key := strings.ToLower(trimmed)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, trimmed)
			if limit > 0 && len(out) >= limit {
				return
			}
		}
	}
	appendUnique(primary)
	if limit <= 0 || len(out) < limit {
		appendUnique(secondary)
	}
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func normalizeExpertiseLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "beginner":
		return "beginner"
	case "expert":
		return "expert"
	default:
		return "intermediate"
	}
}

func expertiseScore(level string) int {
	switch normalizeExpertiseLevel(level) {
	case "beginner":
		return 3
	case "expert":
		return 9
	default:
		return 6
	}
}

func normalizeConversationStatus(value any) string {
	return strings.ToLower(strings.TrimSpace(wisdev.AsOptionalString(value)))
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(math.Round(float64(typed)))
	case float64:
		return int(math.Round(typed))
	default:
		return 0
	}
}

func floatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func weightedAverage(previous float64, current float64, prevSamples int, nextSamples int) int {
	if current <= 0 {
		return int(math.Round(previous))
	}
	if prevSamples <= 0 || previous <= 0 {
		return int(math.Round(current))
	}
	totalWeight := float64(nextSamples)
	if totalWeight <= 0 {
		totalWeight = 1
	}
	return int(math.Round(((previous * float64(prevSamples)) + current) / totalWeight))
}

func boundedRateCount(rate float64, total int) int {
	if total <= 0 || rate <= 0 {
		return 0
	}
	return int(math.Round(rate * float64(total)))
}

func safeRate(count int, total int) float64 {
	if total <= 0 || count <= 0 {
		return 0
	}
	if count > total {
		count = total
	}
	return float64(count) / float64(total)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
