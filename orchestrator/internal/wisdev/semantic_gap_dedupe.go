package wisdev

import (
	"math"
	"regexp"
	"strings"
)

const semanticGapDuplicateThreshold = 0.86

var semanticGapTokenPattern = regexp.MustCompile(`[a-z0-9]+`)

var semanticGapStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "by": {}, "for": {},
	"from": {}, "in": {}, "into": {}, "is": {}, "of": {}, "on": {}, "or": {}, "the": {},
	"to": {}, "vs": {}, "with": {},
}

var semanticGapSynonyms = map[string]string{
	"contradict":      "contradiction",
	"contradictory":   "contradiction",
	"counter":         "contradiction",
	"counterargument": "contradiction",
	"falsification":   "falsify",
	"falsifiability":  "falsify",
	"falsified":       "falsify",
	"independently":   "independent",
	"meta":            "systematic",
	"metadata":        "citation",
	"paper":           "evidence",
	"papers":          "evidence",
	"pdf":             "fulltext",
	"replicate":       "replication",
	"replicated":      "replication",
	"replicates":      "replication",
	"replicating":     "replication",
	"review":          "systematic",
	"reviews":         "systematic",
	"source":          "evidence",
	"sources":         "evidence",
	"trial":           "study",
	"trials":          "study",
	"validation":      "replication",
	"verify":          "verification",
	"verified":        "verification",
	"verifying":       "verification",
}

func semanticGapVector(value string) map[string]float64 {
	tokens := semanticGapTokenPattern.FindAllString(strings.ToLower(value), -1)
	if len(tokens) == 0 {
		return nil
	}
	vector := make(map[string]float64, len(tokens))
	for _, token := range tokens {
		if _, skip := semanticGapStopwords[token]; skip {
			continue
		}
		if canonical, ok := semanticGapSynonyms[token]; ok {
			token = canonical
		}
		if len(token) > 4 && strings.HasSuffix(token, "ies") {
			token = strings.TrimSuffix(token, "ies") + "y"
		} else if len(token) > 5 && strings.HasSuffix(token, "ing") {
			token = strings.TrimSuffix(token, "ing")
		} else if len(token) > 4 && strings.HasSuffix(token, "ed") {
			token = strings.TrimSuffix(token, "ed")
		} else if len(token) > 4 && strings.HasSuffix(token, "s") {
			token = strings.TrimSuffix(token, "s")
		}
		vector[token]++
	}
	if len(vector) == 0 {
		return nil
	}
	return vector
}

func semanticGapCosine(a string, b string) float64 {
	left := semanticGapVector(a)
	right := semanticGapVector(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	var dot, leftNorm, rightNorm float64
	for token, value := range left {
		leftNorm += value * value
		if rightValue, ok := right[token]; ok {
			dot += value * rightValue
		}
	}
	for _, value := range right {
		rightNorm += value * value
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func semanticallyRedundantLoopQuery(candidate string, accepted []string, threshold float64) bool {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return true
	}
	if threshold <= 0 {
		threshold = semanticGapDuplicateThreshold
	}
	for _, existing := range accepted {
		if strings.EqualFold(strings.TrimSpace(existing), trimmed) {
			return true
		}
		if semanticGapCosine(trimmed, existing) >= threshold {
			return true
		}
	}
	return false
}
