package wisdev

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type PlanCandidate struct {
	Plan       *PlanState `json:"-"`
	Score      float64    `json:"score"`
	Hypothesis string     `json:"hypothesis"`
	Rationale  string     `json:"rationale"`
}

type PlanSynthesisResult struct {
	Selected    PlanCandidate
	Alternates  []PlanCandidate
	BranchWidth int
}

func containsAny(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func CreatePlanStep(id string, action string, reason string, risk RiskLevel, target ExecutionTarget, parallel bool, deps ...string) PlanStep {
	var tier ModelTier
	if risk == RiskLevelHigh || risk == RiskLevelMedium {
		tier = ModelTierHeavy
	} else {
		tier = ModelTierLight
	}
	return PlanStep{
		ID:                 id,
		Action:             action,
		Reason:             reason,
		Risk:               risk,
		ModelTier:          tier,
		ExecutionTarget:    target,
		Parallelizable:     parallel,
		DependsOnStepIDs:   deps,
		EstimatedCostCents: 1,
		MaxAttempts:        2,
		TimeoutMs:          30000,
	}
}

func newPlanState(planID string, steps []PlanStep) *PlanState {
	return &PlanState{
		PlanID:           planID,
		Steps:            steps,
		CompletedStepIDs: make(map[string]bool),
		FailedStepIDs:    make(map[string]string),
		StepAttempts:     make(map[string]int),
		StepFailureCount: make(map[string]int),
		ApprovedStepIDs:  make(map[string]bool),
		StepConfidences:  make(map[string]float64),
	}
}

func scorePlanCandidate(query string, domain string, steps []PlanStep) float64 {
	q := strings.ToLower(strings.TrimSpace(query))
	base := 0.58
	base += math.Min(0.25, float64(len(steps))*0.04)
	if containsAny(q, "systematic review", "meta-analysis", "protocol", "prisma") {
		for _, step := range steps {
			if strings.Contains(step.Action, "verifyCitations") || strings.Contains(step.Action, "buildClaimEvidenceTable") {
				base += 0.06
			}
		}
	}
	if containsAny(q, "novel", "frontier", "emerging", "future work", "hypothesis") {
		for _, step := range steps {
			if strings.Contains(step.Action, "snowballCitations") {
				base += 0.05
				break
			}
		}
	}
	if domain == "medicine" {
		for _, step := range steps {
			if strings.Contains(strings.ToLower(step.Action), "claim") || strings.Contains(strings.ToLower(step.Action), "citation") {
				base += 0.04
			}
		}
	}
	return ClampFloat(base, 0.45, 0.95)
}

// Mock memory store for cross-session learning
var priorOutcomes []wisdevOutcomeSummary // defined locally

type wisdevOutcomeSummary struct {
	Query       string
	Success     bool
	Hypothesis  string
	FinalReward float64
}

func getPriorsForQuery(query string) []wisdevOutcomeSummary {
	var results []wisdevOutcomeSummary
	lq := strings.ToLower(query)
	for _, o := range priorOutcomes {
		if strings.Contains(lq, strings.ToLower(o.Query)) {
			results = append(results, o)
		}
	}
	return results
}

func RecordPlanOutcome(summary wisdevOutcomeSummary) {
	if len(priorOutcomes) > 1000 {
		priorOutcomes = priorOutcomes[1:]
	}
	priorOutcomes = append(priorOutcomes, summary)
}

func SynthesizePlanCandidates(session *AgentSession, query string) PlanSynthesisResult {
	lq := strings.ToLower(strings.TrimSpace(query))
	branchWidth := 3
	if containsAny(lq, "quick", "fast", "overview") {
		branchWidth = 2
	}
	if containsAny(lq, "thorough", "deep", "comprehensive") {
		branchWidth = 4
	}

	baseID := fmt.Sprintf("plan_%d", time.Now().UnixMilli())
	candidates := make([]PlanCandidate, 0, 4)

	// Candidate A: balanced evidence-first.
	stepsA := []PlanStep{
		CreatePlanStep("step_query_decompose", "research.queryDecompose", "Decompose request into retrieval intents and evidence criteria.", RiskLevelLow, ExecutionTargetPythonCapability, true),
		CreatePlanStep("step_retrieve", "research.retrievePapers", "Retrieve candidate papers from high-signal scholarly providers.", RiskLevelLow, ExecutionTargetGoNative, true, "step_query_decompose"),
		CreatePlanStep("step_claim_evidence", "research.buildClaimEvidenceTable", "Ground claims in explicit evidence rows.", RiskLevelMedium, ExecutionTargetPythonCapability, false, "step_retrieve"),
		CreatePlanStep("step_citation_verify", "research.verifyCitations", "Verify citation metadata integrity before synthesis.", RiskLevelMedium, ExecutionTargetPythonCapability, false, "step_claim_evidence"),
	}
	candidates = append(candidates, PlanCandidate{
		Plan:       newPlanState(baseID+"_a", stepsA),
		Score:      scorePlanCandidate(query, session.DetectedDomain, stepsA),
		Hypothesis: "balanced_evidence_first",
		Rationale:  "Optimizes for grounded synthesis with mandatory citation checks.",
	})

	// Candidate B: exploration-heavy with citation traversal.
	stepsB := []PlanStep{
		CreatePlanStep("step_query_decompose", "research.queryDecompose", "Decompose request and generate exploratory retrieval facets.", RiskLevelLow, ExecutionTargetPythonCapability, true),
		CreatePlanStep("step_retrieve", "research.retrievePapers", "Retrieve initial seed corpus.", RiskLevelLow, ExecutionTargetGoNative, true, "step_query_decompose"),
		CreatePlanStep("step_snowball", "research.snowballCitations", "Expand retrieval via forward/backward citation traversal.", RiskLevelMedium, ExecutionTargetPythonCapability, true, "step_retrieve"),
		CreatePlanStep("step_claim_evidence", "research.buildClaimEvidenceTable", "Consolidate claims with expanded evidence graph.", RiskLevelMedium, ExecutionTargetPythonCapability, false, "step_snowball"),
	}
	candidates = append(candidates, PlanCandidate{
		Plan:       newPlanState(baseID+"_b", stepsB),
		Score:      scorePlanCandidate(query+" snowball", session.DetectedDomain, stepsB),
		Hypothesis: "exploration_then_grounding",
		Rationale:  "Expands discovery frontier before enforcing evidence table synthesis.",
	})

	// Candidate C: conservative verification-first.
	stepsC := []PlanStep{
		CreatePlanStep("step_query_decompose", "research.queryDecompose", "Constrain scope and define strict inclusion criteria.", RiskLevelLow, ExecutionTargetPythonCapability, false),
		CreatePlanStep("step_retrieve", "research.retrievePapers", "Retrieve high-confidence papers only.", RiskLevelLow, ExecutionTargetGoNative, false, "step_query_decompose"),
		CreatePlanStep("step_citation_verify", "research.verifyCitations", "Verify DOI/title/year coherence for all core sources.", RiskLevelMedium, ExecutionTargetPythonCapability, false, "step_retrieve"),
		CreatePlanStep("step_claim_evidence", "research.buildClaimEvidenceTable", "Produce claim-evidence linkage with strict validation.", RiskLevelMedium, ExecutionTargetPythonCapability, false, "step_citation_verify"),
	}
	candidates = append(candidates, PlanCandidate{
		Plan:       newPlanState(baseID+"_c", stepsC),
		Score:      scorePlanCandidate(query+" verify", session.DetectedDomain, stepsC),
		Hypothesis: "verification_first",
		Rationale:  "Prioritizes reliability and metadata hygiene before synthesis.",
	})

	// Candidate D: fast path (kept for simple queries).
	stepsD := []PlanStep{
		CreatePlanStep("step_query_decompose", "research.queryDecompose", "Quickly normalize query into retrieval terms.", RiskLevelLow, ExecutionTargetPythonCapability, true),
		CreatePlanStep("step_retrieve", "research.retrievePapers", "Parallel retrieval and lightweight ranking.", RiskLevelLow, ExecutionTargetGoNative, true, "step_query_decompose"),
		CreatePlanStep("step_claim_evidence", "research.buildClaimEvidenceTable", "Minimal claim grounding for concise output.", RiskLevelMedium, ExecutionTargetPythonCapability, false, "step_retrieve"),
	}
	candidates = append(candidates, PlanCandidate{
		Plan:       newPlanState(baseID+"_d", stepsD),
		Score:      scorePlanCandidate(query+" fast", session.DetectedDomain, stepsD),
		Hypothesis: "fast_balanced",
		Rationale:  "Optimizes for latency while preserving grounding gate.",
	})

	// Query-conditioned priors.
	for i := range candidates {
		if containsAny(lq, "systematic", "meta-analysis", "protocol", "prisma") && strings.Contains(candidates[i].Hypothesis, "verification") {
			candidates[i].Score += 0.08
		}
		if containsAny(lq, "novel", "emerging", "frontier") && strings.Contains(candidates[i].Hypothesis, "exploration") {
			candidates[i].Score += 0.08
		}
		if containsAny(lq, "quick", "fast", "brief") && candidates[i].Hypothesis == "fast_balanced" {
			candidates[i].Score += 0.12
		}

		// Apply cross-session learning priors
		priors := getPriorsForQuery(query)
		for _, prior := range priors {
			if prior.Success && prior.Hypothesis == candidates[i].Hypothesis {
				candidates[i].Score += 0.05 * prior.FinalReward
			}
		}

		candidates[i].Score = ClampFloat(candidates[i].Score, 0.4, 0.97)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Hypothesis < candidates[j].Hypothesis
		}
		return candidates[i].Score > candidates[j].Score
	})

	selected := candidates[0]
	alternates := make([]PlanCandidate, 0, len(candidates)-1)
	for i := 1; i < len(candidates); i++ {
		alternates = append(alternates, candidates[i])
	}
	return PlanSynthesisResult{
		Selected:    selected,
		Alternates:  alternates,
		BranchWidth: branchWidth,
	}
}
