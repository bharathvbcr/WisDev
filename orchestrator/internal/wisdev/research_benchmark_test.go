// Package wisdev — standing benchmark suite for WisDev literature review quality.
//
// These tests are deterministic (no LLM / network calls) and exercise the four
// major architectural capabilities added in the current sprint:
//
//	Task 1 — Tree-of-Thoughts branching (prune/backtrack, belief-driven convergence)
//	Task 2 — Belief control plane (confidence gates, contradiction pressure)
//	Task 3 — Multi-agent wave ordering (supervisor waves, blackboard handoff)
//	Task 4 — First-class provenance lineage (lineage builder, claim entries)
//
// Run with:
//
//	go test ./internal/wisdev/... -run TestBenchmark -v
package wisdev

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeBeliefState builds a BeliefState populated with n beliefs at the given
// confidence values.  The belief IDs are stable string keys.
func makeBeliefState(t *testing.T, confidences []float64) *BeliefState {
	t.Helper()
	bs := NewBeliefState()
	for i, c := range confidences {
		id := stableWisDevID("bench-belief", fmt.Sprintf("belief-%d", i), "", "")
		bs.AddBelief(&Belief{
			ID:         id,
			Claim:      fmt.Sprintf("test claim %d", i),
			Confidence: c,
			Status:     BeliefStatusActive,
			CreatedAt:  time.Now().UnixMilli(),
			UpdatedAt:  time.Now().UnixMilli(),
		})
	}
	return bs
}

// makeEvidenceFinding creates a minimal EvidenceFinding with the given ID and
// optional ProvenanceChain entries for lineage testing.
func makeEvidenceFinding(id, claim, sourceID string, confidence float64, queryIDs ...string) EvidenceFinding {
	chain := make([]ProvenanceEntry, len(queryIDs))
	for i, qid := range queryIDs {
		chain[i] = ProvenanceEntry{QueryID: qid, Timestamp: time.Now().UnixMilli()}
	}
	return EvidenceFinding{
		ID:              id,
		Claim:           claim,
		SourceID:        sourceID,
		Confidence:      confidence,
		ProvenanceChain: chain,
	}
}

// makeSessionState creates a minimal ResearchSessionState for unit tests.
func makeSessionState(query string, plane ResearchExecutionPlane, plannedQueries []string) *ResearchSessionState {
	return &ResearchSessionState{
		SessionID:      NewTraceID(),
		Query:          query,
		Plane:          plane,
		PlannedQueries: append([]string(nil), plannedQueries...),
		Workers:        []ResearchWorkerState{},
	}
}

// ---------------------------------------------------------------------------
// Task 1 — Tree-of-Thoughts branching
// ---------------------------------------------------------------------------

// TestBenchmark_BeliefConvergenceGate_ConvergesWhenAllHighConfidence verifies
// that shouldConvergeByBeliefState returns true when ≥3 active beliefs all have
// confidence ≥ 0.75.
func TestBenchmark_BeliefConvergenceGate_ConvergesWhenAllHighConfidence(t *testing.T) {
	bs := makeBeliefState(t, []float64{0.8, 0.9, 0.77, 0.85})
	if !shouldConvergeByBeliefState(bs) {
		t.Fatal("expected convergence when all 4 beliefs >= 0.75")
	}
}

// TestBenchmark_BeliefConvergenceGate_NoConvergeBelowThreshold verifies that
// the gate stays open when any belief is below 0.75.
func TestBenchmark_BeliefConvergenceGate_NoConvergeBelowThreshold(t *testing.T) {
	// One belief below threshold — should not converge.
	bs := makeBeliefState(t, []float64{0.8, 0.74, 0.9})
	if shouldConvergeByBeliefState(bs) {
		t.Fatal("expected no convergence when one belief is at 0.74")
	}
}

// TestBenchmark_BeliefConvergenceGate_NoConvergeTooFewBeliefs verifies the
// minimum belief count requirement (need at least 3).
func TestBenchmark_BeliefConvergenceGate_NoConvergeTooFewBeliefs(t *testing.T) {
	bs := makeBeliefState(t, []float64{1.0, 1.0})
	if shouldConvergeByBeliefState(bs) {
		t.Fatal("expected no convergence with only 2 beliefs")
	}
}

// TestBenchmark_BeliefConvergenceGate_NilState verifies nil safety.
func TestBenchmark_BeliefConvergenceGate_NilState(t *testing.T) {
	if shouldConvergeByBeliefState(nil) {
		t.Fatal("expected no convergence for nil BeliefState")
	}
}

// TestBenchmark_BranchScoreRanking verifies that a branch with open research
// gaps receives a strictly higher follow-up candidate score than a branch with
// no gaps — open gaps signal "this branch still needs investigation".
func TestBenchmark_BranchScoreRanking(t *testing.T) {
	gappyBranch := ResearchBranchEvaluation{
		ID:           "gappy",
		OverallScore: 0.1,
		Evidence:     []EvidenceFinding{},
		OpenGaps:     []string{"gap1", "gap2", "gap3"},
	}
	closedBranch := ResearchBranchEvaluation{
		ID:           "closed",
		OverallScore: 0.9,
		Evidence: []EvidenceFinding{
			{ID: "e1", Claim: "claim a", Confidence: 0.9},
			{ID: "e2", Claim: "claim b", Confidence: 0.85},
		},
		OpenGaps: []string{},
	}
	gappyScore := scoreBranchFollowUpCandidate(gappyBranch)
	closedScore := scoreBranchFollowUpCandidate(closedBranch)
	if gappyScore <= closedScore {
		t.Fatalf("gappy branch score %d should be > closed branch score %d (open gaps increase follow-up priority)", gappyScore, closedScore)
	}
}

// ---------------------------------------------------------------------------
// Task 2 — Belief control plane
// ---------------------------------------------------------------------------

// TestBenchmark_BeliefManagerAverageConfidence verifies GetAverageConfidence
// across a known set of beliefs.
func TestBenchmark_BeliefManagerAverageConfidence(t *testing.T) {
	mgr := NewBeliefStateManager()
	mgr.GetState().AddBelief(&Belief{
		ID:                 "bm-a1",
		Claim:              "claim a",
		Confidence:         0.6,
		Status:             BeliefStatusActive,
		SupportingEvidence: []string{"e1"},
		CreatedAt:          time.Now().UnixMilli(),
		UpdatedAt:          time.Now().UnixMilli(),
	})
	mgr.GetState().AddBelief(&Belief{
		ID:                 "bm-a2",
		Claim:              "claim b",
		Confidence:         0.8,
		Status:             BeliefStatusActive,
		SupportingEvidence: []string{"e2"},
		CreatedAt:          time.Now().UnixMilli(),
		UpdatedAt:          time.Now().UnixMilli(),
	})

	avg := mgr.GetAverageConfidence()
	want := 0.7
	if avg < want-0.01 || avg > want+0.01 {
		t.Fatalf("GetAverageConfidence: want ~%.2f, got %.4f", want, avg)
	}
}

// TestBenchmark_BeliefManagerContradictionPressure verifies that
// GetContradictionPressure returns a non-zero value when contradicting evidence
// is present.
func TestBenchmark_BeliefManagerContradictionPressure(t *testing.T) {
	mgr := NewBeliefStateManager()
	mgr.GetState().AddBelief(&Belief{
		ID:                    "bm-p1",
		Claim:                 "contradicted claim",
		Confidence:            0.5,
		Status:                BeliefStatusActive,
		SupportingEvidence:    []string{"e1"},
		ContradictingEvidence: []string{"e_contra1", "e_contra2"},
		CreatedAt:             time.Now().UnixMilli(),
		UpdatedAt:             time.Now().UnixMilli(),
	})

	pressure := mgr.GetContradictionPressure()
	if pressure <= 0 {
		t.Fatalf("expected positive contradiction pressure, got %.4f", pressure)
	}
}

// TestBenchmark_BeliefManagerZeroPressureNoContradictions checks that a belief
// with no contradicting evidence yields zero pressure.
func TestBenchmark_BeliefManagerZeroPressureNoContradictions(t *testing.T) {
	mgr := NewBeliefStateManager()
	mgr.GetState().AddBelief(&Belief{
		ID:                 "bm-z1",
		Claim:              "uncontested claim",
		Confidence:         0.9,
		Status:             BeliefStatusActive,
		SupportingEvidence: []string{"e1"},
		CreatedAt:          time.Now().UnixMilli(),
		UpdatedAt:          time.Now().UnixMilli(),
	})

	pressure := mgr.GetContradictionPressure()
	if pressure != 0 {
		t.Fatalf("expected zero contradiction pressure for uncontested belief, got %.4f", pressure)
	}
}

// TestBenchmark_BeliefRecalculateConfidenceDrop verifies that
// RecalculateConfidence reduces confidence when strong contradicting evidence
// outweighs supporting evidence for a belief.
func TestBenchmark_BeliefRecalculateConfidenceDrop(t *testing.T) {
	mgr := NewBeliefStateManager()
	mgr.GetState().AddBelief(&Belief{
		ID:                    "bm-rc1",
		Claim:                 "vulnerable claim",
		Confidence:            0.9,
		Status:                BeliefStatusActive,
		SupportingEvidence:    []string{"e_sup"},
		ContradictingEvidence: []string{"e_low"},
		CreatedAt:             time.Now().UnixMilli(),
		UpdatedAt:             time.Now().UnixMilli(),
	})

	before := mgr.GetState().GetActiveBeliefs()[0].Confidence
	// Supporting evidence is weak (0.2), contradicting evidence is strong (0.9).
	evidenceMap := map[string]EvidenceFinding{
		"e_sup": {ID: "e_sup", Claim: "supports", Confidence: 0.2},
		"e_low": {ID: "e_low", Claim: "contradicts", Confidence: 0.9},
	}
	mgr.RecalculateConfidence(evidenceMap)
	after := mgr.GetState().GetActiveBeliefs()[0].Confidence
	if after >= before {
		t.Fatalf("expected confidence to drop from %.2f after strong contradiction, got %.2f", before, after)
	}
}

// ---------------------------------------------------------------------------
// Task 3 — Multi-agent wave ordering
// ---------------------------------------------------------------------------

// TestBenchmark_SupervisorWaves_ThreeWavesForHighDepthPlanes verifies that
// deep, multi_agent, and autonomous planes produce the 3-wave plan.
func TestBenchmark_SupervisorWaves_ThreeWavesForHighDepthPlanes(t *testing.T) {
	planes := []ResearchExecutionPlane{
		ResearchExecutionPlaneDeep,
		ResearchExecutionPlaneMultiAgent,
		ResearchExecutionPlaneAutonomous,
	}
	for _, plane := range planes {
		waves := supervisorWaves(plane)
		if len(waves) != 3 {
			t.Errorf("plane %q: expected 3 waves, got %d", plane, len(waves))
		}
	}
}

// TestBenchmark_SupervisorWaves_SingleWaveForOtherPlanes verifies that non-deep
// planes fall back to a single scout-only wave.
func TestBenchmark_SupervisorWaves_SingleWaveForOtherPlanes(t *testing.T) {
	for _, plane := range []ResearchExecutionPlane{ResearchExecutionPlaneJob, ResearchExecutionPlaneQuest, "unknown"} {
		waves := supervisorWaves(plane)
		if len(waves) != 1 {
			t.Errorf("plane %q: expected 1 wave, got %d", plane, len(waves))
		}
		if len(waves[0]) != 1 || waves[0][0] != ResearchWorkerScout {
			t.Errorf("plane %q: expected single Scout wave, got %v", plane, waves[0])
		}
	}
}

// TestBenchmark_SupervisorWaves_GatherWaveContainsAllGatherRoles checks that
// Wave 1 always contains the five gather specialists.
func TestBenchmark_SupervisorWaves_GatherWaveContainsAllGatherRoles(t *testing.T) {
	waves := supervisorWaves(ResearchExecutionPlaneDeep)
	gatherWave := waves[0]
	required := []ResearchWorkerRole{
		ResearchWorkerScout,
		ResearchWorkerSourceDiversifier,
		ResearchWorkerCitationVerifier,
		ResearchWorkerCitationGraph,
		ResearchWorkerContradictionCritic,
	}
	roleSet := make(map[ResearchWorkerRole]bool, len(gatherWave))
	for _, r := range gatherWave {
		roleSet[r] = true
	}
	for _, r := range required {
		if !roleSet[r] {
			t.Errorf("Wave 1 missing required gather role: %q", r)
		}
	}
}

// TestBenchmark_SupervisorWaves_WaveOrderingEnforcesVerifierThenSynthesizer
// verifies that Wave 2 = IndependentVerifier and Wave 3 = Synthesizer.
func TestBenchmark_SupervisorWaves_WaveOrderingEnforcesVerifierThenSynthesizer(t *testing.T) {
	waves := supervisorWaves(ResearchExecutionPlaneMultiAgent)
	if len(waves) < 3 {
		t.Fatalf("expected 3 waves, got %d", len(waves))
	}
	if len(waves[1]) != 1 || waves[1][0] != ResearchWorkerIndependentVerifier {
		t.Errorf("Wave 2 should be [IndependentVerifier], got %v", waves[1])
	}
	if len(waves[2]) != 1 || waves[2][0] != ResearchWorkerSynthesizer {
		t.Errorf("Wave 3 should be [Synthesizer], got %v", waves[2])
	}
}

// TestBenchmark_VerifierConsumesBlackboard verifies that executeResearchWorkerInContext
// populates blackboard-derived artifacts on IndependentVerifier when a non-nil
// blackboard is provided.
func TestBenchmark_VerifierConsumesBlackboard(t *testing.T) {
	rt := &UnifiedResearchRuntime{}
	board := &ResearchBlackboard{
		Evidence:          []EvidenceFinding{{ID: "e1"}, {ID: "e2"}, {ID: "e3"}},
		OpenLedgerCount:   2,
		ReadyForSynthesis: false,
	}
	session := &AgentSession{SessionID: "bench-session"}
	result := rt.executeResearchWorkerInContext(
		context.Background(), ResearchWorkerIndependentVerifier, session,
		"test query", "cs", "deep", false, 0, board,
	)
	if v, ok := result.Artifacts["blackboardEvidenceCount"]; !ok || v.(int) != 3 {
		t.Errorf("expected blackboardEvidenceCount=3, got %v", v)
	}
	if v, ok := result.Artifacts["blackboardOpenLedger"]; !ok || v.(int) != 2 {
		t.Errorf("expected blackboardOpenLedger=2, got %v", v)
	}
	if v, ok := result.Artifacts["blackboardReadyForSynthesis"]; !ok || v.(bool) != false {
		t.Errorf("expected blackboardReadyForSynthesis=false, got %v", v)
	}
	// Should have injected an open-ledger warning note.
	foundNote := false
	for _, n := range result.Notes {
		if strings.HasPrefix(n, "verifier:") {
			foundNote = true
			break
		}
	}
	if !foundNote {
		t.Errorf("expected verifier note about open ledger items, got notes: %v", result.Notes)
	}
}

// TestBenchmark_SynthesizerConsumesBlackboard verifies that Synthesizer
// populates arbitration artifacts and blocks when gate is closed.
func TestBenchmark_SynthesizerConsumesBlackboard(t *testing.T) {
	rt := &UnifiedResearchRuntime{}
	board := &ResearchBlackboard{
		ReadyForSynthesis: false,
		SynthesisGate:     "open ledger blocks synthesis",
		Arbitration: &ResearchArbitrationState{
			Verdict:          "revision_required",
			PromotedClaimIDs: []string{"c1", "c2"},
			RejectedClaimIDs: []string{"c3"},
		},
	}
	session := &AgentSession{SessionID: "bench-synth-session"}
	result := rt.executeResearchWorkerInContext(
		context.Background(), ResearchWorkerSynthesizer, session,
		"test query", "cs", "deep", false, 0, board,
	)
	if v, ok := result.Artifacts["blackboardReadyForSynthesis"]; !ok || v.(bool) != false {
		t.Errorf("expected blackboardReadyForSynthesis=false, got %v", v)
	}
	if v, ok := result.Artifacts["arbitrationVerdict"]; !ok || v.(string) != "revision_required" {
		t.Errorf("expected arbitrationVerdict=revision_required, got %v", v)
	}
	if v, ok := result.Artifacts["promotedClaimCount"]; !ok || v.(int) != 2 {
		t.Errorf("expected promotedClaimCount=2, got %v", v)
	}
	// Should have injected a gate-blocked note.
	foundBlocked := false
	for _, n := range result.Notes {
		if strings.HasPrefix(n, "synthesizer:") {
			foundBlocked = true
			break
		}
	}
	if !foundBlocked {
		t.Errorf("expected synthesizer note about blocked gate, got notes: %v", result.Notes)
	}
}

// TestBenchmark_VerifierAndSynthesizerNilBlackboard confirms nil blackboard
// is handled safely (no panic) for both roles.
func TestBenchmark_VerifierAndSynthesizerNilBlackboard(t *testing.T) {
	rt := &UnifiedResearchRuntime{}
	session := &AgentSession{SessionID: "bench-nil-session"}
	for _, role := range []ResearchWorkerRole{ResearchWorkerIndependentVerifier, ResearchWorkerSynthesizer} {
		result := rt.executeResearchWorkerInContext(
			context.Background(), role, session, "test query", "cs", "deep", false, 0, nil,
		)
		// With nil blackboard no blackboard artifacts should be present.
		if _, ok := result.Artifacts["blackboardEvidenceCount"]; ok {
			t.Errorf("role %q: unexpected blackboard artifact with nil blackboard", role)
		}
	}
}

// ---------------------------------------------------------------------------
// Task 4 — Provenance lineage
// ---------------------------------------------------------------------------

// TestBenchmark_LineageUserQueryPreserved verifies that the lineage preserves
// the original user query exactly.
func TestBenchmark_LineageUserQueryPreserved(t *testing.T) {
	state := makeSessionState("what is retrieval augmented generation?",
		ResearchExecutionPlaneDeep, []string{"RAG fundamentals", "RAG retrieval methods"})
	result := &LoopResult{
		ExecutedQueries: []string{"RAG fundamentals", "RAG retrieval methods"},
		Evidence:        []EvidenceFinding{},
		Papers:          []search.Paper{},
	}
	lineage := buildResearchLineage(state, result)
	if lineage == nil {
		t.Fatal("buildResearchLineage returned nil")
	}
	if lineage.UserQuery != state.Query {
		t.Errorf("UserQuery: want %q, got %q", state.Query, lineage.UserQuery)
	}
}

// TestBenchmark_LineageDecompositionMatchesPlanned verifies that
// Decomposition equals the state's planned queries.
func TestBenchmark_LineageDecompositionMatchesPlanned(t *testing.T) {
	planned := []string{"q1", "q2", "q3"}
	state := makeSessionState("some query", ResearchExecutionPlaneDeep, planned)
	result := &LoopResult{ExecutedQueries: planned, Evidence: nil, Papers: nil}
	lineage := buildResearchLineage(state, result)
	if len(lineage.Decomposition) != len(planned) {
		t.Fatalf("Decomposition length: want %d, got %d", len(planned), len(lineage.Decomposition))
	}
	for i, q := range planned {
		if lineage.Decomposition[i] != q {
			t.Errorf("Decomposition[%d]: want %q, got %q", i, q, lineage.Decomposition[i])
		}
	}
}

// TestBenchmark_LineageSubqueryToEvidenceMapping verifies that evidence findings
// with ProvenanceChain QueryID entries are correctly indexed by subquery.
func TestBenchmark_LineageSubqueryToEvidenceMapping(t *testing.T) {
	ev1 := makeEvidenceFinding("ev1", "claim one", "paper-1", 0.9, "query-a")
	ev2 := makeEvidenceFinding("ev2", "claim two", "paper-2", 0.8, "query-a", "query-b")
	ev3 := makeEvidenceFinding("ev3", "claim three", "paper-3", 0.7, "query-b")

	state := makeSessionState("multi-query test", ResearchExecutionPlaneDeep, []string{"query-a", "query-b"})
	result := &LoopResult{
		ExecutedQueries: []string{"query-a", "query-b"},
		Evidence:        []EvidenceFinding{ev1, ev2, ev3},
		Papers:          []search.Paper{},
	}
	lineage := buildResearchLineage(state, result)

	// query-a should map to ev1 and ev2
	qA := lineage.SubqueryToEvidence["query-a"]
	if len(qA) != 2 {
		t.Errorf("query-a: expected 2 evidence IDs, got %d: %v", len(qA), qA)
	}
	// query-b should map to ev2 and ev3
	qB := lineage.SubqueryToEvidence["query-b"]
	if len(qB) != 2 {
		t.Errorf("query-b: expected 2 evidence IDs, got %d: %v", len(qB), qB)
	}
}

// TestBenchmark_LineageEvidenceToSourceMapping verifies EvidenceToSource index.
func TestBenchmark_LineageEvidenceToSourceMapping(t *testing.T) {
	ev := makeEvidenceFinding("ev10", "a claim", "paper-10", 0.9)
	state := makeSessionState("source mapping test", ResearchExecutionPlaneDeep, nil)
	result := &LoopResult{Evidence: []EvidenceFinding{ev}, Papers: nil}
	lineage := buildResearchLineage(state, result)
	if src, ok := lineage.EvidenceToSource["ev10"]; !ok || src != "paper-10" {
		t.Errorf("EvidenceToSource[ev10]: want paper-10, got %q (ok=%v)", src, ok)
	}
}

// TestBenchmark_LineageClaimProvenanceEntries verifies that one
// ClaimProvenanceEntry is produced per unique evidence finding with a claim.
func TestBenchmark_LineageClaimProvenanceEntries(t *testing.T) {
	ev1 := makeEvidenceFinding("ev20", "claim alpha", "paper-20", 0.85)
	ev2 := makeEvidenceFinding("ev21", "claim beta", "paper-21", 0.75)
	evNoClaim := makeEvidenceFinding("ev22", "", "paper-22", 0.5)

	state := makeSessionState("claim provenance test", ResearchExecutionPlaneDeep, nil)
	result := &LoopResult{
		Evidence: []EvidenceFinding{ev1, ev2, evNoClaim},
		Papers:   nil,
	}
	lineage := buildResearchLineage(state, result)

	// evNoClaim has empty claim — should be excluded.
	if len(lineage.ClaimProvenance) != 2 {
		t.Fatalf("expected 2 ClaimProvenanceEntries (empty-claim excluded), got %d", len(lineage.ClaimProvenance))
	}
	byID := make(map[string]ClaimProvenanceEntry, 2)
	for _, e := range lineage.ClaimProvenance {
		byID[e.ClaimID] = e
	}
	if e, ok := byID["ev20"]; !ok || e.ClaimText != "claim alpha" {
		t.Errorf("expected entry for ev20 with claim alpha, got %+v", byID["ev20"])
	}
	if e, ok := byID["ev21"]; !ok || e.Confidence != 0.75 {
		t.Errorf("expected entry for ev21 with confidence 0.75, got %+v", byID["ev21"])
	}
}

// TestBenchmark_LineageWorkerContributions verifies that worker evidence IDs
// are correctly bucketed by worker role in WorkerContributions.
func TestBenchmark_LineageWorkerContributions(t *testing.T) {
	ev := makeEvidenceFinding("ev30", "worker claim", "paper-30", 0.9)
	state := makeSessionState("worker test", ResearchExecutionPlaneDeep, nil)
	state.Workers = []ResearchWorkerState{
		{
			Role:     ResearchWorkerScout,
			Status:   "completed",
			Evidence: []EvidenceFinding{ev},
		},
	}
	result := &LoopResult{Evidence: []EvidenceFinding{ev}, Papers: nil}
	lineage := buildResearchLineage(state, result)

	ids, ok := lineage.WorkerContributions[string(ResearchWorkerScout)]
	if !ok || len(ids) != 1 || ids[0] != "ev30" {
		t.Errorf("expected worker contributions for scout=[ev30], got %v (ok=%v)", ids, ok)
	}
}

// TestBenchmark_LineageNilSafety verifies that buildResearchLineage does not
// panic on nil inputs.
func TestBenchmark_LineageNilSafety(t *testing.T) {
	if buildResearchLineage(nil, nil) != nil {
		t.Error("expected nil lineage for nil inputs")
	}
	state := makeSessionState("q", ResearchExecutionPlaneDeep, nil)
	if buildResearchLineage(state, nil) != nil {
		t.Error("expected nil lineage for nil loop result")
	}
}

// TestBenchmark_LineageBuilderVersionIsSet verifies the BuilderVersion field
// is present so clients can detect schema evolution.
func TestBenchmark_LineageBuilderVersionIsSet(t *testing.T) {
	state := makeSessionState("versioning test", ResearchExecutionPlaneDeep, nil)
	result := &LoopResult{Evidence: nil, Papers: nil}
	lineage := buildResearchLineage(state, result)
	if lineage.BuilderVersion == "" {
		t.Error("BuilderVersion must not be empty")
	}
}

// ---------------------------------------------------------------------------
// Composite benchmark: end-to-end lineage shape for a well-populated result
// ---------------------------------------------------------------------------

// TestBenchmark_FullLineageShape exercises the complete lineage builder with
// evidence from multiple workers, cross-query provenance, and verifies all
// top-level fields are populated.
func TestBenchmark_FullLineageShape(t *testing.T) {
	const query = "how does attention mechanism improve transformers?"
	planned := []string{"attention mechanism transformers", "self-attention scalability"}
	ev1 := makeEvidenceFinding("ev-full-1", "attention allows parallel processing", "arxiv-1234", 0.9, "attention mechanism transformers")
	ev2 := makeEvidenceFinding("ev-full-2", "self-attention scales quadratically", "arxiv-5678", 0.82, "self-attention scalability")
	ev3 := makeEvidenceFinding("ev-full-3", "multi-head attention improves representation", "arxiv-1234", 0.78, "attention mechanism transformers", "self-attention scalability")

	state := makeSessionState(query, ResearchExecutionPlaneAutonomous, planned)
	state.Workers = []ResearchWorkerState{
		{Role: ResearchWorkerScout, Status: "completed", Evidence: []EvidenceFinding{ev1, ev2}},
		{Role: ResearchWorkerCitationVerifier, Status: "completed", Evidence: []EvidenceFinding{ev3}},
	}
	result := &LoopResult{
		ExecutedQueries: planned,
		Evidence:        []EvidenceFinding{ev1, ev2, ev3},
		Papers:          []search.Paper{{Title: "Attention Is All You Need", ID: "arxiv-1234"}},
	}

	lineage := buildResearchLineage(state, result)
	if lineage == nil {
		t.Fatal("lineage must not be nil")
	}

	// User query preserved.
	if lineage.UserQuery != query {
		t.Errorf("UserQuery mismatch")
	}
	// All 3 evidence findings produce claim entries.
	if len(lineage.ClaimProvenance) != 3 {
		t.Errorf("expected 3 claim provenance entries, got %d", len(lineage.ClaimProvenance))
	}
	// Both workers have contributions.
	if _, ok := lineage.WorkerContributions[string(ResearchWorkerScout)]; !ok {
		t.Error("missing scout contributions")
	}
	if _, ok := lineage.WorkerContributions[string(ResearchWorkerCitationVerifier)]; !ok {
		t.Error("missing citation_verifier contributions")
	}
	// SubqueryToEvidence has entries for both queries.
	if len(lineage.SubqueryToEvidence) == 0 {
		t.Error("SubqueryToEvidence should not be empty")
	}
	// BuiltAtMs is in the recent past (not zero).
	if lineage.BuiltAtMs <= 0 {
		t.Error("BuiltAtMs must be a positive Unix millisecond timestamp")
	}
}
