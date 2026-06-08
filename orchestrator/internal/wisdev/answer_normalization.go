package wisdev

import "strings"

func IsKnownSingleSelectQuestionID(questionID string) bool {
	switch strings.TrimSpace(questionID) {
	case "q2_scope", "q3_timeframe":
		return true
	default:
		return false
	}
}

func NormalizeAnswerValues(isMultiSelect bool, values []string, displayValues []string) ([]string, []string) {
	normalizedValues := make([]string, 0, len(values))
	normalizedDisplayValues := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for index, value := range values {
		trimmedValue := strings.TrimSpace(value)
		if trimmedValue == "" {
			continue
		}
		if _, exists := seen[trimmedValue]; exists {
			continue
		}
		seen[trimmedValue] = struct{}{}
		displayValue := trimmedValue
		if index < len(displayValues) {
			if trimmedDisplayValue := strings.TrimSpace(displayValues[index]); trimmedDisplayValue != "" {
				displayValue = trimmedDisplayValue
			}
		}
		normalizedValues = append(normalizedValues, trimmedValue)
		normalizedDisplayValues = append(normalizedDisplayValues, displayValue)
	}
	if len(normalizedValues) == 0 {
		return []string{}, []string{}
	}
	if !isMultiSelect {
		return normalizedValues[:1], normalizedDisplayValues[:1]
	}
	return normalizedValues, normalizedDisplayValues
}

func NormalizeAnswerForQuestion(answer Answer, isMultiSelect bool) Answer {
	normalized := answer
	normalized.QuestionID = strings.TrimSpace(answer.QuestionID)
	normalized.Values, normalized.DisplayValues = NormalizeAnswerValues(
		isMultiSelect,
		answer.Values,
		answer.DisplayValues,
	)
	return normalized
}
