package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

type ResearchExecutionPlane string

const (
	ResearchExecutionPlaneSimple     ResearchExecutionPlane = "simple"
	ResearchExecutionPlaneAutonomous ResearchExecutionPlane = "autonomous"
	ResearchExecutionPlaneDeep       ResearchExecutionPlane = "deep"
	ResearchExecutionPlaneMultiAgent ResearchExecutionPlane = "multi_agent"
	ResearchExecutionPlaneQuest      ResearchExecutionPlane = "quest"
	ResearchExecutionPlaneJob        ResearchExecutionPlane = "job"
)

type ResearchWorkerRole string

const (
	ResearchWorkerScout               ResearchWorkerRole = "scout"
	ResearchWorkerSourceDiversifier   ResearchWorkerRole = "source_diversifier"
	ResearchWorkerCitationVerifier    ResearchWorkerRole = "citation_verifier"
	ResearchWorkerCitationGraph       ResearchWorkerRole = "citation_graph"
	ResearchWorkerContradictionCritic ResearchWorkerRole = "contradiction_critic"
	ResearchWorkerIndependentVerifier ResearchWorkerRole = "independent_verifier"
	ResearchWorkerSynthesizer         ResearchWorkerRole = "synthesizer"
)

type ResearchWorkerContract struct {
	Role             ResearchWorkerRole `json:"role"`
	Objective        string             `json:"objective"`
	AllowedTools     []string           `json:"allowedTools,omitempty"`
	RequiredOutputs  []string           `json:"requiredOutputs,omitempty"`
	RequiredEvidence []string           `json:"requiredEvidence,omitempty"`
	MustNotDecide    []string           `json:"mustNotDecide,omitempty"`
	CompletionGate   string             `json:"completionGate,omitempty"`
	OutputSchema     []string           `json:"outputSchema,omitempty"`
	ConfidenceFloor  float64            `json:"confidenceFloor,omitempty"`
	MaxSearches      int                `json:"maxSearches,omitempty"`
	StopReasons      []string           `json:"stopReasons,omitempty"`
}

type ResearchWorkerState struct {
	Role            ResearchWorkerRole        `json:"role"`
	Status          string                    `json:"status"`
	Contract        ResearchWorkerContract    `json:"contract,omitempty"`
	PlannedQueries  []string                  `json:"plannedQueries,omitempty"`
	ExecutedQueries []string                  `json:"executedQueries,omitempty"`
	Papers          []search.Paper            `json:"papers,omitempty"`
	QueryCoverage   map[string][]search.Paper `json:"queryCoverage,omitempty"`
	Evidence        []EvidenceFinding         `json:"evidence,omitempty"`
	CoverageLedger  []CoverageLedgerEntry     `json:"coverageLedger,omitempty"`
	Artifacts       map[string]any            `json:"artifacts,omitempty"`
	StartedAt       int64                     `json:"startedAt,omitempty"`
	FinishedAt      int64                     `json:"finishedAt,omitempty"`
	Notes           []string                  `json:"notes,omitempty"`
}

type ResearchBlackboard struct {
	PlannedQueries    []string                                      `json:"plannedQueries,omitempty"`
	ExecutedQueries   []string                                      `json:"executedQueries,omitempty"`
	Evidence          []EvidenceFinding                             `json:"evidence,omitempty"`
	CoverageLedger    []CoverageLedgerEntry                         `json:"coverageLedger,omitempty"`
	BranchEvaluations []ResearchBranchEvaluation                    `json:"branchEvaluations,omitempty"`
	BranchPlans       []ResearchBranchPlan                          `json:"branchPlans,omitempty"`
	CitationGraph     *ResearchCitationGraph                        `json:"citationGraph,omitempty"`
	Contracts         map[ResearchWorkerRole]ResearchWorkerContract `json:"contracts,omitempty"`
	WorkerArtifacts   map[ResearchWorkerRole]map[string]any         `json:"workerArtifacts,omitempty"`
	Arbitration       *ResearchArbitrationState                     `json:"arbitration,omitempty"`
	ReadyForSynthesis bool                                          `json:"readyForSynthesis"`
	OpenLedgerCount   int                                           `json:"openLedgerCount"`
	SynthesisGate     string                                        `json:"synthesisGate,omitempty"`
}

type ResearchBranchEvaluation struct {
	ID                      string            `json:"id"`
	Query                   string            `json:"query"`
	Status                  string            `json:"status"`
	Hypothesis              string            `json:"hypothesis,omitempty"`
	FalsifiabilityCondition string            `json:"falsifiabilityCondition,omitempty"`
	PlannedQueries          []string          `json:"plannedQueries,omitempty"`
	ExecutedQueries         []string          `json:"executedQueries,omitempty"`
	SourceFamilies          []string          `json:"sourceFamilies,omitempty"`
	Evidence                []EvidenceFinding `json:"evidence,omitempty"`
	NoveltyScore            float64           `json:"noveltyScore"`
	CoverageScore           float64           `json:"coverageScore"`
	FalsifiabilityScore     float64           `json:"falsifiabilityScore"`
	EvidenceScore           float64           `json:"evidenceScore"`
	OverallScore            float64           `json:"overallScore"`
	BranchScore             float64           `json:"branchScore,omitempty"`
	VerifierVerdict         string            `json:"verifierVerdict,omitempty"`
	IdempotencyKey          string            `json:"idempotencyKey,omitempty"`
	CheckpointKey           string            `json:"checkpointKey,omitempty"`
	Contradictions          []string          `json:"contradictions,omitempty"`
	SourceIDs               []string          `json:"sourceIds,omitempty"`
	OpenGaps                []string          `json:"openGaps,omitempty"`
	StopReason              string            `json:"stopReason,omitempty"`
}

type ResearchArbitrationState struct {
	Verdict          string   `json:"verdict"`
	ForcedRevision   bool     `json:"forcedRevision"`
	Abstain          bool     `json:"abstain"`
	PromotedClaimIDs []string `json:"promotedClaimIds,omitempty"`
	RejectedClaimIDs []string `json:"rejectedClaimIds,omitempty"`
	Reasons          []string `json:"reasons,omitempty"`
	MergeRules       []string `json:"mergeRules,omitempty"`
}

type ResearchBudgetDecision struct {
	SearchTermBudget        int      `json:"searchTermBudget"`
	WorkerSearchBudget      int      `json:"workerSearchBudget"`
	ReservedLoopSearches    int      `json:"reservedLoopSearches"`
	RecursiveGapDepth       int      `json:"recursiveGapDepth"`
	CritiqueFollowUpLimit   int      `json:"critiqueFollowUpLimit"`
	QueryParallelism        int      `json:"queryParallelism"`
	FollowUpSearchBudget    int      `json:"followUpSearchBudget,omitempty"`
	CoveragePressure        int      `json:"coveragePressure,omitempty"`
	SourceDiversityPressure int      `json:"sourceDiversityPressure,omitempty"`
	ContradictionPressure   int      `json:"contradictionPressure,omitempty"`
	ClaimCriticality        int      `json:"claimCriticality,omitempty"`
	Rationale               []string `json:"rationale,omitempty"`
}

type ClaimVerificationLedger struct {
	Query                   string                    `json:"query"`
	Records                 []ClaimVerificationRecord `json:"records,omitempty"`
	SupportedClaims         int                       `json:"supportedClaims"`
	OpenClaims              int                       `json:"openClaims"`
	ContradictedClaims      int                       `json:"contradictedClaims"`
	CitationHealthScore     float64                   `json:"citationHealthScore"`
	SourceFamilyCoverage    float64                   `json:"sourceFamilyCoverage"`
	CalibratedConfidence    float64                   `json:"calibratedConfidence"`
	StopReason              string                    `json:"stopReason"`
	RejectedCitationCount   int                       `json:"rejectedCitationCount,omitempty"`
	RequiredFollowUpQueries []string                  `json:"requiredFollowUpQueries,omitempty"`
	ObservedSourceFamilies  []string                  `json:"observedSourceFamilies,omitempty"`
}

type ResearchVerifierDecision struct {
	Role             ResearchWorkerRole `json:"role"`
	Verdict          string             `json:"verdict"`
	StopReason       string             `json:"stopReason,omitempty"`
	PromotedClaimIDs []string           `json:"promotedClaimIds,omitempty"`
	RejectedClaimIDs []string           `json:"rejectedClaimIds,omitempty"`
	RevisionReasons  []string           `json:"revisionReasons,omitempty"`
	Confidence       float64            `json:"confidence"`
	EvidenceOnly     bool               `json:"evidenceOnly"`
}

type ClaimVerificationRecord struct {
	ID                  string              `json:"id"`
	Claim               string              `json:"claim"`
	Status              string              `json:"status"`
	Confidence          float64             `json:"confidence"`
	SupportCount        int                 `json:"supportCount"`
	SourceIDs           []string            `json:"sourceIds,omitempty"`
	SourceFamilies      []string            `json:"sourceFamilies,omitempty"`
	EvidenceSpans       []ClaimEvidenceSpan `json:"evidenceSpans,omitempty"`
	CitationHealth      string              `json:"citationHealth"`
	ContradictionStatus string              `json:"contradictionStatus"`
	StopReason          string              `json:"stopReason,omitempty"`
	FollowUpQueries     []string            `json:"followUpQueries,omitempty"`
}

type ClaimEvidenceSpan struct {
	SourceID    string  `json:"sourceId"`
	SourceTitle string  `json:"sourceTitle,omitempty"`
	Snippet     string  `json:"snippet,omitempty"`
	Confidence  float64 `json:"confidence"`
}

type ResearchSessionState struct {
	SessionID         string                         `json:"sessionId"`
	Query             string                         `json:"query"`
	Domain            string                         `json:"domain,omitempty"`
	Plane             ResearchExecutionPlane         `json:"plane"`
	Budget            *ResearchBudgetDecision        `json:"budget,omitempty"`
	PlannedQueries    []string                       `json:"plannedQueries,omitempty"`
	ExecutedQueries   []string                       `json:"executedQueries,omitempty"`
	CoverageLedger    []CoverageLedgerEntry          `json:"coverageLedger,omitempty"`
	BranchEvaluations []ResearchBranchEvaluation     `json:"branchEvaluations,omitempty"`
	BranchPlans       []ResearchBranchPlan           `json:"branchPlans,omitempty"`
	CitationGraph     *ResearchCitationGraph         `json:"citationGraph,omitempty"`
	ClaimVerification *ClaimVerificationLedger       `json:"claimVerification,omitempty"`
	VerifierDecision  *ResearchVerifierDecision      `json:"verifierDecision,omitempty"`
	DurableJob        *ResearchDurableJobState       `json:"durableJob,omitempty"`
	DurableTasks      []ResearchDurableTaskState     `json:"durableTasks,omitempty"`
	SourceAcquisition *ResearchSourceAcquisitionPlan `json:"sourceAcquisition,omitempty"`
	ReasoningGraph    *ReasoningGraph                `json:"reasoningGraph,omitempty"`
	Workers           []ResearchWorkerState          `json:"workers,omitempty"`
	Blackboard        *ResearchBlackboard            `json:"blackboard,omitempty"`
	ObservedSources   []string                       `json:"observedSourceFamilies,omitempty"`
	CritiqueReasoning string                         `json:"critiqueReasoning,omitempty"`
	StopReason        string                         `json:"stopReason,omitempty"`

	DisableProgrammaticPlanning bool `json:"disableProgrammaticPlanning,omitempty"`
	DisableHypothesisGeneration bool `json:"disableHypothesisGeneration,omitempty"`
}

type UnifiedResearchResult struct {
	LoopResult *LoopResult           `json:"loopResult"`
	State      *ResearchSessionState `json:"state,omitempty"`
}

type UnifiedResearchRequest struct {
	Query           string
	Domain          string
	ProjectID       string
	SeedQueries     []string
	MaxIterations   int
	MaxSearchTerms  int
	HitsPerSearch   int
	MaxUniquePapers int
	AllocatedTokens int
	BudgetCents     int
	Mode            string
	Plane           ResearchExecutionPlane
}

type UnifiedResearchRuntime struct {
	loop       *AutonomousLoop
	searchReg  *search.ProviderRegistry
	llmClient  *llm.Client
	exec       func(context.Context, string, map[string]any, *AgentSession) (map[string]any, error)
	stateStore *RuntimeStateStore
	journal    *RuntimeJournal
	adkRuntime *ADKRuntime
}

func NewUnifiedResearchRuntime(
	loop *AutonomousLoop,
	searchReg *search.ProviderRegistry,
	llmClient *llm.Client,
	exec func(context.Context, string, map[string]any, *AgentSession) (map[string]any, error),
	adkRuntime ...*ADKRuntime,
) *UnifiedResearchRuntime {
	rt := &UnifiedResearchRuntime{
		loop:      loop,
		searchReg: searchReg,
		llmClient: llmClient,
		exec:      exec,
	}
	if len(adkRuntime) > 0 {
		rt.adkRuntime = adkRuntime[0]
	}
	return rt
}

func (rt *UnifiedResearchRuntime) RunLoop(
	ctx context.Context,
	req LoopRequest,
	plane ResearchExecutionPlane,
	onEvent func(PlanExecutionEvent),
) (*UnifiedResearchResult, error) {
	if rt == nil || rt.loop == nil {
		return nil, fmt.Errorf("unified research runtime is not initialized")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	state := newResearchSessionState(query, req.Domain, firstNonEmpty(req.ProjectID, NewTraceID()), plane)
	state.DisableProgrammaticPlanning = req.DisableProgrammaticPlanning
	state.DisableHypothesisGeneration = req.DisableHypothesisGeneration
	state.Budget = buildResearchBudgetDecision(req, plane)
	state.DurableJob = newResearchDurableJobState(state, req)
	session := &AgentSession{
		SessionID:      state.SessionID,
		Query:          query,
		CorrectedQuery: query,
		DetectedDomain: strings.TrimSpace(req.Domain),
		Mode:           NormalizeWisDevMode(req.Mode),
		ServiceTier:    ResolveServiceTier(NormalizeWisDevMode(req.Mode), false),
		MemoryTiers:    &MemoryTierState{},
	}

	seedQueries := normalizeLoopQueries(query, req.SeedQueries)
	emitRuntimeLifecycleEvent(onEvent, state, "runtime_started", "unified research runtime started", map[string]any{
		"plane":            plane,
		"budget":           state.Budget,
		"seedQueries":      seedQueries,
		"reasoningRuntime": BuildResearchReasoningRuntimeMetadata(req, plane, state.Budget),
	}, 0.62)
	if state.DurableJob != nil {
		emitRuntimeLifecycleEvent(onEvent, state, "research_job_started", "durable research job started", map[string]any{
			"job": state.DurableJob,
		}, 0.72)
		if err := rt.persistResearchDurableJob(state, "research_job_started"); err != nil {
			emitRuntimeLifecycleEvent(onEvent, state, "research_job_persist_failed", "durable research job start could not be persisted", map[string]any{
				"jobId": state.DurableJob.JobID,
				"error": err.Error(),
			}, 0.28)
		}
	}
	workerQueries := rt.executeResearchWorkers(ctx, state, session, query, req.Domain, req.Mode, !req.DisableProgrammaticPlanning, state.Budget.WorkerSearchBudget, onEvent)
	blackboard := buildResearchBlackboard(state.Workers)
	state.Blackboard = blackboard
	seedQueries = normalizeLoopQueries(query, append(seedQueries, workerQueries...))
	state.PlannedQueries = append([]string(nil), seedQueries...)
	state.ReasoningGraph = initializeLiveReasoningGraph(state, seedQueries)
	emitRuntimeBranchEvents(onEvent, state, seedQueries)

	loopReq := req
	loopReq.ResearchPlane = plane
	loopReq.EnableDynamicProviderSelection = shouldUseDynamicProviderSelection(req.Mode, plane, true, rt.llmClient)
	loopReq.SeedQueries = seedQueries
	loopReq.InitialPapers = blackboardPapers(state.Workers)
	loopReq.InitialExecutedQueries = append([]string(nil), blackboard.ExecutedQueries...)
	loopReq.InitialQueryCoverage = blackboardQueryCoverage(state.Workers)
	loopReq.SteeringJournal = rt.journal
	loopResult, err := rt.loop.Run(ctx, loopReq, onEvent)
	if err != nil {
		failResearchDurableJob(state.DurableJob, err, ctx.Err() != nil)
		if persistErr := rt.persistResearchDurableJob(state, "research_job_failed"); persistErr != nil {
			emitRuntimeLifecycleEvent(onEvent, state, "research_job_persist_failed", "durable research job failure could not be persisted", map[string]any{
				"jobId": state.DurableJob.JobID,
				"error": persistErr.Error(),
			}, 0.28)
		}
		return nil, err
	}
	loopResult.WorkerReports = append([]ResearchWorkerState(nil), state.Workers...)
	loopResult.Evidence = mergeEvidenceFindings(loopResult.Evidence, blackboard.Evidence)
	loopResult.GapAnalysis = mergeClaimEvidenceLedger(loopResult.GapAnalysis, query, loopResult.Evidence)
	if loopResult.GapAnalysis != nil {
		loopResult.GapAnalysis.Ledger = mergeCoverageLedgerEntries(loopResult.GapAnalysis.Ledger, blackboard.CoverageLedger)
	}
	state.SourceAcquisition = buildResearchSourceAcquisitionPlan(query, loopResult.Papers)
	loopResult.GapAnalysis = mergeSourceAcquisitionPlanIntoGap(query, loopResult.GapAnalysis, state.SourceAcquisition)

	if err := rt.finalizeLoopResultWithVerifier(ctx, loopReq, state, loopResult, blackboard, onEvent); err != nil {
		failResearchDurableJob(state.DurableJob, err, ctx.Err() != nil)
		if persistErr := rt.persistResearchDurableJob(state, "research_job_failed"); persistErr != nil {
			emitRuntimeLifecycleEvent(onEvent, state, "research_job_persist_failed", "durable research job failure could not be persisted", map[string]any{
				"jobId": state.DurableJob.JobID,
				"error": persistErr.Error(),
			}, 0.28)
		}
		return nil, err
	}
	// R4: Propagate lineage to session so persistent callers can read it.
	session.Lineage = loopResult.Lineage
	return &UnifiedResearchResult{
		LoopResult: loopResult,
		State:      state,
	}, nil
}

func (rt *UnifiedResearchRuntime) finalizeLoopResultWithVerifier(
	ctx context.Context,
	baseReq LoopRequest,
	state *ResearchSessionState,
	loopResult *LoopResult,
	specialistBlackboard *ResearchBlackboard,
	emit func(PlanExecutionEvent),
) error {
	if state == nil || loopResult == nil {
		return nil
	}
	query := strings.TrimSpace(state.Query)
	state.SourceAcquisition = buildResearchSourceAcquisitionPlan(query, loopResult.Papers)
	loopResult.GapAnalysis = mergeSourceAcquisitionPlanIntoGap(query, loopResult.GapAnalysis, state.SourceAcquisition)
	recordVerifierControlledState(state, loopResult, specialistBlackboard)
	finalizeResearchBudgetDecision(state.Budget, loopResult.GapAnalysis, state.ClaimVerification)

	followUpQueries := buildVerifierControlledFollowUpQueries(query, state.VerifierDecision, state.ClaimVerification, loopResult.GapAnalysis, state.BranchEvaluations, state.Budget)
	if verifierBlocksFinalAnswer(state.VerifierDecision) && len(followUpQueries) > 0 && rt != nil && rt.loop != nil {
		emitRuntimeLifecycleEvent(emit, state, "verifier_revision_started", "independent verifier forced a pre-final revision pass", map[string]any{
			"verdict":         state.VerifierDecision.Verdict,
			"stopReason":      state.VerifierDecision.StopReason,
			"followUpQueries": followUpQueries,
		}, state.VerifierDecision.Confidence)

		revisionReq := buildVerifierRevisionLoopRequest(baseReq, loopResult, followUpQueries, state.Budget)
		revised, err := rt.loop.Run(ctx, revisionReq, emit)
		if err != nil {
			if ctx.Err() != nil && !loopResultHasResearchMaterial(loopResult) {
				return err
			}
			stage := "verifier_revision_failed"
			message := "independent verifier revision pass failed; final answer will remain provisional"
			if ctx.Err() != nil {
				stage = "verifier_revision_cancelled"
				message = "independent verifier revision pass was cancelled after base results were available; preserving provisional answer"
				if state.VerifierDecision != nil {
					state.VerifierDecision.RevisionReasons = dedupeTrimmedStrings(append(state.VerifierDecision.RevisionReasons, "verifier revision cancelled after base loop completed"))
				}
			}
			emitRuntimeLifecycleEvent(emit, state, stage, message, map[string]any{
				"error":      err.Error(),
				"stopReason": decisionStopReason(state.VerifierDecision),
			}, 0.31)
		} else if revised != nil {
			prepareLoopResultForVerifier(query, revised, specialistBlackboard)
			*loopResult = *revised
			state.SourceAcquisition = buildResearchSourceAcquisitionPlan(query, loopResult.Papers)
			loopResult.GapAnalysis = mergeSourceAcquisitionPlanIntoGap(query, loopResult.GapAnalysis, state.SourceAcquisition)
			recordVerifierControlledState(state, loopResult, specialistBlackboard)
			finalizeResearchBudgetDecision(state.Budget, loopResult.GapAnalysis, state.ClaimVerification)
			emitRuntimeLifecycleEvent(emit, state, "verifier_revision_completed", "independent verifier revision pass completed before final answer emission", map[string]any{
				"verdict":     state.VerifierDecision.Verdict,
				"stopReason":  state.VerifierDecision.StopReason,
				"executed":    loopResult.ExecutedQueries,
				"paperCount":  len(loopResult.Papers),
				"claimLedger": state.ClaimVerification,
			}, decisionConfidence(state.VerifierDecision))
		}
	}

	enforceVerifierFinalAnswerGate(query, loopResult, state.VerifierDecision, emit, state)
	applyFinalizationGateToLoopResult(loopResult, state)
	state.ReasoningGraph = mergeLiveReasoningGraph(state.ReasoningGraph, loopResult.ReasoningGraph, loopResult)
	loopResult.ReasoningGraph = state.ReasoningGraph
	loopResult.WorkerReports = append([]ResearchWorkerState(nil), state.Workers...)
	loopResult.RuntimeState = state

	// R4: Build first-class provenance lineage after all evidence is merged.
	loopResult.Lineage = buildResearchLineage(state, loopResult)
	slog.Info("research lineage built",
		"claimCount", len(loopResult.Lineage.ClaimProvenance),
		"subqueryCount", len(loopResult.Lineage.ExecutedSubqueries),
		"evidenceCount", len(loopResult.Lineage.EvidenceToSource))

	completeResearchDurableJob(state.DurableJob, state, loopResult)
	if err := rt.persistResearchDurableJob(state, "research_job_completed"); err != nil {
		emitRuntimeLifecycleEvent(emit, state, "research_job_persist_failed", "durable research job completion could not be persisted", map[string]any{
			"jobId": state.DurableJob.JobID,
			"error": err.Error(),
		}, 0.28)
	}
	emitRuntimeLedgerEvents(emit, state, loopResult)
	return nil
}

func loopResultHasResearchMaterial(loopResult *LoopResult) bool {
	if loopResult == nil {
		return false
	}
	return strings.TrimSpace(loopResult.FinalAnswer) != "" ||
		len(loopResult.Papers) > 0 ||
		len(loopResult.Evidence) > 0 ||
		len(loopResult.ExecutedQueries) > 0
}

func prepareLoopResultForVerifier(query string, loopResult *LoopResult, specialistBlackboard *ResearchBlackboard) {
	if loopResult == nil {
		return
	}
	if specialistBlackboard != nil {
		loopResult.WorkerReports = nil
		loopResult.Evidence = mergeEvidenceFindings(loopResult.Evidence, specialistBlackboard.Evidence)
	}
	loopResult.GapAnalysis = mergeClaimEvidenceLedger(loopResult.GapAnalysis, query, loopResult.Evidence)
	if loopResult.GapAnalysis != nil && specialistBlackboard != nil {
		loopResult.GapAnalysis.Ledger = mergeCoverageLedgerEntries(loopResult.GapAnalysis.Ledger, specialistBlackboard.CoverageLedger)
	}
}

func recordVerifierControlledState(state *ResearchSessionState, loopResult *LoopResult, specialistBlackboard *ResearchBlackboard) {
	if state == nil || loopResult == nil {
		return
	}
	state.ExecutedQueries = append([]string(nil), loopResult.ExecutedQueries...)
	state.CoverageLedger = nil
	state.ObservedSources = nil
	state.CritiqueReasoning = ""
	if loopResult.GapAnalysis != nil {
		state.CoverageLedger = mergeCoverageLedgerEntries(loopResult.GapAnalysis.Ledger, nil)
		if specialistBlackboard != nil {
			state.CoverageLedger = mergeCoverageLedgerEntries(state.CoverageLedger, specialistBlackboard.CoverageLedger)
		}
		state.ObservedSources = append([]string(nil), loopResult.GapAnalysis.ObservedSourceFamilies...)
		state.CritiqueReasoning = strings.TrimSpace(loopResult.GapAnalysis.Reasoning)
	}
	if len(state.ObservedSources) == 0 {
		seen := make(map[string]bool)
		for _, p := range loopResult.Papers {
			if src := strings.TrimSpace(p.Source); src != "" && !seen[src] {
				seen[src] = true
				state.ObservedSources = append(state.ObservedSources, src)
			}
		}
	}
	state.ClaimVerification = buildClaimVerificationLedger(state.Query, loopResult.Evidence, loopResult.Papers, loopResult.GapAnalysis)
	state.BranchEvaluations = buildResearchBranchEvaluations(state.Query, state.PlannedQueries, state.ExecutedQueries, loopResult.QueryCoverage, loopResult.GapAnalysis)
	state.VerifierDecision = buildIndependentVerifierDecision(state.ClaimVerification, loopResult.GapAnalysis, state.BranchEvaluations)
	applyIndependentVerifierDecision(state, state.VerifierDecision)
	state.Blackboard = buildResearchBlackboard(state.Workers)
	attachRuntimeArbitrationToBlackboard(state.Blackboard, state.BranchEvaluations, state.VerifierDecision)
	state.StopReason = determineResearchStopReason(loopResult, state.ClaimVerification, state.VerifierDecision)
	loopResult.WorkerReports = append([]ResearchWorkerState(nil), state.Workers...)
}

func verifierBlocksFinalAnswer(decision *ResearchVerifierDecision) bool {
	return decision != nil && decision.Verdict != "promote"
}

func buildVerifierControlledFollowUpQueries(
	query string,
	decision *ResearchVerifierDecision,
	claims *ClaimVerificationLedger,
	gap *LoopGapState,
	branches []ResearchBranchEvaluation,
	budget *ResearchBudgetDecision,
) []string {
	if !verifierBlocksFinalAnswer(decision) {
		return nil
	}
	limit := 4
	if budget != nil && budget.FollowUpSearchBudget > 0 {
		limit = budget.FollowUpSearchBudget
	}
	candidatesByKey := make(map[string]verifierFollowUpCandidate, limit+8)
	ordinal := 0
	addCandidate := func(value string, score int) {
		for _, candidate := range normalizeLoopQueries("", []string{value}) {
			key := strings.ToLower(candidate)
			existing, exists := candidatesByKey[key]
			if exists && existing.Score >= score {
				continue
			}
			if !exists {
				existing.Ordinal = ordinal
				ordinal++
			}
			existing.Query = candidate
			existing.Score = score
			candidatesByKey[key] = existing
		}
	}
	if claims != nil {
		for _, candidate := range claims.RequiredFollowUpQueries {
			addCandidate(candidate, 1500+scoreFollowUpQueryText(candidate))
		}
		for _, record := range claims.Records {
			for _, candidate := range record.FollowUpQueries {
				addCandidate(candidate, scoreClaimFollowUpCandidate(record, candidate))
			}
		}
	}
	if gap != nil {
		for _, entry := range gap.Ledger {
			if !strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
				continue
			}
			score := scoreCoverageLedgerFollowUpCandidate(entry)
			for _, candidate := range entry.SupportingQueries {
				addCandidate(candidate, score+25)
			}
			if len(entry.SupportingQueries) == 0 {
				for _, candidate := range derivedVerifierLedgerFollowUpQueries(query, entry) {
					addCandidate(candidate, score)
				}
			}
		}
		for _, candidate := range gap.NextQueries {
			addCandidate(candidate, 540+scoreFollowUpQueryText(candidate))
		}
	}
	for _, branch := range branches {
		if len(branch.OpenGaps) == 0 && branch.StopReason != "branch_unexecuted" && branch.StopReason != "branch_needs_evidence" {
			continue
		}
		addCandidate(branch.Query, scoreBranchFollowUpCandidate(branch))
	}
	for _, reason := range decision.RevisionReasons {
		if strings.TrimSpace(reason) == "" {
			continue
		}
		addCandidate(buildResearchWorkerQuery(query, reason), 500+scoreFollowUpQueryText(reason))
	}
	ranked := make([]verifierFollowUpCandidate, 0, len(candidatesByKey))
	for _, candidate := range candidatesByKey {
		ranked = append(ranked, candidate)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Ordinal < ranked[j].Ordinal
	})
	queries := make([]string, 0, minInt(limit, len(ranked)))
	for _, candidate := range ranked {
		if semanticallyRedundantLoopQuery(candidate.Query, queries, semanticGapDuplicateThreshold) {
			continue
		}
		queries = append(queries, candidate.Query)
		if len(queries) >= limit {
			break
		}
	}
	if len(queries) > limit {
		return queries[:limit]
	}
	return queries
}

type verifierFollowUpCandidate struct {
	Query   string
	Score   int
	Ordinal int
}

func scoreCoverageLedgerFollowUpCandidate(entry CoverageLedgerEntry) int {
	score := 620 + int(ledgerInformationGainScore(entry)*100)
	if entry.Priority > 0 {
		score += minInt(entry.Priority, 120)
	}
	if entry.Required {
		score += 80
	}
	switch strings.ToLower(strings.TrimSpace(entry.Category)) {
	case "contradiction":
		score += 180
	case "citation_integrity":
		score += 160
	case "source_acquisition", "full_text_fetch", "source_fetch", "source_identity":
		score += 150
	case "hypothesis_branch":
		score += 130
	case "source_diversity", "claim_source_diversity":
		score += 110
	case "coverage_rubric":
		score += 100
	case "claim_evidence":
		score += 95
	case "citation_gate":
		score += 80
	case "query_coverage", "planned_query":
		score += 55
	}
	return score
}

func scoreClaimFollowUpCandidate(record ClaimVerificationRecord, query string) int {
	score := 760 + scoreFollowUpQueryText(query)
	if record.ContradictionStatus == "open" || record.Status == "contradicted" {
		score += 260
	}
	if !strings.EqualFold(strings.TrimSpace(record.CitationHealth), "healthy") {
		score += 180
	}
	if !strings.EqualFold(strings.TrimSpace(record.Status), "supported") {
		score += 140
	}
	if record.SupportCount <= 1 {
		score += 60
	}
	if len(record.SourceFamilies) < 2 {
		score += 50
	}
	if record.Confidence > 0 && record.Confidence < 0.55 {
		score += 40
	}
	stopReason := strings.ToLower(strings.TrimSpace(record.StopReason))
	switch {
	case strings.Contains(stopReason, "contradiction"):
		score += 100
	case strings.Contains(stopReason, "citation"):
		score += 80
	case strings.Contains(stopReason, "source"):
		score += 70
	}
	return score
}

func scoreBranchFollowUpCandidate(branch ResearchBranchEvaluation) int {
	score := 660 + scoreFollowUpQueryText(branch.Query)
	if len(branch.OpenGaps) > 0 {
		score += 120
	}
	switch strings.TrimSpace(branch.StopReason) {
	case "branch_needs_evidence":
		score += 110
	case "branch_unexecuted":
		score += 90
	}
	return score
}

func scoreFollowUpQueryText(value string) int {
	lower := strings.ToLower(strings.TrimSpace(value))
	score := 0
	if strings.Contains(lower, "contradict") || strings.Contains(lower, "counter") || strings.Contains(lower, "falsif") {
		score += 160
	}
	if strings.Contains(lower, "full text") || strings.Contains(lower, "pdf") || strings.Contains(lower, "open access") || strings.Contains(lower, "unpaywall") || strings.Contains(lower, "source acquisition") {
		score += 130
	}
	if strings.Contains(lower, "doi") || strings.Contains(lower, "arxiv") || strings.Contains(lower, "pubmed") || strings.Contains(lower, "pmc") || strings.Contains(lower, "citation metadata") {
		score += 120
	}
	if strings.Contains(lower, "independent") || strings.Contains(lower, "replication") || strings.Contains(lower, "systematic review") || strings.Contains(lower, "source family") || strings.Contains(lower, "cohort") {
		score += 90
	}
	if strings.Contains(lower, "benchmark") || strings.Contains(lower, "dataset") || strings.Contains(lower, "ablation") {
		score += 60
	}
	return score
}

func derivedVerifierLedgerFollowUpQueries(query string, entry CoverageLedgerEntry) []string {
	baseQuery := strings.TrimSpace(firstNonEmpty(query, "research question"))
	switch strings.ToLower(strings.TrimSpace(entry.Category)) {
	case "coverage_rubric":
		if gapTerms := summarizeLoopGapTerms(firstNonEmpty(entry.Title, entry.Description)); gapTerms != "" {
			return []string{strings.TrimSpace(baseQuery + " " + gapTerms)}
		}
	case "citation_integrity":
		return []string{strings.TrimSpace(baseQuery + " DOI arXiv OpenAlex Semantic Scholar citation metadata")}
	case "source_diversity", "claim_source_diversity":
		return []string{strings.TrimSpace(baseQuery + " independent replication systematic review")}
	case "source_acquisition", "full_text_fetch":
		return []string{strings.TrimSpace(baseQuery + " open access PDF full text source acquisition")}
	case "source_fetch", "source_identity":
		return []string{strings.TrimSpace(baseQuery + " DOI arXiv PubMed PMC source identifiers full text")}
	case "contradiction":
		return []string{strings.TrimSpace(baseQuery + " contradictory evidence replication")}
	case "hypothesis_branch":
		return []string{strings.TrimSpace(baseQuery + " branch evidence independent support")}
	case "citation_gate":
		return []string{strings.TrimSpace(baseQuery + " DOI citation metadata")}
	default:
		if coverageLedgerEntryIsGenericValidationCheckpoint(entry) {
			return []string{strings.TrimSpace(baseQuery + " independent corroborating evidence")}
		}
		if gapTerms := summarizeLoopGapTerms(firstNonEmpty(entry.Description, entry.Title)); gapTerms != "" {
			return []string{strings.TrimSpace(baseQuery + " " + gapTerms)}
		}
	}
	return nil
}

func buildVerifierRevisionLoopRequest(baseReq LoopRequest, loopResult *LoopResult, followUpQueries []string, budget *ResearchBudgetDecision) LoopRequest {
	req := baseReq
	combinedSeeds := append(
		normalizeLoopQueries("", baseReq.SeedQueries),
		followUpQueries...,
	)
	req.SeedQueries = normalizeLoopQueries(baseReq.Query, combinedSeeds)
	if loopResult != nil {
		req.InitialPapers = append([]search.Paper(nil), loopResult.Papers...)
		req.InitialExecutedQueries = append([]string(nil), loopResult.ExecutedQueries...)
		req.InitialQueryCoverage = cloneLoopQueryCoverage(loopResult.QueryCoverage)
	}
	additionalSearches := len(req.SeedQueries)
	if additionalSearches > 0 && strings.EqualFold(strings.TrimSpace(req.SeedQueries[0]), strings.TrimSpace(baseReq.Query)) {
		additionalSearches--
	}
	if budget != nil && budget.FollowUpSearchBudget > 0 {
		additionalSearches = minInt(maxInt(additionalSearches, 1), budget.FollowUpSearchBudget)
	}
	additionalSearches = maxInt(additionalSearches, 1)

	followupQueryCap := resolveCritiqueFollowUpLimit(req.Mode, req.ResearchPlane)
	if budget != nil && budget.CritiqueFollowUpLimit > 0 {
		followupQueryCap = budget.CritiqueFollowUpLimit
	}
	additionalSearches = minInt(additionalSearches, maxInt(followupQueryCap, 1))

	paperCount := 0
	if loopResult != nil {
		paperCount = len(loopResult.Papers)
	}
	baseSearchBudget := resolveLoopSearchTermBudget(baseReq.MaxIterations, baseReq.MaxSearchTerms)
	req.MaxSearchTerms = baseSearchBudget + additionalSearches
	req.MaxIterations = maxInt(baseReq.MaxIterations, additionalSearches)
	if req.MaxUniquePapers > 0 && req.MaxUniquePapers <= paperCount {
		req.MaxUniquePapers = paperCount + additionalSearches*resolveLoopHitsPerSearch(req.HitsPerSearch)
	}
	req.DisableProgrammaticPlanning = true
	return req
}

func enforceVerifierFinalAnswerGate(query string, loopResult *LoopResult, decision *ResearchVerifierDecision, emit func(PlanExecutionEvent), state *ResearchSessionState) {
	if loopResult == nil || !verifierBlocksFinalAnswer(decision) {
		return
	}
	loopResult.FinalAnswer = buildVerifierProvisionalAnswer(query, loopResult.FinalAnswer, decision)
	emitRuntimeLifecycleEvent(emit, state, "final_answer_provisional", "independent verifier blocked final answer promotion", map[string]any{
		"verdict":         decision.Verdict,
		"stopReason":      decision.StopReason,
		"revisionReasons": decision.RevisionReasons,
	}, decision.Confidence)
}

func buildVerifierProvisionalAnswer(query string, draft string, decision *ResearchVerifierDecision) string {
	if decision == nil || decision.Verdict == "promote" {
		return strings.TrimSpace(draft)
	}
	reasons := dedupeTrimmedStrings(decision.RevisionReasons)
	if len(reasons) == 0 {
		reasons = []string{firstNonEmpty(decision.StopReason, "independent verifier did not promote the synthesis")}
	}
	if len(reasons) > 3 {
		reasons = reasons[:3]
	}
	body := strings.TrimSpace(draft)
	if strings.HasPrefix(strings.ToLower(body), "provisional research synthesis:") {
		return body
	}
	if body == "" {
		body = "No synthesis is available until the verifier gaps are resolved."
	}
	return fmt.Sprintf("Provisional research synthesis: the independent verifier did not promote a final answer for %q. Blocking reason: %s.\n\nDraft below is not final and should be treated as requiring more evidence:\n\n%s", strings.TrimSpace(query), strings.Join(reasons, "; "), body)
}

func (rt *UnifiedResearchRuntime) RunAnswer(ctx context.Context, req UnifiedResearchRequest) (*rag.AnswerResponse, error) {
	startedAt := time.Now()
	loopReq := LoopRequest{
		Query:                       req.Query,
		SeedQueries:                 req.SeedQueries,
		Domain:                      req.Domain,
		ProjectID:                   req.ProjectID,
		MaxIterations:               req.MaxIterations,
		MaxSearchTerms:              req.MaxSearchTerms,
		HitsPerSearch:               req.HitsPerSearch,
		MaxUniquePapers:             req.MaxUniquePapers,
		AllocatedTokens:             req.AllocatedTokens,
		BudgetCents:                 req.BudgetCents,
		Mode:                        req.Mode,
		DisableProgrammaticPlanning: false,
		DisableHypothesisGeneration: false,
	}
	looped, err := rt.RunLoop(ctx, loopReq, req.Plane, nil)
	if err != nil {
		return nil, err
	}

	loopResult := looped.LoopResult
	return &rag.AnswerResponse{
		Query:     strings.TrimSpace(req.Query),
		Answer:    loopResult.FinalAnswer,
		Papers:    loopResult.Papers,
		Citations: buildRuntimeCitations(loopResult),
		Timing: rag.AnswerTiming{
			TotalMs:     time.Since(startedAt).Milliseconds(),
			RetrievalMs: time.Since(startedAt).Milliseconds(),
			SynthesisMs: 0,
		},
		TraceID: NewTraceID(),
		Metadata: &rag.ResponseMetadata{
			Backend:           "go-wisdev-canonical-runtime",
			FallbackTriggered: false,
			QueryUsed:         strings.TrimSpace(req.Query),
			Policy: map[string]any{
				"executionPlane":        "go_canonical_runtime",
				"researchPlane":         req.Plane,
				"coverageModel":         "ledger_tree_runtime",
				"reasoningRuntime":      BuildResearchReasoningRuntimeMetadata(loopReq, req.Plane, looped.State.Budget),
				"plannedQueries":        looped.State.PlannedQueries,
				"executedQueries":       looped.State.ExecutedQueries,
				"coverageLedger":        looped.State.CoverageLedger,
				"branchEvaluations":     looped.State.BranchEvaluations,
				"claimVerification":     looped.State.ClaimVerification,
				"verifierDecision":      looped.State.VerifierDecision,
				"durableJob":            looped.State.DurableJob,
				"sourceAcquisition":     looped.State.SourceAcquisition,
				"blackboardArbitration": runtimeBlackboardArbitration(looped.State.Blackboard),
				"adaptiveBudget":        looped.State.Budget,
				"observedSourceFamilies": func() []string {
					if len(looped.State.ObservedSources) > 0 {
						return looped.State.ObservedSources
					}
					return buildObservedSourceFamiliesFromPapers(loopResult.Papers)
				}(),
				"workers":                looped.State.Workers,
				"readyForSynthesis":      looped.State.Blackboard != nil && looped.State.Blackboard.ReadyForSynthesis,
				"answerProvisional":      looped.State.VerifierDecision != nil && looped.State.VerifierDecision.Verdict != "promote",
				"openLedgerCount":        researchBlackboardOpenLedgerCount(looped.State.Blackboard),
				"synthesisGate":          researchBlackboardSynthesisGate(looped.State.Blackboard),
				"finalStopReason":        looped.State.StopReason,
				"multiAgentExecuted":     req.Plane == ResearchExecutionPlaneMultiAgent && researchWorkersExecuted(looped.State.Workers),
				"parallelWorkerPlanning": researchWorkersExecuted(looped.State.Workers),
				"followUpQueries": func() []string {
					if loopResult.FinalizationGate != nil && len(loopResult.FinalizationGate.FollowUpQueries) > 0 {
						return loopResult.FinalizationGate.FollowUpQueries
					}
					if looped.State.ClaimVerification != nil && len(looped.State.ClaimVerification.RequiredFollowUpQueries) > 0 {
						return looped.State.ClaimVerification.RequiredFollowUpQueries
					}
					return nil
				}(),
			},
		},
	}, nil
}

func (rt *UnifiedResearchRuntime) planProgrammaticQueries(ctx context.Context, session *AgentSession, query string, domain string, mode string) []string {
	if rt == nil || rt.exec == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	payload := map[string]any{
		"query": strings.TrimSpace(query),
		"prioritySubtopics": []any{
			"primary evidence",
			"source diversity",
			"citation integrity",
			"counter evidence",
			"replication",
		},
	}
	if strings.TrimSpace(domain) != "" {
		payload["domain"] = strings.TrimSpace(domain)
	}
	if strings.TrimSpace(mode) != "" {
		payload["mode"] = strings.TrimSpace(mode)
	}
	if NormalizeWisDevMode(mode) == WisDevModeYOLO {
		payload["branchWidth"] = float64(4)
		payload["maxDepth"] = float64(3)
	} else {
		payload["branchWidth"] = float64(3)
	}
	tree := RunProgrammaticTreeLoop(ctx, rt.exec, session, ActionResearchQueryDecompose, payload, 4, nil)
	return extractProgrammaticQueriesFromTreeResult(tree)
}

func BuildResearchReasoningRuntimeMetadata(req LoopRequest, plane ResearchExecutionPlane, budget *ResearchBudgetDecision) map[string]any {
	if budget == nil {
		budget = buildResearchBudgetDecision(req, plane)
	}
	programmaticPlannerEnabled := !req.DisableProgrammaticPlanning
	hypothesisTreeEligible := !req.DisableHypothesisGeneration
	treeSearchRuntime := isHighDepthResearchPlane(plane) && hypothesisTreeEligible
	runtimeMode := "classic_loop"
	switch {
	case treeSearchRuntime && programmaticPlannerEnabled:
		runtimeMode = "tree_search_with_programmatic_planner"
	case treeSearchRuntime:
		runtimeMode = "tree_search"
	case programmaticPlannerEnabled:
		runtimeMode = "programmatic_planner"
	}
	return map[string]any{
		"runtimeMode":                      runtimeMode,
		"loopEngine":                       "autonomous_research_loop",
		"treeSearchRuntime":                treeSearchRuntime,
		"treeSearchEligible":               hypothesisTreeEligible,
		"programmaticTreePlanner":          programmaticPlannerEnabled,
		"plannerAction":                    ActionResearchQueryDecompose,
		"branchEvaluationAction":           ActionResearchEvaluateEvidence,
		"branchVerificationAction":         ActionResearchVerifyClaimsBatch,
		"claimSynthesisAction":             ActionResearchSynthesizeAnswer,
		"researchPlane":                    plane,
		"mode":                             NormalizeWisDevMode(req.Mode),
		"workerSearchBudget":               budget.WorkerSearchBudget,
		"followUpSearchBudget":             budget.FollowUpSearchBudget,
		"disableProgrammaticPlanning":      req.DisableProgrammaticPlanning,
		"disableHypothesisGeneration":      req.DisableHypothesisGeneration,
		"dynamicProviderSelectionEligible": NormalizeWisDevMode(req.Mode) == WisDevModeYOLO || plane == ResearchExecutionPlaneDeep || plane == ResearchExecutionPlaneMultiAgent,
	}
}

func buildResearchReasoningRuntimeMetadata(req LoopRequest, plane ResearchExecutionPlane, budget *ResearchBudgetDecision) map[string]any {
	return BuildResearchReasoningRuntimeMetadata(req, plane, budget)
}

func newResearchSessionState(query string, domain string, sessionID string, plane ResearchExecutionPlane) *ResearchSessionState {
	return &ResearchSessionState{
		SessionID: sessionID,
		Query:     strings.TrimSpace(query),
		Domain:    strings.TrimSpace(domain),
		Plane:     plane,
		Workers: []ResearchWorkerState{
			{Role: ResearchWorkerScout, Status: "active", Contract: buildResearchWorkerContract(ResearchWorkerScout), Notes: []string{"retrieval planning and recall expansion"}},
			{Role: ResearchWorkerSourceDiversifier, Status: "active", Contract: buildResearchWorkerContract(ResearchWorkerSourceDiversifier), Notes: []string{"source-family coverage enforcement"}},
			{Role: ResearchWorkerCitationVerifier, Status: "active", Contract: buildResearchWorkerContract(ResearchWorkerCitationVerifier), Notes: []string{"packet grounding and citation integrity"}},
			{Role: ResearchWorkerCitationGraph, Status: "active", Contract: buildResearchWorkerContract(ResearchWorkerCitationGraph), Notes: []string{"citation graph expansion and identifier reconciliation"}},
			{Role: ResearchWorkerContradictionCritic, Status: "active", Contract: buildResearchWorkerContract(ResearchWorkerContradictionCritic), Notes: []string{"contradiction detection and critique reopening"}},
			{Role: ResearchWorkerIndependentVerifier, Status: "pending", Contract: buildResearchWorkerContract(ResearchWorkerIndependentVerifier), Notes: []string{"evidence-only claim verification before synthesis"}},
			{Role: ResearchWorkerSynthesizer, Status: "active", Contract: buildResearchWorkerContract(ResearchWorkerSynthesizer), Notes: []string{"final synthesis from grounded evidence"}},
		},
	}
}

func buildResearchWorkerContract(role ResearchWorkerRole) ResearchWorkerContract {
	contract := ResearchWorkerContract{
		Role: role,
		OutputSchema: []string{
			"plannedQueries",
			"executedQueries",
			"papers",
			"evidence",
			"coverageLedger",
			"artifacts",
			"confidence",
			"stopReason",
		},
		ConfidenceFloor: 0.55,
		StopReasons: []string{
			"budget_exhausted",
			"coverage_resolved",
			"coverage_open",
			"cancelled",
			"degraded",
		},
		MustNotDecide: []string{
			"final answer wording",
			"provider selection policy",
			"citation promotion without ledger agreement",
		},
	}
	switch role {
	case ResearchWorkerScout:
		contract.Objective = "expand the root question into executable, non-duplicative research branches"
		contract.AllowedTools = []string{"research.queryDecompose", "academic_search"}
		contract.RequiredOutputs = []string{"candidate queries", "branch rationale"}
		contract.RequiredEvidence = []string{"at least one grounded source when search is available"}
		contract.CompletionGate = "planned branches are executable and either searched or left as explicit coverage gaps"
		contract.MaxSearches = workerSearchQueryLimit(role)
	case ResearchWorkerSourceDiversifier:
		contract.Objective = "force independent source-family and full-text acquisition coverage before synthesis"
		contract.AllowedTools = []string{"academic_search", "source_family_classifier", "full_text_candidate_lookup"}
		contract.RequiredOutputs = []string{"review/meta-analysis branch", "replication or benchmark branch", "domain-primary branch", "full-text acquisition candidates"}
		contract.RequiredEvidence = []string{"two or more independent source families when available", "open-access PDF/full-text candidates or explicit acquisition gap"}
		contract.CompletionGate = "source-family and source-acquisition ledgers are resolved or emit targeted follow-up queries"
		contract.MaxSearches = workerSearchQueryLimit(role)
	case ResearchWorkerCitationVerifier:
		contract.Objective = "cross-reference citation identity and metadata before writer promotion"
		contract.AllowedTools = []string{"academic_search", "doi_lookup", "arxiv_lookup", "pubmed_lookup", "citation_identity_normalizer"}
		contract.RequiredOutputs = []string{"canonical citation branch", "forward/backward citation trail branch"}
		contract.RequiredEvidence = []string{"persistent identifiers or explicit citation-integrity gap"}
		contract.CompletionGate = "citation promotion requires multi-source agreement or remains blocked"
		contract.MaxSearches = workerSearchQueryLimit(role)
	case ResearchWorkerCitationGraph:
		contract.Objective = "build backward and forward citation expansion graphs and reconcile persistent identifiers"
		contract.AllowedTools = []string{"academic_search", "doi_lookup", "arxiv_lookup", "pubmed_lookup", "citation_graph_builder", "retraction_checker"}
		contract.RequiredOutputs = []string{"citation graph nodes", "citation graph edges", "retraction flags"}
		contract.RequiredEvidence = []string{"at least one persistent identifier per node"}
		contract.CompletionGate = "citation graph is expanded or explicitly bounded with an unresolvable-ID gap entry"
		contract.MaxSearches = workerSearchQueryLimit(role)
	case ResearchWorkerContradictionCritic:
		contract.Objective = "actively search for limitations, null results, failed replications, and conflicting claims"
		contract.AllowedTools = []string{"academic_search", "contradiction_detector", "replication_search"}
		contract.RequiredOutputs = []string{"counter-evidence branch", "limitations branch"}
		contract.RequiredEvidence = []string{"contradiction ledger entries when conflicts are found"}
		contract.CompletionGate = "known contradictions are resolved, qualified, or carried into final critique"
		contract.MaxSearches = workerSearchQueryLimit(role)
	case ResearchWorkerIndependentVerifier:
		contract.Objective = "verify claims from evidence packets and ledgers without seeing or optimizing the writer draft"
		contract.AllowedTools = []string{"blackboard_read", "claim_verification_ledger", "coverage_ledger", "evidence_packet_reader", "claim_entailment_checker"}
		contract.RequiredOutputs = []string{"promoted claim ids", "rejected claim ids", "revise or abstain verdict", "verification confidence"}
		contract.RequiredEvidence = []string{"claim-level evidence spans", "citation health", "contradiction status", "source-family coverage"}
		contract.MustNotDecide = append(contract.MustNotDecide, "writer draft wording", "writer draft chain")
		contract.CompletionGate = "writer may synthesize only when verifier verdict is promote or when unresolved gaps are explicitly surfaced"
		contract.ConfidenceFloor = 0.74
	case ResearchWorkerSynthesizer:
		contract.Objective = "compose only after evidence, coverage, and citation ledgers have been merged"
		contract.AllowedTools = []string{"blackboard_read", "claim_verification_ledger"}
		contract.RequiredOutputs = []string{"synthesis readiness gate"}
		contract.RequiredEvidence = []string{"closed or explicitly surfaced coverage ledger"}
		contract.CompletionGate = "draft generation is blocked from treating unresolved ledger gaps as settled facts"
		contract.ConfidenceFloor = 0.7
	default:
		contract.Objective = "unknown worker role"
	}
	return contract
}

func buildResearchBudgetDecision(req LoopRequest, plane ResearchExecutionPlane) *ResearchBudgetDecision {
	searchTermBudget := resolveLoopSearchTermBudget(req.MaxIterations, req.MaxSearchTerms)
	workerBudget := resolveResearchWorkerSearchBudget(plane, searchTermBudget)
	reservedLoopSearches := maxInt(searchTermBudget-workerBudget, 0)
	decision := &ResearchBudgetDecision{
		SearchTermBudget:      searchTermBudget,
		WorkerSearchBudget:    workerBudget,
		ReservedLoopSearches:  reservedLoopSearches,
		RecursiveGapDepth:     resolveLoopGapRecursionDepth(req.Mode, plane),
		CritiqueFollowUpLimit: resolveCritiqueFollowUpLimit(req.Mode, plane),
		QueryParallelism:      resolveLoopQueryParallelism(req.Mode, plane),
		Rationale: []string{
			"initial worker budget reserves capacity for recursive gap closure and critique retrieval",
		},
	}
	if isHighDepthResearchPlane(plane) {
		decision.Rationale = append(decision.Rationale, "high-depth plane enables broader worker fan-out and deeper gap recursion")
	}
	if req.MaxSearchTerms > 0 && req.MaxIterations > 0 && req.MaxSearchTerms < req.MaxIterations {
		decision.Rationale = append(decision.Rationale, "search-term cap is stricter than iteration cap")
	}
	if len(req.SeedQueries) >= 4 {
		decision.Rationale = append(decision.Rationale, "seed-query pressure will be represented in branch trace events")
	}
	return decision
}

func finalizeResearchBudgetDecision(decision *ResearchBudgetDecision, gap *LoopGapState, claims *ClaimVerificationLedger) {
	if decision == nil {
		return
	}
	decision.FollowUpSearchBudget = 0
	decision.CoveragePressure = 0
	decision.SourceDiversityPressure = 0
	decision.ContradictionPressure = 0
	decision.ClaimCriticality = 0
	if gap != nil {
		for _, entry := range gap.Ledger {
			if !strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
				continue
			}
			decision.CoveragePressure++
			switch strings.TrimSpace(entry.Category) {
			case "source_diversity", "coverage_rubric", "source_inventory":
				decision.SourceDiversityPressure++
			case "contradiction", "hypothesis_branch":
				decision.ContradictionPressure++
			case "claim_evidence", "claim_source_diversity":
				decision.ClaimCriticality++
			}
		}
	}
	if claims != nil {
		decision.ClaimCriticality += claims.OpenClaims + claims.ContradictedClaims
		if claims.SourceFamilyCoverage < 0.67 && len(claims.Records) > 0 {
			decision.SourceDiversityPressure++
		}
		if claims.ContradictedClaims > 0 {
			decision.ContradictionPressure += claims.ContradictedClaims
		}
	}
	pressure := decision.CoveragePressure + decision.SourceDiversityPressure + decision.ContradictionPressure + decision.ClaimCriticality
	if pressure <= 0 {
		decision.FollowUpSearchBudget = 0
		decision.Rationale = appendUniqueString(decision.Rationale, "no open ledger pressure after claim verification")
		return
	}
	base := maxInt(2, decision.CritiqueFollowUpLimit)
	decision.FollowUpSearchBudget = minInt(maxInt(base+pressure/2, base), maxInt(decision.SearchTermBudget, base+6))
	decision.Rationale = appendUniqueString(decision.Rationale, "follow-up budget expanded from open coverage, source-diversity, contradiction, and claim-criticality pressure")
}

func buildClaimVerificationLedger(query string, findings []EvidenceFinding, papers []search.Paper, gap *LoopGapState) *ClaimVerificationLedger {
	ledger := &ClaimVerificationLedger{
		Query:                  strings.TrimSpace(query),
		ObservedSourceFamilies: buildObservedSourceFamiliesFromPapers(papers),
	}
	recordsByKey := make(map[string]int)
	sourceFamilies := buildPaperSourceFamilyIndex(papers)
	for _, finding := range dedupeEvidenceFindings(findings) {
		claim := strings.TrimSpace(finding.Claim)
		if claim == "" {
			continue
		}
		key := strings.ToLower(claim)
		recordIndex, ok := recordsByKey[key]
		if !ok {
			ledger.Records = append(ledger.Records, ClaimVerificationRecord{
				ID:    stableWisDevID("claim-verification", ledger.Query, claim),
				Claim: claim,
			})
			recordIndex = len(ledger.Records) - 1
			recordsByKey[key] = recordIndex
		}
		record := &ledger.Records[recordIndex]
		sourceID := strings.TrimSpace(finding.SourceID)
		record.SourceIDs = dedupeTrimmedStrings(append(record.SourceIDs, sourceID))
		record.SourceFamilies = dedupeTrimmedStrings(append(record.SourceFamilies, sourceFamiliesForFinding(finding, sourceFamilies)...))
		record.EvidenceSpans = append(record.EvidenceSpans, ClaimEvidenceSpan{
			SourceID:    sourceID,
			SourceTitle: strings.TrimSpace(firstNonEmpty(finding.PaperTitle, sourceID)),
			Snippet:     strings.TrimSpace(finding.Snippet),
			Confidence:  ClampFloat(finding.Confidence, 0, 1),
		})
	}
	if len(ledger.Records) == 0 {
		ledger.Records = append(ledger.Records, ClaimVerificationRecord{
			ID:                  stableWisDevID("claim-verification", ledger.Query, "no-evidence"),
			Claim:               strings.TrimSpace(query),
			Status:              "unsupported",
			Confidence:          0.18,
			CitationHealth:      "missing_evidence",
			ContradictionStatus: "unknown",
			StopReason:          "no_claim_evidence",
			FollowUpQueries:     []string{buildResearchWorkerQuery(query, "primary evidence claim support")},
		})
	}
	totalConfidence := 0.0
	totalCitationHealth := 0.0
	totalSourceCoverage := 0.0
	for idx := range ledger.Records {
		record := &ledger.Records[idx]
		record.SupportCount = len(record.EvidenceSpans)
		record.SourceIDs = dedupeTrimmedStrings(record.SourceIDs)
		record.SourceFamilies = dedupeTrimmedStrings(record.SourceFamilies)
		record.ContradictionStatus = "none"
		if claimHasOpenContradiction(record.Claim, gap) {
			record.ContradictionStatus = "open"
		}
		record.CitationHealth = claimCitationHealth(*record)
		record.Confidence = calibrateClaimConfidence(*record)
		record.Status, record.StopReason = claimVerificationStatus(*record)
		if record.StopReason != "" {
			record.FollowUpQueries = claimVerificationFollowUpQueries(query, *record)
		}
		switch record.Status {
		case "supported":
			ledger.SupportedClaims++
		case "contradicted":
			ledger.ContradictedClaims++
			ledger.OpenClaims++
		default:
			ledger.OpenClaims++
		}
		if record.CitationHealth == "missing_evidence" || record.CitationHealth == "missing_source" {
			ledger.RejectedCitationCount++
		}
		totalConfidence += record.Confidence
		totalCitationHealth += citationHealthScore(record.CitationHealth)
		totalSourceCoverage += ClampFloat(float64(len(record.SourceFamilies))/2, 0, 1)
		ledger.RequiredFollowUpQueries = normalizeLoopQueries("", append(ledger.RequiredFollowUpQueries, record.FollowUpQueries...))
	}
	count := float64(maxInt(len(ledger.Records), 1))
	ledger.CitationHealthScore = ClampFloat(totalCitationHealth/count, 0, 1)
	ledger.SourceFamilyCoverage = ClampFloat(totalSourceCoverage/count, 0, 1)
	ledger.CalibratedConfidence = ClampFloat(totalConfidence/count, 0, 1)
	ledger.StopReason = claimLedgerStopReason(ledger)
	return ledger
}

func buildPaperSourceFamilyIndex(papers []search.Paper) map[string][]string {
	index := make(map[string][]string, len(papers)*5)
	for _, paper := range papers {
		families := buildObservedSourceFamiliesFromPapers([]search.Paper{paper})
		if len(families) == 0 {
			families = inferResearchCoverageFamilies([]search.Paper{paper})
		}
		for _, key := range []string{paper.ID, paper.DOI, paper.ArxivID, paper.Link, paper.Title} {
			trimmed := strings.ToLower(strings.TrimSpace(key))
			if trimmed != "" {
				index[trimmed] = families
			}
		}
	}
	return index
}

func sourceFamiliesForFinding(finding EvidenceFinding, index map[string][]string) []string {
	for _, key := range []string{finding.SourceID, finding.PaperTitle} {
		if families := index[strings.ToLower(strings.TrimSpace(key))]; len(families) > 0 {
			return families
		}
	}
	return dedupeTrimmedStrings(finding.Keywords)
}

func claimHasOpenContradiction(claim string, gap *LoopGapState) bool {
	if gap == nil {
		return false
	}
	for _, contradiction := range gap.Contradictions {
		if textOverlapsClaim(claim, contradiction) {
			return true
		}
	}
	for _, entry := range gap.Ledger {
		if !strings.Contains(strings.ToLower(strings.TrimSpace(entry.Category)), "contradiction") {
			continue
		}
		if textOverlapsClaim(claim, entry.Title) || textOverlapsClaim(claim, entry.Description) {
			return true
		}
	}
	return false
}

func textOverlapsClaim(claim string, text string) bool {
	claim = strings.ToLower(strings.TrimSpace(claim))
	text = strings.ToLower(strings.TrimSpace(text))
	if claim == "" || text == "" {
		return false
	}
	if strings.Contains(text, claim) || strings.Contains(claim, text) {
		return true
	}
	matches := 0
	for _, token := range strings.Fields(claim) {
		token = strings.Trim(token, ".,;:()[]{}\"'")
		if len(token) < 6 {
			continue
		}
		if strings.Contains(text, token) {
			matches++
		}
	}
	return matches >= 2
}

func claimCitationHealth(record ClaimVerificationRecord) string {
	switch {
	case record.SupportCount == 0:
		return "missing_evidence"
	case len(record.SourceIDs) == 0:
		return "missing_source"
	case len(record.SourceIDs) == 1:
		return "single_source"
	case len(record.SourceFamilies) < 2:
		return "weak_family_coverage"
	default:
		return "healthy"
	}
}

func calibrateClaimConfidence(record ClaimVerificationRecord) float64 {
	if len(record.EvidenceSpans) == 0 {
		return 0.18
	}
	total := 0.0
	for _, span := range record.EvidenceSpans {
		total += ClampFloat(span.Confidence, 0, 1)
	}
	confidence := total / float64(len(record.EvidenceSpans))
	switch record.CitationHealth {
	case "healthy":
		confidence += 0.06
	case "single_source", "weak_family_coverage":
		confidence -= 0.12
	case "missing_source":
		confidence -= 0.2
	}
	if record.ContradictionStatus == "open" {
		confidence -= 0.28
	}
	return ClampFloat(confidence, 0.05, 0.98)
}

func claimVerificationStatus(record ClaimVerificationRecord) (string, string) {
	if record.ContradictionStatus == "open" {
		return "contradicted", "contradiction_open"
	}
	switch record.CitationHealth {
	case "healthy":
		return "supported", ""
	case "single_source", "weak_family_coverage":
		return "needs_triangulation", "source_diversity_open"
	default:
		return "unsupported", "citation_missing"
	}
}

func claimVerificationFollowUpQueries(query string, record ClaimVerificationRecord) []string {
	switch record.StopReason {
	case "contradiction_open":
		return []string{buildResearchWorkerQuery(query, "contradiction resolution "+record.Claim)}
	case "source_diversity_open":
		return []string{buildResearchWorkerQuery(query, "independent replication "+record.Claim)}
	case "citation_missing":
		return []string{buildResearchWorkerQuery(query, "primary source citation "+record.Claim)}
	default:
		return nil
	}
}

func citationHealthScore(health string) float64 {
	switch health {
	case "healthy":
		return 1
	case "weak_family_coverage":
		return 0.7
	case "single_source":
		return 0.55
	case "missing_source":
		return 0.35
	default:
		return 0.15
	}
}

func claimLedgerStopReason(ledger *ClaimVerificationLedger) string {
	if ledger == nil {
		return "claim_verification_unavailable"
	}
	if ledger.ContradictedClaims > 0 {
		return "contradictions_open"
	}
	if ledger.OpenClaims > 0 {
		return "claim_coverage_open"
	}
	return "claim_verification_satisfied"
}

func buildResearchBranchEvaluations(rootQuery string, plannedQueries []string, executedQueries []string, queryCoverage map[string][]search.Paper, gap *LoopGapState) []ResearchBranchEvaluation {
	planned := normalizeLoopQueries(rootQuery, plannedQueries)
	if len(planned) == 0 {
		return nil
	}
	evaluations := make([]ResearchBranchEvaluation, len(planned))
	var wg sync.WaitGroup
	for idx, query := range planned {
		wg.Add(1)
		go func(idx int, query string) {
			defer wg.Done()
			papers := queryCoverage[strings.TrimSpace(query)]
			evaluations[idx] = evaluateResearchBranch(rootQuery, query, idx, containsNormalizedLoopQuery(executedQueries, query), papers, gap)
		}(idx, query)
	}
	wg.Wait()
	return evaluations
}

func evaluateResearchBranch(rootQuery string, query string, index int, executed bool, papers []search.Paper, gap *LoopGapState) ResearchBranchEvaluation {
	query = strings.TrimSpace(query)
	branch := ResearchBranchEvaluation{
		ID:                      stableWisDevID("research-branch", fmt.Sprintf("%d", index), query),
		Query:                   query,
		Status:                  "open",
		Hypothesis:              query,
		FalsifiabilityCondition: "Collect independent evidence that supports or falsifies this branch.",
		PlannedQueries:          []string{query},
		SourceFamilies:          buildObservedSourceFamiliesFromPapers(papers),
		Evidence:                branchEvidenceFindings(papers),
		NoveltyScore:            branchNoveltyScore(rootQuery, query),
		CoverageScore:           branchCoverageScore(papers),
		FalsifiabilityScore:     branchFalsifiabilityScore(query),
		EvidenceScore:           branchEvidenceScore(papers),
		SourceIDs:               branchSourceIDs(papers),
		OpenGaps:                branchOpenGapIDs(query, gap),
	}
	if executed {
		branch.Status = "executed"
		branch.ExecutedQueries = []string{query}
	}
	if executed && len(papers) > 0 && len(branch.OpenGaps) == 0 {
		branch.Status = "covered"
	}
	branch.OverallScore = ClampFloat((branch.NoveltyScore+branch.CoverageScore+branch.FalsifiabilityScore+branch.EvidenceScore)/4, 0, 1)
	branch.BranchScore = branch.OverallScore
	branch.IdempotencyKey = stableWisDevID("research-branch-idempotency", branch.ID, query)
	branch.CheckpointKey = stableWisDevID("research-branch-checkpoint", branch.ID, branch.Status)
	switch {
	case len(branch.OpenGaps) > 0:
		branch.StopReason = "branch_open_gap"
		branch.VerifierVerdict = "revise_required"
		branch.Contradictions = branchContradictions(query, gap)
	case !executed:
		branch.StopReason = "branch_unexecuted"
		branch.VerifierVerdict = "revise_required"
	case len(papers) == 0:
		branch.StopReason = "branch_needs_evidence"
		branch.VerifierVerdict = "revise_required"
	default:
		branch.StopReason = "branch_covered"
		branch.VerifierVerdict = "promote"
	}
	return branch
}

func branchEvidenceFindings(papers []search.Paper) []EvidenceFinding {
	if len(papers) == 0 {
		return nil
	}
	findings := make([]EvidenceFinding, 0, len(papers))
	for _, paper := range papers {
		findings = append(findings, buildEvidenceFindingsFromSource(mapPaperToSource(paper), 1)...)
	}
	return findings
}

func branchContradictions(query string, gap *LoopGapState) []string {
	if gap == nil {
		return nil
	}
	queryTokens := branchTokenSet(query)
	out := make([]string, 0, len(gap.Contradictions))
	for _, contradiction := range gap.Contradictions {
		contradictionTokens := branchTokenSet(contradiction)
		for token := range queryTokens {
			if _, ok := contradictionTokens[token]; ok {
				out = append(out, contradiction)
				break
			}
		}
	}
	return dedupeTrimmedStrings(out)
}

func branchNoveltyScore(rootQuery string, query string) float64 {
	rootTokens := branchTokenSet(rootQuery)
	queryTokens := branchTokenSet(query)
	if len(queryTokens) == 0 {
		return 0
	}
	novel := 0
	for token := range queryTokens {
		if _, exists := rootTokens[token]; !exists {
			novel++
		}
	}
	return ClampFloat(0.35+float64(novel)/float64(len(queryTokens))*0.65, 0.1, 1)
}

func branchTokenSet(value string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, token := range strings.Fields(strings.ToLower(strings.TrimSpace(value))) {
		token = strings.Trim(token, ".,;:()[]{}\"'")
		if len(token) < 4 {
			continue
		}
		tokens[token] = struct{}{}
	}
	return tokens
}

func branchCoverageScore(papers []search.Paper) float64 {
	if len(papers) == 0 {
		return 0
	}
	return ClampFloat(float64(len(papers))/3, 0.35, 1)
}

func branchEvidenceScore(papers []search.Paper) float64 {
	if len(papers) == 0 {
		return 0
	}
	score := 0.25
	for _, paper := range papers {
		if strings.TrimSpace(paper.Abstract) != "" {
			score += 0.18
		}
		if strings.TrimSpace(firstNonEmpty(paper.DOI, paper.ArxivID, paper.ID)) != "" {
			score += 0.14
		}
		if strings.TrimSpace(firstNonEmpty(paper.PdfUrl, paper.OpenAccessUrl)) != "" {
			score += 0.12
		}
	}
	return ClampFloat(score/float64(len(papers)), 0.2, 1)
}

func branchFalsifiabilityScore(query string) float64 {
	lower := strings.ToLower(strings.TrimSpace(query))
	score := 0.35
	for _, marker := range []string{
		"replication",
		"replicate",
		"contradict",
		"conflict",
		"failed",
		"null result",
		"negative result",
		"limitation",
		"benchmark",
		"cohort",
		"trial",
		"dataset",
		"ablation",
	} {
		if strings.Contains(lower, marker) {
			score += 0.08
		}
	}
	return ClampFloat(score, 0.25, 1)
}

func branchSourceIDs(papers []search.Paper) []string {
	ids := make([]string, 0, len(papers))
	for _, paper := range papers {
		if id := strings.TrimSpace(firstNonEmpty(paper.DOI, paper.ArxivID, paper.ID, paper.Link, paper.Title)); id != "" {
			ids = append(ids, id)
		}
	}
	return dedupeTrimmedStrings(ids)
}

func branchOpenGapIDs(query string, gap *LoopGapState) []string {
	if gap == nil {
		return nil
	}
	ids := make([]string, 0)
	for _, entry := range gap.Ledger {
		if !strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
			continue
		}
		if coverageGapMatchesBranch(query, entry) {
			ids = append(ids, strings.TrimSpace(firstNonEmpty(entry.ID, entry.Category+"|"+entry.Title)))
		}
	}
	return dedupeTrimmedStrings(ids)
}

func coverageGapMatchesBranch(query string, entry CoverageLedgerEntry) bool {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if lowerQuery == "" {
		return false
	}
	category := strings.ToLower(strings.TrimSpace(entry.Category))
	if strings.Contains(category, "contradiction") && !branchHasAnyMarker(lowerQuery, []string{"contradict", "conflict", "failed", "null", "negative"}) {
		return false
	}
	if textContainsWholeBranch(lowerQuery, entry.Title) || textContainsWholeBranch(lowerQuery, entry.Description) {
		return true
	}
	for _, supportingQuery := range entry.SupportingQueries {
		if strings.EqualFold(strings.TrimSpace(supportingQuery), strings.TrimSpace(query)) || textContainsWholeBranch(lowerQuery, supportingQuery) {
			return true
		}
	}
	if !strings.Contains(category, "contradiction") && (textOverlapsClaim(query, entry.Title) || textOverlapsClaim(query, entry.Description)) {
		return true
	}
	return false
}

func textContainsWholeBranch(lowerQuery string, text string) bool {
	lowerText := strings.ToLower(strings.TrimSpace(text))
	if lowerText == "" {
		return false
	}
	return strings.Contains(lowerText, lowerQuery) || strings.Contains(lowerQuery, lowerText)
}

func branchHasAnyMarker(lowerQuery string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(lowerQuery, marker) {
			return true
		}
	}
	return false
}

func buildIndependentVerifierDecision(claims *ClaimVerificationLedger, gap *LoopGapState, branches []ResearchBranchEvaluation) *ResearchVerifierDecision {
	decision := &ResearchVerifierDecision{
		Role:         ResearchWorkerIndependentVerifier,
		Verdict:      "promote",
		StopReason:   "verifier_promoted",
		Confidence:   0.72,
		EvidenceOnly: true,
	}
	if claims == nil {
		decision.Verdict = "abstain"
		decision.StopReason = "verifier_claims_unavailable"
		decision.Confidence = 0.2
		decision.RevisionReasons = []string{"claim verification ledger is unavailable"}
		return decision
	}
	decision.Confidence = claims.CalibratedConfidence
	for _, record := range claims.Records {
		switch {
		case record.Status == "supported" && record.CitationHealth == "healthy" && record.ContradictionStatus != "open":
			decision.PromotedClaimIDs = append(decision.PromotedClaimIDs, strings.TrimSpace(record.ID))
		default:
			decision.RejectedClaimIDs = append(decision.RejectedClaimIDs, strings.TrimSpace(record.ID))
			decision.RevisionReasons = append(decision.RevisionReasons, verifierRevisionReason(record))
		}
	}
	if claims.ContradictedClaims > 0 {
		decision.RevisionReasons = append(decision.RevisionReasons, "contradicted claims remain unresolved")
	}
	if claims.OpenClaims > 0 {
		decision.RevisionReasons = append(decision.RevisionReasons, "open claims still require triangulation or citation repair")
	}
	if hasOpenActionableCoverageGaps(gap) {
		decision.RevisionReasons = append(decision.RevisionReasons, "actionable coverage ledger gaps remain open")
	}
	for _, branch := range branches {
		if len(branch.OpenGaps) > 0 {
			decision.RevisionReasons = append(decision.RevisionReasons, "branch remains blocked by open coverage gaps: "+branch.Query)
		} else if branch.StopReason == "branch_unexecuted" || branch.StopReason == "branch_needs_evidence" {
			decision.RevisionReasons = append(decision.RevisionReasons, "branch has not produced enough evidence: "+branch.Query)
		}
	}
	decision.PromotedClaimIDs = dedupeTrimmedStrings(decision.PromotedClaimIDs)
	decision.RejectedClaimIDs = dedupeTrimmedStrings(decision.RejectedClaimIDs)
	decision.RevisionReasons = dedupeTrimmedStrings(decision.RevisionReasons)
	if len(decision.RejectedClaimIDs) > 0 || hasOpenActionableCoverageGaps(gap) || branchRevisionPressure(branches) > 0 {
		decision.Verdict = "revise_required"
		decision.StopReason = "verifier_requires_revision"
		decision.Confidence = ClampFloat(decision.Confidence-0.18, 0.05, 0.85)
	}
	if claims.ContradictedClaims > 0 || (len(decision.PromotedClaimIDs) == 0 && len(decision.RejectedClaimIDs) > 0) {
		decision.Verdict = "abstain"
		decision.StopReason = "verifier_abstained"
		decision.Confidence = ClampFloat(decision.Confidence-0.22, 0.05, 0.7)
	}
	return decision
}

func verifierRevisionReason(record ClaimVerificationRecord) string {
	claim := strings.TrimSpace(firstNonEmpty(record.Claim, record.ID, "claim"))
	switch {
	case record.ContradictionStatus == "open" || record.Status == "contradicted":
		return "contradiction unresolved for claim: " + claim
	case record.CitationHealth != "healthy":
		return "citation health is " + strings.TrimSpace(firstNonEmpty(record.CitationHealth, "unknown")) + " for claim: " + claim
	case record.Status != "supported":
		return "claim remains " + strings.TrimSpace(firstNonEmpty(record.Status, "unverified")) + ": " + claim
	default:
		return "claim requires verifier review: " + claim
	}
}

func branchRevisionPressure(branches []ResearchBranchEvaluation) int {
	pressure := 0
	for _, branch := range branches {
		if len(branch.OpenGaps) > 0 || branch.StopReason == "branch_unexecuted" || branch.StopReason == "branch_needs_evidence" {
			pressure++
		}
	}
	return pressure
}

func applyIndependentVerifierDecision(state *ResearchSessionState, decision *ResearchVerifierDecision) {
	if state == nil || decision == nil {
		return
	}
	result := ResearchWorkerState{
		Role:       ResearchWorkerIndependentVerifier,
		Status:     "completed",
		Contract:   buildResearchWorkerContract(ResearchWorkerIndependentVerifier),
		StartedAt:  NowMillis(),
		FinishedAt: NowMillis(),
		Artifacts: map[string]any{
			"decision": decision,
		},
		Notes:          []string{"evidence-only verifier evaluated claim ledger and branch state"},
		CoverageLedger: []CoverageLedgerEntry{verifierCoverageLedgerEntry(decision)},
	}
	if idx := findResearchWorkerIndex(state.Workers, ResearchWorkerIndependentVerifier); idx >= 0 {
		state.Workers[idx] = result
		return
	}
	state.Workers = append(state.Workers, result)
}

func verifierCoverageLedgerEntry(args ...any) CoverageLedgerEntry {
	var query string
	var decision *ResearchVerifierDecision
	for _, arg := range args {
		switch typed := arg.(type) {
		case string:
			query = strings.TrimSpace(typed)
		case *ResearchVerifierDecision:
			decision = typed
		}
	}
	status := coverageLedgerStatusResolved
	if decision == nil || decision.Verdict != "promote" {
		status = coverageLedgerStatusOpen
	}
	description := "Independent verifier promoted supported claims from evidence packets."
	if decision != nil && len(decision.RevisionReasons) > 0 {
		description = strings.Join(decision.RevisionReasons, "; ")
	}
	entry := CoverageLedgerEntry{
		ID:                stableWisDevID("verifier-ledger", string(ResearchWorkerIndependentVerifier), firstNonEmpty(query, decisionStopReason(decision), "unknown")),
		Category:          "independent_verifier",
		Status:            status,
		Title:             "Independent verifier arbitration",
		Description:       description,
		SupportingQueries: normalizeLoopQueries("", []string{strings.TrimSpace(firstNonEmpty(query+" "+summarizeLoopGapTerms(description), query))}),
		Confidence:        decisionConfidence(decision),
		Required:          true,
		Priority:          96,
		ObligationType:    "unverified_claim",
		OwnerWorker:       string(ResearchWorkerIndependentVerifier),
		Severity:          "high",
		ClosureEvidence:   []string{"decision:" + firstNonEmpty(decisionStopReason(decision), verifierVerdict(decision), "unknown")},
	}
	lowerDescription := strings.ToLower(description)
	if strings.Contains(lowerDescription, "citation") || strings.Contains(lowerDescription, "source") {
		entry.Category = "citation_integrity"
		entry.ObligationType = "missing_citation_identity"
		entry.OwnerWorker = string(ResearchWorkerCitationGraph)
		entry.Severity = "critical"
	}
	return entry
}

func decisionStopReason(decision *ResearchVerifierDecision) string {
	if decision == nil {
		return ""
	}
	return strings.TrimSpace(decision.StopReason)
}

func decisionConfidence(decision *ResearchVerifierDecision) float64 {
	if decision == nil {
		return 0
	}
	return ClampFloat(decision.Confidence, 0, 1)
}

func findResearchWorkerIndex(workers []ResearchWorkerState, role ResearchWorkerRole) int {
	for idx := range workers {
		if workers[idx].Role == role {
			return idx
		}
	}
	return -1
}

func attachRuntimeArbitrationToBlackboard(board *ResearchBlackboard, branches []ResearchBranchEvaluation, decision *ResearchVerifierDecision) {
	if board == nil {
		return
	}
	board.BranchEvaluations = append([]ResearchBranchEvaluation(nil), branches...)
	board.Arbitration = buildResearchArbitrationState(decision)
	if decision == nil {
		board.ReadyForSynthesis = false
		board.SynthesisGate = "blocked: independent verifier decision is unavailable"
		return
	}
	board.OpenLedgerCount = countOpenCoverageLedgerEntries(board.CoverageLedger)
	switch decision.Verdict {
	case "promote":
		if board.OpenLedgerCount == 0 && len(board.Evidence) > 0 {
			board.ReadyForSynthesis = true
			board.SynthesisGate = "ready: independent verifier promoted grounded claims"
		} else if board.OpenLedgerCount == 0 {
			board.ReadyForSynthesis = false
			board.SynthesisGate = "blocked: no specialist evidence has been merged"
		} else {
			board.ReadyForSynthesis = false
			board.SynthesisGate = fmt.Sprintf("blocked: %d coverage ledger item(s) remain open after verifier promotion", board.OpenLedgerCount)
		}
	case "abstain":
		board.ReadyForSynthesis = false
		board.SynthesisGate = "blocked: independent verifier abstained"
	default:
		board.ReadyForSynthesis = false
		board.SynthesisGate = "blocked: independent verifier requires revision"
	}
}

func buildResearchArbitrationState(decision *ResearchVerifierDecision) *ResearchArbitrationState {
	if decision == nil {
		return &ResearchArbitrationState{
			Verdict:        "missing",
			ForcedRevision: true,
			Abstain:        true,
			Reasons:        []string{"independent verifier decision missing"},
			MergeRules:     researchArbitrationMergeRules(),
		}
	}
	return &ResearchArbitrationState{
		Verdict:          decision.Verdict,
		ForcedRevision:   decision.Verdict == "revise_required",
		Abstain:          decision.Verdict == "abstain",
		PromotedClaimIDs: append([]string(nil), decision.PromotedClaimIDs...),
		RejectedClaimIDs: append([]string(nil), decision.RejectedClaimIDs...),
		Reasons:          append([]string(nil), decision.RevisionReasons...),
		MergeRules:       researchArbitrationMergeRules(),
	}
}

func researchArbitrationMergeRules() []string {
	return []string{
		"all research agents read and write through the blackboard",
		"claim promotion requires independent verifier approval",
		"unresolved coverage ledger entries block synthesis",
		"writer synthesis cannot override verifier abstain or revise outcomes",
	}
}

func runtimeBlackboardArbitration(board *ResearchBlackboard) *ResearchArbitrationState {
	if board == nil {
		return nil
	}
	return board.Arbitration
}

func determineResearchStopReason(loopResult *LoopResult, claims *ClaimVerificationLedger, verifier *ResearchVerifierDecision) string {
	if loopResult == nil {
		return "runtime_no_result"
	}
	if verifier != nil && verifier.StopReason != "" && verifier.Verdict != "promote" {
		return verifier.StopReason
	}
	if claims != nil && claims.StopReason != "" && claims.StopReason != "claim_verification_satisfied" {
		return claims.StopReason
	}
	if hasOpenActionableCoverageGaps(loopResult.GapAnalysis) {
		return "coverage_open"
	}
	if len(loopResult.Papers) == 0 {
		return "no_grounded_sources"
	}
	if loopResult.Converged {
		return "coverage_satisfied"
	}
	return "budget_or_iteration_limit"
}

func initializeLiveReasoningGraph(state *ResearchSessionState, plannedQueries []string) *ReasoningGraph {
	if state == nil {
		return nil
	}
	rootID := stableWisDevID("runtime-root", state.SessionID, state.Query)
	graph := &ReasoningGraph{
		Query: state.Query,
		Nodes: []ReasoningNode{{
			ID:         rootID,
			Text:       state.Query,
			Type:       ReasoningNodeQuestion,
			Label:      "research_root",
			Depth:      0,
			Confidence: 1,
			Metadata: map[string]any{
				"plane": state.Plane,
			},
		}},
		Edges: []ReasoningEdge{},
		Root:  rootID,
	}
	scoutID := rootID
	for idx, worker := range state.Workers {
		nodeID := stableWisDevID("runtime-worker", state.SessionID, string(worker.Role))
		if worker.Role == ResearchWorkerScout {
			scoutID = nodeID
		}
		graph.Nodes = append(graph.Nodes, ReasoningNode{
			ID:       nodeID,
			Text:     string(worker.Role),
			Type:     ReasoningNodeWorker,
			Label:    string(worker.Role),
			Depth:    1,
			ParentID: rootID,
			Metadata: map[string]any{
				"status": worker.Status,
				"index":  idx,
			},
		})
		graph.Edges = append(graph.Edges, ReasoningEdge{From: rootID, To: nodeID, Label: "worker"})
	}
	for idx, query := range plannedQueries {
		nodeID := stableWisDevID("runtime-query", state.SessionID, fmt.Sprintf("%d", idx), query)
		graph.Nodes = append(graph.Nodes, ReasoningNode{
			ID:           nodeID,
			Text:         strings.TrimSpace(query),
			Type:         ReasoningNodeQuestion,
			Label:        "planned_query",
			Depth:        2,
			ParentID:     scoutID,
			RefinedQuery: strings.TrimSpace(query),
		})
		graph.Edges = append(graph.Edges, ReasoningEdge{From: scoutID, To: nodeID, Label: "plans"})
	}
	return indexReasoningGraph(graph)
}

func mergeLiveReasoningGraph(base *ReasoningGraph, loopGraph *ReasoningGraph, loopResult *LoopResult) *ReasoningGraph {
	merged := cloneReasoningGraph(base)
	merged = absorbReasoningGraph(merged, loopGraph)
	if loopResult == nil {
		return indexReasoningGraph(merged)
	}

	parentForEvidence := merged.Root
	if parentForEvidence == "" && loopGraph != nil {
		parentForEvidence = loopGraph.Root
	}
	for idx, finding := range loopResult.Evidence {
		nodeID := stableWisDevID("runtime-evidence", finding.SourceID, fmt.Sprintf("%d", idx), finding.Claim)
		if reasoningNodeExists(merged, nodeID) {
			continue
		}
		merged.Nodes = append(merged.Nodes, ReasoningNode{
			ID:         nodeID,
			Text:       finding.Claim,
			Type:       ReasoningNodeEvidence,
			Label:      strings.TrimSpace(firstNonEmpty(finding.PaperTitle, "evidence")),
			Depth:      3,
			ParentID:   parentForEvidence,
			Confidence: finding.Confidence,
			SourceIDs:  dedupeTrimmedStrings([]string{finding.SourceID}),
			Metadata: map[string]any{
				"snippet": finding.Snippet,
				"status":  finding.Status,
			},
		})
		if parentForEvidence != "" {
			merged.Edges = append(merged.Edges, ReasoningEdge{From: parentForEvidence, To: nodeID, Label: "grounds"})
		}
	}
	if loopResult.GapAnalysis != nil {
		for idx, entry := range loopResult.GapAnalysis.Ledger {
			nodeID := stableWisDevID("runtime-ledger", entry.ID, fmt.Sprintf("%d", idx))
			if reasoningNodeExists(merged, nodeID) {
				continue
			}
			merged.Nodes = append(merged.Nodes, ReasoningNode{
				ID:         nodeID,
				Text:       strings.TrimSpace(firstNonEmpty(entry.Description, entry.Title)),
				Type:       ReasoningNodeClaim,
				Label:      strings.TrimSpace(entry.Category),
				Depth:      2,
				ParentID:   merged.Root,
				Confidence: entry.Confidence,
				Metadata: map[string]any{
					"status":            entry.Status,
					"supportingQueries": entry.SupportingQueries,
					"sourceFamilies":    entry.SourceFamilies,
				},
			})
			if strings.TrimSpace(merged.Root) != "" {
				merged.Edges = append(merged.Edges, ReasoningEdge{From: merged.Root, To: nodeID, Label: "ledger"})
			}
		}
	}
	return indexReasoningGraph(merged)
}

func cloneReasoningGraph(graph *ReasoningGraph) *ReasoningGraph {
	if graph == nil {
		return &ReasoningGraph{}
	}
	cloned := &ReasoningGraph{
		Query: graph.Query,
		Nodes: append([]ReasoningNode(nil), graph.Nodes...),
		Edges: append([]ReasoningEdge(nil), graph.Edges...),
		Root:  graph.Root,
	}
	return indexReasoningGraph(cloned)
}

func absorbReasoningGraph(base *ReasoningGraph, incoming *ReasoningGraph) *ReasoningGraph {
	if incoming == nil {
		return base
	}
	if base == nil {
		return cloneReasoningGraph(incoming)
	}
	for _, node := range incoming.Nodes {
		if reasoningNodeExists(base, node.ID) {
			continue
		}
		base.Nodes = append(base.Nodes, node)
	}
	seenEdges := map[string]struct{}{}
	for _, edge := range base.Edges {
		seenEdges[edge.From+"->"+edge.To+"::"+edge.Label] = struct{}{}
	}
	for _, edge := range incoming.Edges {
		key := edge.From + "->" + edge.To + "::" + edge.Label
		if _, exists := seenEdges[key]; exists {
			continue
		}
		seenEdges[key] = struct{}{}
		base.Edges = append(base.Edges, edge)
	}
	if strings.TrimSpace(base.Root) == "" {
		base.Root = incoming.Root
	}
	if strings.TrimSpace(base.Query) == "" {
		base.Query = incoming.Query
	}
	return indexReasoningGraph(base)
}

func reasoningNodeExists(graph *ReasoningGraph, nodeID string) bool {
	if graph == nil || strings.TrimSpace(nodeID) == "" {
		return false
	}
	for _, node := range graph.Nodes {
		if node.ID == nodeID {
			return true
		}
	}
	return false
}

func indexReasoningGraph(graph *ReasoningGraph) *ReasoningGraph {
	if graph == nil {
		return nil
	}
	graph.NodesMap = make(map[string]*ReasoningNode, len(graph.Nodes))
	for idx := range graph.Nodes {
		node := &graph.Nodes[idx]
		graph.NodesMap[node.ID] = node
	}
	return graph
}

func emitRuntimeBranchEvents(emit func(PlanExecutionEvent), state *ResearchSessionState, queries []string) {
	if emit == nil || state == nil {
		return
	}
	for idx, query := range queries {
		if idx >= 12 {
			emitRuntimeLifecycleEvent(emit, state, "branch_created", "additional branch creation events omitted", map[string]any{
				"omittedCount": len(queries) - idx,
			}, 0.5)
			return
		}
		emitRuntimeLifecycleEvent(emit, state, "branch_created", "research branch created", map[string]any{
			"branchIndex": idx,
			"query":       strings.TrimSpace(query),
			"plane":       state.Plane,
		}, 0.58)
	}
}

func emitRuntimeLedgerEvents(emit func(PlanExecutionEvent), state *ResearchSessionState, loopResult *LoopResult) {
	if emit == nil || state == nil {
		return
	}
	emitRuntimeLifecycleEvent(emit, state, "adaptive_budget", "adaptive research budget finalized", map[string]any{
		"budget": state.Budget,
	}, 0.68)
	if state.DurableJob != nil {
		stage := "research_job_completed"
		if state.DurableJob.Status == researchDurableJobStatusCancelled {
			stage = "research_job_cancelled"
		} else if state.DurableJob.Status == researchDurableJobStatusFailed {
			stage = "research_job_failed"
		}
		emitRuntimeLifecycleEvent(emit, state, stage, "durable research job state recorded", map[string]any{
			"jobId":       state.DurableJob.JobID,
			"status":      state.DurableJob.Status,
			"resumeToken": state.DurableJob.ResumeToken,
			"budgetUsed":  state.DurableJob.BudgetUsed,
			"storage":     state.DurableJob.Storage,
			"stopReason":  state.DurableJob.StopReason,
		}, 0.75)
	}
	if state.SourceAcquisition != nil {
		for idx, attempt := range state.SourceAcquisition.Attempts {
			if idx >= 24 {
				emitRuntimeLifecycleEvent(emit, state, "source_fetch_planned", "additional source acquisition attempts omitted", map[string]any{
					"omittedCount": len(state.SourceAcquisition.Attempts) - idx,
				}, 0.45)
				break
			}
			stage := "source_fetch_planned"
			confidence := 0.62
			if attempt.Status == sourceAcquisitionStatusSucceeded {
				stage = "source_fetched"
				confidence = 0.76
			} else if attempt.Status == sourceAcquisitionStatusFailed {
				stage = "source_fetch_failed"
				confidence = 0.38
			} else if attempt.NeedsPythonExtraction {
				stage = "pdf_extraction_queued"
				confidence = 0.64
			}
			emitRuntimeLifecycleEvent(emit, state, stage, "source acquisition attempt recorded", map[string]any{
				"sourceId":              attempt.SourceID,
				"canonicalId":           attempt.CanonicalID,
				"sourceType":            attempt.SourceType,
				"fetchUrl":              attempt.FetchURL,
				"workerPlane":           attempt.WorkerPlane,
				"status":                attempt.Status,
				"errorCode":             attempt.ErrorCode,
				"needsPythonExtraction": attempt.NeedsPythonExtraction,
			}, confidence)
		}
	}
	for idx, branch := range state.BranchEvaluations {
		if idx >= 24 {
			emitRuntimeLifecycleEvent(emit, state, "branch_score", "additional branch score events omitted", map[string]any{
				"omittedCount": len(state.BranchEvaluations) - idx,
			}, 0.45)
			break
		}
		emitRuntimeLifecycleEvent(emit, state, "branch_score", "research branch scored", map[string]any{
			"branchId":            branch.ID,
			"query":               branch.Query,
			"status":              branch.Status,
			"noveltyScore":        branch.NoveltyScore,
			"coverageScore":       branch.CoverageScore,
			"falsifiabilityScore": branch.FalsifiabilityScore,
			"evidenceScore":       branch.EvidenceScore,
			"overallScore":        branch.OverallScore,
			"openGaps":            branch.OpenGaps,
			"stopReason":          branch.StopReason,
		}, branch.OverallScore)
		if branch.StopReason == "branch_open_gap" || branch.StopReason == "branch_unexecuted" || branch.StopReason == "branch_needs_evidence" {
			emitRuntimeLifecycleEvent(emit, state, "branch_pruned", "research branch withheld from promotion", map[string]any{
				"branchId":   branch.ID,
				"query":      branch.Query,
				"openGaps":   branch.OpenGaps,
				"stopReason": branch.StopReason,
			}, 0.5)
		}
	}
	if loopResult != nil {
		for idx, paper := range loopResult.Papers {
			if idx >= 12 {
				emitRuntimeLifecycleEvent(emit, state, "source_fetched", "additional fetched-source events omitted", map[string]any{
					"omittedCount": len(loopResult.Papers) - idx,
				}, 0.5)
				break
			}
			emitRuntimeLifecycleEvent(emit, state, "source_fetched", "source fetched into research ledger", map[string]any{
				"sourceId": firstNonEmpty(strings.TrimSpace(paper.ID), strings.TrimSpace(paper.DOI), strings.TrimSpace(paper.ArxivID), strings.TrimSpace(paper.Title)),
				"title":    strings.TrimSpace(paper.Title),
				"provider": strings.TrimSpace(paper.Source),
			}, 0.7)
		}
	}
	for idx, entry := range state.CoverageLedger {
		if idx >= 24 {
			emitRuntimeLifecycleEvent(emit, state, "gap_opened", "additional ledger events omitted", map[string]any{
				"omittedCount": len(state.CoverageLedger) - idx,
			}, 0.45)
			break
		}
		stage := "gap_closed"
		confidence := 0.74
		if strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
			stage = "gap_opened"
			confidence = 0.52
		}
		emitRuntimeLifecycleEvent(emit, state, stage, "coverage ledger updated", map[string]any{
			"ledgerId":          entry.ID,
			"category":          entry.Category,
			"status":            entry.Status,
			"title":             entry.Title,
			"supportingQueries": entry.SupportingQueries,
			"sourceFamilies":    entry.SourceFamilies,
		}, confidence)
	}
	if state.ClaimVerification != nil {
		for _, record := range state.ClaimVerification.Records {
			stage := "claim_verified"
			confidence := record.Confidence
			if record.Status != "supported" {
				stage = "citation_rejected"
			}
			emitRuntimeLifecycleEvent(emit, state, stage, "claim verification updated", map[string]any{
				"claimId":             record.ID,
				"claim":               record.Claim,
				"status":              record.Status,
				"citationHealth":      record.CitationHealth,
				"contradictionStatus": record.ContradictionStatus,
				"supportCount":        record.SupportCount,
				"followUpQueries":     record.FollowUpQueries,
			}, confidence)
			claimStage := "claim_promoted"
			if record.Status != "supported" {
				claimStage = "claim_rejected"
			}
			emitRuntimeLifecycleEvent(emit, state, claimStage, "claim arbitration updated", map[string]any{
				"claimId":             record.ID,
				"claim":               record.Claim,
				"status":              record.Status,
				"citationHealth":      record.CitationHealth,
				"contradictionStatus": record.ContradictionStatus,
				"supportCount":        record.SupportCount,
			}, confidence)
		}
	}
	if state.VerifierDecision != nil {
		stage := "verifier_passed"
		if state.VerifierDecision.Verdict != "promote" {
			stage = "verifier_failed"
		}
		emitRuntimeLifecycleEvent(emit, state, stage, "independent verifier decision recorded", map[string]any{
			"verdict":          state.VerifierDecision.Verdict,
			"stopReason":       state.VerifierDecision.StopReason,
			"promotedClaimIds": state.VerifierDecision.PromotedClaimIDs,
			"rejectedClaimIds": state.VerifierDecision.RejectedClaimIDs,
			"revisionReasons":  state.VerifierDecision.RevisionReasons,
		}, state.VerifierDecision.Confidence)
	}
	emitRuntimeLifecycleEvent(emit, state, "final_stop_reason", "unified research runtime stopped", map[string]any{
		"stopReason":        state.StopReason,
		"openLedgerCount":   countOpenCoverageLedgerEntries(state.CoverageLedger),
		"claimVerification": state.ClaimVerification,
	}, 0.7)
}

func emitRuntimeLifecycleEvent(
	emit func(PlanExecutionEvent),
	state *ResearchSessionState,
	stage string,
	message string,
	payload map[string]any,
	confidence float64,
) {
	if emit == nil || state == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["component"] = "wisdev.runtime"
	payload["runtime"] = "go"
	payload["operation"] = "unified_research_runtime"
	payload["stage"] = strings.TrimSpace(stage)
	payload["researchPlane"] = state.Plane
	emit(PlanExecutionEvent{
		Type:               EventProgress,
		TraceID:            NewTraceID(),
		SessionID:          strings.TrimSpace(state.SessionID),
		Message:            strings.TrimSpace(message),
		Payload:            payload,
		Owner:              "go",
		SubAgent:           "unified_research_runtime",
		OwningComponent:    "wisdev-agent-os/orchestrator/internal/wisdev",
		ResultOrigin:       strings.TrimSpace(stage),
		ResultConfidence:   ClampFloat(confidence, 0, 1),
		ResultFusionIntent: "runtime_trace",
		CreatedAt:          NowMillis(),
	})
}

func buildRuntimeCitations(loopResult *LoopResult) []rag.Citation {
	if loopResult == nil {
		return nil
	}
	citations := make([]rag.Citation, 0, len(loopResult.Evidence))
	for _, finding := range loopResult.Evidence {
		if strings.TrimSpace(finding.Claim) == "" || strings.TrimSpace(finding.SourceID) == "" {
			continue
		}
		citations = append(citations, rag.Citation{
			Claim:           strings.TrimSpace(finding.Claim),
			SourceID:        strings.TrimSpace(finding.SourceID),
			SourceTitle:     strings.TrimSpace(firstNonEmpty(finding.PaperTitle, finding.SourceID)),
			Confidence:      finding.Confidence,
			CredibilityTier: runtimeCredibilityTier(finding.Confidence),
			Category:        runtimeCitationCategory(finding),
		})
	}
	if len(citations) > 12 {
		citations = citations[:12]
	}
	return citations
}

func runtimeCredibilityTier(confidence float64) string {
	switch {
	case confidence >= 0.9:
		return "High Credibility"
	case confidence >= 0.75:
		return "Established"
	case confidence >= 0.6:
		return "Moderate"
	default:
		return "Provisional"
	}
}

func runtimeCitationCategory(finding EvidenceFinding) string {
	if len(finding.Keywords) > 0 && strings.TrimSpace(finding.Keywords[0]) != "" {
		return strings.TrimSpace(finding.Keywords[0])
	}
	return "evidence"
}

func extractProgrammaticQueriesFromTreeResult(result treeLoopResult) []string {
	queries := extractProgrammaticQueries(result.Final)
	if len(queries) == 0 {
		for _, iteration := range result.Iterations {
			if len(queries) > 0 {
				break
			}
			queries = extractProgrammaticQueries(iteration.Output)
		}
	}
	return normalizeLoopQueries("", queries)
}

func extractProgrammaticQueries(result map[string]any) []string {
	if len(result) == 0 {
		return nil
	}

	queries := make([]string, 0)
	appendQuery := func(candidate string) {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			return
		}
		queries = append(queries, trimmed)
	}

	switch tasks := result["tasks"].(type) {
	case []ResearchTask:
		for _, task := range tasks {
			appendQuery(task.Name)
		}
	case []map[string]any:
		for _, task := range tasks {
			appendQuery(firstNonEmpty(
				AsOptionalString(task["name"]),
				AsOptionalString(task["label"]),
				AsOptionalString(task["query"]),
			))
		}
	case []any:
		for _, rawTask := range tasks {
			task := asMap(rawTask)
			if len(task) == 0 {
				continue
			}
			appendQuery(firstNonEmpty(
				AsOptionalString(task["name"]),
				AsOptionalString(task["label"]),
				AsOptionalString(task["query"]),
			))
		}
	}
	return normalizeLoopQueries("", queries)
}
