package docgen

import (
	"fmt"
	"strings"
)

// Intent names the ScholarDoc document type to generate.
type Intent string

const (
	IntentReport    Intent = "report"
	IntentLitReview Intent = "litreview"
	IntentFullPaper Intent = "fullpaper"
)

// ParseIntent normalizes and validates a document intent string.
func ParseIntent(raw string) (Intent, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(IntentFullPaper), "full-paper", "full_paper", "manuscript":
		return IntentFullPaper, nil
	case string(IntentReport), "quick-report", "quickreport":
		return IntentReport, nil
	case string(IntentLitReview), "lit-review", "literature-review", "review":
		return IntentLitReview, nil
	default:
		return "", fmt.Errorf("unknown intent %q (want report, litreview, or fullpaper)", raw)
	}
}

// String returns the canonical intent name.
func (i Intent) String() string {
	return string(i)
}

// DisplayName returns a human-readable label for CLI/TUI surfaces.
func (i Intent) DisplayName() string {
	switch i {
	case IntentReport:
		return "Quick Report"
	case IntentLitReview:
		return "Literature Review"
	case IntentFullPaper:
		return "Full Paper"
	default:
		return string(i)
	}
}
