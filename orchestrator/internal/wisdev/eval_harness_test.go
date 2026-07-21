package wisdev

import (
	"strings"
	"testing"
)

func TestEvalHarness_GoldenScenarios(t *testing.T) {
	report, err := RunEvalHarnessScenarios(NewToolRegistry())
	if err != nil {
		t.Fatalf("run eval harness: %v", err)
	}
	if report.FailedCount != 0 {
		failures := make([]string, 0, report.FailedCount)
		for group, results := range report.Groups {
			for _, result := range results {
				if !result.Passed {
					failures = append(failures, group+"/"+result.Name+": "+result.Error)
				}
			}
		}
		t.Fatalf("expected eval harness to pass, got %d failures: %s", report.FailedCount, strings.Join(failures, "; "))
	}
	if report.ScenarioCount == 0 {
		t.Fatalf("expected eval harness scenarios to be loaded")
	}
}
