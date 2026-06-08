package wisdev

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

type EvalHarnessScenarioFile struct {
	Questioning     []QuestioningEvalScenario     `json:"questioning"`
	WebFilter       []WebFilterEvalScenario       `json:"webFilter"`
	DeepAgents      []DeepAgentsEvalScenario      `json:"deepAgents"`
	ResearchQuality []ResearchQualityEvalScenario `json:"researchQuality"`
}

type QuestioningEvalScenario struct {
	Name                 string   `json:"name"`
	Query                string   `json:"query"`
	Domain               string   `json:"domain"`
	ExpectedSequence     []string `json:"expectedSequence"`
	ExpectedMinQuestions int      `json:"expectedMinQuestions"`
	ExpectedMaxQuestions int      `json:"expectedMaxQuestions"`
}

type WebFilterEvalScenario struct {
	Name              string                `json:"name"`
	Query             string                `json:"query"`
	Policy            SearchPolicyHints     `json:"policy"`
	Results           []WebSearchResultItem `json:"results"`
	ExpectedCount     int                   `json:"expectedCount"`
	ExpectedFirstLink string                `json:"expectedFirstLink"`
}

type DeepAgentsEvalScenario struct {
	Name                             string   `json:"name"`
	Mode                             string   `json:"mode"`
	ExpectedRequireHumanConfirmation bool     `json:"expectedRequireHumanConfirmation"`
	ExpectedIncludedActions          []string `json:"expectedIncludedActions"`
	ExpectedExcludedActions          []string `json:"expectedExcludedActions"`
}

type ResearchQualityEvalScenario struct {
	Name                         string                `json:"name"`
	Query                        string                `json:"query"`
	Plane                        string                `json:"plane,omitempty"`
	Ledger                       []CoverageLedgerEntry `json:"ledger"`
	Findings                     []EvidenceFinding     `json:"findings,omitempty"`
	Papers                       []search.Paper        `json:"papers,omitempty"`
	ExpectedFollowUps            []string              `json:"expectedFollowUps"`
	ExpectedOpenCount            int                   `json:"expectedOpenCount"`
	ExpectedCoverageRecall       float64               `json:"expectedCoverageRecall"`
	ExpectedContradictionCount   int                   `json:"expectedContradictionCount"`
	ExpectedCitationIntegrity    float64               `json:"expectedCitationIntegrity"`
	ExpectedVerifierVerdict      string                `json:"expectedVerifierVerdict,omitempty"`
	ExpectedPromotedClaims       int                   `json:"expectedPromotedClaims,omitempty"`
	ExpectedRejectedClaims       int                   `json:"expectedRejectedClaims,omitempty"`
	ExpectedAnswerStatus         string                `json:"expectedAnswerStatus,omitempty"`
	ExpectedObligationTypes      []string              `json:"expectedObligationTypes,omitempty"`
	ExpectedOwnerWorkers         []string              `json:"expectedOwnerWorkers,omitempty"`
	ExpectedDurableRequired      *bool                 `json:"expectedDurableRequired,omitempty"`
	ExpectedDurableTaskOps       []string              `json:"expectedDurableTaskOps,omitempty"`
	ExpectedBudgetExhausted      *bool                 `json:"expectedBudgetExhausted,omitempty"`
	ExpectedStaleSourceCount     int                   `json:"expectedStaleSourceCount,omitempty"`
	ExpectedCitationConflicts    int                   `json:"expectedCitationConflicts,omitempty"`
	ExpectedAcquisitionAttempts  int                   `json:"expectedAcquisitionAttempts,omitempty"`
	ExpectedAcquisitionFailures  int                   `json:"expectedAcquisitionFailures,omitempty"`
	ExpectedPythonPDFExtractions int                   `json:"expectedPythonPdfExtractions,omitempty"`
}

type EvalHarnessCheckResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

type EvalHarnessReport struct {
	ScenarioCount int                                 `json:"scenarioCount"`
	PassedCount   int                                 `json:"passedCount"`
	FailedCount   int                                 `json:"failedCount"`
	Groups        map[string][]EvalHarnessCheckResult `json:"groups"`
}

func LoadEvalHarnessScenarios() (EvalHarnessScenarioFile, error) {
	candidates := []string{
		filepath.Join("testdata", "eval_harness_scenarios.json"),
		filepath.Join("internal", "wisdev", "testdata", "eval_harness_scenarios.json"),
	}
	if _, currentFile, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(currentFile), "testdata", "eval_harness_scenarios.json"))
	}
	var lastErr error
	for _, candidate := range candidates {
		raw, err := os.ReadFile(candidate)
		if err != nil {
			lastErr = err
			continue
		}
		var scenarios EvalHarnessScenarioFile
		if err := json.Unmarshal(raw, &scenarios); err != nil {
			return EvalHarnessScenarioFile{}, err
		}
		return scenarios, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("eval harness scenarios not found")
	}
	return EvalHarnessScenarioFile{}, lastErr
}

func RunEvalHarnessScenarios(registry *ToolRegistry) (EvalHarnessReport, error) {
	scenarios, err := LoadEvalHarnessScenarios()
	if err != nil {
		return EvalHarnessReport{}, err
	}
	if registry == nil {
		registry = NewToolRegistry()
	}

	report := EvalHarnessReport{
		Groups: map[string][]EvalHarnessCheckResult{
			"questioning":     {},
			"webFilter":       {},
			"deepAgents":      {},
			"researchQuality": {},
		},
	}

	record := func(group, name string, err error) {
		result := EvalHarnessCheckResult{Name: name, Passed: err == nil}
		if err != nil {
			result.Error = err.Error()
			report.FailedCount++
		} else {
			report.PassedCount++
		}
		report.ScenarioCount++
		report.Groups[group] = append(report.Groups[group], result)
	}

	for _, scenario := range scenarios.Questioning {
		complexity := EstimateComplexityScore(scenario.Query)
		sequence, minQuestions, maxQuestions := BuildAdaptiveQuestionSequence(complexity, scenario.Domain)
		if len(sequence) != len(scenario.ExpectedSequence) {
			record("questioning", scenario.Name, fmt.Errorf("expected %d questions, got %d", len(scenario.ExpectedSequence), len(sequence)))
			continue
		}
		matchErr := error(nil)
		for i, expected := range scenario.ExpectedSequence {
			if sequence[i] != expected {
				matchErr = fmt.Errorf("question %d: expected %q, got %q", i, expected, sequence[i])
				break
			}
		}
		if matchErr == nil && minQuestions != scenario.ExpectedMinQuestions {
			matchErr = fmt.Errorf("expected min questions %d, got %d", scenario.ExpectedMinQuestions, minQuestions)
		}
		if matchErr == nil && maxQuestions != scenario.ExpectedMaxQuestions {
			matchErr = fmt.Errorf("expected max questions %d, got %d", scenario.ExpectedMaxQuestions, maxQuestions)
		}
		record("questioning", scenario.Name, matchErr)
	}

	for _, scenario := range scenarios.WebFilter {
		policy := DeriveSearchPolicyHints(scenario.Query, &scenario.Policy, nil)
		ranked, _ := FilterAndRankWebSearchResults(scenario.Query, scenario.Results, policy)
		var matchErr error
		if len(ranked) != scenario.ExpectedCount {
			matchErr = fmt.Errorf("expected %d results, got %d", scenario.ExpectedCount, len(ranked))
		} else if scenario.ExpectedCount > 0 && ranked[0].Link != scenario.ExpectedFirstLink {
			matchErr = fmt.Errorf("expected first link %q, got %q", scenario.ExpectedFirstLink, ranked[0].Link)
		}
		record("webFilter", scenario.Name, matchErr)
	}

	caps := BuildDeepAgentsCapabilities(registry)
	for _, scenario := range scenarios.DeepAgents {
		policy, ok := caps.PolicyByMode[scenario.Mode]
		if !ok {
			record("deepAgents", scenario.Name, fmt.Errorf("missing policy for mode %q", scenario.Mode))
			continue
		}
		matchErr := error(nil)
		if policy.RequireHumanConfirmation != scenario.ExpectedRequireHumanConfirmation {
			matchErr = fmt.Errorf("expected requireHumanConfirmation=%v, got %v", scenario.ExpectedRequireHumanConfirmation, policy.RequireHumanConfirmation)
		}
		if matchErr == nil {
			allowlisted := make(map[string]struct{}, len(policy.AllowlistedTools))
			for _, action := range policy.AllowlistedTools {
				allowlisted[action] = struct{}{}
			}
			for _, expected := range scenario.ExpectedIncludedActions {
				if _, ok := allowlisted[expected]; !ok {
					matchErr = fmt.Errorf("expected %q to be allowlisted in mode %q", expected, scenario.Mode)
					break
				}
			}
			if matchErr == nil {
				for _, excluded := range scenario.ExpectedExcludedActions {
					if _, ok := allowlisted[excluded]; ok {
						matchErr = fmt.Errorf("expected %q to be excluded in mode %q", excluded, scenario.Mode)
						break
					}
				}
			}
		}
		record("deepAgents", scenario.Name, matchErr)
	}

	for _, scenario := range scenarios.ResearchQuality {
		ledger := mergeCoverageLedgerEntries(nil, scenario.Ledger)
		openCount := countOpenCoverageLedgerEntries(ledger)
		followUpLimit := maxInt(3, len(scenario.ExpectedFollowUps))
		followUps := buildFollowUpQueriesFromLedger(scenario.Query, ledger, followUpLimit)
		matchedFollowUps := 0
		for _, expected := range scenario.ExpectedFollowUps {
			if containsNormalizedLoopQuery(followUps, expected) {
				matchedFollowUps++
			}
		}
		coverageRecall := 1.0
		if len(scenario.ExpectedFollowUps) > 0 {
			coverageRecall = float64(matchedFollowUps) / float64(len(scenario.ExpectedFollowUps))
		}
		contradictionCount := 0
		for _, entry := range ledger {
			if entry.Status == coverageLedgerStatusOpen && entry.Category == "contradiction" {
				contradictionCount++
			}
		}
		citationIntegrity := computeCitationIntegrityFromFindings(scenario.Findings)
		var verifierDecision *ResearchVerifierDecision
		if scenario.ExpectedVerifierVerdict != "" {
			gap := &LoopGapState{Ledger: ledger}
			claims := buildClaimVerificationLedger(scenario.Query, scenario.Findings, nil, gap)
			verifierDecision = buildIndependentVerifierDecision(claims, gap, nil)
		}
		durableRequired := false
		if strings.TrimSpace(scenario.Plane) != "" {
			durableRequired = researchPlaneRequiresDurableJob(ResearchExecutionPlane(strings.TrimSpace(scenario.Plane)))
		}
		budgetExhausted := researchEvalBudgetExhausted(ledger)
		staleSourceCount := researchEvalStaleSourceCount(scenario.Papers)
		citationConflictCount := researchEvalCitationConflictCount(ledger, scenario.Findings)
		sourceAcquisition := buildResearchSourceAcquisitionPlan(scenario.Query, scenario.Papers)
		answerStatus := researchEvalAnswerStatus(verifierDecision, openCount, budgetExhausted)
		durableTaskOps := researchEvalDurableTaskOps(scenario, ledger, verifierDecision)

		var matchErr error
		switch {
		case openCount != scenario.ExpectedOpenCount:
			matchErr = fmt.Errorf("expected open ledger count %d, got %d", scenario.ExpectedOpenCount, openCount)
		case coverageRecall+1e-9 < scenario.ExpectedCoverageRecall:
			matchErr = fmt.Errorf("expected coverage recall %.2f, got %.2f", scenario.ExpectedCoverageRecall, coverageRecall)
		case contradictionCount != scenario.ExpectedContradictionCount:
			matchErr = fmt.Errorf("expected contradiction count %d, got %d", scenario.ExpectedContradictionCount, contradictionCount)
		case math.Abs(citationIntegrity-scenario.ExpectedCitationIntegrity) > 0.001:
			matchErr = fmt.Errorf("expected citation integrity %.2f, got %.2f", scenario.ExpectedCitationIntegrity, citationIntegrity)
		case scenario.ExpectedVerifierVerdict != "" && verifierDecision == nil:
			matchErr = fmt.Errorf("expected verifier verdict %q, got no decision", scenario.ExpectedVerifierVerdict)
		case scenario.ExpectedVerifierVerdict != "" && verifierDecision.Verdict != scenario.ExpectedVerifierVerdict:
			matchErr = fmt.Errorf("expected verifier verdict %q, got %q", scenario.ExpectedVerifierVerdict, verifierDecision.Verdict)
		case scenario.ExpectedVerifierVerdict != "" && len(verifierDecision.PromotedClaimIDs) != scenario.ExpectedPromotedClaims:
			matchErr = fmt.Errorf("expected %d promoted claims, got %d", scenario.ExpectedPromotedClaims, len(verifierDecision.PromotedClaimIDs))
		case scenario.ExpectedVerifierVerdict != "" && len(verifierDecision.RejectedClaimIDs) != scenario.ExpectedRejectedClaims:
			matchErr = fmt.Errorf("expected %d rejected claims, got %d", scenario.ExpectedRejectedClaims, len(verifierDecision.RejectedClaimIDs))
		case scenario.ExpectedAnswerStatus != "" && answerStatus != scenario.ExpectedAnswerStatus:
			matchErr = fmt.Errorf("expected answerStatus=%q, got %q", scenario.ExpectedAnswerStatus, answerStatus)
		case !containsAllResearchEvalValues(researchEvalObligationTypes(ledger), scenario.ExpectedObligationTypes):
			matchErr = fmt.Errorf("expected obligation types %#v, got %#v", scenario.ExpectedObligationTypes, researchEvalObligationTypes(ledger))
		case !containsAllResearchEvalValues(researchEvalOwnerWorkers(ledger), scenario.ExpectedOwnerWorkers):
			matchErr = fmt.Errorf("expected owner workers %#v, got %#v", scenario.ExpectedOwnerWorkers, researchEvalOwnerWorkers(ledger))
		case scenario.ExpectedDurableRequired != nil && durableRequired != *scenario.ExpectedDurableRequired:
			matchErr = fmt.Errorf("expected durableRequired=%v, got %v", *scenario.ExpectedDurableRequired, durableRequired)
		case !containsAllResearchEvalValues(durableTaskOps, scenario.ExpectedDurableTaskOps):
			matchErr = fmt.Errorf("expected durable task operations %#v, got %#v", scenario.ExpectedDurableTaskOps, durableTaskOps)
		case scenario.ExpectedBudgetExhausted != nil && budgetExhausted != *scenario.ExpectedBudgetExhausted:
			matchErr = fmt.Errorf("expected budgetExhausted=%v, got %v", *scenario.ExpectedBudgetExhausted, budgetExhausted)
		case scenario.ExpectedStaleSourceCount > 0 && staleSourceCount != scenario.ExpectedStaleSourceCount:
			matchErr = fmt.Errorf("expected %d stale sources, got %d", scenario.ExpectedStaleSourceCount, staleSourceCount)
		case scenario.ExpectedCitationConflicts > 0 && citationConflictCount != scenario.ExpectedCitationConflicts:
			matchErr = fmt.Errorf("expected %d citation conflicts, got %d", scenario.ExpectedCitationConflicts, citationConflictCount)
		case scenario.ExpectedAcquisitionAttempts > 0 && len(sourceAcquisition.Attempts) != scenario.ExpectedAcquisitionAttempts:
			matchErr = fmt.Errorf("expected %d acquisition attempts, got %d", scenario.ExpectedAcquisitionAttempts, len(sourceAcquisition.Attempts))
		case scenario.ExpectedAcquisitionFailures > 0 && sourceAcquisition.FetchFailures != scenario.ExpectedAcquisitionFailures:
			matchErr = fmt.Errorf("expected %d acquisition failures, got %d", scenario.ExpectedAcquisitionFailures, sourceAcquisition.FetchFailures)
		case scenario.ExpectedPythonPDFExtractions > 0 && sourceAcquisition.RequiredPythonExtractions != scenario.ExpectedPythonPDFExtractions:
			matchErr = fmt.Errorf("expected %d Python PDF extractions, got %d", scenario.ExpectedPythonPDFExtractions, sourceAcquisition.RequiredPythonExtractions)
		}
		record("researchQuality", scenario.Name, matchErr)
	}

	return report, nil
}

func researchEvalAnswerStatus(decision *ResearchVerifierDecision, openCount int, budgetExhausted bool) string {
	state := &ResearchSessionState{VerifierDecision: decision}
	if budgetExhausted {
		state.DurableJob = &ResearchDurableJobState{
			BudgetUsed: ResearchBudgetUsage{Exhausted: true},
			StopReason: "budget_exhausted_with_open_gaps",
		}
		return researchAnswerStatus(state, nil, false, "budget_exhausted_with_open_gaps")
	}
	if decision != nil && decision.Verdict == "promote" && openCount == 0 {
		return researchAnswerStatus(state, &ResearchFinalizationGate{Status: "final", Ready: true, Provisional: false, VerifierVerdict: "promote"}, true, "verified_final")
	}
	if decision != nil && decision.Verdict == "abstain" {
		// When the verifier abstains but has actively rejected specific claims,
		// the answer status is "rejected" rather than merely "blocked".
		if len(decision.RejectedClaimIDs) > 0 {
			return researchAnswerStatus(state, &ResearchFinalizationGate{Status: "rejected", Provisional: false, VerifierVerdict: "abstain"}, false, "claims_rejected")
		}
		return researchAnswerStatus(state, &ResearchFinalizationGate{Status: "blocked", Provisional: true, VerifierVerdict: "abstain"}, false, decision.StopReason)
	}
	return researchAnswerStatus(state, &ResearchFinalizationGate{Status: "provisional", Provisional: true, VerifierVerdict: verifierVerdict(decision)}, false, "coverage_open")
}

func researchEvalObligationTypes(ledger []CoverageLedgerEntry) []string {
	out := make([]string, 0, len(ledger))
	for _, entry := range ledger {
		out = append(out, entry.ObligationType)
	}
	return dedupeTrimmedStrings(out)
}

func researchEvalOwnerWorkers(ledger []CoverageLedgerEntry) []string {
	out := make([]string, 0, len(ledger))
	for _, entry := range ledger {
		out = append(out, entry.OwnerWorker)
	}
	return dedupeTrimmedStrings(out)
}

func researchEvalDurableTaskOps(scenario ResearchQualityEvalScenario, ledger []CoverageLedgerEntry, decision *ResearchVerifierDecision) []string {
	ops := []string{}
	if researchPlaneRequiresDurableJob(ResearchExecutionPlane(strings.TrimSpace(scenario.Plane))) {
		ops = append(ops, researchDurableTaskWorker, researchDurableTaskSearchBatch)
	}
	if len(ledger) > 0 {
		ops = append(ops, researchDurableTaskBranch)
	}
	if decision != nil {
		ops = append(ops, researchDurableTaskVerifier)
	}
	for _, entry := range ledger {
		if entry.OwnerWorker == string(ResearchWorkerCitationGraph) || entry.ObligationType == "missing_citation_identity" {
			ops = append(ops, researchDurableTaskCitationGraph)
			break
		}
	}
	return dedupeTrimmedStrings(ops)
}

func containsAllResearchEvalValues(actual []string, expected []string) bool {
	if len(expected) == 0 {
		return true
	}
	seen := make(map[string]struct{}, len(actual))
	for _, value := range actual {
		seen[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, value := range expected {
		if _, ok := seen[strings.ToLower(strings.TrimSpace(value))]; !ok {
			return false
		}
	}
	return true
}

func researchEvalBudgetExhausted(ledger []CoverageLedgerEntry) bool {
	for _, entry := range ledger {
		if !strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
			continue
		}
		text := strings.ToLower(strings.Join([]string{entry.Category, entry.Title, entry.Description}, " "))
		if strings.Contains(text, "budget") || strings.Contains(text, "exhaust") {
			return true
		}
	}
	return false
}

func researchEvalStaleSourceCount(papers []search.Paper) int {
	currentYear := time.Now().UTC().Year()
	count := 0
	for _, paper := range papers {
		if paper.Year > 0 && currentYear-paper.Year >= 8 {
			count++
		}
	}
	return count
}

func researchEvalCitationConflictCount(ledger []CoverageLedgerEntry, findings []EvidenceFinding) int {
	count := 0
	for _, entry := range ledger {
		if !strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
			continue
		}
		category := strings.ToLower(strings.TrimSpace(entry.Category))
		if strings.Contains(category, "citation_conflict") || strings.Contains(category, "citation_metadata") {
			count++
		}
	}
	seenTitles := map[string]string{}
	for _, finding := range findings {
		sourceID := strings.ToLower(strings.TrimSpace(finding.SourceID))
		title := strings.ToLower(strings.TrimSpace(finding.PaperTitle))
		if sourceID == "" || title == "" {
			continue
		}
		if existing, ok := seenTitles[sourceID]; ok && existing != title {
			count++
			continue
		}
		seenTitles[sourceID] = title
	}
	return count
}
