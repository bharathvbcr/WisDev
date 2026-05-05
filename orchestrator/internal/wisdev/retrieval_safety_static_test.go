package wisdev

import (
	"os"
	"strings"
	"testing"
)

func TestWisDevLLMInputSurfacesUseRetrievalSafety(t *testing.T) {
	files := map[string][]string{
		"autonomous.go": {
			"func (l *AutonomousLoop) evaluateSufficiency",
			"func (l *AutonomousLoop) synthesizeWithEvidence",
			"func (l *AutonomousLoop) legacySynthesizePlainText",
			"func (l *AutonomousLoop) intermediateSynthesis",
			"func (l *AutonomousLoop) refineDraftWithCritique",
		},
		"autonomous_hardening_helpers.go": {
			"func (l *AutonomousLoop) critiqueDraft",
		},
	}
	for file, functions := range files {
		sourceBytes, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("failed to read %s: %v", file, err)
		}
		source := string(sourceBytes)
		for _, fn := range functions {
			body := functionBodyForStaticCheck(source, fn)
			if !strings.Contains(body, "SanitizeRetrievedPapersForLLM") && !strings.Contains(body, "SanitizeEvidenceItemsForLLM") {
				t.Fatalf("%s must sanitize retrieved content before LLM prompt construction", fn)
			}
		}
	}
}

func functionBodyForStaticCheck(source string, marker string) string {
	start := strings.Index(source, marker)
	if start < 0 {
		return ""
	}
	next := strings.Index(source[start+len(marker):], "\nfunc ")
	if next < 0 {
		return source[start:]
	}
	return source[start : start+len(marker)+next]
}
