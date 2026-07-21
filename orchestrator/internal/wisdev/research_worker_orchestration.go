package wisdev

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagents/parallelagent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type researchWorkerExecution struct {
	Role            ResearchWorkerRole
	Status          string
	Contract        ResearchWorkerContract
	Queries         []string
	ExecutedQueries []string
	Papers          []search.Paper
	QueryCoverage   map[string][]search.Paper
	Evidence        []EvidenceFinding
	Ledger          []CoverageLedgerEntry
	Artifacts       map[string]any
	StartedAt       int64
	FinishedAt      int64
	Notes           []string
	Err             error
}

// ResearchSupervisor orchestrates wave-based multi-specialist dispatch.
// Specialists run in dependency-ordered waves so that verifiers and synthesizers
// can consume the merged blackboard produced by earlier gather workers.
//
//	Wave 1 (gather):    Scout, SourceDiversifier, CitationVerifier, CitationGraph, ContradictionCritic
//	Wave 2 (verify):    IndependentVerifier — reads Wave 1 merged blackboard
//	Wave 3 (synthesize): Synthesizer — reads Wave 2 merged blackboard + arbitration
type ResearchSupervisor struct {
	rt *UnifiedResearchRuntime
}

func newResearchSupervisor(rt *UnifiedResearchRuntime) *ResearchSupervisor {
	return &ResearchSupervisor{rt: rt}
}

// supervisorWaves returns the ordered wave plan for the given plane.
func supervisorWaves(plane ResearchExecutionPlane) [][]ResearchWorkerRole {
	if plane != ResearchExecutionPlaneMultiAgent && plane != ResearchExecutionPlaneDeep && plane != ResearchExecutionPlaneAutonomous {
		return [][]ResearchWorkerRole{{ResearchWorkerScout}}
	}
	return [][]ResearchWorkerRole{
		{ResearchWorkerScout, ResearchWorkerSourceDiversifier, ResearchWorkerCitationVerifier, ResearchWorkerCitationGraph, ResearchWorkerContradictionCritic},
		{ResearchWorkerIndependentVerifier},
		{ResearchWorkerSynthesizer},
	}
}

// workerRoleNames returns a string slice of role names for structured logging.
func workerRoleNames(roles []ResearchWorkerRole) []string {
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = string(r)
	}
	return names
}

// runWaves dispatches each wave in order, rebuilding the shared blackboard between
// waves so each later-stage worker can read the full accumulated evidence.
func (s *ResearchSupervisor) runWaves(
	ctx context.Context,
	state *ResearchSessionState,
	session *AgentSession,
	query, domain, mode string,
	allowProgrammaticPlanning bool,
	searchBudget int,
	bypassSearchCache bool,
	emit func(PlanExecutionEvent),
) []string {
	waves := supervisorWaves(state.Plane)
	allRoles := activeResearchWorkerRoles(state.Plane)
	allBudgets := assignResearchWorkerSearchBudgets(allRoles, searchBudget)
	var board *ResearchBlackboard
	allQueries := make([]string, 0)

	for waveIdx, wave := range waves {
		results := make([]researchWorkerExecution, len(wave))
		var wg sync.WaitGroup
		for i, role := range wave {
			emitResearchWorkerProgress(emit, session, role, "started", researchWorkerExecution{Role: role, Status: "active"})
			wg.Add(1)
			go func(idx int, r ResearchWorkerRole) {
				defer wg.Done()
				results[idx] = s.rt.executeResearchWorkerInContext(ctx, r, session, query, domain, mode, allowProgrammaticPlanning, allBudgets[r], bypassSearchCache, board)
			}(i, role)
		}
		wg.Wait()

		statusByRole := make(map[ResearchWorkerRole]researchWorkerExecution, len(results))
		for _, res := range results {
			statusByRole[res.Role] = res
			allQueries = append(allQueries, res.Queries...)
		}
		for idx := range state.Workers {
			res, ok := statusByRole[state.Workers[idx].Role]
			if !ok {
				continue
			}
			state.Workers[idx].Status = res.Status
			if res.Contract.Role != "" {
				state.Workers[idx].Contract = res.Contract
			}
			state.Workers[idx].PlannedQueries = append([]string(nil), res.Queries...)
			state.Workers[idx].ExecutedQueries = append([]string(nil), res.ExecutedQueries...)
			state.Workers[idx].Papers = append([]search.Paper(nil), res.Papers...)
			state.Workers[idx].QueryCoverage = cloneLoopQueryCoverage(res.QueryCoverage)
			state.Workers[idx].Evidence = append([]EvidenceFinding(nil), res.Evidence...)
			state.Workers[idx].CoverageLedger = append([]CoverageLedgerEntry(nil), res.Ledger...)
			state.Workers[idx].Artifacts = cloneAnyMap(res.Artifacts)
			state.Workers[idx].StartedAt = res.StartedAt
			state.Workers[idx].FinishedAt = res.FinishedAt
			state.Workers[idx].Notes = dedupeTrimmedStrings(append(state.Workers[idx].Notes, res.Notes...))
			if res.Err != nil {
				state.Workers[idx].Notes = append(state.Workers[idx].Notes, "error: "+res.Err.Error())
			}
			recordResearchWorkerDurableTasks(state, res)
			emitResearchWorkerProgress(emit, session, res.Role, "completed", res)
		}
		// Add any wave workers that were not in the initial state.Workers list.
		// This handles specialist roles like CitationGraph that are activated by
		// the supervisor but are not pre-seeded in newResearchSessionState.
		existingRoles := make(map[ResearchWorkerRole]struct{}, len(state.Workers))
		for _, w := range state.Workers {
			existingRoles[w.Role] = struct{}{}
		}
		for _, res := range results {
			if _, exists := existingRoles[res.Role]; exists {
				continue
			}
			ws := ResearchWorkerState{
				Role:            res.Role,
				Status:          res.Status,
				Contract:        res.Contract,
				PlannedQueries:  append([]string(nil), res.Queries...),
				ExecutedQueries: append([]string(nil), res.ExecutedQueries...),
				Papers:          append([]search.Paper(nil), res.Papers...),
				QueryCoverage:   cloneLoopQueryCoverage(res.QueryCoverage),
				Evidence:        append([]EvidenceFinding(nil), res.Evidence...),
				CoverageLedger:  append([]CoverageLedgerEntry(nil), res.Ledger...),
				Artifacts:       cloneAnyMap(res.Artifacts),
				StartedAt:       res.StartedAt,
				FinishedAt:      res.FinishedAt,
				Notes:           dedupeTrimmedStrings(res.Notes),
			}
			if res.Err != nil {
				ws.Notes = append(ws.Notes, "error: "+res.Err.Error())
			}
			state.Workers = append(state.Workers, ws)
			existingRoles[res.Role] = struct{}{}
			recordResearchWorkerDurableTasks(state, res)
			emitResearchWorkerProgress(emit, session, res.Role, "completed", res)
		}
		// Rebuild the shared blackboard so the next wave can read accumulated evidence.
		board = buildResearchBlackboard(state.Workers)
		slog.Info("ResearchSupervisor: wave complete",
			"wave", waveIdx+1,
			"roles", workerRoleNames(wave),
			"evidenceCount", len(board.Evidence),
			"openLedger", board.OpenLedgerCount,
			"readyForSynthesis", board.ReadyForSynthesis)
	}
	return normalizeLoopQueries("", allQueries)
}

func (rt *UnifiedResearchRuntime) executeResearchWorkers(
	ctx context.Context,
	state *ResearchSessionState,
	session *AgentSession,
	query string,
	domain string,
	mode string,
	allowProgrammaticPlanning bool,
	searchBudget int,
	bypassSearchCache bool,
	emitters ...func(PlanExecutionEvent),
) []string {
	if state == nil {
		return nil
	}
	var emit func(PlanExecutionEvent)
	if len(emitters) > 0 {
		emit = emitters[0]
	}

	// Phase 7: Transition to true ADK Swarm if runtime is available
	if rt.adkRuntime != nil && rt.adkRuntime.Agent != nil {
		slog.Info("Executing concurrent research swarm via ADK framework", "sessionID", state.SessionID)
		return rt.executeADKSwarm(ctx, state, session, query, domain, mode, allowProgrammaticPlanning, searchBudget, bypassSearchCache, emit)
	}

	// Use ResearchSupervisor for wave-based dispatch:
	// Wave 1 gathers evidence, Wave 2 verifies, Wave 3 synthesizes.
	supervisor := newResearchSupervisor(rt)
	return supervisor.runWaves(ctx, state, session, query, domain, mode, allowProgrammaticPlanning, searchBudget, bypassSearchCache, emit)
}

func (rt *UnifiedResearchRuntime) executeADKSwarm(
	ctx context.Context,
	state *ResearchSessionState,
	session *AgentSession,
	query string,
	domain string,
	mode string,
	allowProgrammaticPlanning bool,
	searchBudget int,
	bypassSearchCache bool,
	emit func(PlanExecutionEvent),
) []string {
	allQueries := make([]string, 0)
	roles := activeResearchWorkerRoles(state.Plane)
	searchBudgets := assignResearchWorkerSearchBudgets(roles, searchBudget)
	var board *ResearchBlackboard

	for waveIdx, wave := range supervisorWaves(state.Plane) {
		results, ok := rt.executeADKWorkerWave(ctx, state, session, query, domain, mode, allowProgrammaticPlanning, searchBudgets, bypassSearchCache, board, wave)
		if !ok {
			results = rt.executeSupervisorWorkerWave(ctx, session, query, domain, mode, allowProgrammaticPlanning, searchBudgets, bypassSearchCache, board, wave)
		}
		for _, res := range results {
			allQueries = append(allQueries, res.Queries...)
			applyResearchWorkerExecution(state, res)
			metadata := map[string]any{
				"adkRuntime":       "google.golang.org/adk/v2",
				"adkWorkflowAgent": "parallelagent",
				"adkWorkflowStage": "parallel_fanout_gather",
				"adkWave":          waveIdx + 1,
				"adkWaveRoles":     workerRoleNames(wave),
			}
			if res.Artifacts != nil {
				if value := AsOptionalString(res.Artifacts["adkInvocationId"]); value != "" {
					metadata["adkInvocationId"] = value
				}
				if value := AsOptionalString(res.Artifacts["adkEventAuthor"]); value != "" {
					metadata["adkEventAuthor"] = value
				}
				if value, ok := res.Artifacts["adkRunnerExecuted"].(bool); ok {
					metadata["adkRunnerExecuted"] = value
				}
				if value, ok := res.Artifacts["adkFallback"].(bool); ok {
					metadata["adkFallback"] = value
				}
				if value := AsOptionalString(res.Artifacts["adkFallbackStage"]); value != "" {
					metadata["adkFallbackStage"] = value
				}
			}
			emitResearchWorkerProgress(emit, session, res.Role, "completed_adk_parallel", res, metadata)
		}
		board = buildResearchBlackboard(state.Workers)
		state.Blackboard = board
		slog.Info("ADK research swarm wave complete",
			"sessionID", state.SessionID,
			"wave", waveIdx+1,
			"roles", workerRoleNames(wave),
			"evidenceCount", len(board.Evidence),
			"openLedger", board.OpenLedgerCount,
			"readyForSynthesis", board.ReadyForSynthesis,
		)
	}

	return normalizeLoopQueries("", allQueries)
}

func (rt *UnifiedResearchRuntime) executeADKWorkerWave(
	ctx context.Context,
	state *ResearchSessionState,
	session *AgentSession,
	query string,
	domain string,
	mode string,
	allowProgrammaticPlanning bool,
	searchBudgets map[ResearchWorkerRole]int,
	bypassSearchCache bool,
	board *ResearchBlackboard,
	wave []ResearchWorkerRole,
) ([]researchWorkerExecution, bool) {
	if len(wave) == 0 {
		return nil, true
	}
	subAgents := make([]agent.Agent, 0, len(wave))
	for _, role := range wave {
		workerRole := role
		budget := searchBudgets[workerRole]
		workerBoard := board
		workerAgent, err := agent.New(agent.Config{
			Name:        string(workerRole),
			Description: buildResearchWorkerContract(workerRole).Objective,
			Run: func(invocation agent.InvocationContext) iter.Seq2[*adksession.Event, error] {
				return func(yield func(*adksession.Event, error) bool) {
					res := rt.executeResearchWorkerInContext(invocation, workerRole, session, query, domain, mode, allowProgrammaticPlanning, budget, bypassSearchCache, workerBoard)
					if res.Artifacts == nil {
						res.Artifacts = map[string]any{}
					}
					event := adksession.NewEvent(invocation, invocation.InvocationID())
					event.Author = string(workerRole)
					event.Content = genai.NewContentFromText(fmt.Sprintf("Research worker completed: %s", workerRole), genai.RoleModel)
					event.Actions.StateDelta["researchWorkerResult"] = res
					event.Actions.StateDelta["adkRuntime"] = "google.golang.org/adk/v2"
					event.Actions.StateDelta["adkWorkflowAgent"] = "parallelagent"
					event.Actions.StateDelta["adkSubAgent"] = string(workerRole)
					event.TurnComplete = true
					yield(event, nil)
				}
			},
		})
		if err != nil {
			slog.Warn("Failed to create ADK research worker agent; falling back to supervisor worker",
				"role", workerRole,
				"error", err,
			)
			return nil, false
		}
		subAgents = append(subAgents, workerAgent)
	}
	if len(subAgents) == 0 {
		return nil, true
	}

	parallelAgent, err := parallelagent.New(parallelagent.Config{
		AgentConfig: agent.Config{
			Name:        "wisdev-research-swarm-" + stableWisDevID("wave", workerRoleNames(wave)...),
			Description: "ADK parallel fan-out/gather wave coordinating WisDev research workers.",
			SubAgents:   subAgents,
		},
	})
	if err != nil {
		slog.Warn("Failed to create ADK parallel research swarm; falling back to wave supervisor", "error", err)
		return nil, false
	}

	adkRunner, err := runnerForADKSwarm(rt, parallelAgent)
	if err != nil {
		slog.Warn("Failed to create ADK parallel research swarm runner; falling back to wave supervisor", "error", err)
		return nil, false
	}

	userID := "wisdev"
	sessionID := state.SessionID
	if session != nil {
		if strings.TrimSpace(session.UserID) != "" {
			userID = session.UserID
		}
		if strings.TrimSpace(session.SessionID) != "" {
			sessionID = session.SessionID
		}
	}
	if strings.TrimSpace(sessionID) == "" {
		sessionID = "wisdev-research-swarm"
	}

	results := make([]researchWorkerExecution, 0, len(wave))
	for event, runErr := range adkRunner.Run(ctx, userID, sessionID, genai.NewContentFromText(query, genai.RoleUser), agent.RunConfig{}) {
		if runErr != nil {
			slog.Warn("ADK research swarm worker failed; continuing with completed worker events", "error", runErr)
			continue
		}
		if event == nil {
			continue
		}
		res, ok := event.Actions.StateDelta["researchWorkerResult"].(researchWorkerExecution)
		if !ok {
			continue
		}
		if res.Artifacts == nil {
			res.Artifacts = map[string]any{}
		}
		res.Artifacts["adkRuntime"] = AsOptionalString(event.Actions.StateDelta["adkRuntime"])
		res.Artifacts["adkWorkflowAgent"] = AsOptionalString(event.Actions.StateDelta["adkWorkflowAgent"])
		res.Artifacts["adkSubAgent"] = AsOptionalString(event.Actions.StateDelta["adkSubAgent"])
		res.Artifacts["adkInvocationId"] = event.InvocationID
		res.Artifacts["adkEventAuthor"] = event.Author
		res.Artifacts["adkRunnerExecuted"] = true
		results = append(results, res)
	}

	return results, true
}

func (rt *UnifiedResearchRuntime) executeSupervisorWorkerWave(
	ctx context.Context,
	session *AgentSession,
	query string,
	domain string,
	mode string,
	allowProgrammaticPlanning bool,
	searchBudgets map[ResearchWorkerRole]int,
	bypassSearchCache bool,
	board *ResearchBlackboard,
	wave []ResearchWorkerRole,
) []researchWorkerExecution {
	results := make([]researchWorkerExecution, len(wave))
	var wg sync.WaitGroup
	for idx, role := range wave {
		wg.Add(1)
		go func(idx int, role ResearchWorkerRole) {
			defer wg.Done()
			res := rt.executeResearchWorkerInContext(ctx, role, session, query, domain, mode, allowProgrammaticPlanning, searchBudgets[role], bypassSearchCache, board)
			if res.Artifacts == nil {
				res.Artifacts = map[string]any{}
			}
			res.Artifacts["adkRuntime"] = "google.golang.org/adk/v2"
			res.Artifacts["adkWorkflowAgent"] = "parallelagent"
			res.Artifacts["adkFallback"] = true
			res.Artifacts["adkFallbackStage"] = "parallel_wave_runtime"
			results[idx] = res
		}(idx, role)
	}
	wg.Wait()
	return results
}

func runnerForADKSwarm(rt *UnifiedResearchRuntime, swarm agent.Agent) (*runner.Runner, error) {
	appName := "wisdev-adk"
	if rt != nil && rt.adkRuntime != nil {
		if configured := strings.TrimSpace(rt.adkRuntime.Config.Runtime.Name); configured != "" {
			appName = configured
		}
	}
	return runner.New(runner.Config{
		AppName:           appName,
		Agent:             swarm,
		SessionService:    adksession.InMemoryService(),
		AutoCreateSession: true,
	})
}

func applyResearchWorkerExecution(state *ResearchSessionState, res researchWorkerExecution) {
	if state == nil {
		return
	}
	for idx := range state.Workers {
		if state.Workers[idx].Role == res.Role {
			state.Workers[idx].Status = res.Status
			if res.Contract.Role != "" {
				state.Workers[idx].Contract = res.Contract
			}
			state.Workers[idx].PlannedQueries = res.Queries
			state.Workers[idx].ExecutedQueries = res.ExecutedQueries
			state.Workers[idx].Papers = res.Papers
			state.Workers[idx].QueryCoverage = res.QueryCoverage
			state.Workers[idx].Evidence = res.Evidence
			state.Workers[idx].CoverageLedger = res.Ledger
			state.Workers[idx].Artifacts = res.Artifacts
			state.Workers[idx].StartedAt = res.StartedAt
			state.Workers[idx].FinishedAt = res.FinishedAt
			state.Workers[idx].Notes = dedupeTrimmedStrings(append(state.Workers[idx].Notes, res.Notes...))
			if res.Err != nil {
				state.Workers[idx].Notes = append(state.Workers[idx].Notes, "error: "+res.Err.Error())
			}
			return
		}
	}
	notes := dedupeTrimmedStrings(res.Notes)
	if res.Err != nil {
		notes = append(notes, "error: "+res.Err.Error())
	}
	state.Workers = append(state.Workers, ResearchWorkerState{
		Role:            res.Role,
		Status:          res.Status,
		Contract:        res.Contract,
		PlannedQueries:  append([]string(nil), res.Queries...),
		ExecutedQueries: append([]string(nil), res.ExecutedQueries...),
		Papers:          append([]search.Paper(nil), res.Papers...),
		QueryCoverage:   cloneLoopQueryCoverage(res.QueryCoverage),
		Evidence:        append([]EvidenceFinding(nil), res.Evidence...),
		CoverageLedger:  append([]CoverageLedgerEntry(nil), res.Ledger...),
		Artifacts:       cloneAnyMap(res.Artifacts),
		StartedAt:       res.StartedAt,
		FinishedAt:      res.FinishedAt,
		Notes:           notes,
	})
}

func activeResearchWorkerRoles(plane ResearchExecutionPlane) []ResearchWorkerRole {
	roles := []ResearchWorkerRole{
		ResearchWorkerScout,
		ResearchWorkerSourceDiversifier,
		ResearchWorkerCitationVerifier,
		ResearchWorkerCitationGraph,
		ResearchWorkerContradictionCritic,
		ResearchWorkerIndependentVerifier,
		ResearchWorkerSynthesizer,
	}
	if plane == ResearchExecutionPlaneMultiAgent || plane == ResearchExecutionPlaneDeep || plane == ResearchExecutionPlaneAutonomous {
		return roles
	}
	return roles[:1]
}

// executeResearchWorker is the Wave 1 entry point (nil blackboard).
func (rt *UnifiedResearchRuntime) executeResearchWorker(
	ctx context.Context,
	role ResearchWorkerRole,
	session *AgentSession,
	query string,
	domain string,
	mode string,
	allowProgrammaticPlanning bool,
	searchBudget int,
) researchWorkerExecution {
	return rt.executeResearchWorkerInContext(ctx, role, session, query, domain, mode, allowProgrammaticPlanning, searchBudget, false, nil)
}

// executeResearchWorkerInContext is the canonical worker entry point.
// blackboard carries merged evidence from prior waves; nil for Wave 1 workers.
func (rt *UnifiedResearchRuntime) executeResearchWorkerInContext(
	ctx context.Context,
	role ResearchWorkerRole,
	session *AgentSession,
	query string,
	domain string,
	mode string,
	allowProgrammaticPlanning bool,
	searchBudget int,
	bypassSearchCache bool,
	blackboard *ResearchBlackboard,
) researchWorkerExecution {
	result := researchWorkerExecution{
		Role:      role,
		Status:    "completed",
		Contract:  buildResearchWorkerContract(role),
		StartedAt: NowMillis(),
		Artifacts: map[string]any{},
	}
	switch role {
	case ResearchWorkerScout:
		if allowProgrammaticPlanning {
			branchPlans := rt.planProgrammaticBranches(ctx, session, query, domain, mode)
			result.Artifacts["branchPlans"] = branchPlans
			result.Queries = researchBranchPlanQueries(branchPlans)
		} else {
			result.Notes = append(result.Notes, "programmatic tree planner disabled by request policy")
		}
		if len(result.Queries) == 0 {
			result.Queries = append(result.Queries, buildResearchWorkerQuery(query, "core mechanisms evidence map"))
		}
		result.Queries = normalizeLoopQueries(query, result.Queries)
		result.Notes = append(result.Notes, fmt.Sprintf("tree-branch planner produced %d candidate queries", len(result.Queries)))
	case ResearchWorkerSourceDiversifier:
		result.Queries = append(result.Queries,
			buildResearchWorkerQuery(query, "systematic review meta analysis open access full text"),
			buildResearchWorkerQuery(query, "dataset benchmark replication full text PDF"),
			buildResearchWorkerQuery(query, domainSpecificEvidenceQuery(domain)+" open access full text"),
		)
		result.Artifacts["sourceFamiliesRequired"] = []string{"systematic_review", "replication", "domain_primary_sources", "independent_triangulation", "full_text_acquisition"}
		result.Artifacts["sourceAcquisitionRequired"] = true
		result.Notes = append(result.Notes, "source-family and full-text acquisition requirements queued")
	case ResearchWorkerCitationVerifier:
		result.Queries = append(result.Queries,
			buildResearchWorkerQuery(query, "canonical citations replication evidence"),
			buildResearchWorkerQuery(query, "forward backward citation trail"),
		)
		result.Artifacts["crossReferencePlan"] = []string{"normalize DOI/arXiv/PubMed identifiers", "run backward citation snowballing", "run forward citation snowballing", "prefer primary-source corroboration"}
		result.Notes = append(result.Notes, "citation verification and snowballing queries queued")
	case ResearchWorkerCitationGraph:
		result.Queries = append(result.Queries,
			buildResearchWorkerQuery(query, "forward citation graph citing papers"),
			buildResearchWorkerQuery(query, "backward citation graph references retraction withdrawn correction"),
			buildResearchWorkerQuery(query, "DOI arXiv PubMed OpenAlex Semantic Scholar source identity conflicts"),
		)
		result.Artifacts["citationGraphContract"] = []string{"backward citation expansion", "forward citation expansion", "persistent identifier reconciliation", "duplicate source identity detection", "retraction and correction checks"}
		result.Notes = append(result.Notes, "citation-graph snowballing and identity reconciliation queued")
	case ResearchWorkerContradictionCritic:
		result.Queries = append(result.Queries,
			buildResearchWorkerQuery(query, "limitations contradictory evidence"),
			buildResearchWorkerQuery(query, "failed replication null results"),
		)
		result.Artifacts["criticFocus"] = []string{"negative results", "failed replication", "methodological limitations", "conflicting claims"}
		result.Notes = append(result.Notes, "counter-evidence search obligations queued")
	case ResearchWorkerIndependentVerifier:
		result.Artifacts["verificationContract"] = []string{"read specialist blackboard", "verify claim evidence packets", "block synthesis on unresolved critical ledgers"}
		result.Artifacts["verifierFinalizationGate"] = "claim-level verifier decision is applied after loop evidence and worker ledgers merge"
		result.Notes = append(result.Notes, "independent verifier contract packet queued for finalization gate")
		// Consume Wave 1 blackboard: surface evidence stats and open gaps to the verifier.
		if blackboard != nil {
			result.Artifacts["blackboardEvidenceCount"] = len(blackboard.Evidence)
			result.Artifacts["blackboardOpenLedger"] = blackboard.OpenLedgerCount
			result.Artifacts["blackboardReadyForSynthesis"] = blackboard.ReadyForSynthesis
			if blackboard.Arbitration != nil {
				result.Artifacts["priorArbitrationVerdict"] = blackboard.Arbitration.Verdict
			}
			if blackboard.OpenLedgerCount > 0 {
				result.Notes = append(result.Notes, fmt.Sprintf("verifier: %d open coverage ledger item(s) require resolution before synthesis", blackboard.OpenLedgerCount))
			}
			if len(blackboard.Evidence) > 0 {
				result.Notes = append(result.Notes, fmt.Sprintf("verifier: reading %d evidence findings from gather wave", len(blackboard.Evidence)))
			}
			slog.Info("ResearchSupervisor: IndependentVerifier consuming Wave 1 blackboard",
				"evidenceCount", len(blackboard.Evidence),
				"openLedger", blackboard.OpenLedgerCount,
				"readyForSynthesis", blackboard.ReadyForSynthesis)
		}
	case ResearchWorkerSynthesizer:
		result.Artifacts["synthesisGate"] = "waits for claim-evidence and coverage ledgers before final answer generation"
		result.Notes = append(result.Notes, "synthesis deferred until claim-evidence and coverage ledgers close")
		// Consume Wave 2 blackboard: apply arbitration verdict and gate on synthesis readiness.
		if blackboard != nil {
			result.Artifacts["blackboardReadyForSynthesis"] = blackboard.ReadyForSynthesis
			result.Artifacts["blackboardSynthesisGate"] = blackboard.SynthesisGate
			if blackboard.Arbitration != nil {
				result.Artifacts["arbitrationVerdict"] = blackboard.Arbitration.Verdict
				result.Artifacts["promotedClaimCount"] = len(blackboard.Arbitration.PromotedClaimIDs)
				result.Artifacts["rejectedClaimCount"] = len(blackboard.Arbitration.RejectedClaimIDs)
			}
			if !blackboard.ReadyForSynthesis {
				result.Notes = append(result.Notes, "synthesizer: gate blocked — "+blackboard.SynthesisGate)
			}
			slog.Info("ResearchSupervisor: Synthesizer consuming Wave 2 blackboard",
				"readyForSynthesis", blackboard.ReadyForSynthesis,
				"synthesisGate", blackboard.SynthesisGate)
		}
	default:
		result.Status = "skipped"
		result.Notes = append(result.Notes, "unknown worker role")
	}
	if result.Status == "completed" && role != ResearchWorkerSynthesizer {
		rt.executeWorkerSearches(ctx, role, query, domain, searchBudget, bypassSearchCache, &result)
	}
	if result.Status == "completed" && role == ResearchWorkerCitationGraph {
		rt.attachCitationGraphWorkerArtifact(ctx, query, &result)
	}
	if ctx.Err() != nil {
		result.Status = "cancelled"
		result.Err = ctx.Err()
	}
	if result.Err != nil {
		slog.Warn("wisdev research worker degraded",
			"component", "wisdev.runtime",
			"operation", "executeResearchWorker",
			"role", string(role),
			"error", result.Err.Error(),
		)
	}
	result.Queries = normalizeLoopQueries("", result.Queries)
	result.ExecutedQueries = normalizeLoopQueries("", result.ExecutedQueries)
	result.Evidence = dedupeEvidenceFindings(result.Evidence)
	result.Ledger = append(result.Ledger, buildWorkerCoverageLedger(result)...)
	result.FinishedAt = NowMillis()
	return result
}

func (rt *UnifiedResearchRuntime) executeWorkerSearches(
	ctx context.Context,
	role ResearchWorkerRole,
	query string,
	domain string,
	searchBudget int,
	bypassSearchCache bool,
	result *researchWorkerExecution,
) {
	if rt == nil || rt.searchReg == nil || result == nil || len(result.Queries) == 0 {
		return
	}
	queries := result.Queries
	if limit := workerSearchQueryLimit(role); len(queries) > limit {
		queries = queries[:limit]
	}
	if searchBudget <= 0 {
		result.Notes = append(result.Notes, fmt.Sprintf("%s search skipped because worker search budget is exhausted", role))
		result.Artifacts["searchBudget"] = searchBudget
		return
	}
	if len(queries) > searchBudget {
		queries = queries[:searchBudget]
	}
	result.QueryCoverage = make(map[string][]search.Paper, len(queries))
	branchResults := rt.executeWorkerSearchBranches(ctx, role, query, domain, queries, bypassSearchCache)
	for _, branch := range branchResults {
		if branch.Err != nil && ctx.Err() != nil {
			result.Err = branch.Err
			return
		}
		if branch.TimedOut {
			result.Notes = append(result.Notes, fmt.Sprintf("%s search timed out for %q", role, branch.Query))
		}
		result.ExecutedQueries = appendUniqueLoopQuery(result.ExecutedQueries, branch.Query)
		result.Papers = appendUniqueSearchPapers(result.Papers, branch.Papers)
		recordLoopQueryCoverage(result.QueryCoverage, branch.Query, branch.Papers)
		for _, finding := range branch.Findings {
			finding.Status = firstNonEmpty(strings.TrimSpace(finding.Status), "worker_observed")
			finding.Keywords = dedupeTrimmedStrings(append(finding.Keywords, string(role)))
			result.Evidence = append(result.Evidence, finding)
		}
		if len(branch.Warnings) > 0 {
			result.Notes = append(result.Notes, fmt.Sprintf("%s search warnings: %d", role, len(branch.Warnings)))
			result.Artifacts["searchWarnings"] = appendWorkerSearchWarnings(result.Artifacts["searchWarnings"], branch.Query, branch.Warnings)
		}
	}
	if len(result.ExecutedQueries) > 0 {
		result.Notes = append(result.Notes, fmt.Sprintf("%s executed %d bounded specialist search branch(es)", role, len(result.ExecutedQueries)))
	}
	result.Artifacts["branchParallelism"] = workerSearchParallelism(role)
	result.Artifacts["searchBudget"] = searchBudget
	result.Artifacts["executedSearches"] = len(result.ExecutedQueries)
	result.Artifacts["evidenceCount"] = len(result.Evidence)
	result.Artifacts["paperCount"] = len(result.Papers)
}

func resolveResearchWorkerSearchBudget(plane ResearchExecutionPlane, searchTermBudget int) int {
	if searchTermBudget <= 0 {
		if plane == ResearchExecutionPlaneMultiAgent || plane == ResearchExecutionPlaneDeep || plane == ResearchExecutionPlaneAutonomous {
			return 5
		}
		return 1
	}
	if plane != ResearchExecutionPlaneMultiAgent && plane != ResearchExecutionPlaneDeep && plane != ResearchExecutionPlaneAutonomous {
		return minInt(searchTermBudget, 1)
	}
	if searchTermBudget <= 2 {
		return 1
	}
	if searchTermBudget <= 4 {
		return 2
	}
	if searchTermBudget <= 6 {
		return minInt(searchTermBudget-2, 5)
	}
	reservedForLoop := maxInt(2, searchTermBudget/3)
	workerBudget := searchTermBudget - reservedForLoop
	maxWorkerBudget := 7
	if plane == ResearchExecutionPlaneDeep || plane == ResearchExecutionPlaneMultiAgent {
		maxWorkerBudget = 8
	}
	if unleashedBudgetMode() {
		maxWorkerBudget *= 3
	}
	return minInt(maxInt(workerBudget, 4), maxWorkerBudget)
}

func assignResearchWorkerSearchBudgets(roles []ResearchWorkerRole, totalBudget int) map[ResearchWorkerRole]int {
	budgets := make(map[ResearchWorkerRole]int, len(roles))
	if totalBudget <= 0 {
		return budgets
	}
	remaining := totalBudget
	assign := func(role ResearchWorkerRole, slots int) {
		if remaining <= 0 || slots <= 0 || !researchWorkerRoleActive(roles, role) {
			return
		}
		current := budgets[role]
		maxSlots := workerSearchQueryLimit(role) - current
		if maxSlots <= 0 {
			return
		}
		if slots > maxSlots {
			slots = maxSlots
		}
		if slots > remaining {
			slots = remaining
		}
		budgets[role] = current + slots
		remaining -= slots
	}

	assign(ResearchWorkerScout, 1)
	assign(ResearchWorkerSourceDiversifier, 1)
	assign(ResearchWorkerCitationVerifier, 1)
	assign(ResearchWorkerCitationGraph, 1)
	assign(ResearchWorkerContradictionCritic, 1)
	if totalBudget >= 5 {
		assign(ResearchWorkerSourceDiversifier, 1)
	}
	if totalBudget >= 6 {
		assign(ResearchWorkerCitationVerifier, 1)
		assign(ResearchWorkerCitationGraph, 1)
		assign(ResearchWorkerContradictionCritic, 1)
	}
	for _, role := range roles {
		if remaining <= 0 {
			break
		}
		if role == ResearchWorkerSynthesizer {
			continue
		}
		if budgets[role] >= workerSearchQueryLimit(role) {
			continue
		}
		budgets[role]++
		remaining--
	}
	return budgets
}

func researchWorkerRoleActive(roles []ResearchWorkerRole, role ResearchWorkerRole) bool {
	for _, candidate := range roles {
		if candidate == role {
			return true
		}
	}
	return false
}

type workerSearchBranchResult struct {
	Query    string
	Papers   []search.Paper
	Findings []EvidenceFinding
	Warnings []search.ProviderWarning
	TimedOut bool
	Err      error
}

func (rt *UnifiedResearchRuntime) executeWorkerSearchBranches(
	ctx context.Context,
	role ResearchWorkerRole,
	rootQuery string,
	domain string,
	queries []string,
	bypassSearchCache bool,
) []workerSearchBranchResult {
	queries = normalizeLoopQueries("", queries)
	if len(queries) == 0 || rt == nil || rt.searchReg == nil {
		return nil
	}
	parallelism := workerSearchParallelism(role)
	if parallelism > len(queries) {
		parallelism = len(queries)
	}
	results := make([]workerSearchBranchResult, len(queries))
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for idx, workerQuery := range queries {
		wg.Add(1)
		go func(idx int, workerQuery string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = workerSearchBranchResult{Query: workerQuery, Err: ctx.Err()}
				return
			}
			results[idx] = rt.executeWorkerSearchBranch(ctx, role, rootQuery, domain, workerQuery, bypassSearchCache)
		}(idx, workerQuery)
	}
	wg.Wait()
	return results
}

func (rt *UnifiedResearchRuntime) executeWorkerSearchBranch(
	ctx context.Context,
	role ResearchWorkerRole,
	rootQuery string,
	domain string,
	workerQuery string,
	bypassSearchCache bool,
) workerSearchBranchResult {
	result := workerSearchBranchResult{Query: strings.TrimSpace(workerQuery)}
	if result.Query == "" {
		return result
	}
	searchCtx := ctx
	var cancel context.CancelFunc
	if timeout := workerSearchTimeout(role); timeout > 0 {
		searchCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	papers, payload, searchErr := retrieveCanonicalSearchPapers(searchCtx, rt.searchReg, result.Query, search.SearchOpts{
		Limit:       workerSearchPaperLimit(role),
		Domain:      strings.TrimSpace(domain),
		QualitySort: true,
		SkipCache:   bypassSearchCache,
	})
	if cancel != nil {
		cancel()
	}
	result.Papers = papers
	result.Warnings = providerWarningsFromRetrievalPayload(payload)
	result.TimedOut = searchCtx.Err() != nil && ctx.Err() == nil
	if searchErr != nil {
		result.Err = searchErr
		return result
	}
	result.Err = ctx.Err()
	findings := buildEvidenceFindingsFromRawMaterial(firstNonEmpty(result.Query, rootQuery), papers, workerEvidenceLimit(role))
	if len(findings) == 0 {
		for _, paper := range papers {
			findings = append(findings, buildEvidenceFindingsFromSource(mapPaperToSource(paper), 2)...)
			if len(findings) >= workerEvidenceLimit(role) {
				findings = findings[:workerEvidenceLimit(role)]
				break
			}
		}
	}
	result.Findings = findings
	return result
}

func workerSearchQueryLimit(role ResearchWorkerRole) int {
	switch role {
	case ResearchWorkerScout:
		return 1
	case ResearchWorkerSourceDiversifier:
		return 3
	case ResearchWorkerCitationGraph:
		return 3
	case ResearchWorkerIndependentVerifier:
		return 0
	default:
		return 2
	}
}

func workerSearchParallelism(role ResearchWorkerRole) int {
	switch role {
	case ResearchWorkerSourceDiversifier:
		return 3
	case ResearchWorkerCitationGraph:
		return 2
	case ResearchWorkerScout:
		return 1
	default:
		return 2
	}
}

func workerSearchPaperLimit(role ResearchWorkerRole) int {
	switch role {
	case ResearchWorkerScout:
		return 4
	case ResearchWorkerSourceDiversifier:
		return 4
	case ResearchWorkerCitationGraph:
		return 4
	default:
		return 3
	}
}

func workerEvidenceLimit(role ResearchWorkerRole) int {
	switch role {
	case ResearchWorkerSourceDiversifier:
		return 8
	case ResearchWorkerCitationGraph:
		return 8
	default:
		return 6
	}
}

func workerSearchTimeout(role ResearchWorkerRole) time.Duration {
	switch role {
	case ResearchWorkerSourceDiversifier:
		return 25 * time.Second
	case ResearchWorkerCitationGraph:
		return 25 * time.Second
	case ResearchWorkerScout:
		return 20 * time.Second
	default:
		return 18 * time.Second
	}
}

func appendWorkerSearchWarnings(existing any, query string, warnings []search.ProviderWarning) []map[string]string {
	out, _ := existing.([]map[string]string)
	for _, warning := range warnings {
		out = append(out, map[string]string{
			"query":    strings.TrimSpace(query),
			"provider": strings.TrimSpace(warning.Provider),
			"message":  strings.TrimSpace(warning.Message),
		})
	}
	return out
}

func emitResearchWorkerProgress(
	emit func(PlanExecutionEvent),
	session *AgentSession,
	role ResearchWorkerRole,
	stage string,
	result researchWorkerExecution,
	metadata ...map[string]any,
) {
	if emit == nil {
		return
	}
	payload := map[string]any{
		"component":          "wisdev.runtime",
		"operation":          "research_worker",
		"stage":              strings.TrimSpace(stage),
		"role":               string(role),
		"status":             strings.TrimSpace(result.Status),
		"plannedQueryCount":  len(result.Queries),
		"executedQueryCount": len(result.ExecutedQueries),
		"paperCount":         len(result.Papers),
		"evidenceCount":      len(result.Evidence),
		"openLedgerCount":    countOpenCoverageLedgerEntries(result.Ledger),
	}
	if result.Err != nil {
		payload["error"] = result.Err.Error()
	}
	for _, extra := range metadata {
		for key, value := range extra {
			if strings.TrimSpace(key) != "" && value != nil {
				payload[key] = value
			}
		}
	}
	emit(PlanExecutionEvent{
		Type:               EventProgress,
		TraceID:            NewTraceID(),
		SessionID:          safeAgentSessionID(session),
		Message:            fmt.Sprintf("research worker %s %s", role, strings.TrimSpace(stage)),
		Payload:            payload,
		Owner:              "go",
		SubAgent:           string(role),
		OwningComponent:    "orchestrator/internal/wisdev",
		ResultOrigin:       "research_worker",
		ResultConfidence:   workerProgressConfidence(result),
		ResultFusionIntent: "blackboard_merge",
		CreatedAt:          NowMillis(),
	})
}

func workerProgressConfidence(result researchWorkerExecution) float64 {
	if result.Err != nil || strings.EqualFold(result.Status, "cancelled") {
		return 0.2
	}
	if len(result.Evidence) > 0 {
		return 0.78
	}
	if len(result.ExecutedQueries) > 0 {
		return 0.55
	}
	return 0.4
}

func safeAgentSessionID(session *AgentSession) string {
	if session == nil {
		return ""
	}
	return strings.TrimSpace(session.SessionID)
}

func buildWorkerCoverageLedger(result researchWorkerExecution) []CoverageLedgerEntry {
	if result.Status != "completed" {
		return nil
	}
	if result.Role == ResearchWorkerSynthesizer {
		return []CoverageLedgerEntry{{
			ID:                stableWisDevID("worker-ledger", string(result.Role), "synthesis-gate"),
			Category:          "worker_" + string(result.Role),
			Status:            coverageLedgerStatusResolved,
			Title:             "Synthesizer gated on closed evidence ledger",
			Description:       "The synthesizer is intentionally deferred until specialist evidence and coverage ledgers have been merged.",
			SupportingQueries: append([]string(nil), result.Queries...),
			Confidence:        0.72,
			ObligationType:    "coverage_gap",
			OwnerWorker:       string(result.Role),
			Severity:          "low",
		}}
	}
	if result.Role == ResearchWorkerIndependentVerifier {
		return []CoverageLedgerEntry{{
			ID:                stableWisDevID("worker-ledger", string(result.Role), "finalization-gate"),
			Category:          "worker_" + string(result.Role),
			Status:            coverageLedgerStatusResolved,
			Title:             "Independent verifier packet delegated to finalization gate",
			Description:       "The independent verifier emits its contract packet before the final claim-level verifier gate scores merged evidence and open ledgers.",
			SupportingQueries: append([]string(nil), result.Queries...),
			Confidence:        0.74,
			ObligationType:    "unverified_claim",
			OwnerWorker:       string(result.Role),
			Severity:          "low",
			ClosureEvidence:   []string{"finalization_gate_required"},
		}}
	}
	status := coverageLedgerStatusResolved
	title := fmt.Sprintf("%s worker produced grounded evidence", result.Role)
	description := fmt.Sprintf("%s worker executed %d query branch(es) and produced %d evidence item(s).", result.Role, len(result.ExecutedQueries), len(result.Evidence))
	confidence := 0.76
	if len(result.ExecutedQueries) > 0 && len(result.Evidence) == 0 {
		status = coverageLedgerStatusOpen
		title = fmt.Sprintf("%s worker needs follow-up evidence", result.Role)
		description = fmt.Sprintf("%s worker executed query branches without producing grounded evidence.", result.Role)
		confidence = 0.52
	}
	if len(result.ExecutedQueries) == 0 && result.Role != ResearchWorkerSynthesizer {
		status = coverageLedgerStatusOpen
		title = fmt.Sprintf("%s worker did not execute search", result.Role)
		description = fmt.Sprintf("%s worker only planned queries; no specialist search results were available.", result.Role)
		confidence = 0.48
	}

	entry := CoverageLedgerEntry{
		ID:                stableWisDevID("worker-ledger", string(result.Role), title),
		Category:          "worker_" + string(result.Role),
		Status:            status,
		Title:             title,
		Description:       description,
		SupportingQueries: append([]string(nil), result.Queries...),
		SourceFamilies:    buildObservedSourceFamiliesFromPapers(result.Papers),
		Confidence:        confidence,
	}

	entry.ObligationType = inferCoverageObligationType(entry)
	entry.OwnerWorker = string(result.Role)
	entry.Severity = inferCoverageObligationSeverity(entry)
	if status == coverageLedgerStatusOpen && entry.Severity == "low" {
		entry.Severity = "high"
	}
	if entry.ObligationType == "coverage_gap" {
		switch result.Role {
		case ResearchWorkerContradictionCritic:
			entry.ObligationType = "missing_counter_evidence"
			entry.OwnerWorker = string(ResearchWorkerContradictionCritic)
			entry.Severity = "critical"
		case ResearchWorkerSourceDiversifier:
			entry.ObligationType = "missing_source_diversity"
			entry.OwnerWorker = string(ResearchWorkerSourceDiversifier)
		case ResearchWorkerCitationVerifier, ResearchWorkerCitationGraph:
			entry.ObligationType = "missing_citation_identity"
			entry.OwnerWorker = string(ResearchWorkerCitationGraph)
			entry.Severity = "high"
		case ResearchWorkerScout:
			entry.ObligationType = "missing_population"
			entry.OwnerWorker = string(ResearchWorkerScout)
		}
	}
	return []CoverageLedgerEntry{entry}
}

func buildResearchWorkerQuery(query string, focus string) string {
	return normalizeAgendaFocus(query, focus)
}

func domainSpecificEvidenceQuery(domain string) string {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "medicine", "medical", "biomedicine", "clinical":
		return "clinical trial cohort guideline evidence"
	case "computer science", "cs", "machine learning", "ml":
		return "benchmark ablation reproducibility dataset"
	case "law", "legal":
		return "primary source precedent review"
	default:
		return "independent source triangulation evidence"
	}
}

func researchWorkersExecuted(workers []ResearchWorkerState) bool {
	for _, worker := range workers {
		if strings.EqualFold(strings.TrimSpace(worker.Status), "completed") {
			return true
		}
	}
	return false
}

func buildResearchBlackboard(workers []ResearchWorkerState) *ResearchBlackboard {
	board := &ResearchBlackboard{
		Contracts:       make(map[ResearchWorkerRole]ResearchWorkerContract),
		WorkerArtifacts: make(map[ResearchWorkerRole]map[string]any),
	}
	for _, worker := range workers {
		board.PlannedQueries = normalizeLoopQueries("", append(board.PlannedQueries, worker.PlannedQueries...))
		board.ExecutedQueries = normalizeLoopQueries("", append(board.ExecutedQueries, worker.ExecutedQueries...))
		board.Evidence = mergeEvidenceFindings(board.Evidence, worker.Evidence)
		board.CoverageLedger = mergeCoverageLedgerEntries(board.CoverageLedger, worker.CoverageLedger)
		if worker.Contract.Role != "" {
			board.Contracts[worker.Role] = worker.Contract
		}
		if len(worker.Artifacts) > 0 {
			board.WorkerArtifacts[worker.Role] = cloneAnyMap(worker.Artifacts)
			if graph, ok := worker.Artifacts["citationGraph"].(*ResearchCitationGraph); ok {
				board.CitationGraph = mergeResearchCitationGraphs(board.CitationGraph, graph)
			}
		}
	}
	if board.CitationGraph != nil {
		board.CoverageLedger = mergeCoverageLedgerEntries(board.CoverageLedger, board.CitationGraph.CoverageLedger)
	}
	if len(board.Contracts) == 0 {
		board.Contracts = nil
	}
	if len(board.WorkerArtifacts) == 0 {
		board.WorkerArtifacts = nil
	}
	board.OpenLedgerCount = countOpenCoverageLedgerEntries(board.CoverageLedger)
	if board.OpenLedgerCount == 0 && len(board.Evidence) > 0 {
		board.ReadyForSynthesis = true
		board.SynthesisGate = "ready: specialist evidence and coverage ledger are mergeable"
	} else if board.OpenLedgerCount == 0 {
		board.SynthesisGate = "blocked: no specialist evidence has been merged"
	} else {
		board.SynthesisGate = fmt.Sprintf("blocked: %d specialist coverage ledger item(s) remain open", board.OpenLedgerCount)
	}
	return board
}

func researchBlackboardOpenLedgerCount(board *ResearchBlackboard) int {
	if board == nil {
		return 0
	}
	if board.OpenLedgerCount > 0 {
		return board.OpenLedgerCount
	}
	return countOpenCoverageLedgerEntries(board.CoverageLedger)
}

func researchBlackboardSynthesisGate(board *ResearchBlackboard) string {
	if board == nil {
		return ""
	}
	return strings.TrimSpace(board.SynthesisGate)
}

func blackboardPapers(workers []ResearchWorkerState) []search.Paper {
	var papers []search.Paper
	for _, worker := range workers {
		papers = appendUniqueSearchPapers(papers, worker.Papers)
	}
	return papers
}

func blackboardQueryCoverage(workers []ResearchWorkerState) map[string][]search.Paper {
	coverage := make(map[string][]search.Paper)
	for _, worker := range workers {
		for query, papers := range worker.QueryCoverage {
			recordLoopQueryCoverage(coverage, query, papers)
		}
	}
	if len(coverage) == 0 {
		return nil
	}
	return coverage
}

func dedupeEvidenceFindings(findings []EvidenceFinding) []EvidenceFinding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]EvidenceFinding, 0, len(findings))
	seen := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		key := evidenceFindingDedupeKey(finding)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, finding)
	}
	return out
}

func evidenceFindingDedupeKey(finding EvidenceFinding) string {
	for _, candidate := range []string{
		strings.TrimSpace(finding.ID),
		strings.TrimSpace(finding.SourceID) + "|" + strings.TrimSpace(finding.Claim),
		strings.TrimSpace(finding.PaperTitle) + "|" + strings.TrimSpace(finding.Snippet),
	} {
		trimmed := strings.Trim(candidate, "| ")
		if trimmed == "" {
			continue
		}
		return strings.ToLower(trimmed)
	}
	return ""
}

func mergeCoverageLedgerEntries(primary []CoverageLedgerEntry, secondary []CoverageLedgerEntry) []CoverageLedgerEntry {
	merged := make([]CoverageLedgerEntry, 0, len(primary)+len(secondary))
	for _, entry := range primary {
		normalized := normalizeCoverageLedgerObligation(entry)
		merged = append(merged, normalized)
	}
	seen := make(map[string]struct{}, len(merged)+len(secondary))
	for _, entry := range merged {
		seen[strings.ToLower(strings.TrimSpace(firstNonEmpty(entry.ID, entry.Category+"|"+entry.Title)))] = struct{}{}
	}
	for _, entry := range secondary {
		entry = normalizeCoverageLedgerObligation(entry)
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(entry.ID, entry.Category+"|"+entry.Title)))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, entry)
	}
	return merged
}

func normalizeCoverageLedgerObligation(entry CoverageLedgerEntry) CoverageLedgerEntry {
	entry.Category = strings.TrimSpace(entry.Category)
	entry.Status = strings.TrimSpace(firstNonEmpty(entry.Status, coverageLedgerStatusOpen))
	entry.Title = strings.TrimSpace(entry.Title)
	entry.Description = strings.TrimSpace(entry.Description)
	entry.SupportingQueries = normalizeLoopQueries("", entry.SupportingQueries)
	entry.SourceFamilies = dedupeTrimmedStrings(entry.SourceFamilies)
	entry.ClosureEvidence = dedupeTrimmedStrings(entry.ClosureEvidence)
	if strings.TrimSpace(entry.ObligationType) == "" {
		entry.ObligationType = inferCoverageObligationType(entry)
	}
	if strings.TrimSpace(entry.OwnerWorker) == "" {
		entry.OwnerWorker = inferCoverageObligationOwner(entry)
	}
	if strings.TrimSpace(entry.Severity) == "" {
		entry.Severity = inferCoverageObligationSeverity(entry)
	}
	if len(entry.ClosureEvidence) == 0 && entry.Status == coverageLedgerStatusResolved {
		entry.ClosureEvidence = inferCoverageClosureEvidence(entry)
	}
	return entry
}

func inferCoverageObligationType(entry CoverageLedgerEntry) string {
	category := strings.ToLower(strings.TrimSpace(entry.Category))
	status := strings.ToLower(strings.TrimSpace(entry.Status))
	text := strings.ToLower(strings.Join([]string{entry.Category, entry.Title, entry.Description}, " "))
	switch {
	case strings.Contains(category, "contradiction"):
		return "missing_counter_evidence"
	case strings.Contains(category, "population") || strings.Contains(category, "population_gap") || strings.Contains(category, "query_coverage") || strings.Contains(category, "planned_query") || strings.Contains(category, "coverage_gap"):
		return "missing_population"
	case strings.Contains(category, "source_diversity") || strings.Contains(category, "source diversity"):
		return "missing_source_diversity"
	case strings.Contains(category, "citation"):
		return "missing_citation_identity"
	case strings.Contains(category, "claim") && status != coverageLedgerStatusResolved:
		return "unverified_claim"
	case strings.Contains(text, "counter") || strings.Contains(text, "contradiction") || strings.Contains(text, "failed replication") || strings.Contains(text, "null result"):
		return "missing_counter_evidence"
	case strings.Contains(text, "replication") || strings.Contains(text, "benchmark") || strings.Contains(text, "reproduc") || strings.Contains(text, "validation"):
		return "missing_replication"
	case strings.Contains(text, "population") || strings.Contains(text, "cohort") || strings.Contains(text, "demographics"):
		return "missing_population"
	case strings.Contains(text, "source diversity") || strings.Contains(text, "source_diversity") || strings.Contains(text, "independent source") || strings.Contains(text, "triangulat"):
		return "missing_source_diversity"
	case strings.Contains(text, "full text") || strings.Contains(text, "pdf") || strings.Contains(text, "source acquisition") || strings.Contains(text, "fetch"):
		return "missing_full_text"
	case strings.Contains(text, "citation") || strings.Contains(text, "doi") || strings.Contains(text, "arxiv") || strings.Contains(text, "pubmed") || strings.Contains(text, "openalex") || strings.Contains(text, "identity"):
		return "missing_citation_identity"
	case strings.Contains(text, "claim") || strings.Contains(text, "evidence"):
		return "unverified_claim"
	case strings.Contains(text, "budget") || strings.Contains(text, "exhaust"):
		return "budget_exhausted"
	default:
		return "coverage_gap"
	}
}

func inferCoverageObligationOwner(entry CoverageLedgerEntry) string {
	category := strings.ToLower(strings.TrimSpace(entry.Category))
	obligationType := strings.ToLower(strings.TrimSpace(entry.ObligationType))
	switch {
	case strings.Contains(category, string(ResearchWorkerCitationGraph)) || strings.Contains(category, "citation_graph") || strings.Contains(category, "citation_identity") || obligationType == "missing_citation_identity":
		return string(ResearchWorkerCitationGraph)
	case strings.Contains(category, string(ResearchWorkerCitationVerifier)):
		return string(ResearchWorkerCitationVerifier)
	case strings.Contains(category, "source") || obligationType == "missing_source_diversity" || obligationType == "missing_full_text" || obligationType == "missing_population":
		return string(ResearchWorkerSourceDiversifier)
	case strings.Contains(category, "contradiction") || obligationType == "missing_counter_evidence" || obligationType == "missing_replication":
		return string(ResearchWorkerContradictionCritic)
	case strings.Contains(category, "claim") || obligationType == "unverified_claim":
		return string(ResearchWorkerIndependentVerifier)
	case strings.Contains(category, "hypothesis") || strings.Contains(category, "branch"):
		return string(ResearchWorkerScout)
	default:
		return string(ResearchWorkerScout)
	}
}

func inferCoverageObligationSeverity(entry CoverageLedgerEntry) string {
	if entry.Required || entry.Priority >= 95 {
		return "critical"
	}
	if entry.Priority >= 80 || entry.Status == coverageLedgerStatusOpen {
		return "high"
	}
	if entry.Priority >= 50 {
		return "medium"
	}
	return "low"
}

func inferCoverageClosureEvidence(entry CoverageLedgerEntry) []string {
	out := []string{}
	if len(entry.SourceFamilies) > 0 {
		out = append(out, "source_families:"+strings.Join(entry.SourceFamilies, ","))
	}
	if entry.Confidence > 0 {
		out = append(out, fmt.Sprintf("confidence:%.2f", entry.Confidence))
	}
	if len(entry.SupportingQueries) > 0 {
		out = append(out, "queries:"+strings.Join(entry.SupportingQueries, " | "))
	}
	if len(out) == 0 {
		out = append(out, firstNonEmpty(entry.Description, entry.Title, "resolved"))
	}
	return dedupeTrimmedStrings(out)
}
