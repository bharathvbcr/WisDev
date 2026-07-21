package wisdev

import (
	"regexp"
	"strings"
)

type DiscoverySignal struct {
	Type       string  `json:"type"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

type DiscoverySignalsResponse struct {
	Signals []string          `json:"signals"`
	Details []DiscoverySignal `json:"details"`
}

var (
	rxCamelCaseModel = regexp.MustCompile(`\b[A-Z][a-z]+(?:[A-Z][a-zA-Z0-9]+)+\b`)
	rxArxivID        = regexp.MustCompile(`\b(?:arXiv:)?(\d{4}\.\d{4,5}(?:v\d+)?)\b`)
	rxAuthorEtAl     = regexp.MustCompile(`\b([A-Z][a-z]+(?:\s+[A-Z][a-z]+)?)\s+et\s+al\.?`)
)

func normalizeSignalID(prefix string, value string) string {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return ""
	}
	return prefix + ":" + cleaned
}

func ExtractDiscoverySignals(text string, maxSignals int) DiscoverySignalsResponse {
	if maxSignals <= 0 {
		maxSignals = 5
	}
	if maxSignals > 20 {
		maxSignals = 20
	}

	seen := make(map[string]bool)
	signals := make([]string, 0, maxSignals)
	details := make([]DiscoverySignal, 0, maxSignals)

	add := func(signalType string, value string, confidence float64) {
		if len(signals) >= maxSignals {
			return
		}
		id := normalizeSignalID(signalType, value)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		signals = append(signals, id)
		details = append(details, DiscoverySignal{
			Type:       signalType,
			Value:      strings.TrimSpace(value),
			Confidence: confidence,
		})
	}

	for _, match := range rxCamelCaseModel.FindAllString(text, -1) {
		add("model", match, 0.82)
	}
	for _, match := range rxArxivID.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			add("arxiv", match[1], 0.91)
		}
	}
	for _, match := range rxAuthorEtAl.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			add("author", match[1], 0.75)
		}
	}

	return DiscoverySignalsResponse{
		Signals: signals,
		Details: details,
	}
}
