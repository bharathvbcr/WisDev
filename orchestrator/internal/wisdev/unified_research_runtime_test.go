package wisdev

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/mock"
)

func TestExtractProgrammaticQueriesFromTreeResult(t *testing.T) {
	tree := treeLoopResult{
		Final: map[string]any{
			"tasks": []map[string]any{
				{"name": "longitudinal biomarker validation"},
				{"query": "systematic review biomarker reproducibility"},
			},
		},
	}

	queries := extractProgrammaticQueriesFromTreeResult(tree)
	if len(queries) != 2 {
		t.Fatalf("expected 2 queries, got %d", len(queries))
	}
	if queries[0] != "longitudinal biomarker validation" {
		t.Fatalf("unexpected first query %q", queries[0])
	}
}

func TestInitializeLiveReasoningGraphIncludesWorkerNodes(t *testing.T) {
	state := newResearchSessionState("oncology biomarker reproducibility", "medicine", "sess_1", ResearchExecutionPlaneAutonomous)
	graph := initializeLiveReasoningGraph(state, []string{"oncology biomarker longitudinal cohort"})
	if graph == nil {
		t.Fatalf("expected graph")
	}
	if len(graph.Nodes) < 3 {
		t.Fatalf("expected worker and query nodes, got %d nodes", len(graph.Nodes))
	}
	if graph.Root == "" {
		t.Fatalf("expected root node")
	}
}

func TestExecuteResearchWorkersProducesExecutableRoleQueries(t *testing.T) {
	calls := 0
	rt := NewUnifiedResearchRuntime(nil, nil, nil, func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		calls++
		if action == "research.verifyClaims" {
			return map[string]any{"score": 0.88}, nil
		}
		if action == "research.generateThoughts" {
			return map[string]any{
				"branches": []map[string]any{{
					"label":      "mechanism",
					"hypothesis": "graph neural networks drug discovery mechanism branch",
					"reasoning":  "decompose mechanism evidence",
				}},
				"confidence": 0.86,
			}, nil
		}
		if action != ActionResearchQueryDecompose {
			t.Fatalf("unexpected action %s", action)
		}
		return map[string]any{
			"tasks": []map[string]any{
				{"query": "graph neural networks drug discovery mechanism branch"},
			},
			"confidence": 0.91,
		}, nil
	})
	state := newResearchSessionState("graph neural networks in drug discovery", "medicine", "sess_workers", ResearchExecutionPlaneMultiAgent)
	session := &AgentSession{SessionID: state.SessionID, Query: state.Query, DetectedDomain: state.Domain}

	queries := rt.executeResearchWorkers(context.Background(), state, session, state.Query, state.Domain, string(WisDevModeYOLO), true, 5)

	if calls == 0 {
		t.Fatalf("expected scout worker to invoke programmatic tree planning")
	}
	for _, expected := range []string{
		"graph neural networks drug discovery mechanism branch",
		"systematic review meta analysis",
		"open access full text",
		"canonical citations replication evidence",
		"limitations contradictory evidence",
	} {
		if !containsQueryFragment(queries, expected) {
			t.Fatalf("expected worker query containing %q, got %#v", expected, queries)
		}
	}
	for _, worker := range state.Workers {
		if worker.Role == ResearchWorkerIndependentVerifier {
			if worker.Status != "completed" {
				t.Fatalf("expected independent verifier to emit a contract packet, got %s", worker.Status)
			}
			if _, ok := worker.Artifacts["verifierFinalizationGate"]; !ok {
				t.Fatalf("expected independent verifier to point at finalization gate, got %#v", worker.Artifacts)
			}
			continue
		}
		if worker.Status != "completed" {
			t.Fatalf("expected worker %s completed, got %s", worker.Role, worker.Status)
		}
		if worker.Contract.Role != worker.Role {
			t.Fatalf("expected worker %s to carry typed contract, got %#v", worker.Role, worker.Contract)
		}
	}
	if !researchWorkersExecuted(state.Workers) {
		t.Fatalf("expected worker execution marker")
	}
}

func TestExecuteResearchWorkersHonorsDisabledProgrammaticPlanning(t *testing.T) {
	calls := 0
	rt := NewUnifiedResearchRuntime(nil, nil, nil, func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		calls++
		return map[string]any{"tasks": []map[string]any{{"query": "should not run"}}}, nil
	})
	state := newResearchSessionState("sleep and memory", "neuroscience", "session-worker-policy", ResearchExecutionPlaneAutonomous)
	session := &AgentSession{SessionID: state.SessionID, Query: state.Query, DetectedDomain: state.Domain}

	queries := rt.executeResearchWorkers(context.Background(), state, session, state.Query, state.Domain, string(WisDevModeGuided), false, 5)

	if calls != 0 {
		t.Fatalf("programmatic planner executed despite disabled planning: %d calls", calls)
	}
	if !containsQueryFragment(queries, "core mechanisms evidence map") {
		t.Fatalf("expected deterministic scout fallback query when programmatic planning is disabled, got %#v", queries)
	}
	if !containsWorkerNote(state.Workers, ResearchWorkerScout, "programmatic tree planner disabled") {
		t.Fatalf("expected scout worker to record disabled programmatic planning, got %#v", state.Workers)
	}
}

func TestExecuteResearchWorkersRunsBoundedSpecialistSearches(t *testing.T) {
	reg := search.NewProviderRegistry()
	var mu sync.Mutex
	seenQueries := make([]string, 0)
	reg.Register(&mockSearchProvider{
		name: "mock-worker-search",
		SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			mu.Lock()
			seenQueries = append(seenQueries, query)
			mu.Unlock()
			return []search.Paper{{
				ID:       stableWisDevID("paper", query),
				Title:    "Evidence for " + query,
				Abstract: "This grounded source reports evidence, limitations, replication context, and citation metadata for " + query + ".",
				Source:   "mock-worker-search",
				Score:    0.91,
			}}, nil
		},
		GetCitationsFunc: func(ctx context.Context, paperID string, limit int) ([]search.Paper, error) {
			return []search.Paper{{
				ID:            stableWisDevID("citing-paper", paperID),
				Title:         "Follow-up citation for " + paperID,
				Abstract:      "This citing paper independently discusses replication and citation context.",
				Source:        "semantic_scholar",
				DOI:           "10.1000/" + strings.TrimPrefix(stableWisDevID("doi", paperID), "wis_"),
				CitationCount: 5,
				Year:          2024,
			}}, nil
		},
	})
	rt := NewUnifiedResearchRuntime(nil, reg, nil, nil)
	state := newResearchSessionState("graph neural networks in drug discovery", "", "sess-worker-search", ResearchExecutionPlaneMultiAgent)
	session := &AgentSession{SessionID: state.SessionID, Query: state.Query}
	var events []PlanExecutionEvent

	queries := rt.executeResearchWorkers(context.Background(), state, session, state.Query, state.Domain, string(WisDevModeYOLO), false, 5, func(event PlanExecutionEvent) {
		events = append(events, event)
	})
	board := buildResearchBlackboard(state.Workers)

	if len(queries) == 0 {
		t.Fatalf("expected planned worker queries")
	}
	if len(seenQueries) == 0 {
		t.Fatalf("expected workers to execute bounded specialist searches")
	}
	if len(board.Evidence) == 0 {
		t.Fatalf("expected worker blackboard evidence")
	}
	if board.Contracts[ResearchWorkerCitationVerifier].CompletionGate == "" {
		t.Fatalf("expected blackboard to preserve citation verifier contract, got %#v", board.Contracts)
	}
	if board.Contracts[ResearchWorkerCitationGraph].CompletionGate == "" {
		t.Fatalf("expected blackboard to preserve citation graph contract, got %#v", board.Contracts)
	}
	if board.Contracts[ResearchWorkerIndependentVerifier].CompletionGate == "" {
		t.Fatalf("expected blackboard to preserve independent verifier contract, got %#v", board.Contracts)
	}
	if board.CitationGraph == nil || len(board.CitationGraph.Nodes) == 0 || len(board.CitationGraph.Edges) == 0 {
		t.Fatalf("expected citation graph worker to attach nodes and edges, got %#v", board.CitationGraph)
	}
	if !containsLedgerCategory(board.CoverageLedger, "citation_graph") {
		t.Fatalf("expected citation graph coverage obligation in blackboard ledger, got %#v", board.CoverageLedger)
	}
	if !containsLedgerCategory(board.CoverageLedger, "worker_independent_verifier") {
		t.Fatalf("expected independent verifier packet in blackboard ledger, got %#v", board.CoverageLedger)
	}
	if len(blackboardPapers(state.Workers)) == 0 {
		t.Fatalf("expected worker papers to seed the autonomous loop")
	}
	if len(blackboardQueryCoverage(state.Workers)) == 0 {
		t.Fatalf("expected worker query coverage")
	}
	if !containsWorkerNote(state.Workers, ResearchWorkerCitationVerifier, "executed") {
		t.Fatalf("expected citation verifier to report executed search, got %#v", state.Workers)
	}
	if !containsQueryFragment(board.ExecutedQueries, "canonical citations") {
		t.Fatalf("expected citation verifier query to execute, got %#v", board.ExecutedQueries)
	}
	if !containsWorkerEvent(events, ResearchWorkerCitationVerifier, "completed") {
		t.Fatalf("expected worker completion event for citation verifier, got %#v", events)
	}
	if !containsWorkerNote(state.Workers, ResearchWorkerIndependentVerifier, "finalization gate") {
		t.Fatalf("expected independent verifier to emit finalization-gate packet, got %#v", state.Workers)
	}
	if !containsWorkerEvent(events, ResearchWorkerIndependentVerifier, "completed") {
		t.Fatalf("expected worker completion event for independent verifier, got %#v", events)
	}
	for _, operation := range []string{researchDurableTaskWorker, researchDurableTaskSearchBatch, researchDurableTaskCitationGraph} {
		task := findDurableTaskByOperation(state.DurableTasks, operation)
		if task == nil {
			t.Fatalf("expected durable task operation %q, got %#v", operation, state.DurableTasks)
		}
		if task.TaskKey == "" || task.CheckpointKey == "" || task.TraceID == "" || task.TimeoutMs <= 0 || task.RetryPolicy.MaxAttempts <= 0 {
			t.Fatalf("expected durable task to include idempotency/checkpoint/timeout/retry metadata, got %#v", task)
		}
	}
}

func TestExecuteResearchWorkersAnchorsScoutOnRootQueryAndHonorsBudget(t *testing.T) {
	reg := search.NewProviderRegistry()
	var mu sync.Mutex
	seenQueries := make([]string, 0)
	reg.Register(&mockSearchProvider{
		name: "mock-worker-budget",
		SearchFunc: func(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
			mu.Lock()
			seenQueries = append(seenQueries, query)
			mu.Unlock()
			return []search.Paper{{
				ID:       stableWisDevID("paper", query),
				Title:    "Evidence for " + query,
				Abstract: "Grounded evidence for " + query,
				Source:   "mock-worker-budget",
				Score:    0.87,
			}}, nil
		},
	})
	rt := NewUnifiedResearchRuntime(nil, reg, nil, func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		return map[string]any{
			"tasks": []map[string]any{
				{"query": "derived branch should not replace root"},
			},
		}, nil
	})
	state := newResearchSessionState("sleep and memory", "neuroscience", "sess-worker-budget", ResearchExecutionPlaneAutonomous)
	session := &AgentSession{SessionID: state.SessionID, Query: state.Query, DetectedDomain: state.Domain}

	rt.executeResearchWorkers(context.Background(), state, session, state.Query, state.Domain, string(WisDevModeGuided), true, 1)

	mu.Lock()
	defer mu.Unlock()
	if len(seenQueries) != 1 {
		t.Fatalf("expected exactly one budgeted worker search, got %#v", seenQueries)
	}
	if seenQueries[0] != "sleep and memory" {
		t.Fatalf("expected scout to anchor on root query, got %#v", seenQueries)
	}
	if !containsQueryFragment(state.Workers[0].PlannedQueries, "derived branch") {
		t.Fatalf("expected scout to keep derived branches planned for the loop, got %#v", state.Workers[0].PlannedQueries)
	}
}

func TestAssignResearchWorkerSearchBudgetsLeavesLoopBudgetForGapClosure(t *testing.T) {
	roles := activeResearchWorkerRoles(ResearchExecutionPlaneAutonomous)
	budgets := assignResearchWorkerSearchBudgets(roles, resolveResearchWorkerSearchBudget(ResearchExecutionPlaneAutonomous, 6))

	if budgets[ResearchWorkerScout] != 1 {
		t.Fatalf("expected one scout root-search slot, got %#v", budgets)
	}
	if budgets[ResearchWorkerSourceDiversifier] != 1 {
		t.Fatalf("expected one source-diversifier slot at this budget, got %#v", budgets)
	}
	if budgets[ResearchWorkerCitationVerifier] != 1 {
		t.Fatalf("expected one citation-verifier slot, got %#v", budgets)
	}
	if budgets[ResearchWorkerCitationGraph] != 1 {
		t.Fatalf("expected one citation-graph slot, got %#v", budgets)
	}
	if _, ok := budgets[ResearchWorkerSynthesizer]; ok {
		t.Fatalf("synthesizer must not receive retrieval budget, got %#v", budgets)
	}
	total := 0
	for _, budget := range budgets {
		total += budget
	}
	if total != 4 {
		t.Fatalf("expected worker budget to reserve two searches for loop/gap closure, got total=%d budgets=%#v", total, budgets)
	}
}

func TestResearchWorkerContractsExposeToolPolicyBudgetsAndStopReasons(t *testing.T) {
	contract := buildResearchWorkerContract(ResearchWorkerCitationVerifier)

	if contract.Role != ResearchWorkerCitationVerifier {
		t.Fatalf("unexpected contract role %#v", contract)
	}
	if !containsQueryFragment(contract.AllowedTools, "doi_lookup") || !containsQueryFragment(contract.AllowedTools, "citation_identity_normalizer") {
		t.Fatalf("expected citation verifier tool policy, got %#v", contract.AllowedTools)
	}
	if contract.MaxSearches != workerSearchQueryLimit(ResearchWorkerCitationVerifier) {
		t.Fatalf("expected max searches to match role query limit, got %#v", contract)
	}
	if !containsQueryFragment(contract.OutputSchema, "coverageLedger") || !containsQueryFragment(contract.OutputSchema, "stopReason") {
		t.Fatalf("expected durable output schema obligations, got %#v", contract.OutputSchema)
	}
	if !containsQueryFragment(contract.StopReasons, "coverage_open") {
		t.Fatalf("expected explicit stop reasons, got %#v", contract.StopReasons)
	}

	verifierContract := buildResearchWorkerContract(ResearchWorkerIndependentVerifier)
	if !containsQueryFragment(verifierContract.AllowedTools, "claim_entailment_checker") || !containsQueryFragment(verifierContract.AllowedTools, "evidence_packet_reader") {
		t.Fatalf("expected independent verifier to expose evidence-only verification tools, got %#v", verifierContract.AllowedTools)
	}
	if !containsQueryFragment(verifierContract.MustNotDecide, "writer draft") {
		t.Fatalf("independent verifier must not inspect writer draft chain, got %#v", verifierContract.MustNotDecide)
	}
	if !containsQueryFragment(verifierContract.RequiredOutputs, "promoted claim IDs") || !containsQueryFragment(verifierContract.RequiredOutputs, "rejected claim IDs") {
		t.Fatalf("expected independent verifier typed output obligations, got %#v", verifierContract.RequiredOutputs)
	}

	sourceContract := buildResearchWorkerContract(ResearchWorkerSourceDiversifier)
	if !containsQueryFragment(sourceContract.AllowedTools, "full_text_candidate_lookup") {
		t.Fatalf("expected source diversifier to advertise full-text acquisition tool policy, got %#v", sourceContract.AllowedTools)
	}
	if !containsQueryFragment(sourceContract.RequiredEvidence, "open-access PDF") {
		t.Fatalf("expected source diversifier evidence contract to require full-text candidates, got %#v", sourceContract.RequiredEvidence)
	}
}

func TestClaimVerificationLedgerCalibratesClaimHealthAndStopReason(t *testing.T) {
	papers := []search.Paper{
		{ID: "paper-1", Title: "Sleep Cohort", Abstract: "Sleep improves recall.", Source: "pubmed"},
		{ID: "paper-2", Title: "Sleep Replication", Abstract: "Replication evidence.", Source: "openalex"},
		{ID: "paper-3", Title: "Caffeine Null", Abstract: "Caffeine recall changes.", Source: "semantic_scholar"},
	}
	ledger := buildClaimVerificationLedger("sleep memory", []EvidenceFinding{
		{
			ID:         "finding-1",
			Claim:      "Sleep improves memory consolidation.",
			SourceID:   "paper-1",
			PaperTitle: "Sleep Cohort",
			Snippet:    "Sleep improved delayed recall.",
			Confidence: 0.86,
		},
		{
			ID:         "finding-2",
			Claim:      "Sleep improves memory consolidation.",
			SourceID:   "paper-2",
			PaperTitle: "Sleep Replication",
			Snippet:    "The effect replicated independently.",
			Confidence: 0.82,
		},
		{
			ID:         "finding-3",
			Claim:      "Caffeine has no effect on recall.",
			SourceID:   "paper-3",
			PaperTitle: "Caffeine Null",
			Snippet:    "Recall changed after caffeine dosing.",
			Confidence: 0.68,
		},
	}, papers, &LoopGapState{Contradictions: []string{"Caffeine recall changed after caffeine dosing."}})

	if ledger == nil || len(ledger.Records) != 2 {
		t.Fatalf("expected two claim verification records, got %#v", ledger)
	}
	if ledger.SupportedClaims != 1 {
		t.Fatalf("expected one supported claim, got %#v", ledger)
	}
	if ledger.ContradictedClaims != 1 || ledger.StopReason != "contradictions_open" {
		t.Fatalf("expected open contradiction stop reason, got %#v", ledger)
	}
	supported := findClaimVerificationRecord(ledger, "Sleep improves memory")
	if supported == nil || supported.Status != "supported" || supported.CitationHealth != "healthy" {
		t.Fatalf("expected healthy supported sleep claim, got %#v", supported)
	}
	contradicted := findClaimVerificationRecord(ledger, "Caffeine has no effect")
	if contradicted == nil || contradicted.Status != "contradicted" || len(contradicted.FollowUpQueries) == 0 {
		t.Fatalf("expected contradicted caffeine claim with follow-up query, got %#v", contradicted)
	}
}

func TestBranchEvaluationScoresAndPersistsBranchState(t *testing.T) {
	papers := []search.Paper{{
		ID:       "paper-replication",
		Title:    "Replication Study",
		Abstract: "Independent cohort replication evidence.",
		Source:   "pubmed",
		DOI:      "10.1000/replication",
		PdfUrl:   "https://example.com/replication.pdf",
	}}
	gap := &LoopGapState{Ledger: []CoverageLedgerEntry{{
		ID:                "gap-contradiction",
		Category:          "contradiction",
		Status:            coverageLedgerStatusOpen,
		Title:             "Conflicting cohort results remain unresolved",
		Description:       "oncology biomarker reproducibility conflicting cohort results",
		SupportingQueries: []string{"oncology biomarker reproducibility conflicting cohort results"},
		Confidence:        0.58,
	}}}

	branches := buildResearchBranchEvaluations(
		"oncology biomarker reproducibility",
		[]string{
			"oncology biomarker reproducibility",
			"oncology biomarker reproducibility replication cohort",
			"oncology biomarker reproducibility conflicting cohort results",
		},
		[]string{"oncology biomarker reproducibility replication cohort"},
		map[string][]search.Paper{
			"oncology biomarker reproducibility replication cohort": papers,
		},
		gap,
	)

	if len(branches) != 3 {
		t.Fatalf("expected three branch evaluations, got %#v", branches)
	}
	replication := findBranchEvaluation(branches, "replication cohort")
	if replication == nil || replication.Status != "covered" || replication.StopReason != "branch_covered" {
		t.Fatalf("expected covered replication branch, got %#v", replication)
	}
	if replication.FalsifiabilityScore <= 0.35 || replication.EvidenceScore <= 0.25 || len(replication.SourceIDs) == 0 {
		t.Fatalf("expected replication branch to score falsifiability/evidence/source identity, got %#v", replication)
	}
	if replication.Hypothesis == "" || replication.FalsifiabilityCondition == "" || replication.BranchScore != replication.OverallScore || replication.VerifierVerdict != "promote" {
		t.Fatalf("expected covered branch graph metadata and verifier verdict, got %#v", replication)
	}
	if len(replication.PlannedQueries) == 0 || len(replication.ExecutedQueries) == 0 || len(replication.SourceFamilies) == 0 || len(replication.Evidence) == 0 {
		t.Fatalf("expected branch to preserve planned/executed queries, source families, and evidence, got %#v", replication)
	}
	if replication.IdempotencyKey == "" || replication.CheckpointKey == "" {
		t.Fatalf("expected durable branch keys, got %#v", replication)
	}
	conflict := findBranchEvaluation(branches, "conflicting cohort")
	if conflict == nil || conflict.StopReason != "branch_open_gap" || len(conflict.OpenGaps) == 0 {
		t.Fatalf("expected contradiction branch to persist open gap state, got %#v", conflict)
	}
	if conflict.VerifierVerdict != "revise_required" {
		t.Fatalf("expected contradiction branch to require revision, got %#v", conflict)
	}
}

func TestIndependentVerifierBlocksSynthesisWhenClaimsOpen(t *testing.T) {
	state := newResearchSessionState("sleep memory", "neuroscience", "sess-verifier", ResearchExecutionPlaneDeep)
	state.Workers[0].Status = "completed"
	state.Workers[0].Evidence = []EvidenceFinding{{
		ID:         "finding-single-source",
		Claim:      "Sleep improves memory consolidation.",
		SourceID:   "paper-1",
		PaperTitle: "Sleep Cohort",
		Snippet:    "Sleep improved recall.",
		Confidence: 0.78,
	}}
	claims := &ClaimVerificationLedger{
		Query:                "sleep memory",
		OpenClaims:           1,
		CalibratedConfidence: 0.58,
		Records: []ClaimVerificationRecord{{
			ID:                  "claim-single-source",
			Claim:               "Sleep improves memory consolidation.",
			Status:              "needs_triangulation",
			CitationHealth:      "single_source",
			ContradictionStatus: "none",
			SupportCount:        1,
			Confidence:          0.58,
		}},
		StopReason: "claim_coverage_open",
	}
	gap := &LoopGapState{Ledger: []CoverageLedgerEntry{{
		ID:                "gap-source-diversity",
		Category:          "source_diversity",
		Status:            coverageLedgerStatusOpen,
		Title:             "Need independent replication",
		Description:       "independent replication sleep memory",
		SupportingQueries: []string{"sleep memory independent replication"},
	}}}
	branches := []ResearchBranchEvaluation{{
		ID:         "branch-replication",
		Query:      "sleep memory independent replication",
		Status:     "open",
		OpenGaps:   []string{"gap-source-diversity"},
		StopReason: "branch_open_gap",
	}}

	decision := buildIndependentVerifierDecision(claims, gap, branches)
	if decision.Verdict != "abstain" {
		t.Fatalf("expected evidence-only verifier to abstain without promotable claims, got %#v", decision)
	}
	applyIndependentVerifierDecision(state, decision)
	board := buildResearchBlackboard(state.Workers)
	attachRuntimeArbitrationToBlackboard(board, branches, decision)

	if board.ReadyForSynthesis {
		t.Fatalf("open verifier decision must block synthesis, got %#v", board)
	}
	if board.Arbitration == nil || !board.Arbitration.Abstain || len(board.Arbitration.RejectedClaimIDs) == 0 {
		t.Fatalf("expected blackboard arbitration to preserve verifier rejection, got %#v", board.Arbitration)
	}
	if !strings.Contains(board.SynthesisGate, "verifier abstained") {
		t.Fatalf("expected verifier abstain synthesis gate, got %q", board.SynthesisGate)
	}
	idx := findResearchWorkerIndex(state.Workers, ResearchWorkerIndependentVerifier)
	if idx < 0 || state.Workers[idx].Status != "completed" || len(state.Workers[idx].CoverageLedger) == 0 {
		t.Fatalf("expected completed independent verifier worker with ledger output, got %#v", state.Workers)
	}
}

func TestVerifierControlledFollowUpQueriesPreferClaimAndLedgerPressure(t *testing.T) {
	decision := &ResearchVerifierDecision{
		Role:            ResearchWorkerIndependentVerifier,
		Verdict:         "revise_required",
		StopReason:      "verifier_requires_revision",
		RevisionReasons: []string{"single-source claim requires independent support"},
		Confidence:      0.44,
		EvidenceOnly:    true,
	}
	claims := &ClaimVerificationLedger{
		RequiredFollowUpQueries: []string{"sleep memory independent replication claim"},
		Records: []ClaimVerificationRecord{{
			ID:              "claim-1",
			Claim:           "Sleep improves memory.",
			StopReason:      "source_diversity_open",
			FollowUpQueries: []string{"sleep memory cohort replication"},
		}},
	}
	gap := &LoopGapState{
		NextQueries: []string{"sleep memory contradiction resolution"},
		Ledger: []CoverageLedgerEntry{{
			ID:                "source-gap",
			Category:          "source_diversity",
			Status:            coverageLedgerStatusOpen,
			Title:             "Need independent replication",
			SupportingQueries: []string{"sleep memory independent source family"},
		}},
	}

	queries := buildVerifierControlledFollowUpQueries(
		"sleep memory",
		decision,
		claims,
		gap,
		[]ResearchBranchEvaluation{{Query: "sleep memory branch evidence", OpenGaps: []string{"source-gap"}, StopReason: "branch_open_gap"}},
		&ResearchBudgetDecision{FollowUpSearchBudget: 2},
	)

	if len(queries) != 2 {
		t.Fatalf("expected follow-up queries to respect verifier budget, got %#v", queries)
	}
	if queries[0] != "sleep memory independent replication claim" || queries[1] != "sleep memory cohort replication" {
		t.Fatalf("expected claim-authored verifier follow-ups first, got %#v", queries)
	}
}

func TestVerifierControlledFollowUpQueriesPrioritizeHighInformationGainGaps(t *testing.T) {
	decision := &ResearchVerifierDecision{
		Role:            ResearchWorkerIndependentVerifier,
		Verdict:         "revise_required",
		StopReason:      "verifier_requires_revision",
		RevisionReasons: []string{"coverage is still too broad"},
		Confidence:      0.41,
		EvidenceOnly:    true,
	}
	claims := &ClaimVerificationLedger{
		Records: []ClaimVerificationRecord{{
			ID:                  "claim-low-pressure",
			Claim:               "Sleep may affect recall.",
			Status:              "supported",
			CitationHealth:      "healthy",
			ContradictionStatus: "none",
			SupportCount:        3,
			SourceFamilies:      []string{"pubmed", "openalex"},
			Confidence:          0.81,
			FollowUpQueries:     []string{"sleep memory broad background"},
		}},
	}
	gap := &LoopGapState{
		NextQueries: []string{"sleep memory general follow up"},
		Ledger: []CoverageLedgerEntry{
			{
				ID:                "low-coverage",
				Category:          "coverage",
				Status:            coverageLedgerStatusOpen,
				Title:             "Broad validation checkpoint",
				SupportingQueries: []string{"sleep memory broad validation"},
				Confidence:        0.9,
				Priority:          20,
			},
			{
				ID:                "source-acquisition",
				Category:          "source_acquisition",
				Status:            coverageLedgerStatusOpen,
				Title:             "Full text missing",
				SupportingQueries: []string{"sleep memory open access PDF full text"},
				Confidence:        0.54,
				Priority:          94,
				Required:          true,
			},
			{
				ID:                "citation-integrity",
				Category:          "citation_integrity",
				Status:            coverageLedgerStatusOpen,
				Title:             "Citation metadata missing",
				SupportingQueries: []string{"sleep memory DOI citation metadata"},
				Confidence:        0.5,
				Priority:          96,
				Required:          true,
			},
			{
				ID:                "contradiction",
				Category:          "contradiction",
				Status:            coverageLedgerStatusOpen,
				Title:             "Contradictory evidence unresolved",
				SupportingQueries: []string{"sleep memory contradictory trial evidence"},
				Confidence:        0.5,
				Priority:          96,
				Required:          true,
			},
		},
	}

	queries := buildVerifierControlledFollowUpQueries(
		"sleep memory",
		decision,
		claims,
		gap,
		nil,
		&ResearchBudgetDecision{FollowUpSearchBudget: 3},
	)

	if len(queries) != 3 {
		t.Fatalf("expected follow-up queries to respect tight budget, got %#v", queries)
	}
	if queries[0] != "sleep memory contradictory trial evidence" {
		t.Fatalf("expected contradiction pressure to rank first, got %#v", queries)
	}
	if !containsQueryFragment(queries, "doi citation metadata") || !containsQueryFragment(queries, "open access pdf full text") {
		t.Fatalf("expected citation integrity and source acquisition to outrank broad background, got %#v", queries)
	}
	if containsQueryFragment(queries, "broad background") || containsQueryFragment(queries, "broad validation") {
		t.Fatalf("low-information broad follow-ups should be displaced under tight budgets, got %#v", queries)
	}
}

func TestVerifierControlledFollowUpQueriesSemanticDedupe(t *testing.T) {
	decision := &ResearchVerifierDecision{
		Verdict:         "revise_required",
		RevisionReasons: []string{"needs independent replication"},
	}
	claims := &ClaimVerificationLedger{
		RequiredFollowUpQueries: []string{
			"sleep memory independent replication evidence",
			"sleep and memory independently replicated source",
		},
	}

	queries := buildVerifierControlledFollowUpQueries(
		"sleep memory",
		decision,
		claims,
		nil,
		nil,
		&ResearchBudgetDecision{FollowUpSearchBudget: 2},
	)

	if len(queries) != 2 {
		t.Fatalf("expected semantically redundant claim query to leave room for revision follow-up, got %#v", queries)
	}
	if queries[0] != "sleep memory independent replication evidence" {
		t.Fatalf("expected first canonical claim query to be preserved, got %#v", queries)
	}
	if strings.EqualFold(queries[1], "sleep and memory independently replicated source") {
		t.Fatalf("expected synonymous second query to be filtered, got %#v", queries)
	}
}

func TestBuildVerifierRevisionLoopRequestExpandsSearchAndPaperBudgets(t *testing.T) {
	base := LoopRequest{
		Query:           "sleep memory",
		MaxIterations:   4,
		MaxSearchTerms:  4,
		HitsPerSearch:   3,
		MaxUniquePapers: 2,
	}
	current := &LoopResult{
		ExecutedQueries: []string{"sleep memory", "sleep memory cohort", "sleep memory review", "sleep memory replication"},
		Papers: []search.Paper{
			{ID: "paper-1", Title: "Sleep Cohort", Source: "pubmed"},
			{ID: "paper-2", Title: "Memory Review", Source: "openalex"},
		},
		QueryCoverage: map[string][]search.Paper{
			"sleep memory": {{ID: "paper-1", Title: "Sleep Cohort", Source: "pubmed"}},
		},
	}

	req := buildVerifierRevisionLoopRequest(
		base,
		current,
		[]string{"sleep memory independent replication", "sleep memory contradictory evidence"},
		&ResearchBudgetDecision{FollowUpSearchBudget: 2},
	)

	if req.MaxSearchTerms != 6 {
		t.Fatalf("expected verifier revision to extend search-term budget to 6, got %d", req.MaxSearchTerms)
	}
	if req.MaxUniquePapers != 8 {
		t.Fatalf("expected exhausted paper budget to expand for verifier follow-up, got %d", req.MaxUniquePapers)
	}
	if len(req.InitialExecutedQueries) != len(current.ExecutedQueries) || len(req.InitialPapers) != len(current.Papers) {
		t.Fatalf("expected revision request to carry previous execution state, got %#v", req)
	}
	if !req.DisableProgrammaticPlanning {
		t.Fatalf("verifier revision pass should not restart worker planning")
	}
}

func TestBuildVerifierRevisionLoopRequestUsesBaseSeedQueriesAndCritiqueLimit(t *testing.T) {
	base := LoopRequest{
		Query:           "sleep memory",
		SeedQueries:     []string{"sleep memory", "neural replay", "neural replay", "memory consolidation"},
		MaxIterations:   2,
		MaxSearchTerms:  2,
		HitsPerSearch:   2,
		MaxUniquePapers: 1,
	}
	current := &LoopResult{
		ExecutedQueries: []string{"sleep memory"},
		Papers: []search.Paper{
			{ID: "paper-1", Title: "Consolidation Study", Source: "pubmed"},
		},
		QueryCoverage: map[string][]search.Paper{
			"sleep memory": {
				{ID: "paper-1", Title: "Consolidation Study", Source: "pubmed"},
			},
		},
	}

	req := buildVerifierRevisionLoopRequest(
		base,
		current,
		[]string{"sleep memory", "replication support", "memory consolidation"},
		&ResearchBudgetDecision{
			FollowUpSearchBudget:  5,
			CritiqueFollowUpLimit: 1,
		},
	)

	if req.MaxSearchTerms != 3 {
		t.Fatalf("expected verifier revision to apply capped critique limit, got %d", req.MaxSearchTerms)
	}
	if req.SeedQueries[0] != "sleep memory" {
		t.Fatalf("expected normalized seed queries to keep base query first, got %#v", req.SeedQueries)
	}
	if len(req.SeedQueries) < 3 || !containsQueryFragment(req.SeedQueries, "replication support") {
		t.Fatalf("expected follow-up and base seed queries to be merged and deduplicated, got %#v", req.SeedQueries)
	}
	if req.MaxUniquePapers != 3 {
		t.Fatalf("expected capped follow-up budget to still expand paper budget, got %d", req.MaxUniquePapers)
	}
	if !req.DisableProgrammaticPlanning {
		t.Fatalf("verifier revision pass should disable programmatic planning")
	}
}

func TestFinalizeLoopResultWithVerifierKeepsBaseResultWhenRevisionIsCancelled(t *testing.T) {
	rt := &UnifiedResearchRuntime{loop: &AutonomousLoop{}}
	state := &ResearchSessionState{
		SessionID: "revision-cancelled",
		Query:     "sleep memory",
		Plane:     ResearchExecutionPlaneDeep,
		Budget:    &ResearchBudgetDecision{FollowUpSearchBudget: 1},
	}
	loopResult := &LoopResult{
		FinalAnswer:     "Sleep may improve recall in the cited study.",
		ExecutedQueries: []string{"sleep memory"},
		Papers: []search.Paper{{
			ID:     "p1",
			Title:  "Sleep and memory consolidation",
			Source: "pubmed",
		}},
		Evidence: []EvidenceFinding{{
			ID:         "ev1",
			Claim:      "Sleep may improve recall.",
			SourceID:   "p1",
			PaperTitle: "Sleep and memory consolidation",
			Confidence: 0.82,
			Status:     "supported",
		}},
		GapAnalysis: &LoopGapState{
			NextQueries: []string{"sleep memory independent replication"},
			Ledger: []CoverageLedgerEntry{{
				ID:                "source-gap",
				Category:          "source_diversity",
				Status:            coverageLedgerStatusOpen,
				Title:             "Need independent replication",
				Description:       "The synthesis still needs an independent source family.",
				SupportingQueries: []string{"sleep memory independent replication"},
				Confidence:        0.63,
				Required:          true,
				Priority:          92,
				ObligationType:    "source_diversity",
				OwnerWorker:       string(ResearchWorkerIndependentVerifier),
				Severity:          "high",
			}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var events []PlanExecutionEvent
	err := rt.finalizeLoopResultWithVerifier(ctx, LoopRequest{Query: "sleep memory", MaxIterations: 1, MaxSearchTerms: 1}, state, loopResult, nil, func(event PlanExecutionEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("expected cancelled verifier revision to preserve base result, got error: %v", err)
	}
	if loopResult.FinalizationGate == nil || !loopResult.FinalizationGate.Provisional {
		t.Fatalf("expected provisional finalization gate, got %#v", loopResult.FinalizationGate)
	}
	if !strings.HasPrefix(loopResult.FinalAnswer, "Provisional") {
		t.Fatalf("expected provisional final answer, got %q", loopResult.FinalAnswer)
	}
	if loopResult.StopReason == "" {
		t.Fatalf("expected verifier stop reason to remain visible")
	}
	if !containsRuntimeStage(events, "verifier_revision_cancelled") {
		t.Fatalf("expected verifier_revision_cancelled event, got %#v", events)
	}
}

func TestVerifierProvisionalAnswerBlocksUnpromotedFinalEmission(t *testing.T) {
	decision := &ResearchVerifierDecision{
		Role:            ResearchWorkerIndependentVerifier,
		Verdict:         "abstain",
		StopReason:      "verifier_abstained",
		RevisionReasons: []string{"contradicted claims remain unresolved", "actionable coverage ledger gaps remain open"},
		Confidence:      0.33,
		EvidenceOnly:    true,
	}

	answer := buildVerifierProvisionalAnswer("sleep memory", "Sleep improves recall.", decision)
	if !strings.HasPrefix(answer, "Provisional research synthesis:") {
		t.Fatalf("expected verifier-blocked answer to be clearly provisional, got %q", answer)
	}
	if !strings.Contains(answer, "contradicted claims remain unresolved") || !strings.Contains(answer, "not final") {
		t.Fatalf("expected answer to surface verifier reasons and non-final status, got %q", answer)
	}
	if again := buildVerifierProvisionalAnswer("sleep memory", answer, decision); again != answer {
		t.Fatalf("expected provisional wrapper to be idempotent, got %q", again)
	}
}

func TestAdaptiveBudgetDecisionExpandsFromOpenClaimPressure(t *testing.T) {
	decision := buildResearchBudgetDecision(LoopRequest{
		Query:          "sleep memory",
		MaxIterations:  8,
		MaxSearchTerms: 8,
		Mode:           string(WisDevModeGuided),
	}, ResearchExecutionPlaneDeep)
	claims := &ClaimVerificationLedger{
		OpenClaims:           2,
		ContradictedClaims:   1,
		SourceFamilyCoverage: 0.4,
		Records:              []ClaimVerificationRecord{{Claim: "one"}, {Claim: "two"}},
	}
	finalizeResearchBudgetDecision(decision, &LoopGapState{Ledger: []CoverageLedgerEntry{
		{Category: "source_diversity", Status: coverageLedgerStatusOpen},
		{Category: "contradiction", Status: coverageLedgerStatusOpen},
	}}, claims)

	if decision.FollowUpSearchBudget == 0 {
		t.Fatalf("expected follow-up search budget from open pressure, got %#v", decision)
	}
	if decision.SourceDiversityPressure == 0 || decision.ContradictionPressure == 0 || decision.ClaimCriticality == 0 {
		t.Fatalf("expected pressure dimensions to be populated, got %#v", decision)
	}
}

func TestRuntimeTraceEventsIncludeSourceGapClaimAndStopReason(t *testing.T) {
	state := newResearchSessionState("sleep memory", "neuroscience", "trace-session", ResearchExecutionPlaneDeep)
	state.Budget = buildResearchBudgetDecision(LoopRequest{Query: state.Query, MaxIterations: 4, MaxSearchTerms: 4}, state.Plane)
	state.CoverageLedger = []CoverageLedgerEntry{{
		ID:       "gap-1",
		Category: "claim_evidence",
		Status:   coverageLedgerStatusOpen,
		Title:    "Need independent claim support",
	}}
	state.ClaimVerification = &ClaimVerificationLedger{
		StopReason: "claim_coverage_open",
		Records: []ClaimVerificationRecord{{
			ID:             "claim-1",
			Claim:          "Sleep improves memory consolidation.",
			Status:         "needs_triangulation",
			CitationHealth: "single_source",
			SupportCount:   1,
			Confidence:     0.58,
		}},
	}
	state.BranchEvaluations = []ResearchBranchEvaluation{{
		ID:           "branch-1",
		Query:        "sleep memory independent replication",
		Status:       "open",
		OverallScore: 0.42,
		OpenGaps:     []string{"gap-1"},
		StopReason:   "branch_open_gap",
	}}
	state.VerifierDecision = &ResearchVerifierDecision{
		Role:             ResearchWorkerIndependentVerifier,
		Verdict:          "revise_required",
		StopReason:       "verifier_requires_revision",
		RejectedClaimIDs: []string{"claim-1"},
		RevisionReasons:  []string{"single-source claim requires independent support"},
		Confidence:       0.42,
		EvidenceOnly:     true,
	}
	state.StopReason = "claim_coverage_open"
	loopResult := &LoopResult{Papers: []search.Paper{{
		ID:     "paper-1",
		Title:  "Sleep Cohort",
		Source: "pubmed",
	}}}
	var events []PlanExecutionEvent

	emitRuntimeLedgerEvents(func(event PlanExecutionEvent) {
		events = append(events, event)
	}, state, loopResult)

	for _, stage := range []string{"adaptive_budget", "branch_score", "branch_pruned", "source_fetched", "gap_opened", "citation_rejected", "claim_rejected", "verifier_failed", "final_stop_reason"} {
		if !containsRuntimeStage(events, stage) {
			t.Fatalf("expected trace stage %q, got %#v", stage, events)
		}
	}
}

func TestResearchReasoningRuntimeMetadataExposesTreePlannerMode(t *testing.T) {
	req := LoopRequest{
		Query:                       "sleep memory",
		Mode:                        string(WisDevModeGuided),
		MaxIterations:               4,
		MaxSearchTerms:              6,
		DisableProgrammaticPlanning: false,
		DisableHypothesisGeneration: false,
	}
	budget := buildResearchBudgetDecision(req, ResearchExecutionPlaneDeep)

	metadata := buildResearchReasoningRuntimeMetadata(req, ResearchExecutionPlaneDeep, budget)

	if got := metadata["runtimeMode"]; got != "tree_search_with_programmatic_planner" {
		t.Fatalf("expected tree_search_with_programmatic_planner runtime mode, got %#v", got)
	}
	if got := metadata["treeSearchRuntime"]; got != true {
		t.Fatalf("expected treeSearchRuntime=true, got %#v", got)
	}
	if got := metadata["programmaticTreePlanner"]; got != true {
		t.Fatalf("expected programmaticTreePlanner=true, got %#v", got)
	}
	if got := metadata["plannerAction"]; got != ActionResearchQueryDecompose {
		t.Fatalf("expected planner action %q, got %#v", ActionResearchQueryDecompose, got)
	}
	if got := metadata["branchVerificationAction"]; got != ActionResearchVerifyClaimsBatch {
		t.Fatalf("expected verifier action %q, got %#v", ActionResearchVerifyClaimsBatch, got)
	}
}

func TestVerifierRevisionInjectsClaimAndBranchFollowUpQueries(t *testing.T) {
	state := newResearchSessionState("sleep memory", "neuroscience", "sess-post-verifier", ResearchExecutionPlaneDeep)
	state.ClaimVerification = &ClaimVerificationLedger{
		Query:                   state.Query,
		RequiredFollowUpQueries: []string{"sleep memory independent replication"},
		Records: []ClaimVerificationRecord{{
			ID:              "claim-1",
			Claim:           "Sleep improves memory consolidation.",
			Status:          "needs_triangulation",
			CitationHealth:  "single_source",
			FollowUpQueries: []string{"sleep memory cohort replication"},
		}},
	}
	state.BranchEvaluations = []ResearchBranchEvaluation{{
		Query:      "sleep memory contradiction null result",
		Status:     "open",
		OpenGaps:   []string{"gap-1"},
		StopReason: "branch_open_gap",
	}}
	state.VerifierDecision = &ResearchVerifierDecision{
		Verdict:         "revise_required",
		StopReason:      "verifier_requires_revision",
		RevisionReasons: []string{"single-source claim requires independent support"},
		Confidence:      0.42,
		EvidenceOnly:    true,
	}

	gap := mergeVerifierDecisionIntoGapState(&LoopGapState{Sufficient: true}, state)
	if gap.Sufficient {
		t.Fatalf("expected verifier pressure to reopen the gap state")
	}
	queries := buildPostVerifierFollowUpQueries(state.Query, gap, state, 6)
	for _, expected := range []string{
		"sleep memory independent replication",
		"sleep memory cohort replication",
		"sleep memory contradiction null result",
	} {
		if !containsQueryFragment(queries, expected) {
			t.Fatalf("expected follow-up query containing %q, got %#v", expected, queries)
		}
	}
	if countOpenCoverageLedgerEntries(gap.Ledger) == 0 {
		t.Fatalf("expected open post-verifier ledger entries, got %#v", gap.Ledger)
	}
}

func TestFinalizationGateMarksUnverifiedAnswerProvisional(t *testing.T) {
	state := newResearchSessionState("sleep memory", "neuroscience", "sess-final-gate", ResearchExecutionPlaneDeep)
	state.StopReason = "verifier_requires_revision"
	state.VerifierDecision = &ResearchVerifierDecision{
		Verdict:          "revise_required",
		StopReason:       "verifier_requires_revision",
		RevisionReasons:  []string{"single-source claim requires independent support"},
		RejectedClaimIDs: []string{"claim-1"},
		Confidence:       0.42,
		EvidenceOnly:     true,
	}
	state.Blackboard = &ResearchBlackboard{
		ReadyForSynthesis: false,
		OpenLedgerCount:   1,
		SynthesisGate:     "blocked: independent verifier requires revision",
	}
	result := &LoopResult{FinalAnswer: "Sleep may improve memory.", GapAnalysis: &LoopGapState{Ledger: []CoverageLedgerEntry{{
		Category:          "claim_evidence",
		Status:            coverageLedgerStatusOpen,
		SupportingQueries: []string{"sleep memory independent replication"},
	}}}}

	applyFinalizationGateToLoopResult(result, state)

	if result.FinalizationGate == nil || !result.FinalizationGate.Provisional {
		t.Fatalf("expected provisional finalization gate, got %#v", result.FinalizationGate)
	}
	if !strings.HasPrefix(result.FinalAnswer, "Provisional answer:") {
		t.Fatalf("expected final answer to be explicitly provisional, got %q", result.FinalAnswer)
	}
	if result.StopReason != "verifier_requires_revision" {
		t.Fatalf("expected stop reason to propagate, got %q", result.StopReason)
	}
	if !containsQueryFragment(result.FinalizationGate.FollowUpQueries, "independent replication") {
		t.Fatalf("expected gate to expose follow-up queries, got %#v", result.FinalizationGate)
	}
}

func TestRunAnswerExposesFinalizationGateMetadata(t *testing.T) {
	reg := search.NewProviderRegistry()
	reg.Register(&mockSearchProvider{
		name: "metadata-finalization-search",
		SearchFunc: func(_ context.Context, _ string, _ search.SearchOpts) ([]search.Paper, error) {
			return []search.Paper{{
				ID:       "paper-1",
				Title:    "Single source sleep memory evidence",
				Abstract: "One source reports improved recall after sleep.",
				Source:   "crossref",
			}}, nil
		},
	})
	reg.SetDefaultOrder([]string{"metadata-finalization-search"})

	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	allowAutonomousHypothesisProposals(msc, "")
	allowAutonomousHypothesisEvaluation(msc, "")
	allowAutonomousSufficiency(msc, `{"sufficient": true, "reasoning": "single source is enough", "nextQuery": ""}`)
	allowAutonomousDossier(msc, `[]`)
	allowAutonomousCritique(msc, `{"needsRevision": false, "reasoning": "draft is coherent", "confidence": 0.82}`)
	msc.On("Generate", mock.Anything, mock.MatchedBy(func(req *llmv1.GenerateRequest) bool {
		return req != nil && strings.Contains(req.Prompt, "Synthesize a comprehensive research report")
	})).Return(&llmv1.GenerateResponse{Text: "Final synthesis"}, nil).Maybe()

	rt := NewUnifiedResearchRuntime(NewAutonomousLoop(reg, lc), reg, lc, nil)
	resp, err := rt.RunAnswer(context.Background(), UnifiedResearchRequest{
		Query:          "sleep memory",
		MaxIterations:  1,
		MaxSearchTerms: 1,
		HitsPerSearch:  1,
		Mode:           string(WisDevModeGuided),
		Plane:          ResearchExecutionPlaneMultiAgent,
	})
	if err != nil {
		t.Fatalf("RunAnswer returned error: %v", err)
	}
	if resp == nil || resp.Metadata == nil || resp.Metadata.Policy == nil {
		t.Fatalf("expected response metadata policy, got %#v", resp)
	}

	policy := resp.Metadata.Policy
	reasoningRuntime, ok := policy["reasoningRuntime"].(map[string]any)
	if !ok {
		t.Fatalf("expected reasoningRuntime policy metadata, got %#v", policy["reasoningRuntime"])
	}
	if reasoningRuntime["runtimeMode"] != "tree_search_with_programmatic_planner" || reasoningRuntime["treeSearchRuntime"] != true {
		t.Fatalf("expected tree-search reasoning runtime metadata, got %#v", reasoningRuntime)
	}
	if reasoningRuntime["researchPlane"] != ResearchExecutionPlaneMultiAgent {
		t.Fatalf("expected multi-agent research plane in reasoning runtime metadata, got %#v", reasoningRuntime["researchPlane"])
	}
	gate, _ := policy["finalizationGate"].(*ResearchFinalizationGate)
	if gate != nil {
		if !gate.Provisional {
			t.Fatalf("expected single-source/no-claim response to remain provisional, got %#v", gate)
		}
		if policy["answerProvisional"] != true {
			t.Fatalf("expected answerProvisional=true when finalization gate is present, got %#v", policy["answerProvisional"])
		}
		if policy["answerVerified"] != false {
			t.Fatalf("expected answerVerified=false when finalization gate is present, got %#v", policy["answerVerified"])
		}
	} else if verified, hasVerified := policy["answerVerified"]; hasVerified && verified != true {
		t.Fatalf("expected answerVerified=true when present and no finalization gate is emitted, got %#v", verified)
	}
	if status, hasStatus := policy["answerStatus"]; hasStatus {
		if statusText, ok := status.(string); !ok || statusText == "" {
			t.Fatalf("expected non-empty answerStatus when present, got %#v", status)
		}
	}
	if verdict, hasVerdict := policy["verifierVerdict"]; hasVerdict {
		if verdictText, ok := verdict.(string); !ok || verdictText == "" {
			t.Fatalf("expected non-empty verifierVerdict when present, got %#v", verdict)
		}
	}
	if branches, hasBranches := policy["researchBranches"]; hasBranches {
		if typedBranches, ok := branches.([]ResearchBranchEvaluation); !ok || len(typedBranches) == 0 {
			t.Fatalf("expected non-empty typed researchBranches when present, got %#v", branches)
		}
	}
	if contracts, hasContracts := policy["workerContracts"]; hasContracts {
		typedContracts, ok := contracts.(map[ResearchWorkerRole]ResearchWorkerContract)
		if !ok || typedContracts[ResearchWorkerCitationGraph].Role != ResearchWorkerCitationGraph {
			t.Fatalf("expected workerContracts metadata with citation graph contract when present, got %#v", contracts)
		}
	}
	if obligations, hasObligations := policy["coverageObligations"]; hasObligations {
		if typedObligations, ok := obligations.([]CoverageLedgerEntry); !ok || len(typedObligations) == 0 {
			t.Fatalf("expected non-empty typed coverageObligations when present, got %#v", obligations)
		}
	}
	if citationGraph, hasCitationGraph := policy["citationGraph"]; hasCitationGraph {
		if _, ok := citationGraph.(*ResearchCitationGraph); !ok {
			t.Fatalf("expected typed citationGraph when present, got %#v", citationGraph)
		}
	}
	if durableTasks, hasDurableTasks := policy["durableTasks"]; hasDurableTasks {
		typedDurableTasks, ok := durableTasks.([]ResearchDurableTaskState)
		if !ok || !containsDurableTaskOperation(typedDurableTasks, researchDurableTaskVerifier) {
			t.Fatalf("expected durableTasks metadata with verifier pass when present, got %#v", durableTasks)
		}
	}
	if gate != nil {
		if policy["finalizationStopReason"] != gate.StopReason || policy["finalStopReason"] != gate.StopReason {
			t.Fatalf("expected stop reason to mirror finalization gate, policy=%#v gate=%#v", policy, gate)
		}
		if followUps, ok := policy["followUpQueries"].([]string); !ok || len(followUps) == 0 {
			t.Fatalf("expected metadata follow-up queries from finalization gate, got %#v", policy["followUpQueries"])
		}
	}
}

func TestFinalizationGateCountsRuntimeSourceAcquisitionLedger(t *testing.T) {
	state := newResearchSessionState("sleep memory", "neuroscience", "sess-final-source-acquisition", ResearchExecutionPlaneDeep)
	state.VerifierDecision = &ResearchVerifierDecision{
		Verdict:          "promote",
		StopReason:       "verifier_promoted",
		Confidence:       0.82,
		EvidenceOnly:     true,
		PromotedClaimIDs: []string{"claim-1"},
	}
	state.Blackboard = &ResearchBlackboard{
		ReadyForSynthesis: true,
		OpenLedgerCount:   0,
		SynthesisGate:     "ready: independent verifier promoted grounded claims",
		Evidence: []EvidenceFinding{{
			ID:       "evidence-1",
			Claim:    "Sleep improves memory.",
			SourceID: "arxiv:2401.12345",
			Snippet:  "Sleep group retained more items.",
		}},
	}
	state.SourceAcquisition = buildResearchSourceAcquisitionPlan(state.Query, []search.Paper{{
		ID:      "arxiv:2401.12345",
		Title:   "Sleep Memory Trial",
		ArxivID: "2401.12345",
		PdfUrl:  "https://arxiv.org/pdf/2401.12345.pdf",
		Source:  "arxiv",
	}})
	result := &LoopResult{
		FinalAnswer: "Sleep improves recall in the cited study.",
		Papers: []search.Paper{{
			ID:      "arxiv:2401.12345",
			Title:   "Sleep Memory Trial",
			ArxivID: "2401.12345",
			PdfUrl:  "https://arxiv.org/pdf/2401.12345.pdf",
			Source:  "arxiv",
		}},
		Converged: true,
	}

	applyFinalizationGateToLoopResult(result, state)

	if result.FinalizationGate == nil {
		t.Fatalf("expected finalization gate")
	}
	if result.FinalizationGate.Ready || !result.FinalizationGate.Provisional {
		t.Fatalf("open source acquisition must block final readiness, got %#v", result.FinalizationGate)
	}
	if result.FinalizationGate.OpenLedgerCount == 0 {
		t.Fatalf("expected source acquisition ledger pressure to be counted, got %#v", result.FinalizationGate)
	}
	if result.FinalizationGate.StopReason != "coverage_open" || result.StopReason != "coverage_open" {
		t.Fatalf("expected open runtime ledger to override promoted stop reason, gate=%#v stop=%q", result.FinalizationGate, result.StopReason)
	}
	if !strings.Contains(result.FinalizationGate.SynthesisGate, "runtime coverage ledger") {
		t.Fatalf("expected synthesis gate to mention runtime ledger pressure, got %q", result.FinalizationGate.SynthesisGate)
	}
	if !containsQueryFragment(result.FinalizationGate.FollowUpQueries, "full text pdf") {
		t.Fatalf("expected source acquisition follow-up queries, got %#v", result.FinalizationGate.FollowUpQueries)
	}
	if !strings.HasPrefix(result.FinalAnswer, "Provisional answer:") {
		t.Fatalf("expected final answer to be downgraded to provisional, got %q", result.FinalAnswer)
	}
}

func TestFilterUnexecutedLoopQueriesSkipsWorkerExecutedBranches(t *testing.T) {
	pending := filterUnexecutedLoopQueries(
		[]string{"base query", "citation branch", "contradiction branch"},
		[]string{"citation branch"},
	)
	if containsQueryFragment(pending, "citation branch") {
		t.Fatalf("expected worker-executed branch to be skipped, got %#v", pending)
	}
	if !containsQueryFragment(pending, "base query") || !containsQueryFragment(pending, "contradiction branch") {
		t.Fatalf("expected unexecuted branches to remain pending, got %#v", pending)
	}
}

func TestMergeClaimEvidenceLedgerAddsClaimLevelCoverage(t *testing.T) {
	gap := mergeClaimEvidenceLedger(&LoopGapState{Sufficient: true}, "sleep memory", []EvidenceFinding{{
		ID:         "finding-1",
		Claim:      "Sleep improves memory consolidation.",
		SourceID:   "paper-1",
		PaperTitle: "Sleep Paper",
		Snippet:    "Participants retained more after sleep.",
		Confidence: 0.86,
	}})

	if gap == nil || len(gap.Ledger) == 0 {
		t.Fatalf("expected claim-level ledger entries")
	}
	if !containsLedgerCategory(gap.Ledger, "claim_evidence") {
		t.Fatalf("expected claim_evidence ledger entry, got %#v", gap.Ledger)
	}
	if !containsLedgerCategory(gap.Ledger, "claim_source_diversity") {
		t.Fatalf("expected source diversity gap for single-source claim set, got %#v", gap.Ledger)
	}
	if gap.Sufficient {
		t.Fatalf("single-source claim set should keep gap state open for triangulation")
	}
}

func TestMergeClaimEvidenceLedgerOpensZeroEvidenceGap(t *testing.T) {
	gap := mergeClaimEvidenceLedger(&LoopGapState{Sufficient: true}, "sleep memory", nil)
	if gap == nil || len(gap.Ledger) == 0 {
		t.Fatalf("expected zero-evidence claim ledger")
	}
	if !containsLedgerCategory(gap.Ledger, "claim_evidence") {
		t.Fatalf("expected claim_evidence gap, got %#v", gap.Ledger)
	}
	if gap.Sufficient {
		t.Fatalf("zero-evidence claim ledger should keep gap state open")
	}
	if len(gap.NextQueries) == 0 {
		t.Fatalf("expected follow-up query from zero-evidence claim ledger")
	}
}

func TestBuildLoopGapStateCreatesLedgerWithoutStructuredAnalysis(t *testing.T) {
	gap := buildLoopGapState(
		[]string{"primary query", "secondary query"},
		[]string{"primary query"},
		map[string][]search.Paper{"primary query": nil},
		nil,
		nil,
		false,
	)
	if gap == nil || len(gap.Ledger) == 0 {
		t.Fatalf("expected coverage ledger without structured analysis")
	}
	if !containsLedgerCategory(gap.Ledger, "query_coverage") {
		t.Fatalf("expected query coverage gap, got %#v", gap.Ledger)
	}
	if !containsLedgerCategory(gap.Ledger, "planned_query") {
		t.Fatalf("expected unexecuted planned query gap, got %#v", gap.Ledger)
	}
	if gap.Sufficient {
		t.Fatalf("uncovered branches should keep gap state insufficient")
	}
}

func TestBuildLoopGapStateAddsDeterministicCoverageRubric(t *testing.T) {
	gap := buildLoopGapState(
		[]string{"oncology biomarker reproducibility", "oncology biomarker source diversity"},
		[]string{"oncology biomarker reproducibility", "oncology biomarker source diversity"},
		map[string][]search.Paper{
			"oncology biomarker reproducibility": {
				{
					ID:       "paper-1",
					Title:    "Single cohort biomarker study",
					Abstract: "A primary cohort study reports biomarker performance.",
					Source:   "openalex",
				},
				{
					ID:       "paper-2",
					Title:    "Second cohort biomarker study",
					Abstract: "A second cohort study reports biomarker performance.",
					Source:   "semantic_scholar",
				},
			},
			"oncology biomarker source diversity": {{
				ID:       "paper-3",
				Title:    "Third cohort biomarker study",
				Abstract: "A third cohort study reports biomarker performance.",
				Source:   "pubmed",
			}},
		},
		[]search.Paper{
			{
				ID:       "paper-1",
				Title:    "Single cohort biomarker study",
				Abstract: "A primary cohort study reports biomarker performance.",
				Source:   "openalex",
			},
			{
				ID:       "paper-2",
				Title:    "Second cohort biomarker study",
				Abstract: "A second cohort study reports biomarker performance.",
				Source:   "semantic_scholar",
			},
			{
				ID:       "paper-3",
				Title:    "Third cohort biomarker study",
				Abstract: "A third cohort study reports biomarker performance.",
				Source:   "pubmed",
			},
		},
		&sufficiencyAnalysis{Sufficient: true, Reasoning: "Initial coverage looks adequate.", Confidence: 0.91},
		true,
	)
	if gap == nil {
		t.Fatalf("expected gap state")
	}
	if !containsLedgerCategory(gap.Ledger, "coverage_rubric") {
		t.Fatalf("expected deterministic coverage rubric ledger, got %#v", gap.Ledger)
	}
	if !hasOpenActionableCoverageGaps(gap) {
		t.Fatalf("expected missing review/replication/counter-evidence rubric gaps to remain actionable")
	}
	queries := buildRecursiveGapFollowUpQueries("oncology biomarker reproducibility", gap, nil, 4)
	if !containsQueryFragment(queries, "systematic review") || !containsQueryFragment(queries, "replication") {
		t.Fatalf("expected rubric-driven follow-up queries, got %#v", queries)
	}
}

func TestSourceAcquisitionLedgerKeepsHighDepthOpenUntilFullTextAcquired(t *testing.T) {
	papers := []search.Paper{
		{
			ID:            "paper-1",
			Title:         "Open Access Candidate",
			Abstract:      "Abstract-only primary evidence.",
			Source:        "openalex",
			DOI:           "10.1234/source.1",
			OpenAccessUrl: "https://oa.example.com/paper-1",
			PdfUrl:        "https://oa.example.com/paper-1.pdf",
		},
		{
			ID:       "paper-2",
			Title:    "Landing Page Candidate",
			Abstract: "Another abstract-only source.",
			Source:   "semantic_scholar",
			Link:     "https://example.com/paper-2",
		},
	}
	gap := buildLoopGapState(
		[]string{"oncology biomarker reproducibility"},
		[]string{"oncology biomarker reproducibility"},
		map[string][]search.Paper{"oncology biomarker reproducibility": papers},
		papers,
		&sufficiencyAnalysis{Sufficient: true, Reasoning: "Abstract coverage looks adequate.", Confidence: 0.9},
		true,
		ResearchExecutionPlaneDeep,
	)
	entry := findLedgerEntryByCategory(gap.Ledger, "source_acquisition")
	if entry == nil {
		t.Fatalf("expected source_acquisition ledger, got %#v", gap.Ledger)
	}
	if entry.Status != coverageLedgerStatusOpen {
		t.Fatalf("expected source acquisition to remain open without full text, got %#v", entry)
	}
	if gap.Sufficient {
		t.Fatalf("high-depth research must not mark abstract-only evidence sufficient while source acquisition is open")
	}
	if !containsQueryFragment(entry.SupportingQueries, "open access PDF full text") || !containsQueryFragment(gap.NextQueries, "open access PDF full text") {
		t.Fatalf("expected full-text acquisition follow-up query, entry=%#v gapQueries=%#v", entry.SupportingQueries, gap.NextQueries)
	}
}

func TestSourceAcquisitionLedgerResolvesWhenFullTextIsPresent(t *testing.T) {
	papers := []search.Paper{
		{
			ID:       "paper-1",
			Title:    "Full Text Study",
			Abstract: "Summary evidence.",
			FullText: "Methods and results describe independently grounded primary evidence with enough text for synthesis.",
			Source:   "pubmed",
			DOI:      "10.1234/source.2",
		},
		{
			ID:       "paper-2",
			Title:    "Supporting Study",
			Abstract: "Supporting abstract.",
			Source:   "openalex",
			Link:     "https://example.com/supporting-study",
		},
	}
	gap := buildLoopGapState(
		[]string{"sleep memory full text"},
		[]string{"sleep memory full text"},
		map[string][]search.Paper{"sleep memory full text": papers},
		papers,
		&sufficiencyAnalysis{Sufficient: true, Reasoning: "Coverage is adequate.", Confidence: 0.88},
		true,
		ResearchExecutionPlaneDeep,
	)
	entry := findLedgerEntryByCategory(gap.Ledger, "source_acquisition")
	if entry == nil {
		t.Fatalf("expected source_acquisition ledger, got %#v", gap.Ledger)
	}
	if entry.Status != coverageLedgerStatusResolved {
		t.Fatalf("expected source acquisition to resolve when full text is available, got %#v", entry)
	}
	if !gap.Sufficient {
		t.Fatalf("resolved source acquisition should not reopen an otherwise sufficient two-source gap state: %#v", gap)
	}
}

func TestSourceAcquisitionLedgerDoesNotBlockDefaultPlane(t *testing.T) {
	papers := []search.Paper{{
		ID:            "paper-1",
		Title:         "Open Access Candidate",
		Abstract:      "Abstract-only primary evidence.",
		Source:        "openalex",
		OpenAccessUrl: "https://oa.example.com/paper-1",
		PdfUrl:        "https://oa.example.com/paper-1.pdf",
	}}
	gap := buildLoopGapState(
		[]string{"default plane query"},
		[]string{"default plane query"},
		map[string][]search.Paper{"default plane query": papers},
		papers,
		&sufficiencyAnalysis{Sufficient: true, Reasoning: "Default coverage is adequate.", Confidence: 0.84},
		true,
	)
	if containsLedgerCategory(gap.Ledger, "source_acquisition") {
		t.Fatalf("source acquisition should be enforced only for high-depth research planes, got %#v", gap.Ledger)
	}
	if !gap.Sufficient {
		t.Fatalf("default plane should preserve existing sufficiency behavior, got %#v", gap)
	}
}

func TestProgrammaticLoopExecutorRoutesThroughGatewayActionDispatcher(t *testing.T) {
	gw := &AgentGateway{Registry: NewToolRegistry()}
	called := false
	gw.PythonExecute = func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
		called = true
		if action != ActionResearchQueryDecompose {
			t.Fatalf("unexpected action %s", action)
		}
		return map[string]any{"tasks": []map[string]any{{"query": "executor routed query"}}}, nil
	}

	exec := gw.ProgrammaticLoopExecutor()
	result, err := exec(context.Background(), ActionResearchQueryDecompose, map[string]any{"query": "q"}, &AgentSession{SessionID: "s"})
	if err != nil {
		t.Fatalf("ProgrammaticLoopExecutor returned error: %v", err)
	}
	if !called {
		t.Fatalf("expected ProgrammaticLoopExecutor to route through gateway dispatcher and PythonExecute fallback")
	}
	if got := extractProgrammaticQueries(result); len(got) != 1 || got[0] != "executor routed query" {
		t.Fatalf("unexpected queries %#v", got)
	}
}

func TestRecursiveGapFollowUpHelpers(t *testing.T) {
	pending := []string{"q1", "q2", "q3"}
	batch := nextLoopQueryBatch(&pending, 2)
	if len(batch) != 2 || len(pending) != 1 {
		t.Fatalf("expected 2 dequeued and 1 pending, got batch=%#v pending=%#v", batch, pending)
	}

	gap := &LoopGapState{
		Ledger: []CoverageLedgerEntry{{
			ID:                "gap_1",
			Status:            coverageLedgerStatusOpen,
			Title:             "Need independent replication",
			SupportingQueries: []string{"base query independent replication"},
		}},
	}
	if !hasOpenActionableCoverageGaps(gap) {
		t.Fatalf("expected open ledger to be actionable")
	}
	queries := buildRecursiveGapFollowUpQueries("base query", gap, nil, 2)
	if len(queries) == 0 || !strings.Contains(queries[0], "replication") {
		t.Fatalf("expected replication follow-up query, got %#v", queries)
	}
}

func TestCoverageLedgerEntriesNormalizeTypedObligations(t *testing.T) {
	entries := mergeCoverageLedgerEntries(nil, []CoverageLedgerEntry{
		{
			ID:                "citation-identity-gap",
			Category:          "citation_identity_conflict",
			Status:            coverageLedgerStatusOpen,
			Title:             "DOI and arXiv source identity mismatch",
			SupportingQueries: []string{"paper DOI arXiv PubMed identity"},
			Required:          true,
			Priority:          98,
		},
		{
			ID:                "replication-gap",
			Category:          "contradiction",
			Status:            coverageLedgerStatusResolved,
			Title:             "Failed replication search closed",
			SupportingQueries: []string{"paper failed replication null result"},
			SourceFamilies:    []string{"replication"},
			Confidence:        0.84,
		},
	})

	citation := findLedgerEntryByCategory(entries, "citation_identity_conflict")
	if citation == nil || citation.ObligationType != "missing_citation_identity" || citation.OwnerWorker != string(ResearchWorkerCitationGraph) || citation.Severity != "critical" {
		t.Fatalf("expected citation identity obligation to route to citation graph worker, got %#v", citation)
	}
	replication := findLedgerEntryByCategory(entries, "contradiction")
	if replication == nil || replication.ObligationType != "missing_counter_evidence" || replication.OwnerWorker != string(ResearchWorkerContradictionCritic) {
		t.Fatalf("expected contradiction obligation to route to contradiction critic, got %#v", replication)
	}
	if len(replication.ClosureEvidence) == 0 {
		t.Fatalf("expected resolved obligation to include closure evidence, got %#v", replication)
	}
}

func TestResearchBlackboardSynthesisGateBlocksOpenLedger(t *testing.T) {
	board := buildResearchBlackboard([]ResearchWorkerState{
		{
			Role:     ResearchWorkerCitationVerifier,
			Status:   "completed",
			Contract: buildResearchWorkerContract(ResearchWorkerCitationVerifier),
			Evidence: []EvidenceFinding{{
				ID:         "finding-1",
				Claim:      "Citation metadata is incomplete.",
				SourceID:   "paper-1",
				Snippet:    "Missing DOI.",
				Confidence: 0.62,
			}},
			CoverageLedger: []CoverageLedgerEntry{{
				ID:       "citation-gap",
				Category: "worker_citation_verifier",
				Status:   coverageLedgerStatusOpen,
				Title:    "Citation verifier needs follow-up evidence",
			}},
		},
	})

	if board.ReadyForSynthesis {
		t.Fatalf("expected synthesis to be blocked while worker ledger is open")
	}
	if board.OpenLedgerCount != 1 {
		t.Fatalf("expected one open ledger item, got %d", board.OpenLedgerCount)
	}
	if !strings.Contains(board.SynthesisGate, "blocked") {
		t.Fatalf("expected blocked synthesis gate, got %q", board.SynthesisGate)
	}
}

func TestHypothesisBranchLedgerKeepsUnsupportedBranchesActionable(t *testing.T) {
	gap := mergeHypothesisBranchLedger(&LoopGapState{Sufficient: true}, "sleep memory", []Hypothesis{
		{
			Claim:                   "Sleep improves memory consolidation.",
			FalsifiabilityCondition: "No improvement in delayed recall.",
			ConfidenceScore:         0.83,
			Evidence: []*EvidenceFinding{{
				ID:         "finding-1",
				Claim:      "Sleep improved delayed recall.",
				SourceID:   "paper-1",
				Snippet:    "Participants retained more after sleep.",
				Confidence: 0.86,
			}},
			EvidenceCount: 1,
		},
		{
			Claim:                   "Caffeine has no effect on recall.",
			FalsifiabilityCondition: "Recall changes after caffeine dosing.",
			ConfidenceScore:         0.44,
		},
	})

	if gap == nil || !containsLedgerCategory(gap.Ledger, "hypothesis_branch") {
		t.Fatalf("expected hypothesis branch ledger entries, got %#v", gap)
	}
	if gap.Sufficient {
		t.Fatalf("unsupported hypothesis branch should keep the gap state open")
	}
	if !containsQueryFragment(gap.NextQueries, "Caffeine has no effect on recall") {
		t.Fatalf("expected unsupported branch follow-up query, got %#v", gap.NextQueries)
	}
}

func TestResearchDepthPolicyIsPlaneAware(t *testing.T) {
	if got := resolveLoopQueryParallelism(string(WisDevModeYOLO), ResearchExecutionPlaneDeep); got <= resolveLoopQueryParallelism(string(WisDevModeYOLO)) {
		t.Fatalf("expected deep yolo parallelism to exceed default yolo, got %d", got)
	}
	if got := resolveLoopGapRecursionDepth(string(WisDevModeGuided), ResearchExecutionPlaneMultiAgent); got < 3 {
		t.Fatalf("expected multi-agent guided gap recursion depth >= 3, got %d", got)
	}
	if got := resolveCritiqueFollowUpLimit(string(WisDevModeGuided), ResearchExecutionPlaneDeep); got < 4 {
		t.Fatalf("expected deep guided critique follow-up limit >= 4, got %d", got)
	}
	if shouldUseDynamicProviderSelection(string(WisDevModeYOLO), ResearchExecutionPlaneDeep, false, nil) {
		t.Fatalf("dynamic provider selection must stay disabled unless explicitly enabled and backed by an LLM client")
	}
}

func TestResearchWorkerBudgetAdaptsForHighDepthWithoutStarvingLoop(t *testing.T) {
	low := resolveResearchWorkerSearchBudget(ResearchExecutionPlaneAutonomous, 6)
	high := resolveResearchWorkerSearchBudget(ResearchExecutionPlaneDeep, 12)

	if low != 4 {
		t.Fatalf("expected existing six-term budget behavior to stay stable, got %d", low)
	}
	if high <= low {
		t.Fatalf("expected high-depth budget to expand beyond low-depth reserve, got low=%d high=%d", low, high)
	}
	if high >= 12 {
		t.Fatalf("worker budget must reserve searches for recursive loop closure, got %d", high)
	}
}

func containsQueryFragment(queries []string, fragment string) bool {
	fragment = strings.ToLower(strings.TrimSpace(fragment))
	for _, query := range queries {
		if strings.Contains(strings.ToLower(query), fragment) {
			return true
		}
	}
	return false
}

func containsWorkerNote(workers []ResearchWorkerState, role ResearchWorkerRole, fragment string) bool {
	fragment = strings.ToLower(strings.TrimSpace(fragment))
	for _, worker := range workers {
		if worker.Role != role {
			continue
		}
		for _, note := range worker.Notes {
			if strings.Contains(strings.ToLower(note), fragment) {
				return true
			}
		}
	}
	return false
}

func containsLedgerCategory(entries []CoverageLedgerEntry, category string) bool {
	for _, entry := range entries {
		if entry.Category == category {
			return true
		}
	}
	return false
}

func findLedgerEntryByCategory(entries []CoverageLedgerEntry, category string) *CoverageLedgerEntry {
	for idx := range entries {
		if entries[idx].Category == category {
			return &entries[idx]
		}
	}
	return nil
}

func containsDurableTaskOperation(tasks []ResearchDurableTaskState, operation string) bool {
	return findDurableTaskByOperation(tasks, operation) != nil
}

func findDurableTaskByOperation(tasks []ResearchDurableTaskState, operation string) *ResearchDurableTaskState {
	for idx := range tasks {
		if tasks[idx].Operation == operation {
			return &tasks[idx]
		}
	}
	return nil
}

func findBranchEvaluation(branches []ResearchBranchEvaluation, fragment string) *ResearchBranchEvaluation {
	fragment = strings.ToLower(strings.TrimSpace(fragment))
	for idx := range branches {
		if strings.Contains(strings.ToLower(branches[idx].Query), fragment) {
			return &branches[idx]
		}
	}
	return nil
}

func containsWorkerEvent(events []PlanExecutionEvent, role ResearchWorkerRole, stage string) bool {
	for _, event := range events {
		payloadRole, _ := event.Payload["role"].(string)
		payloadStage, _ := event.Payload["stage"].(string)
		if payloadRole == string(role) && strings.EqualFold(payloadStage, stage) {
			return true
		}
	}
	return false
}

func findClaimVerificationRecord(ledger *ClaimVerificationLedger, fragment string) *ClaimVerificationRecord {
	fragment = strings.ToLower(strings.TrimSpace(fragment))
	for idx := range ledger.Records {
		if strings.Contains(strings.ToLower(ledger.Records[idx].Claim), fragment) {
			return &ledger.Records[idx]
		}
	}
	return nil
}

func containsRuntimeStage(events []PlanExecutionEvent, stage string) bool {
	for _, event := range events {
		if got, _ := event.Payload["stage"].(string); got == stage {
			return true
		}
	}
	return false
}
