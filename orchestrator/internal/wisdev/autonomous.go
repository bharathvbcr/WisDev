package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

// AutonomousLoop handles the budgeted research iteration.
type AutonomousLoop struct {
	searchReg          *search.ProviderRegistry
	llmClient          *llm.Client
	brainCaps          *BrainCapabilities
	evaluator          *HypothesisEvaluator
	beliefManager      *BeliefStateManager
	hypothesisExplorer *HypothesisExplorer
}

const optionalCritiqueRefinementLatencyBudget = 15 * time.Second

// assembleDossierMaxConcurrentExtractions bounds the per-paper LLM evidence
// extraction fan-out in assembleDossier.
const assembleDossierMaxConcurrentExtractions = 3

func NewAutonomousLoop(reg *search.ProviderRegistry, llm *llm.Client) *AutonomousLoop {
	var brainCaps *BrainCapabilities
	if llm != nil {
		brainCaps = NewBrainCapabilities(llm)
	}

	evaluator := NewHypothesisEvaluator(brainCaps)

	beliefManager := NewBeliefStateManager()

	var hypothesisExplorer *HypothesisExplorer
	if reg != nil {
		hypothesisExplorer = NewHypothesisExplorer(reg, evaluator, brainCaps, 3)
	}

	return &AutonomousLoop{
		searchReg:          reg,
		llmClient:          llm,
		brainCaps:          brainCaps,
		evaluator:          evaluator,
		beliefManager:      beliefManager,
		hypothesisExplorer: hypothesisExplorer,
	}
}

type LoopRequest struct {
	Query                          string                    `json:"query"`
	OriginalQuery                  string                    `json:"originalQuery,omitempty"`
	DisableQueryEnhance            bool                      `json:"disableQueryEnhance,omitempty"`
	SeedQueries                    []string                  `json:"seedQueries,omitempty"`
	InitialPapers                  []search.Paper            `json:"initialPapers,omitempty"`
	InitialQueryCoverage           map[string][]search.Paper `json:"initialQueryCoverage,omitempty"`
	InitialExecutedQueries         []string                  `json:"initialExecutedQueries,omitempty"`
	InitialMemoryTiers             *MemoryTierState          `json:"initialMemoryTiers,omitempty"`
	ResearchPlane                  ResearchExecutionPlane    `json:"researchPlane,omitempty"`
	Domain                         string                    `json:"domain"`
	ProjectID                      string                    `json:"projectId"`
	MaxIterations                  int                       `json:"maxIterations"`
	MinIterations                  int                       `json:"minIterations,omitempty"`
	MaxSearchTerms                 int                       `json:"maxSearchTerms,omitempty"`
	BudgetCents                    int                       `json:"budgetCents"`
	HitsPerSearch                  int                       `json:"hitsPerSearch,omitempty"`
	MaxUniquePapers                int                       `json:"maxUniquePapers,omitempty"`
	AllocatedTokens                int                       `json:"allocatedTokens,omitempty"`
	DurableJobID                   string                    `json:"durableJobId,omitempty"`
	Mode                           string                    `json:"mode,omitempty"`
	ServiceTier                    ServiceTier               `json:"serviceTier,omitempty"`
	TraceID                        string                    `json:"traceId,omitempty"`
	EnableDynamicProviderSelection bool                      `json:"enableDynamicProviderSelection,omitempty"`
	BypassSearchCache              bool                      `json:"bypassSearchCache,omitempty"`
	DisableProgrammaticPlanning    bool                      `json:"disableProgrammaticPlanning,omitempty"`
	DisableHypothesisGeneration    bool                      `json:"disableHypothesisGeneration,omitempty"`
	SteeringChan                   <-chan SteeringSignal     `json:"-"`
	SteeringJournal                *RuntimeJournal           `json:"-"`
}

func shouldBypassLoopSearchCache(req LoopRequest) bool {
	return req.BypassSearchCache
}

type LoopResult struct {
	FinalAnswer      string                    `json:"finalAnswer"`
	StructuredAnswer *rag.StructuredAnswer     `json:"structuredAnswer,omitempty"`
	Papers           []search.Paper            `json:"papers"`
	Evidence         []EvidenceFinding         `json:"evidence"`
	Branches         []ResearchBranch          `json:"branches,omitempty"`
	Hypotheses       []Hypothesis              `json:"hypotheses,omitempty"`
	Iterations       int                       `json:"iterations"`
	Converged        bool                      `json:"converged"`
	BranchPlans      []ResearchBranchPlan      `json:"branchPlans,omitempty"`
	ExecutedQueries  []string                  `json:"executedQueries,omitempty"`
	QueryCoverage    map[string][]search.Paper `json:"queryCoverage,omitempty"`
	GapAnalysis      *LoopGapState             `json:"gapAnalysis,omitempty"`
	DraftCritique    *LoopDraftCritique        `json:"draftCritique,omitempty"`
	FinalizationGate *ResearchFinalizationGate `json:"finalizationGate,omitempty"`
	StopReason       string                    `json:"stopReason,omitempty"`
	SynthesisMode    string                    `json:"synthesisMode,omitempty"`
	ReasoningGraph   *ReasoningGraph           `json:"reasoningGraph,omitempty"`
	MemoryTiers      *MemoryTierState          `json:"memoryTiers,omitempty"`
	WorkerReports    []ResearchWorkerState     `json:"workerReports,omitempty"`
	RuntimeState     *ResearchSessionState     `json:"runtimeState,omitempty"`
	Mode             WisDevMode                `json:"mode,omitempty"`
	ServiceTier      ServiceTier               `json:"serviceTier,omitempty"`
	BeliefState      *BeliefState              `json:"beliefState,omitempty"` // R2: Belief tracking
	Lineage          *ResearchLineage          `json:"lineage,omitempty"`     // R4: Provenance lineage
	ReasoningTrace   []ReasoningTraceEntry     `json:"reasoningTrace,omitempty"`
}

type LoopCoverageState struct {
	PlannedQueryCount        int      `json:"plannedQueryCount"`
	ExecutedQueryCount       int      `json:"executedQueryCount"`
	CoveredQueryCount        int      `json:"coveredQueryCount"`
	UniquePaperCount         int      `json:"uniquePaperCount"`
	QueriesWithoutCoverage   []string `json:"queriesWithoutCoverage,omitempty"`
	UnexecutedPlannedQueries []string `json:"unexecutedPlannedQueries,omitempty"`
}

type LoopGapState struct {
	Sufficient             bool                  `json:"sufficient"`
	Reasoning              string                `json:"reasoning,omitempty"`
	NextQueries            []string              `json:"nextQueries,omitempty"`
	MissingAspects         []string              `json:"missingAspects,omitempty"`
	MissingSourceTypes     []string              `json:"missingSourceTypes,omitempty"`
	Contradictions         []string              `json:"contradictions,omitempty"`
	Confidence             float64               `json:"confidence,omitempty"`
	ObservedSourceFamilies []string              `json:"observedSourceFamilies,omitempty"`
	ObservedEvidenceCount  int                   `json:"observedEvidenceCount,omitempty"`
	Ledger                 []CoverageLedgerEntry `json:"ledger,omitempty"`
	Coverage               LoopCoverageState     `json:"coverage"`
}

type EvidenceItem struct {
	Claim      string  `json:"claim"`
	Snippet    string  `json:"snippet"`
	PaperTitle string  `json:"paperTitle"`
	PaperID    string  `json:"paperId"`
	Status     string  `json:"status,omitempty"`
	Confidence float64 `json:"confidence"`
}

func (l *AutonomousLoop) Run(ctx context.Context, req LoopRequest, onEvent ...func(PlanExecutionEvent)) (*LoopResult, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, fmt.Errorf("autonomous loop: query is required")
	}
	if l == nil || l.searchReg == nil {
		return nil, fmt.Errorf("autonomous loop: search registry is not initialized")
	}
	var emit func(PlanExecutionEvent)
	if len(onEvent) > 0 {
		emit = onEvent[0]
	}
	if emit != nil {
		ctx = WithLoopProgress(ctx, &LoopProgressEmitter{Emit: emit, Req: req})
	}
	loopLLMClient := GlobalLLMClient
	if l.llmClient != nil {
		loopLLMClient = l.llmClient
	}
	researchQuery, originalQuery, queryPrep := resolveLoopResearchQuery(ctx, loopLLMClient, req)
	if strings.TrimSpace(req.OriginalQuery) == "" {
		req.OriginalQuery = originalQuery
	}
	req.Query = researchQuery
	if queryPrep.Changed && researchQuery != originalQuery {
		slog.Info("Query grammar corrected for autonomous loop",
			"component", "wisdev.autonomous",
			"operation", "run",
			"stage", "query_prepared",
			"original_query", originalQuery,
			"corrected_query", researchQuery,
		)
		EmitLoopStage(ctx, "query_prepared", fmt.Sprintf("Query corrected: %s → %s", QueryPreview(originalQuery), QueryPreview(researchQuery)), map[string]any{
			"original_query":  originalQuery,
			"corrected_query": researchQuery,
		})
	}
	plannedQueries := buildAutonomousResearchAgendaQueries(req.Query, req.Domain, req.Mode, req.ResearchPlane, req.SeedQueries)
	initialAgendaQueries := append([]string(nil), plannedQueries...)
	// Cross-session domain learning: when this domain has historically
	// underperformed, widen retrieval up front instead of rediscovering the
	// shortfall mid-run.
	if historical := GlobalDomainOutcomes.HistoricalReward(req.Domain); historical < 0.45 {
		if req.HitsPerSearch > 0 {
			req.HitsPerSearch = minInt(req.HitsPerSearch+2, req.HitsPerSearch*2)
		}
		slog.Info("Domain warm start: widening retrieval for historically weak domain",
			"component", "wisdev.domain_learning",
			"domain", req.Domain,
			"historicalReward", historical,
			"hitsPerSearch", req.HitsPerSearch)
	}
	slog.Info("Starting autonomous research loop",
		"component", "wisdev.autonomous",
		"operation", "run",
		"stage", "loop_started",
		"trace_id", strings.TrimSpace(req.TraceID),
		"session_id", strings.TrimSpace(req.ProjectID),
		"query", req.Query,
		"original_query", req.OriginalQuery,
		"mode", strings.TrimSpace(req.Mode),
		"execution_mode", strings.TrimSpace(req.Mode),
		"research_plane", string(req.ResearchPlane),
		"maxIterations", req.MaxIterations,
		"max_search_terms", req.MaxSearchTerms,
		"hits_per_search", req.HitsPerSearch,
		"max_unique_papers", req.MaxUniquePapers,
		"seedQueryCount", maxInt(len(plannedQueries)-1, 0),
		"bypass_search_cache", shouldBypassLoopSearchCache(req),
	)
	if emit != nil {
		emitLoopProgress(emit, req, "loop_started", "autonomous loop started", map[string]any{
			"mode":              strings.TrimSpace(req.Mode),
			"executionMode":     strings.TrimSpace(req.Mode),
			"query":             req.Query,
			"original_query":    req.OriginalQuery,
			"maxIterations":     req.MaxIterations,
			"maxSearchTerms":    req.MaxSearchTerms,
			"hitsPerSearch":     req.HitsPerSearch,
			"maxUniquePapers":   req.MaxUniquePapers,
			"seedQueryCount":    maxInt(len(plannedQueries)-1, 0),
			"dynamicProviders":  req.EnableDynamicProviderSelection,
			"bypassSearchCache": shouldBypassLoopSearchCache(req),
		})
	}

	papers, _ := admitSearchPapersForQuery(nil, req.Query, req.InitialPapers, maxInt(req.MaxUniquePapers, 0))
	iterations := 0
	converged := false
	executedQueries := normalizeLoopQueries("", req.InitialExecutedQueries)
	queryCoverage := cloneLoopQueryCoverage(req.InitialQueryCoverage)
	pendingQueries := filterUnexecutedLoopQueries(plannedQueries, executedQueries)
	querySeen := make(map[string]struct{}, len(plannedQueries))
	hitsPerSearch := resolveLoopHitsPerSearch(req.HitsPerSearch)
	maxUniquePapers := maxInt(req.MaxUniquePapers, 0)
	maxLoopIterations := maxInt(req.MaxIterations, 0)
	searchTermBudget := resolveLoopSearchTermBudget(req.MaxIterations, req.MaxSearchTerms)
	queryParallelism := resolveLoopQueryParallelism(req.Mode, req.ResearchPlane)
	if maxLoopIterations <= 0 {
		maxLoopIterations = searchTermBudget
	}
	var lastAnalysis *sufficiencyAnalysis
	queueCandidate := func(candidate string) bool {
		if enqueueLoopQuery(&pendingQueries, querySeen, candidate) {
			plannedQueries = appendUniqueLoopQuery(plannedQueries, candidate)
			return true
		}
		return false
	}
	queuePriorityCandidate := func(candidate string) bool {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			return false
		}
		key := strings.ToLower(trimmed)
		if _, exists := querySeen[key]; exists {
			return false
		}
		querySeen[key] = struct{}{}
		pendingQueries = append([]string{trimmed}, pendingQueries...)
		plannedQueries = appendUniqueLoopQuery(plannedQueries, trimmed)
		return true
	}
	for _, plannedQuery := range plannedQueries {
		querySeen[strings.ToLower(plannedQuery)] = struct{}{}
	}
	if req.MaxSearchTerms > 0 && req.MaxSearchTerms != req.MaxIterations {
		slog.Info("autonomous loop capped by search-term budget",
			"component", "wisdev.autonomous",
			"operation", "run",
			"requestedIterations", req.MaxIterations,
			"maxSearchTerms", req.MaxSearchTerms,
			"maxLoopCycles", maxLoopIterations,
			"searchTermBudget", searchTermBudget,
		)
	}

	var hypotheses []Hypothesis
	var findings []EvidenceFinding
	var gapAnalysis *LoopGapState
	var reasoningTrace []ReasoningTraceEntry
	var completedBranches []ResearchBranch
	branchPlans := researchBranchPlansFromQueries(req.Query, plannedQueries)
	reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
		Timestamp:    NowMillis(),
		Phase:        "planning",
		Decision:     "cot_plan_summary",
		Reasoning:    fmt.Sprintf("Structured reasoning summary: decomposed the research goal into %d dependency-aware branch plans before retrieval.", len(branchPlans)),
		Alternatives: researchBranchPlanQueries(branchPlans),
	})

	if shouldSeedPreRetrievalHypotheses(req) {
		hypotheses = l.proposeLoopHypotheses(ctx, req.Query, plannedQueries, nil, queryCoverage, 0, req.DisableHypothesisGeneration)
		hypothesisPlans := researchBranchPlansFromHypotheses(req.Query, hypotheses)
		if len(hypothesisPlans) > 0 {
			branchPlans = mergeResearchBranchPlans(req.Query, hypothesisPlans, branchPlans)
			// Cap hypothesis-probe enqueues to half the search-term budget so the
			// probes cannot crowd out the user's agenda/topic queries when
			// MaxSearchTerms is small.
			hypothesisProbeCap := maxInt(1, searchTermBudget/2)
			hypothesisProbesEnqueued := 0
			hypothesisProbesSkipped := 0
			for planIndex := len(hypothesisPlans) - 1; planIndex >= 0; planIndex-- {
				retrievalPlan := hypothesisPlans[planIndex].RetrievalPlan
				for queryIndex := len(retrievalPlan) - 1; queryIndex >= 0; queryIndex-- {
					if hypothesisProbesEnqueued >= hypothesisProbeCap {
						hypothesisProbesSkipped++
						continue
					}
					if queuePriorityCandidate(retrievalPlan[queryIndex]) {
						hypothesisProbesEnqueued++
					}
				}
			}
			if hypothesisProbesSkipped > 0 {
				slog.Info("autonomous loop capped hypothesis-probe enqueues to preserve agenda budget",
					"component", "wisdev.autonomous",
					"operation", "run",
					"stage", "hypothesis_probe_budget_capped",
					"enqueued", hypothesisProbesEnqueued,
					"skipped", hypothesisProbesSkipped,
					"cap", hypothesisProbeCap,
				)
			}
			if l.beliefManager != nil {
				l.beliefManager.BuildBeliefsFromHypotheses(toHypothesisPtrs(hypotheses), nil, nil, req.Query)
			}
			reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
				Timestamp:    NowMillis(),
				Phase:        "planning",
				Decision:     "pre_retrieval_hypotheses",
				Reasoning:    fmt.Sprintf("Seeded %d hypotheses before first retrieval and planned falsification-aware searches.", len(hypothesisPlans)),
				Alternatives: researchBranchPlanQueries(hypothesisPlans),
			})
			EmitLoopStage(ctx, "pre_retrieval_hypotheses", fmt.Sprintf("Seeded %d falsification-aware hypothesis branches before retrieval", len(hypothesisPlans)), map[string]any{
				"hypothesisCount": len(hypothesisPlans),
				"queries":         researchBranchPlanQueries(hypothesisPlans),
			})
		}
	}

	steeringChan := req.SteeringChan
	if steeringChan == nil {
		ReplayJournaledSteeringSignals(req.ProjectID, req.SteeringJournal, 64)
		var unregister func()
		steeringChan, unregister = RegisterSteeringChannel(req.ProjectID)
		defer unregister()
	}

	for i := 0; i < maxLoopIterations; i++ {
		var bs *BeliefState
		if l.beliefManager != nil {
			bs = l.beliefManager.GetState()
		}

		// 3.3 Mid-Session User Steering
		if steeringChan != nil {
			select {
			case signal := <-steeringChan:
				l.applySteeringSignal(ctx, signal, &pendingQueries, bs, executedQueries)
			default:
				// No steering signal available, continue normally
			}
		}

		decision := l.beliefDrivenContinuation(bs, searchTermBudget, len(executedQueries), i)

		// Record decision in reasoning trace
		inputBeliefs := make(map[string]float64)
		if bs != nil {
			for id, b := range bs.Beliefs {
				inputBeliefs[id] = b.Confidence
			}
		}
		reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
			Timestamp:    NowMillis(),
			Phase:        "loop_control",
			Decision:     fmt.Sprintf("Continue=%v", decision.ShouldContinue),
			Reasoning:    decision.Reason,
			InputBeliefs: inputBeliefs,
		})

		if !decision.ShouldContinue {
			if loopMinimumIterationsMet(req, iterations) {
				slog.Info("belief-driven continuation stopped loop", "reason", decision.Reason)
				if decision.Reason == "belief convergence" {
					converged = true
				}
				break
			}
			slog.Info("deferring belief-driven stop until minimum iterations",
				"component", "wisdev.autonomous",
				"operation", "run",
				"reason", decision.Reason,
				"iterations", iterations,
				"minIterations", req.MinIterations,
			)
		}
		iterations++
		remainingTerms := searchTermBudget - len(executedQueries)
		EmitLoopStage(ctx, "loop_iteration", fmt.Sprintf("Loop iteration %d started", iterations), map[string]any{
			"iteration":      iterations,
			"remainingTerms": remainingTerms,
			"paperCount":     len(papers),
		})

		// Consume belief-driven query strategy. Feedback queries are rooted in
		// the original research task so follow-up searches stay comparable with
		// the broader agenda and remain bounded by the remaining search budget.
		var feedbackQueries []string
		if shouldEnableBeliefFeedback(req, initialAgendaQueries, executedQueries) {
			feedbackQueries = buildBeliefFeedbackQueries(req.Query, decision, bs, minInt(remainingTerms, 4))
		}
		for feedbackIndex := len(feedbackQueries) - 1; feedbackIndex >= 0; feedbackIndex-- {
			candidate := feedbackQueries[feedbackIndex]
			if queuePriorityCandidate(candidate) {
				slog.Info("Belief feedback: queued targeted query",
					"component", "wisdev.autonomous",
					"operation", "belief_feedback",
					"strategy", decision.QueryStrategy,
					"query", candidate)
			}
		}

		// Phase 6: Prune obsolete queries based on current belief state (R2/R5)
		if l.beliefManager != nil {
			if pruned := l.pruneObsoleteQueries(&pendingQueries, l.beliefManager.GetState()); pruned > 0 {
				slog.Debug("Pruned obsolete queries from pool", "count", pruned)
			}
		}

		llmCooldown := autonomousLLMCooldownRemaining(l)

		// R5 Refinement: Adaptive parallelism based on current confidence
		confidence := 0.0
		if lastAnalysis != nil {
			confidence = lastAnalysis.Confidence
		}
		currentParallelism := resolveCooldownAwareParallelism(l.resolveAdaptiveParallelism(req.Mode, confidence, req.ResearchPlane), llmCooldown)

		// 1. Retrieval: execute independent research branches concurrently.
		currentSearchLimit := remainingLoopSearchLimit(len(papers), hitsPerSearch, maxUniquePapers)
		if currentSearchLimit <= 0 {
			slog.Info("autonomous loop stopping because unique-paper budget is exhausted",
				"component", "wisdev.autonomous",
				"operation", "run",
				"maxUniquePapers", maxUniquePapers,
				"totalPapers", len(papers),
			)
			break
		}
		searchOpts := search.SearchOpts{
			Limit:            currentSearchLimit,
			Domain:           req.Domain,
			TraceID:          strings.TrimSpace(req.TraceID),
			QualitySort:      true,
			DynamicProviders: shouldUseDynamicProviderSelection(req.Mode, req.ResearchPlane, req.EnableDynamicProviderSelection, l.llmClient),
			SkipCache:        shouldBypassLoopSearchCache(req),
			LLMClient:        l.llmClient,
		}

		newCount := 0

		// R3: Concurrent Hypothesis Exploration (Phase 2)
		// Active on high-depth planes as soon as the loop has evidence and a
		// qualitative analysis to branch from.
		availableTreeQueries := searchTermBudget - len(executedQueries)
		phase2Active := availableTreeQueries > 0 &&
			llmCooldown <= 0 &&
			lastAnalysis != nil &&
			isHighDepthResearchPlane(req.ResearchPlane) &&
			len(papers) > 0 &&
			l.hypothesisExplorer != nil &&
			!req.DisableHypothesisGeneration

		if phase2Active {
			// Refresh hypotheses and evaluate them to guide exploration
			findings, hypotheses = l.refreshLoopReasoning(ctx, req, papers, queryCoverage, gapAnalysis, "")

			if len(hypotheses) > 0 {
				slog.Info("Autonomous loop switching to Phase 2: Concurrent Hypothesis Exploration",
					"iteration", i+1,
					"hypothesisCount", len(hypotheses),
					"plane", string(req.ResearchPlane))

				// P2B: Prioritize under-supported hypotheses using a lightweight reasoning graph
				explorationHypotheses := hypotheses
				inLoopGraph := BuildReasoningGraph(req.Query, hypotheses, findings)
				if targets := SuggestExplorationTargets(inLoopGraph, hypotheses, currentParallelism); len(targets) > 0 {
					slog.Info("Graph-driven exploration: prioritizing under-supported hypotheses",
						"targetCount", len(targets), "totalCount", len(hypotheses))
					explorationHypotheses = targets
				}

				// Belief-state-driven hypothesis prioritization:
				// Deprioritize hypotheses whose beliefs are already high-confidence.
				if l.beliefManager != nil {
					bs := l.beliefManager.GetState()
					explorationHypotheses = l.deprioritizeHighConfidenceHypotheses(explorationHypotheses, bs)
				}

				treeQueriesPerBranch := minInt(2, maxInt(1, availableTreeQueries))
				treeMaxBranches := minInt(5, maxInt(1, availableTreeQueries/treeQueriesPerBranch))
				if len(explorationHypotheses) > treeMaxBranches {
					explorationHypotheses = explorationHypotheses[:treeMaxBranches]
				}

				// R5: Adaptive compute allocation
				l.computeAdaptiveBudgets(toHypothesisPtrs(explorationHypotheses), currentParallelism)

				treeRuntime := NewTreeSearchRuntime(l, l.hypothesisExplorer, TreeSearchConfig{
					MaxBranches:      treeMaxBranches,
					PruneBelow:       0.3,
					MaxDepth:         2,
					MaxUniquePapers:  maxUniquePapers,
					Parallelism:      currentParallelism,
					QueriesPerBranch: treeQueriesPerBranch,
					DisableAdvance:   req.MaxSearchTerms > 0,
				})
				emitLoopProgress(emit, req, "tree_search_started", "isolated tree search started", map[string]any{
					"runtimeMode":       "tree_search",
					"hypothesisCount":   len(explorationHypotheses),
					"maxBranches":       treeMaxBranches,
					"maxDepth":          2,
					"parallelism":       currentParallelism,
					"queriesPerBranch":  treeQueriesPerBranch,
					"disableAdvance":    req.MaxSearchTerms > 0,
					"treeSearchRuntime": true,
				})
				treeResult := treeRuntime.Run(ctx, req, explorationHypotheses, searchOpts, queryCoverage)
				completedBranches = append(completedBranches, treeResult.Branches...)
				reasoningTrace = append(reasoningTrace, treeResult.Trace...)
				hypotheses = append(hypotheses, treeResult.SpawnedHypotheses...)
				// Collapse near-duplicate branches so hypotheses iterate instead
				// of accumulating across loop iterations.
				hypotheses = MergeSimilarHypotheses(hypotheses)
				// Keep the belief ledger consistent with the hypothesis ledger:
				// merged/refuted hypotheses must not leave live beliefs behind.
				if l.beliefManager != nil {
					l.beliefManager.RetireBeliefsForInactiveHypotheses(hypotheses)
				}
				for _, branch := range treeResult.Branches {
					for _, query := range branch.ExecutedQueries {
						plannedQueries = appendUniqueLoopQuery(plannedQueries, query)
						executedQueries = appendUniqueLoopQuery(executedQueries, query)
					}
				}
				if treeResult.MergeCandidate != nil {
					beforeTreeMerge := len(papers)
					papers, _ = admitSearchPapersForQuery(papers, req.Query, treeResult.MergeCandidate.Papers, maxUniquePapers)
					newCount += len(papers) - beforeTreeMerge
				}

				// Incremental belief update only after isolated tree-search commits a merge candidate.
				if l.beliefManager != nil && len(treeResult.Findings) > 0 {
					l.beliefManager.BuildBeliefsFromHypotheses(toHypothesisPtrs(hypotheses), treeResult.Findings, gapAnalysis, req.Query)
					l.beliefManager.TriangulateBeliefs(papers)
					treeResult.Findings = l.beliefManager.RecalibrateEvidenceConfidence(treeResult.Findings)
					slog.Debug("Belief state updated after isolated tree-search merge",
						"activeBeliefs", len(l.beliefManager.GetState().GetActiveBeliefs()))
				}

				// Belief-state convergence fast-path:
				// If all active beliefs are already high-confidence, skip further iterations.
				if l.beliefManager != nil && shouldConvergeByBeliefState(l.beliefManager.GetState()) {
					if shouldAllowLoopEarlyStop(req, iterations) {
						slog.Info("Belief state convergence: all active beliefs high-confidence, stopping loop early",
							"iteration", i+1)
						converged = true
						break
					}
					slog.Info("deferring belief convergence stop until minimum iterations",
						"component", "wisdev.autonomous",
						"operation", "run",
						"iterations", iterations,
						"minIterations", req.MinIterations,
					)
				}
			}
		}

		// If newCount is 0, it means we either didn't do Phase 2 or Phase 2 found nothing new.
		// Fall back to Phase 1 (Broad search) for the current batch.
		if newCount == 0 {
			batch := nextLoopQueryBatch(&pendingQueries, minInt(currentParallelism, remainingTerms))
			if len(batch) == 0 {
				if !shouldAllowLoopEarlyStop(req, iterations) {
					if enqueueExhaustiveContinuationQueries(req, &pendingQueries, querySeen, executedQueries) {
						batch = nextLoopQueryBatch(&pendingQueries, minInt(currentParallelism, remainingTerms))
						slog.Info("exhaustive mode queued continuation queries",
							"component", "wisdev.autonomous",
							"operation", "run",
							"iterations", iterations,
							"minIterations", req.MinIterations,
							"batchSize", len(batch),
						)
					}
				}
				if len(batch) == 0 {
					break
				}
			}
			slog.Info("Loop iteration (Phase 1)", "index", i+1, "queryCount", len(batch), "queries", batch)
			reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
				Timestamp:    NowMillis(),
				Phase:        "retrieval",
				Decision:     "react_action_retrieve",
				Reasoning:    fmt.Sprintf("Action: retrieve evidence for %d queued branch query or queries selected from the DAG frontier.", len(batch)),
				Alternatives: append([]string(nil), batch...),
			})
			EmitLoopStage(ctx, "search_batch_started", fmt.Sprintf("Searching %d queries", len(batch)), map[string]any{
				"iteration":  iterations,
				"queryCount": len(batch),
				"queries":    batch,
			})

			batchResults := l.executeLoopSearchBatch(ctx, batch, searchOpts, currentParallelism)
			phase1Findings := make([]EvidenceFinding, 0)
			for _, batchResult := range batchResults {
				executedQueries = appendUniqueLoopQuery(executedQueries, batchResult.Query)
				beforeCount := len(papers)
				var acceptedPapers []search.Paper
				papers, acceptedPapers = admitSearchPapersForQuery(papers, req.Query, batchResult.Result.Papers, maxUniquePapers)
				recordLoopQueryCoverage(queryCoverage, batchResult.Query, acceptedPapers)
				newCount += len(papers) - beforeCount
				slog.Info("autonomous loop search result admitted",
					"component", "wisdev.autonomous",
					"operation", "research_loop",
					"stage", "search_result_admitted",
					"trace_id", strings.TrimSpace(req.TraceID),
					"session_id", strings.TrimSpace(req.ProjectID),
					"query_preview", QueryPreview(batchResult.Query),
					"raw_result_count", batchResult.Result.RawResultCount,
					"fused_result_count", batchResult.Result.FusedResultCount,
					"deduped_count", batchResult.Result.DedupedCount,
					"final_count", batchResult.Result.FinalCount,
					"accepted_count", len(acceptedPapers),
					"new_unique_count", len(papers)-beforeCount,
					"total_unique_count", len(papers),
					"max_unique_papers", maxUniquePapers,
					"limit_applied", batchResult.Result.LimitApplied,
					"providers", batchResult.Result.Providers,
					"warning_count", len(batchResult.Result.Warnings),
				)
				EmitLoopStage(ctx, "search_result_admitted", fmt.Sprintf("Admitted %d papers for %s", len(acceptedPapers), QueryPreview(batchResult.Query)), map[string]any{
					"query_preview":      QueryPreview(batchResult.Query),
					"accepted_count":     len(acceptedPapers),
					"new_unique_count":   len(papers) - beforeCount,
					"total_unique_count": len(papers),
					"providers":          batchResult.Result.Providers,
				})
				for _, p := range acceptedPapers {
					phase1Findings = append(phase1Findings, EvidenceFinding{
						ID:         stableWisDevID("p1finding", batchResult.Query, p.ID),
						Claim:      p.Title,
						Snippet:    evidenceTextFromPaper(p),
						PaperTitle: p.Title,
						SourceID:   p.ID,
						Confidence: calculateInitialConfidence(p),
						Year:       p.Year,
					})
				}
				if maxUniquePapers > 0 && len(papers) >= maxUniquePapers {
					break
				}
			}
			reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
				Timestamp: NowMillis(),
				Phase:     "retrieval",
				Decision:  "react_observation_evidence",
				Reasoning: fmt.Sprintf("Observation: retrieved %d new unique paper(s) and %d provisional evidence finding(s) from %d executed query or queries.", newCount, len(phase1Findings), len(batchResults)),
			})

			// Belief control plane: update confidence and detect contradictions from Phase 1 evidence.
			if l.beliefManager != nil && len(phase1Findings) > 0 {
				// Snapshot confidence before update to detect drops.
				bs := l.beliefManager.GetState()
				preConf := make(map[string]float64, len(bs.Beliefs))
				for id, b := range bs.Beliefs {
					preConf[id] = b.Confidence
				}

				// Build evidence lookup for RecalculateConfidence.
				evMap := make(map[string]EvidenceFinding, len(phase1Findings))
				for _, f := range phase1Findings {
					evMap[f.ID] = f
				}
				l.beliefManager.RecalculateConfidence(evMap)

				// Inject targeted rebuttal queries for beliefs whose confidence dropped.
				bs = l.beliefManager.GetState()
				rebuttalBudget := maxBeliefRebuttalQueriesPerIteration(llmCooldown)
				rebuttalQueued := 0
				for id, b := range bs.Beliefs {
					if rebuttalQueued >= rebuttalBudget {
						break
					}
					if b.Status != BeliefStatusActive {
						continue
					}
					if old, ok := preConf[id]; ok && b.Confidence < old-0.1 {
						rebuttalQ := "counter-evidence: " + strings.TrimSpace(b.Claim)
						if queueCandidate(rebuttalQ) {
							rebuttalQueued++
							slog.Info("Belief control plane: confidence drop injected rebuttal query",
								"beliefID", id,
								"oldConf", old,
								"newConf", b.Confidence,
								"query", rebuttalQ)
						}
					}
				}

				// Detect contradictions and inject reconciliation queries.
				refuted := l.beliefManager.RefuteBeliefsContradictedByEvidence(phase1Findings, 0.7)
				for _, refutedID := range refuted {
					if b, exists := bs.Beliefs[refutedID]; exists {
						reconcileQ := "reconcile contradiction: " + strings.TrimSpace(b.Claim)
						if queueCandidate(reconcileQ) {
							slog.Info("Belief control plane: refuted belief injected reconciliation query",
								"beliefID", refutedID,
								"claim", b.Claim)
						}
					}
				}

				// Saturation detection runs after belief update so contradictions are handled first.
				saturation := l.beliefManager.DetectEvidenceSaturation(phase1Findings)
				if saturation.IsSaturated {
					if shouldAllowLoopEarlyStop(req, iterations) {
						slog.Info("Evidence saturation detected, skipping remaining retrieval", "diversity", saturation.DiversityScore)
						converged = true
						break
					}
					slog.Info("deferring evidence saturation stop until minimum iterations",
						"component", "wisdev.autonomous",
						"operation", "run",
						"iterations", iterations,
						"minIterations", req.MinIterations,
						"diversity", saturation.DiversityScore,
					)
				} else if saturation.Recommendation == "expand-diversity" {
					slog.Info("Evidence concentrated, expanding diversity in next queries", "diversity", saturation.DiversityScore)
					queueCandidate(req.Query + " alternative perspectives")
				}
			}

			if shouldBootstrapLoopHypotheses(req, hypotheses, phase1Findings, initialAgendaQueries, executedQueries) {
				bootstrapGap := buildLoopGapState(plannedQueries, executedQueries, queryCoverage, papers, lastAnalysis, false, req.ResearchPlane)
				findings, hypotheses = l.refreshLoopReasoning(ctx, req, papers, queryCoverage, bootstrapGap, req.Query)
				slog.Info("autonomous loop bootstrapped hypotheses from first-pass evidence",
					"component", "wisdev.autonomous",
					"operation", "belief_feedback",
					"stage", "hypothesis_bootstrap",
					"findingCount", len(findings),
					"hypothesisCount", len(hypotheses))
			}
		}

		slog.Debug("After iteration retrieval", "total", len(papers), "newCount", newCount)
		paperBudgetReached := maxUniquePapers > 0 && len(papers) >= maxUniquePapers

		// Belief-driven finalization gate: if all active beliefs are high-confidence,
		// override the sufficiency check outcome rather than burning an LLM call.
		if l.beliefManager != nil && shouldConvergeByBeliefState(l.beliefManager.GetState()) {
			if shouldAllowLoopEarlyStop(req, iterations) {
				slog.Info("Belief finalization gate: skipping LLM sufficiency check — all beliefs converged",
					"iteration", i+1,
					"avgConfidence", l.beliefManager.GetAverageConfidence(),
					"contradictionPressure", l.beliefManager.GetContradictionPressure())
				converged = true
				break
			}
			slog.Info("deferring belief finalization stop until minimum iterations",
				"component", "wisdev.autonomous",
				"operation", "run",
				"iterations", iterations,
				"minIterations", req.MinIterations,
			)
		}

		// 2. Verification & Convergence Check
		// Graceful degradation: skip expensive LLM sufficiency check when budget
		// is nearly exhausted (>80% of search terms used) and the budget is
		// non-trivial (>10 terms) — rely on heuristic instead.
		budgetRatio := float64(len(executedQueries)) / float64(maxInt(searchTermBudget, 1))
		var analysis *sufficiencyAnalysis
		var err error
		if budgetRatio > 0.8 && searchTermBudget > 10 {
			analysis = heuristicsufficiencyAnalysisWithoutLLM(req.Query, papers)
			EmitLoopDegraded(ctx, "evaluate_sufficiency", "Search budget nearly exhausted; using heuristic sufficiency", map[string]any{
				"budgetRatio":      budgetRatio,
				"searchTermBudget": searchTermBudget,
				"paperCount":       len(papers),
			})
		} else {
			analysis, err = l.evaluateSufficiency(ctx, req.Query, papers)
		}
		if err == nil {
			lastAnalysis = analysis
			reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
				Timestamp: NowMillis(),
				Phase:     "evaluation",
				Decision:  "react_reflect_sufficiency",
				Reasoning: fmt.Sprintf("Reflection: sufficiency=%t confidence=%.2f. %s", analysis.Sufficient, analysis.Confidence, strings.TrimSpace(analysis.Reasoning)),
			})
			EmitLoopStage(ctx, "sufficiency_evaluated", fmt.Sprintf("Sufficiency confidence=%.2f sufficient=%v", analysis.Confidence, analysis.Sufficient), map[string]any{
				"confidence":           analysis.Confidence,
				"sufficient":           analysis.Sufficient,
				"missing_aspect_count": len(analysis.MissingAspects),
				"iteration":            iterations,
			})
			if i == maxLoopIterations-1 || paperBudgetReached {
				converged = analysis.Sufficient
				break
			}
			if analysis.Sufficient && loopMinimumIterationsMet(req, iterations) {
				// Test-time compute on the convergence decision: a borderline
				// "sufficient" verdict needs a second independent evaluation to
				// agree before the loop is allowed to stop.
				if l.confirmBorderlineSufficiency(ctx, req, analysis, papers) {
					converged = true
					break
				}
				analysis.Sufficient = false
			}
			if analysis.Sufficient {
				slog.Info("deferring sufficiency stop until minimum iterations",
					"component", "wisdev.autonomous",
					"operation", "run",
					"iterations", iterations,
					"minIterations", req.MinIterations,
				)
			}

			// Phase 4: Swarm Interjection (D2)
			// Allow specialized roles to "interject" based on gap analysis
			interjections := l.executeSwarmInterjections(ctx, req, papers, analysis, hypotheses)
			for _, q := range interjections {
				if queueCandidate(q) {
					slog.Info("Swarm interjection: adding targeted query", "query", q)
				}
			}

			// Phase 6: Qualitative Critique & Synthesis
			if i > 0 && (i+1)%2 == 0 && !converged {
				evidenceItems, _ := l.assembleDossier(ctx, req.Query, papers)
				var qualAnalysis *sufficiencyAnalysis

				if (i+1)%4 == 0 {
					qualAnalysis, _ = l.intermediateSynthesis(ctx, req.Query, papers, evidenceItems)
				} else if l.brainCaps != nil {
					qualAnalysis, _ = l.brainCaps.CritiqueEvidenceSet(ctx, req.Query, evidenceItems, "")
				}
				if qualAnalysis != nil {
					slog.Info("Qualitative analysis identified nuanced gaps", "gapCount", len(qualAnalysis.MissingAspects))
					for _, q := range qualAnalysis.NextQueries {
						queueCandidate(q)
					}
					analysis.MissingAspects = append(analysis.MissingAspects, qualAnalysis.MissingAspects...)

					// P5: Feed synthesis-identified gaps into belief state as low-confidence gap beliefs
					if l.beliefManager != nil && len(qualAnalysis.MissingAspects) > 0 {
						bs := l.beliefManager.GetState()
						for _, aspect := range qualAnalysis.MissingAspects {
							gapID := stableWisDevID("gap-belief", req.Query, aspect)
							if _, exists := bs.Beliefs[gapID]; !exists {
								bs.Beliefs[gapID] = &Belief{
									ID:         gapID,
									Claim:      "Gap: " + aspect,
									Confidence: 0.1,
									Status:     BeliefStatusActive,
									CreatedAt:  NowMillis(),
									UpdatedAt:  NowMillis(),
									ProvenanceChain: []ProvenanceEntry{{
										GapID:       gapID,
										Timestamp:   NowMillis(),
										Description: "Identified by intermediate qualitative synthesis",
									}},
								}
							}
						}
					}
				}
			}

			// 3. Refine query based on explicit and inferred gaps.
			// LLM-proposed queries are evidence-conditioned replanning output, so
			// they are enqueued alongside (not behind) the heuristic gap-ledger
			// candidates; queueCandidate dedupes across both sources.
			gapState := buildLoopGapState(plannedQueries, executedQueries, queryCoverage, papers, analysis, false, req.ResearchPlane)
			enqueued := false
			for _, candidate := range analysis.NextQueries {
				if queueCandidate(candidate) {
					enqueued = true
				}
			}
			for _, candidate := range buildFollowUpQueriesFromLedger(req.Query, gapState.Ledger, 4) {
				if queueCandidate(candidate) {
					enqueued = true
				}
			}
			if !enqueued {
				for _, candidate := range deriveLoopFollowUpQueries(req.Query, analysis, papers) {
					if queueCandidate(candidate) {
						enqueued = true
					}
				}
			}
			// Mid-loop replanning: when even the heuristic follow-ups produced
			// nothing, regenerate agenda queries from the gap state instead of
			// letting the loop starve on the static query pool.
			if len(pendingQueries) == 0 {
				for _, candidate := range l.regenerateLoopAgenda(ctx, req, analysis, gapState, papers, executedQueries) {
					if queueCandidate(candidate) {
						enqueued = true
					}
				}
			}
			if enqueued && len(pendingQueries) > 0 {
				slog.Info("autonomous loop queued follow-up research", "pendingQueryCount", len(pendingQueries))
			}
		} else {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("autonomous sufficiency evaluation unavailable: %w", ctxErr)
			}
			if shouldAbortAutonomousLoop(err) {
				return nil, fmt.Errorf("autonomous sufficiency evaluation unavailable: %w", err)
			}
			analysis = heuristicsufficiencyAnalysisWithoutLLM(req.Query, papers)
			lastAnalysis = analysis
			slog.Warn("Sufficiency evaluation failed; using heuristic fallback",
				"component", "wisdev.autonomous",
				"operation", "evaluate_sufficiency",
				"error", err,
				"paperCount", len(papers),
				"confidence", analysis.Confidence,
				"sufficient", analysis.Sufficient,
				"missing_aspect_count", len(analysis.MissingAspects),
				"missing_source_type_count", len(analysis.MissingSourceTypes),
			)
			EmitLoopDegraded(ctx, "evaluate_sufficiency", "Sufficiency evaluation failed; using heuristic fallback", map[string]any{
				"error":                     err.Error(),
				"paperCount":                len(papers),
				"confidence":                analysis.Confidence,
				"sufficient":                analysis.Sufficient,
				"missing_aspect_count":      len(analysis.MissingAspects),
				"missing_source_type_count": len(analysis.MissingSourceTypes),
			})
			if paperBudgetReached || (analysis.Sufficient && loopMinimumIterationsMet(req, iterations)) {
				slog.Info("autonomous loop stopping after heuristic sufficiency fallback",
					"component", "wisdev.autonomous",
					"operation", "research_loop",
					"stage", "heuristic_fallback_stop",
					"trace_id", strings.TrimSpace(req.TraceID),
					"session_id", strings.TrimSpace(req.ProjectID),
					"paper_count", len(papers),
					"max_unique_papers", maxUniquePapers,
					"paper_budget_reached", paperBudgetReached,
					"sufficient", analysis.Sufficient,
				)
				converged = analysis.Sufficient || paperBudgetReached
				break
			}
			if analysis.Sufficient {
				slog.Info("deferring heuristic sufficiency stop until minimum iterations",
					"component", "wisdev.autonomous",
					"operation", "run",
					"iterations", iterations,
					"minIterations", req.MinIterations,
				)
			}
		}
	}
	gapAnalysis = buildLoopGapState(plannedQueries, executedQueries, queryCoverage, papers, lastAnalysis, converged, req.ResearchPlane)

	// Final adaptive parallelism check
	finalParallelism := resolveCooldownAwareParallelism(l.resolveAdaptiveParallelism(req.Mode, gapAnalysis.Confidence, req.ResearchPlane), autonomousLLMCooldownRemaining(l))

	closure, err := l.closeRecursiveCoverageGaps(ctx, req, plannedQueries, executedQueries, queryCoverage, papers, querySeen, lastAnalysis, converged, hitsPerSearch, maxUniquePapers, searchTermBudget, finalParallelism, emit)
	if err != nil {
		return nil, err
	}
	plannedQueries = closure.PlannedQueries
	executedQueries = closure.ExecutedQueries
	queryCoverage = closure.QueryCoverage
	papers = closure.Papers
	lastAnalysis = closure.Analysis
	converged = closure.Converged
	gapAnalysis = closure.GapAnalysis

	// 4. Evidence Assembly & Final Hypothesis Evaluation
	previousHypotheses := append([]Hypothesis(nil), hypotheses...)
	findings, hypotheses = l.refreshLoopReasoning(ctx, req, papers, queryCoverage, gapAnalysis, "")
	if len(hypotheses) == 0 && len(previousHypotheses) > 0 {
		hypotheses = previousHypotheses
	}
	gapAnalysis = mergeHypothesisBranchLedger(gapAnalysis, req.Query, hypotheses)
	evidenceItems, _ := l.assembleDossier(ctx, req.Query, papers)

	// Context Compaction / Working Memory
	if l.beliefManager != nil {
		wmm := NewWorkingMemoryManager(l.llmClient)
		evidenceItems = wmm.CompactItems(ctx, evidenceItems, l.beliefManager.GetState())
		findings = wmm.Compact(ctx, findings, l.beliefManager.GetState())
	}

	mode := NormalizeWisDevMode(req.Mode)
	serviceTier := ResolveLoopServiceTier(mode, false, req.ServiceTier)
	session := &AgentSession{
		SessionID:      strings.TrimSpace(req.ProjectID),
		Query:          strings.TrimSpace(req.OriginalQuery),
		CorrectedQuery: strings.TrimSpace(req.Query),
		DetectedDomain: strings.TrimSpace(req.Domain),
		Mode:           mode,
		ServiceTier:    serviceTier,
		MemoryTiers:    cloneMemoryTierState(req.InitialMemoryTiers),
	}
	if session.MemoryTiers == nil {
		session.MemoryTiers = &MemoryTierState{}
	}
	if l.beliefManager != nil {
		session.BeliefState = l.beliefManager.GetState()
	}
	UpdateSessionReasoningGraph(session, hypotheses, findings, papers...)
	if session.MemoryTiers != nil {
		reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
			Timestamp: NowMillis(),
			Phase:     "memory",
			Decision:  "memory_context_refresh",
			Reasoning: fmt.Sprintf("Refreshed working/vector/artifact memory tiers from %d hypotheses and %d evidence items; working=%d vector=%d artifact=%d.", len(hypotheses), len(findings), len(session.MemoryTiers.ShortTermWorking), len(session.MemoryTiers.LongTermVector), len(session.MemoryTiers.ArtifactMemory)),
		})
	}

	// 5. Draft Synthesis (Using Heavy Brain), then mandatory critique-driven retrieval reopening.
	reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
		Timestamp: NowMillis(),
		Phase:     "synthesis",
		Decision:  "draft",
		Reasoning: fmt.Sprintf("Synthesizing draft from %d papers and %d evidence items", len(papers), len(evidenceItems)),
	})
	EmitLoopStage(ctx, "synthesis_started", fmt.Sprintf("Synthesizing draft from %d papers", len(papers)), map[string]any{
		"paperCount":    len(papers),
		"evidenceCount": len(evidenceItems),
	})
	structuredAnswer, err := l.synthesizeWithEvidence(ctx, req.Query, papers, evidenceItems)
	if err != nil {
		return nil, err
	}
	finalAnswer := renderStructuredAnswerWithInlineCitations(req.Query, structuredAnswer, papers, evidenceItems, gapAnalysis)
	if structuredAnswer != nil {
		structuredAnswer.Text = finalAnswer
	}
	critique := l.critiqueDraft(ctx, req.Query, finalAnswer, papers, evidenceItems, gapAnalysis)
	if critique != nil && critique.NeedsRevision {
		reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
			Timestamp: NowMillis(),
			Phase:     "synthesis",
			Decision:  "critique",
			Reasoning: critique.Reasoning,
		})
		retrievedMore := false
		retrievalReopened := false
		critiqueCandidates := buildCritiqueFollowUpQueries(req.Query, critique, gapAnalysis, papers)
		if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
			if shouldLogWisDevCooldownFallback("wisdev.autonomous.critique_retrieval", time.Now()) {
				slog.Warn("autonomous critique retrieval deferred during provider cooldown",
					"component", "wisdev.autonomous",
					"operation", "critique_retrieval",
					"stage", "cooldown_defer",
					"retry_after_ms", remaining.Milliseconds(),
					"candidateCount", len(critiqueCandidates),
				)
				EmitLoopDegraded(ctx, "critique_retrieval", "Critique retrieval deferred during cooldown", map[string]any{
					"retry_after_ms": remaining.Milliseconds(),
					"candidateCount": len(critiqueCandidates),
					"fallback":       "defer",
				})
			}
			critiqueCandidates = nil
		}
		remainingCritiqueTerms := searchTermBudget - len(executedQueries)
		if remainingCritiqueTerms <= 0 {
			critiqueCandidates = nil
		} else if len(critiqueCandidates) > remainingCritiqueTerms {
			critiqueCandidates = critiqueCandidates[:remainingCritiqueTerms]
		}
		if limit := resolveCritiqueFollowUpLimit(req.Mode, req.ResearchPlane); len(critiqueCandidates) > limit {
			critiqueCandidates = critiqueCandidates[:limit]
		}
		if critiqueReplans := buildCritiqueReplanBranchPlans(req.Query, critique, critiqueCandidates); len(critiqueReplans) > 0 {
			branchPlans = mergeResearchBranchPlans(req.Query, critiqueReplans, branchPlans)
			reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
				Timestamp:    NowMillis(),
				Phase:        "replan",
				Decision:     "critique_replan",
				Reasoning:    fmt.Sprintf("Draft critique opened %d targeted retrieval branch(es). %s", len(critiqueReplans), strings.TrimSpace(critique.Reasoning)),
				Alternatives: researchBranchPlanQueries(critiqueReplans),
			})
		}
		currentSearchLimit := remainingLoopSearchLimit(len(papers), hitsPerSearch, maxUniquePapers)
		if currentSearchLimit > 0 && len(critiqueCandidates) > 0 {
			retrievalReopened = true
		}
		var batchResults []loopSearchBatchResult
		if retrievalReopened {
			EmitLoopStage(ctx, "critique_retrieval_started", fmt.Sprintf("Reopening retrieval for %d critique queries", len(critiqueCandidates)), map[string]any{
				"candidateCount": len(critiqueCandidates),
			})
			batchResults = l.executeLoopSearchBatch(ctx, critiqueCandidates, search.SearchOpts{
				Limit:            currentSearchLimit,
				Domain:           req.Domain,
				TraceID:          strings.TrimSpace(req.TraceID),
				QualitySort:      true,
				DynamicProviders: shouldUseDynamicProviderSelection(req.Mode, req.ResearchPlane, req.EnableDynamicProviderSelection, l.llmClient),
				SkipCache:        shouldBypassLoopSearchCache(req),
				LLMClient:        l.llmClient,
			}, queryParallelism)
		}
		for _, batchResult := range batchResults {
			candidate := batchResult.Query
			plannedQueries = appendUniqueLoopQuery(plannedQueries, candidate)
			executedQueries = appendUniqueLoopQuery(executedQueries, candidate)
			slog.Info("autonomous critique reopening retrieval",
				"component", "wisdev.autonomous",
				"operation", "critique_retrieval",
				"query", candidate,
			)
			beforeCount := len(papers)
			var acceptedPapers []search.Paper
			papers, acceptedPapers = admitSearchPapersForQuery(papers, req.Query, batchResult.Result.Papers, maxUniquePapers)
			recordLoopQueryCoverage(queryCoverage, candidate, acceptedPapers)
			if len(papers) > beforeCount {
				retrievedMore = true
			}
		}
		critique.RetrievalReopened = retrievalReopened
		critique.AdditionalEvidenceFound = retrievedMore
		if retrievedMore {
			// The post-critique sufficiency evaluation only feeds the converged
			// flag and gap bookkeeping, while the re-synthesis leg depends only
			// on papers and the re-assembled evidence. Run both legs
			// concurrently and join before anything consumes either result —
			// and before any return path, so the goroutine never leaks.
			type postCritiqueSufficiencyOutcome struct {
				analysis *sufficiencyAnalysis
				err      error
			}
			sufficiencyCh := make(chan postCritiqueSufficiencyOutcome, 1)
			go func() {
				analysis, err := l.evaluateSufficiency(ctx, req.Query, papers)
				sufficiencyCh <- postCritiqueSufficiencyOutcome{analysis: analysis, err: err}
			}()
			evidenceItems, _ = l.assembleDossier(ctx, req.Query, papers)
			resynthesizedAnswer, resynthesisErr := l.synthesizeWithEvidence(ctx, req.Query, papers, evidenceItems)
			sufficiencyOutcome := <-sufficiencyCh
			if analysis, err := sufficiencyOutcome.analysis, sufficiencyOutcome.err; err == nil {
				lastAnalysis = analysis
				converged = analysis.Sufficient
			} else if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("autonomous sufficiency evaluation unavailable: %w", ctxErr)
			} else if shouldAbortAutonomousLoop(err) {
				return nil, fmt.Errorf("autonomous sufficiency evaluation unavailable: %w", err)
			} else {
				analysis = heuristicsufficiencyAnalysisWithoutLLM(req.Query, papers)
				lastAnalysis = analysis
				slog.Warn("post-critique sufficiency evaluation failed; using heuristic fallback",
					"component", "wisdev.autonomous",
					"operation", "post_critique_sufficiency",
					"error", err,
					"paperCount", len(papers),
					"confidence", analysis.Confidence,
					"sufficient", analysis.Sufficient,
				)
				EmitLoopDegraded(ctx, "post_critique_sufficiency", "Post-critique sufficiency evaluation failed; using heuristic fallback", map[string]any{
					"error":      err.Error(),
					"paperCount": len(papers),
					"confidence": analysis.Confidence,
					"sufficient": analysis.Sufficient,
				})
				if analysis.Sufficient || (maxUniquePapers > 0 && len(papers) >= maxUniquePapers) {
					converged = true
				}
			}
			gapAnalysis = buildLoopGapState(plannedQueries, executedQueries, queryCoverage, papers, lastAnalysis, converged, req.ResearchPlane)
			findings, hypotheses = l.refreshLoopReasoning(ctx, req, papers, queryCoverage, gapAnalysis, "")
			gapAnalysis = mergeHypothesisBranchLedger(gapAnalysis, req.Query, hypotheses)
			if l.beliefManager != nil {
				session.BeliefState = l.beliefManager.GetState()
			}
			UpdateSessionReasoningGraph(session, hypotheses, findings, papers...)
			structuredAnswer, err = resynthesizedAnswer, resynthesisErr
			if err != nil {
				return nil, err
			}
			finalAnswer = renderStructuredAnswerWithInlineCitations(req.Query, structuredAnswer, papers, evidenceItems, gapAnalysis)
			if structuredAnswer != nil {
				structuredAnswer.Text = finalAnswer
			}
			postRetrievalCritique := l.critiqueDraft(ctx, req.Query, finalAnswer, papers, evidenceItems, gapAnalysis)
			if postRetrievalCritique != nil {
				critique = mergePostRetrievalDraftCritique(critique, postRetrievalCritique, retrievalReopened, retrievedMore)
				reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
					Timestamp: NowMillis(),
					Phase:     "synthesis",
					Decision:  "post_critique_review",
					Reasoning: critique.Reasoning,
				})
				if critique.NeedsRevision {
					finalAnswer, err = l.refineDraftWithCritique(ctx, req.Query, finalAnswer, critique, evidenceItems)
					if err != nil {
						return nil, err
					}
					structuredAnswer.Text = finalAnswer
				}
			}
		}
		if !retrievedMore {
			finalAnswer, err = l.refineDraftWithCritique(ctx, req.Query, finalAnswer, critique, evidenceItems)
			if err != nil {
				return nil, err
			}
			structuredAnswer.Text = finalAnswer
		}
	}
	gapAnalysis = mergeDraftCritiqueIntoGapState(gapAnalysis, critique, req.Query)

	if emit != nil {
		emitLoopProgress(emit, req, "loop_completed", "autonomous loop completed", map[string]any{
			"iterations":  iterations,
			"paperCount":  len(papers),
			"converged":   converged,
			"executedQueries": len(executedQueries),
		})
		emit(PlanExecutionEvent{
			Type:      EventCompleted,
			SessionID: strings.TrimSpace(req.ProjectID),
			Message:   "autonomous loop completed",
			Payload: map[string]any{
				"stage":       "loop_completed",
				"iterations":  iterations,
				"paperCount":  len(papers),
				"converged":   converged,
			},
			CreatedAt: NowMillis(),
		})
	}

	// R2: Get final belief state
	var beliefState *BeliefState
	if l.beliefManager != nil {
		beliefState = l.beliefManager.GetState()
		session.BeliefState = beliefState
		if session.ReasoningGraph != nil {
			session.ReasoningGraph = MergeBeliefStateIntoReasoningGraph(session.ReasoningGraph, beliefState)
		}
		slog.Info("Belief state summary", "activeBeliefs", len(beliefState.GetActiveBeliefs()))
	}

	stopReason := determineAutonomousStopReason(&LoopResult{GapAnalysis: gapAnalysis, Converged: converged, Papers: papers})
	// Fold this run's outcome into the domain history for future warm starts.
	GlobalDomainOutcomes.Record(ctx, req.Domain, gapAnalysis.Confidence, len(papers))
	reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
		Timestamp: NowMillis(),
		Phase:     "finalization",
		Decision:  "stop",
		Reasoning: stopReason,
	})

	finalAnswer = renderStructuredAnswerWithInlineCitations(req.Query, structuredAnswer, papers, evidenceItems, gapAnalysis)
	if structuredAnswer != nil {
		structuredAnswer.Text = finalAnswer
	}

	return &LoopResult{
		FinalAnswer:      finalAnswer,
		StructuredAnswer: structuredAnswer,
		Papers:           search.SortPapersByPreferenceWithQuery(papers, req.Query),
		Evidence:         findings,
		Branches:         completedBranches,
		Hypotheses:       hypotheses,
		Iterations:       iterations,
		Converged:        converged,
		BranchPlans:      applyResearchBranchPlanExecutionStatus(mergeResearchBranchPlans(req.Query, branchPlans, researchBranchPlansFromQueries(req.Query, plannedQueries)), executedQueries, queryCoverage),
		ExecutedQueries:  executedQueries,
		QueryCoverage:    queryCoverage,
		GapAnalysis:      gapAnalysis,
		DraftCritique:    critique,
		ReasoningGraph:   session.ReasoningGraph,
		MemoryTiers:      session.MemoryTiers,
		Mode:             mode,
		ServiceTier:      serviceTier,
		BeliefState:      beliefState, // R2: Include belief state
		ReasoningTrace:   reasoningTrace,
		StopReason:       stopReason,
		SynthesisMode:    detectSynthesisMode(finalAnswer),
	}, nil
}

func appendUniqueLoopQuery(existing []string, query string) []string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return existing
	}
	key := strings.ToLower(trimmed)
	for _, candidate := range existing {
		if strings.ToLower(strings.TrimSpace(candidate)) == key {
			return existing
		}
	}
	return append(existing, trimmed)
}

func recordLoopQueryCoverage(coverage map[string][]search.Paper, query string, papers []search.Paper) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" || coverage == nil {
		return
	}
	coverage[trimmedQuery] = appendUniqueSearchPapers(coverage[trimmedQuery], papers)
}

func cloneLoopQueryCoverage(in map[string][]search.Paper) map[string][]search.Paper {
	out := make(map[string][]search.Paper, len(in))
	for query, papers := range in {
		trimmed := strings.TrimSpace(query)
		if trimmed == "" {
			continue
		}
		out[trimmed] = appendUniqueSearchPapers(out[trimmed], papers)
	}
	return out
}

func filterUnexecutedLoopQueries(plannedQueries []string, executedQueries []string) []string {
	if len(plannedQueries) == 0 {
		return nil
	}
	pending := make([]string, 0, len(plannedQueries))
	for _, query := range plannedQueries {
		if containsNormalizedLoopQuery(executedQueries, query) {
			continue
		}
		pending = appendUniqueLoopQuery(pending, query)
	}
	return pending
}

func appendUniqueSearchPapers(existing []search.Paper, incoming []search.Paper) []search.Paper {
	merged, _ := appendUniqueSearchPapersWithinBudget(existing, incoming, 0)
	return merged
}

func appendUniqueSearchPapersWithinBudget(existing []search.Paper, incoming []search.Paper, maxUniquePapers int) ([]search.Paper, []search.Paper) {
	existing = SanitizeRetrievedPapersForLLM(existing, "appendUniqueSearchPapersWithinBudget.existing")
	incoming = SanitizeRetrievedPapersForLLM(incoming, "appendUniqueSearchPapersWithinBudget.incoming")
	if len(incoming) == 0 {
		return existing, nil
	}
	merged := append([]search.Paper(nil), existing...)
	admitted := make([]search.Paper, 0, len(incoming))
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	admittedSeen := make(map[string]struct{}, len(incoming))
	for _, paper := range existing {
		if key := searchPaperDedupKey(paper); key != "" {
			seen[key] = struct{}{}
		}
	}
	for _, paper := range incoming {
		key := searchPaperDedupKey(paper)
		if key != "" {
			if _, exists := admittedSeen[key]; exists {
				continue
			}
			admittedSeen[key] = struct{}{}
			if _, exists := seen[key]; exists {
				admitted = append(admitted, paper)
				continue
			}
			if maxUniquePapers > 0 && len(merged) >= maxUniquePapers {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, paper)
			admitted = append(admitted, paper)
			continue
		}
		if maxUniquePapers > 0 && len(merged) >= maxUniquePapers {
			continue
		}
		merged = append(merged, paper)
		admitted = append(admitted, paper)
	}
	return merged, admitted
}

func searchPaperDedupKey(paper search.Paper) string {
	for _, candidate := range []string{paper.ID, paper.DOI, paper.Link, paper.Title} {
		if trimmed := strings.ToLower(strings.TrimSpace(candidate)); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func resolveLoopHitsPerSearch(value int) int {
	if value > 0 {
		return value
	}
	return 10
}

func remainingLoopSearchLimit(currentUniqueCount int, hitsPerSearch int, maxUniquePapers int) int {
	if hitsPerSearch <= 0 {
		hitsPerSearch = resolveLoopHitsPerSearch(hitsPerSearch)
	}
	if maxUniquePapers <= 0 {
		return hitsPerSearch
	}
	remaining := maxUniquePapers - currentUniqueCount
	if remaining <= 0 {
		return 0
	}
	if remaining < hitsPerSearch {
		return remaining
	}
	return hitsPerSearch
}

func loopMinimumIterationsMet(req LoopRequest, completedIterations int) bool {
	min := req.MinIterations
	if min <= 0 {
		return true
	}
	return completedIterations >= min
}

func loopExhaustiveMode(req LoopRequest) bool {
	return req.MinIterations > 0 && req.MaxIterations > 0 && req.MinIterations >= req.MaxIterations
}

func shouldAllowLoopEarlyStop(req LoopRequest, completedIterations int) bool {
	return loopMinimumIterationsMet(req, completedIterations)
}

func enqueueExhaustiveContinuationQueries(req LoopRequest, pending *[]string, seen map[string]struct{}, executed []string) bool {
	if !loopExhaustiveMode(req) {
		return false
	}
	clauses := queryTopicClauses(req.Query)
	candidates := []string{
		req.Query + " systematic review",
		req.Query + " randomized controlled trial",
		req.Query + " meta-analysis outcomes",
		req.Query + " recent advances",
		req.Query + " clinical outcomes long-term follow-up",
	}
	for _, clause := range clauses {
		if strings.TrimSpace(clause) == "" {
			continue
		}
		candidates = append(candidates,
			clause+" systematic review",
			clause+" clinical trial",
		)
	}
	for _, seed := range req.SeedQueries {
		candidates = append(candidates, seed)
	}
	for _, query := range executed {
		candidates = append(candidates, query+" corroborating evidence")
	}
	queued := false
	for _, candidate := range candidates {
		if enqueueLoopQuery(pending, seen, candidate) {
			queued = true
		}
	}
	return queued
}

func resolveLoopSearchTermBudget(maxIterations int, maxSearchTerms int) int {
	if maxSearchTerms > 0 {
		return maxSearchTerms
	}
	if maxIterations > 0 {
		return maxIterations
	}
	return 0
}

func resolveLoopQueryParallelism(mode string, planes ...ResearchExecutionPlane) int {
	plane := firstResearchExecutionPlane(planes...)
	switch NormalizeWisDevMode(mode) {
	case WisDevModeYOLO:
		// YOLO skips the guided question flow, but it must remain bounded at the
		// provider edge. Keep runtime fan-out aligned with guided mode so a
		// single no-questions launch does not multiply embedding/model calls.
		if isHighDepthResearchPlane(plane) {
			return 3
		}
		return 2
	default:
		if isHighDepthResearchPlane(plane) {
			return 3
		}
		return 2
	}
}

func (l *AutonomousLoop) resolveAdaptiveParallelism(mode string, confidence float64, planes ...ResearchExecutionPlane) int {
	base := resolveLoopQueryParallelism(mode, planes...)
	if confidence <= 0 {
		return base
	}
	if confidence < 0.4 {
		return base + 2 // Increase breadth for low confidence
	}
	if confidence > 0.85 {
		return maxInt(base-1, 1) // Consolidate for high confidence
	}
	return base
}

// computeAdaptiveBudgets implements R5: Adaptive compute allocation.
// It allocates follow-up query budgets proportionally to hypothesis uncertainty (1 - confidence).
func (l *AutonomousLoop) computeAdaptiveBudgets(hypotheses []*Hypothesis, totalBudget int) {
	if len(hypotheses) == 0 || totalBudget <= 0 {
		return
	}

	// Calculate total uncertainty
	totalUncertainty := 0.0
	activeCount := 0
	for _, h := range hypotheses {
		if h.IsTerminated {
			continue
		}
		// Uncertainty = 1 - confidence
		totalUncertainty += (1.0 - h.ConfidenceScore)
		activeCount++
	}

	if activeCount == 0 {
		return
	}
	if totalBudget < activeCount {
		totalBudget = activeCount
	}

	if totalUncertainty <= 0 {
		// Evenly distribute if all are 1.0 confidence or uncertainty is zero
		share := totalBudget / activeCount
		remainder := totalBudget % activeCount
		for _, h := range hypotheses {
			if !h.IsTerminated {
				h.AllocatedQueryBudget = share
				if remainder > 0 {
					h.AllocatedQueryBudget++
					remainder--
				}
			}
		}
		return
	}

	// Allocate proportionally to uncertainty
	allocated := 0
	for _, h := range hypotheses {
		if h.IsTerminated {
			continue
		}
		uncertainty := 1.0 - h.ConfidenceScore
		h.AllocatedQueryBudget = int(float64(totalBudget) * (uncertainty / totalUncertainty))
		// Ensure at least 1 query if not terminated
		if h.AllocatedQueryBudget < 1 {
			h.AllocatedQueryBudget = 1
		}
		allocated += h.AllocatedQueryBudget
	}

	// Adjust for rounding errors
	if allocated != totalBudget && activeCount > 0 {
		diff := totalBudget - allocated
		// Add/subtract from the first active hypothesis
		for _, h := range hypotheses {
			if !h.IsTerminated {
				h.AllocatedQueryBudget += diff
				if h.AllocatedQueryBudget < 1 {
					h.AllocatedQueryBudget = 1
				}
				break
			}
		}
	}
}

func toHypothesisPtrs(hypotheses []Hypothesis) []*Hypothesis {
	ptrs := make([]*Hypothesis, len(hypotheses))
	for i := range hypotheses {
		ptrs[i] = &hypotheses[i]
	}
	return ptrs
}

func resolveLoopGapRecursionDepth(mode string, planes ...ResearchExecutionPlane) int {
	plane := firstResearchExecutionPlane(planes...)
	switch NormalizeWisDevMode(mode) {
	case WisDevModeYOLO:
		if isHighDepthResearchPlane(plane) {
			return 3
		}
		return 2
	default:
		if isHighDepthResearchPlane(plane) {
			return 3
		}
		return 2
	}
}

func resolveCritiqueFollowUpLimit(mode string, planes ...ResearchExecutionPlane) int {
	plane := firstResearchExecutionPlane(planes...)
	switch NormalizeWisDevMode(mode) {
	case WisDevModeYOLO:
		if isHighDepthResearchPlane(plane) {
			return 4
		}
		return 3
	default:
		if isHighDepthResearchPlane(plane) {
			return 4
		}
		return 3
	}
}

func firstResearchExecutionPlane(planes ...ResearchExecutionPlane) ResearchExecutionPlane {
	for _, plane := range planes {
		if strings.TrimSpace(string(plane)) != "" {
			return plane
		}
	}
	return ""
}

func isHighDepthResearchPlane(plane ResearchExecutionPlane) bool {
	switch plane {
	case ResearchExecutionPlaneAutonomous, ResearchExecutionPlaneDeep, ResearchExecutionPlaneMultiAgent, ResearchExecutionPlaneQuest, ResearchExecutionPlaneJob:
		return true
	default:
		return false
	}
}

func shouldUseDynamicProviderSelection(mode string, plane ResearchExecutionPlane, allow bool, llmClient *llm.Client) bool {
	if !allow || llmClient == nil {
		return false
	}
	return shouldUseDynamicProviderSelectionForCooldown(mode, plane, allow, llmClient.ProviderCooldownRemaining())
}

func shouldUseDynamicProviderSelectionForCooldown(mode string, plane ResearchExecutionPlane, allow bool, llmCooldown time.Duration) bool {
	if !allow {
		return false
	}
	if llmCooldown > 0 {
		return false
	}
	return plane == ResearchExecutionPlaneDeep || plane == ResearchExecutionPlaneMultiAgent
}

func resolveCooldownAwareParallelism(parallelism int, llmCooldown time.Duration) int {
	if parallelism <= 0 {
		return 1
	}
	if llmCooldown > 0 {
		return 1
	}
	return parallelism
}

func maxBeliefRebuttalQueriesPerIteration(llmCooldown time.Duration) int {
	if llmCooldown > 0 {
		return 2
	}
	return 8
}

func resolveLoopMaxIterations(maxIterations int, maxSearchTerms int) int {
	if maxIterations <= 0 {
		return maxIterations
	}
	if maxSearchTerms <= 0 || maxSearchTerms >= maxIterations {
		return maxIterations
	}
	return maxSearchTerms
}

type loopSearchBatchResult struct {
	Query  string
	Result search.SearchResult
}

func appendOptionalTraceLogAttr(attrs []any, traceID string) []any {
	if normalized := strings.TrimSpace(traceID); normalized != "" {
		return append(attrs, "trace_id", normalized)
	}
	return attrs
}

func nextLoopQueryBatch(pending *[]string, limit int) []string {
	if pending == nil || len(*pending) == 0 || limit <= 0 {
		return nil
	}
	if limit > len(*pending) {
		limit = len(*pending)
	}
	batch := append([]string(nil), (*pending)[:limit]...)
	*pending = (*pending)[limit:]
	return batch
}

func (l *AutonomousLoop) executeLoopSearchBatch(ctx context.Context, queries []string, opts search.SearchOpts, parallelism int) []loopSearchBatchResult {
	queries = normalizeLoopQueries("", queries)
	if len(queries) == 0 || l == nil || l.searchReg == nil {
		return nil
	}
	if parallelism <= 0 {
		parallelism = 1
	}
	if parallelism > len(queries) {
		parallelism = len(queries)
	}
	batchLogAttrs := []any{
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "wisdev.autonomous",
		"operation", "execute_search_batch",
		"stage", "batch_started",
	}
	batchLogAttrs = appendOptionalTraceLogAttr(batchLogAttrs, opts.TraceID)
	batchLogAttrs = append(batchLogAttrs,
		"query_count", len(queries),
		"parallelism", parallelism,
		"limit", opts.Limit,
		"domain", opts.Domain,
		"dynamic_providers", opts.DynamicProviders,
		"skip_cache", opts.SkipCache,
	)
	slog.Info("wisdev search batch started", batchLogAttrs...)
	EmitLoopStage(ctx, "search_batch_started", fmt.Sprintf("Provider batch started (%d queries)", len(queries)), map[string]any{
		"query_count": len(queries),
		"parallelism": parallelism,
		"limit":       opts.Limit,
		"domain":      opts.Domain,
	})
	results := make([]loopSearchBatchResult, len(queries))
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for idx, query := range queries {
		wg.Add(1)
		go func(idx int, query string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = loopSearchBatchResult{Query: query}
				return
			}
			searchQuery := prepareSearchQueryText(query)
			result, err := retrieveCanonicalSearchResult(ctx, l.searchReg, searchQuery, opts)
			if err != nil {
				result.Warnings = append(result.Warnings, search.ProviderWarning{
					Provider: "wisdev_core_mcp_tool",
					Message:  err.Error(),
				})
			}
			queryLogAttrs := []any{
				"service", "go_orchestrator",
				"runtime", "go",
				"component", "wisdev.autonomous",
				"operation", "execute_search_batch",
				"stage", "query_completed",
			}
			queryLogAttrs = appendOptionalTraceLogAttr(queryLogAttrs, opts.TraceID)
			queryLogAttrs = append(queryLogAttrs,
				"query_preview", QueryPreview(query),
				"provider_result_counts", result.Providers,
				"raw_result_count", result.RawResultCount,
				"fused_result_count", result.FusedResultCount,
				"deduped_count", result.DedupedCount,
				"final_count", len(result.Papers),
				"limit_applied", result.LimitApplied,
				"latency_ms", result.LatencyMs,
				"warning_count", len(result.Warnings),
			)
			slog.Info("wisdev search query completed", queryLogAttrs...)
			EmitLoopStage(ctx, "query_completed", fmt.Sprintf("Query completed: %s (%d papers)", QueryPreview(query), len(result.Papers)), map[string]any{
				"query_preview":      QueryPreview(query),
				"final_count":        len(result.Papers),
				"warning_count":      len(result.Warnings),
				"latency_ms":         result.LatencyMs,
				"provider_result_counts": result.Providers,
			})
			results[idx] = loopSearchBatchResult{Query: query, Result: result}
		}(idx, query)
	}
	wg.Wait()
	ordered := make([]loopSearchBatchResult, 0, len(queries))
	used := make([]bool, len(results))
	for _, query := range queries {
		for idx, result := range results {
			if used[idx] {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(result.Query), strings.TrimSpace(query)) {
				ordered = append(ordered, result)
				used[idx] = true
				break
			}
		}
	}
	for idx, result := range results {
		if !used[idx] {
			ordered = append(ordered, result)
		}
	}
	return ordered
}

func (l *AutonomousLoop) advanceBranchSession(ctx context.Context, branch *ResearchBranch, opts search.SearchOpts, maxUniquePapers int, queryCoverage map[string][]search.Paper) []EvidenceFinding {
	if l == nil || branch == nil || branch.Status != "active" || len(branch.PendingQueries) == 0 {
		return nil
	}
	pending := filterUnexecutedLoopQueries(branch.PendingQueries, branch.ExecutedQueries)
	if len(pending) == 0 {
		branch.PendingQueries = nil
		return nil
	}
	query := pending[0]
	branch.PendingQueries = pending[1:]
	branch.ExecutedQueries = appendUniqueLoopQuery(branch.ExecutedQueries, query)

	results := l.executeLoopSearchBatch(ctx, []string{query}, opts, 1)
	findings := make([]EvidenceFinding, 0)
	for _, result := range results {
		safePapers := SanitizeRetrievedPapersForLLM(result.Result.Papers, "advanceBranchSession")
		var accepted []search.Paper
		branch.Papers, accepted = appendUniqueSearchPapersWithinBudget(branch.Papers, safePapers, maxUniquePapers)
		if len(accepted) == 0 {
			continue
		}
		recordLoopQueryCoverage(queryCoverage, "branch:"+branch.ID+":"+result.Query, accepted)
		for _, paper := range accepted {
			finding := EvidenceFinding{
				ID:         stableWisDevID("branchfinding", branch.ID, result.Query, paper.ID),
				Claim:      paper.Title,
				Snippet:    evidenceTextFromPaper(paper),
				PaperTitle: paper.Title,
				SourceID:   paper.ID,
				Confidence: calculateInitialConfidence(paper),
				Year:       paper.Year,
			}
			branch.Evidence = append(branch.Evidence, finding)
			findings = append(findings, finding)
		}
	}
	attachBranchEvidence(branch)
	return findings
}

type loopGapClosureResult struct {
	PlannedQueries  []string
	ExecutedQueries []string
	QueryCoverage   map[string][]search.Paper
	Papers          []search.Paper
	Analysis        *sufficiencyAnalysis
	Converged       bool
	GapAnalysis     *LoopGapState
}

func (l *AutonomousLoop) closeRecursiveCoverageGaps(
	ctx context.Context,
	req LoopRequest,
	plannedQueries []string,
	executedQueries []string,
	queryCoverage map[string][]search.Paper,
	papers []search.Paper,
	querySeen map[string]struct{},
	lastAnalysis *sufficiencyAnalysis,
	converged bool,
	hitsPerSearch int,
	maxUniquePapers int,
	searchTermBudget int,
	queryParallelism int,
	emit func(PlanExecutionEvent),
) (loopGapClosureResult, error) {
	result := loopGapClosureResult{
		PlannedQueries:  append([]string(nil), plannedQueries...),
		ExecutedQueries: append([]string(nil), executedQueries...),
		QueryCoverage:   queryCoverage,
		Papers:          append([]search.Paper(nil), papers...),
		Analysis:        lastAnalysis,
		Converged:       converged,
	}
	result.GapAnalysis = buildLoopGapState(result.PlannedQueries, result.ExecutedQueries, result.QueryCoverage, result.Papers, result.Analysis, result.Converged, req.ResearchPlane)
	if (result.Converged && !hasOpenActionableCoverageGaps(result.GapAnalysis)) || searchTermBudget <= 0 || queryParallelism <= 0 {
		return result, nil
	}
	if querySeen == nil {
		querySeen = make(map[string]struct{}, len(result.PlannedQueries))
		for _, query := range result.PlannedQueries {
			if trimmed := strings.ToLower(strings.TrimSpace(query)); trimmed != "" {
				querySeen[trimmed] = struct{}{}
			}
		}
	}

	for cycle := 0; cycle < resolveLoopGapRecursionDepth(req.Mode, req.ResearchPlane); cycle++ {
		if !hasOpenActionableCoverageGaps(result.GapAnalysis) {
			break
		}
		remainingTerms := searchTermBudget - len(result.ExecutedQueries)
		if remainingTerms <= 0 {
			break
		}
		currentSearchLimit := remainingLoopSearchLimit(len(result.Papers), hitsPerSearch, maxUniquePapers)
		if currentSearchLimit <= 0 {
			break
		}

		currentParallelism := resolveCooldownAwareParallelism(queryParallelism, autonomousLLMCooldownRemaining(l))
		candidates := buildRecursiveGapFollowUpQueries(req.Query, result.GapAnalysis, result.Analysis, currentParallelism+1)
		selected := make([]string, 0, minInt(len(candidates), remainingTerms))
		for _, candidate := range candidates {
			trimmed := strings.TrimSpace(candidate)
			if trimmed == "" {
				continue
			}
			key := strings.ToLower(trimmed)
			if _, exists := querySeen[key]; exists {
				continue
			}
			querySeen[key] = struct{}{}
			result.PlannedQueries = appendUniqueLoopQuery(result.PlannedQueries, trimmed)
			selected = append(selected, trimmed)
			if len(selected) >= remainingTerms || len(selected) >= currentParallelism {
				break
			}
		}
		if len(selected) == 0 {
			break
		}

		slog.Info("autonomous recursive gap closure",
			"component", "wisdev.autonomous",
			"operation", "gap_closure",
			"cycle", cycle+1,
			"queryCount", len(selected),
		)
		emitLoopProgress(emit, req, "recursive_gap_closure_started", fmt.Sprintf("recursive gap closure cycle %d started", cycle+1), map[string]any{
			"cycle":           cycle + 1,
			"queryCount":      len(selected),
			"queries":         append([]string(nil), selected...),
			"openLedgerCount": countOpenCoverageLedgerEntries(result.GapAnalysis.Ledger),
		})
		batchResults := l.executeLoopSearchBatch(ctx, selected, search.SearchOpts{
			Limit:            currentSearchLimit,
			Domain:           req.Domain,
			TraceID:          strings.TrimSpace(req.TraceID),
			QualitySort:      true,
			DynamicProviders: shouldUseDynamicProviderSelection(req.Mode, req.ResearchPlane, req.EnableDynamicProviderSelection, l.llmClient),
			SkipCache:        shouldBypassLoopSearchCache(req),
			LLMClient:        l.llmClient,
		}, currentParallelism)
		for _, batchResult := range batchResults {
			result.ExecutedQueries = appendUniqueLoopQuery(result.ExecutedQueries, batchResult.Query)
			var acceptedPapers []search.Paper
			result.Papers, acceptedPapers = admitSearchPapersForQuery(result.Papers, req.Query, batchResult.Result.Papers, maxUniquePapers)
			recordLoopQueryCoverage(result.QueryCoverage, batchResult.Query, acceptedPapers)
		}
		analysis, err := l.evaluateSufficiency(ctx, req.Query, result.Papers)
		if err == nil {
			result.Analysis = analysis
			result.Converged = analysis.Sufficient
		} else if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("autonomous sufficiency evaluation unavailable: %w", ctxErr)
		} else if shouldAbortAutonomousLoop(err) {
			return result, fmt.Errorf("autonomous sufficiency evaluation unavailable: %w", err)
		} else {
			analysis = heuristicsufficiencyAnalysisWithoutLLM(req.Query, result.Papers)
			result.Analysis = analysis
			slog.Warn("recursive gap sufficiency evaluation failed; using heuristic fallback",
				"component", "wisdev.autonomous",
				"operation", "recursive_gap_sufficiency",
				"error", err,
				"paperCount", len(result.Papers),
				"confidence", analysis.Confidence,
				"sufficient", analysis.Sufficient,
			)
			EmitLoopDegraded(ctx, "recursive_gap_sufficiency", "Recursive gap sufficiency evaluation failed; using heuristic fallback", map[string]any{
				"error":      err.Error(),
				"paperCount": len(result.Papers),
				"confidence": analysis.Confidence,
				"sufficient": analysis.Sufficient,
			})
			if analysis.Sufficient || (maxUniquePapers > 0 && len(result.Papers) >= maxUniquePapers) {
				result.Converged = true
			}
		}
		result.GapAnalysis = buildLoopGapState(result.PlannedQueries, result.ExecutedQueries, result.QueryCoverage, result.Papers, result.Analysis, result.Converged, req.ResearchPlane)
		emitLoopProgress(emit, req, "recursive_gap_closure_completed", fmt.Sprintf("recursive gap closure cycle %d completed", cycle+1), map[string]any{
			"cycle":              cycle + 1,
			"converged":          result.Converged,
			"totalPapers":        len(result.Papers),
			"executedQueryCount": len(result.ExecutedQueries),
			"openLedgerCount":    countOpenCoverageLedgerEntries(result.GapAnalysis.Ledger),
		})
		if result.Converged && !hasOpenActionableCoverageGaps(result.GapAnalysis) {
			break
		}
	}
	return result, nil
}

func emitLoopProgress(emit func(PlanExecutionEvent), req LoopRequest, stage string, message string, payload map[string]any) {
	if emit == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["component"] = "wisdev.autonomous"
	payload["operation"] = "research_loop"
	payload["stage"] = strings.TrimSpace(stage)
	payload["researchPlane"] = strings.TrimSpace(string(req.ResearchPlane))
	traceID := strings.TrimSpace(req.TraceID)
	if traceID == "" {
		traceID = NewTraceID()
	}
	emit(PlanExecutionEvent{
		Type:            EventProgress,
		TraceID:         traceID,
		SessionID:       strings.TrimSpace(req.ProjectID),
		Message:         strings.TrimSpace(message),
		Payload:         payload,
		Owner:           "go",
		OwningComponent: "orchestrator/internal/wisdev",
		ResultOrigin:    "autonomous_research_loop",
		CreatedAt:       NowMillis(),
	})
}

func hasOpenActionableCoverageGaps(gap *LoopGapState) bool {
	if gap == nil {
		return false
	}
	for _, entry := range gap.Ledger {
		if !strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
			continue
		}
		if coverageLedgerEntryIsGenericValidationCheckpoint(entry) {
			continue
		}
		if isOpenCoverageLedgerEntryActionable(entry) {
			return true
		}
	}
	return len(gap.NextQueries) > 0 || len(gap.MissingAspects) > 0 || len(gap.MissingSourceTypes) > 0 || len(gap.Contradictions) > 0
}

func determineAutonomousStopReason(loopResult *LoopResult) string {
	return determineResearchStopReason(loopResult, nil, nil)
}

func isOpenCoverageLedgerEntryActionable(entry CoverageLedgerEntry) bool {
	obligationType := strings.ToLower(strings.TrimSpace(entry.ObligationType))
	if obligationType == "" {
		obligationType = inferCoverageObligationType(entry)
	}
	switch obligationType {
	case "", "budget_exhausted":
		return false
	default:
		return true
	}
}

func buildRecursiveGapFollowUpQueries(query string, gap *LoopGapState, analysis *sufficiencyAnalysis, limit int) []string {
	candidates := make([]string, 0, limit+4)
	if gap != nil {
		candidates = append(candidates, buildFollowUpQueriesFromLedger(query, gap.Ledger, limit+2)...)
		candidates = append(candidates, gap.NextQueries...)
	}
	if analysis != nil {
		candidates = append(candidates, analysis.NextQueries...)
	}
	if len(candidates) == 0 && analysis != nil {
		candidates = append(candidates, deriveLoopFollowUpQueries(query, analysis, nil)...)
	}
	candidates = normalizeLoopQueries("", candidates)
	if limit > 0 && len(candidates) > limit {
		return candidates[:limit]
	}
	return candidates
}

func normalizeLoopQueries(primary string, seeds []string) []string {
	queries := make([]string, 0, len(seeds)+1)
	seen := make(map[string]struct{}, len(seeds)+1)
	for _, query := range append([]string{primary}, seeds...) {
		trimmed := prepareSearchQueryText(query)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		queries = append(queries, trimmed)
	}
	return queries
}

func cloneMemoryTierState(tiers *MemoryTierState) *MemoryTierState {
	if tiers == nil {
		return nil
	}
	return &MemoryTierState{
		ShortTermWorking: append([]MemoryEntry(nil), tiers.ShortTermWorking...),
		LongTermVector:   append([]MemoryEntry(nil), tiers.LongTermVector...),
		ArtifactMemory:   append([]MemoryEntry(nil), tiers.ArtifactMemory...),
		UserPersonalized: append([]MemoryEntry(nil), tiers.UserPersonalized...),
	}
}

func buildAutonomousResearchAgendaQueries(primary string, domain string, mode string, plane ResearchExecutionPlane, seeds []string) []string {
	primary = prepareSearchQueryText(primary)
	queries := normalizeLoopQueries(primary, seeds)
	root := strings.TrimSpace(primary)
	if root == "" {
		return queries
	}
	normalizedMode := NormalizeWisDevMode(mode)
	if normalizedMode != WisDevModeYOLO && !isHighDepthResearchPlane(plane) {
		return queries
	}

	for _, focused := range BuildTopicFocusedQueries(root) {
		queries = appendUniqueLoopQuery(queries, focused)
	}

	for _, focus := range lookupAgendaQueries(root) {
		queries = appendUniqueLoopQuery(queries, normalizeAgendaFocus(root, focus))
	}
	return queries
}

func buildBeliefFeedbackQueries(primary string, decision LoopDecision, bs *BeliefState, limit int) []string {
	if !decision.ShouldContinue || limit <= 0 || bs == nil || len(decision.TargetBeliefs) == 0 {
		return nil
	}
	queries := make([]string, 0, minInt(limit, len(decision.TargetBeliefs)*3))
	for _, beliefID := range decision.TargetBeliefs {
		if len(queries) >= limit {
			break
		}
		belief := bs.Beliefs[strings.TrimSpace(beliefID)]
		if belief == nil || belief.Status != BeliefStatusActive {
			continue
		}
		claim := strings.TrimSpace(belief.Claim)
		if claim == "" {
			continue
		}
		for _, focus := range beliefFeedbackQueryFoci(decision.QueryStrategy, belief) {
			if len(queries) >= limit {
				break
			}
			queries = appendUniqueLoopQuery(queries, buildResearchWorkerQuery(primary, focus+": "+claim))
		}
	}
	return queries
}

func shouldBootstrapLoopHypotheses(req LoopRequest, hypotheses []Hypothesis, findings []EvidenceFinding, initialAgendaQueries []string, executedQueries []string) bool {
	if req.DisableHypothesisGeneration || len(hypotheses) > 0 || len(findings) == 0 {
		return false
	}
	return shouldEnableBeliefFeedback(req, initialAgendaQueries, executedQueries)
}

func shouldSeedPreRetrievalHypotheses(req LoopRequest) bool {
	if req.DisableHypothesisGeneration {
		return false
	}
	return NormalizeWisDevMode(req.Mode) == WisDevModeYOLO
}

func shouldEnableBeliefFeedback(req LoopRequest, initialAgendaQueries []string, executedQueries []string) bool {
	switch NormalizeWisDevMode(req.Mode) {
	case WisDevModeYOLO:
		return true
	case WisDevModeGuided:
		return isHighDepthResearchPlane(req.ResearchPlane) && loopAgendaCovered(initialAgendaQueries, executedQueries)
	default:
		return false
	}
}

func loopAgendaCovered(initialAgendaQueries []string, executedQueries []string) bool {
	agenda := normalizeLoopQueries("", initialAgendaQueries)
	if len(agenda) == 0 {
		return false
	}
	for _, query := range agenda {
		if !containsNormalizedLoopQuery(executedQueries, query) {
			return false
		}
	}
	return true
}

func beliefFeedbackQueryFoci(strategy string, belief *Belief) []string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "reconciliation":
		return []string{
			"contradiction resolution",
			"falsification check",
			"independent replication",
		}
	case "focus":
		return []string{
			"primary evidence for",
			"methodology evidence for",
			"limitations counter evidence",
		}
	case "falsification":
		return []string{
			"falsification check",
			"independent replication",
			"failed replication null results",
		}
	default:
		if belief != nil && len(belief.ContradictingEvidence) > 0 {
			return []string{"contradiction resolution", "falsification check"}
		}
		return []string{"primary evidence for"}
	}
}

func enqueueLoopQuery(pending *[]string, seen map[string]struct{}, query string) bool {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return false
	}
	key := strings.ToLower(trimmed)
	if _, exists := seen[key]; exists {
		return false
	}
	seen[key] = struct{}{}
	*pending = append(*pending, trimmed)
	return true
}

func buildLoopGapState(
	plannedQueries []string,
	executedQueries []string,
	queryCoverage map[string][]search.Paper,
	papers []search.Paper,
	analysis *sufficiencyAnalysis,
	converged bool,
	planes ...ResearchExecutionPlane,
) *LoopGapState {
	coverage := LoopCoverageState{
		PlannedQueryCount:  len(plannedQueries),
		ExecutedQueryCount: len(executedQueries),
		UniquePaperCount:   len(papers),
	}
	for _, query := range plannedQueries {
		trimmed := strings.TrimSpace(query)
		if trimmed == "" {
			continue
		}
		if !containsNormalizedLoopQuery(executedQueries, trimmed) {
			coverage.UnexecutedPlannedQueries = append(coverage.UnexecutedPlannedQueries, trimmed)
		}
	}
	for _, query := range executedQueries {
		trimmed := strings.TrimSpace(query)
		if trimmed == "" {
			continue
		}
		if len(queryCoverage[trimmed]) > 0 {
			coverage.CoveredQueryCount++
			continue
		}
		coverage.QueriesWithoutCoverage = append(coverage.QueriesWithoutCoverage, trimmed)
	}

	state := &LoopGapState{
		Sufficient: converged,
		Coverage: LoopCoverageState{
			PlannedQueryCount:        coverage.PlannedQueryCount,
			ExecutedQueryCount:       coverage.ExecutedQueryCount,
			CoveredQueryCount:        coverage.CoveredQueryCount,
			UniquePaperCount:         coverage.UniquePaperCount,
			QueriesWithoutCoverage:   dedupeTrimmedStrings(coverage.QueriesWithoutCoverage),
			UnexecutedPlannedQueries: dedupeTrimmedStrings(coverage.UnexecutedPlannedQueries),
		},
	}
	state.ObservedSourceFamilies = buildObservedSourceFamiliesFromPapers(papers)
	state.ObservedEvidenceCount = len(collectEvidenceItemsFromPapers(papers, 2, 8))
	if analysis == nil {
		if state.Coverage.ExecutedQueryCount > 0 && state.Coverage.CoveredQueryCount < state.Coverage.ExecutedQueryCount {
			state.MissingAspects = []string{"Some planned research branches executed without adding grounded evidence."}
		}
		if len(state.MissingAspects) == 0 && len(state.Coverage.UnexecutedPlannedQueries) > 0 {
			state.MissingAspects = append([]string(nil), state.Coverage.UnexecutedPlannedQueries...)
		}
		if len(state.Coverage.QueriesWithoutCoverage) > 0 {
			state.NextQueries = append([]string(nil), state.Coverage.QueriesWithoutCoverage...)
		} else if len(state.Coverage.UnexecutedPlannedQueries) > 0 {
			state.NextQueries = append([]string(nil), state.Coverage.UnexecutedPlannedQueries...)
		}
		if state.Confidence == 0 {
			state.Confidence = map[bool]float64{true: 0.82, false: 0.45}[converged]
		}
		state.Ledger = buildLoopCoverageLedger(nil, state.Coverage, papers, plannedQueries)
		state = mergeSourceAcquisitionLedger(state, firstNonEmpty(plannedQueries...), papers, firstResearchExecutionPlane(planes...))
		return state
	}

	state.Reasoning = strings.TrimSpace(analysis.Reasoning)
	state.NextQueries = dedupeTrimmedStrings(append([]string(nil), analysis.NextQueries...))
	state.MissingAspects = dedupeTrimmedStrings(append([]string(nil), analysis.MissingAspects...))
	state.MissingSourceTypes = dedupeTrimmedStrings(append([]string(nil), analysis.MissingSourceTypes...))
	state.Contradictions = dedupeTrimmedStrings(append([]string(nil), analysis.Contradictions...))
	state.Confidence = ClampFloat(analysis.Confidence, 0, 1)

	if len(state.NextQueries) == 0 && len(state.Coverage.QueriesWithoutCoverage) > 0 {
		state.NextQueries = append([]string(nil), state.Coverage.QueriesWithoutCoverage...)
	}
	if len(state.MissingAspects) == 0 && len(state.Coverage.UnexecutedPlannedQueries) > 0 {
		state.MissingAspects = append([]string(nil), state.Coverage.UnexecutedPlannedQueries...)
	}
	if state.Confidence == 0 {
		state.Confidence = map[bool]float64{true: 0.82, false: 0.45}[state.Sufficient]
	}
	if !state.Sufficient {
		state.Sufficient = converged
	}
	state.Ledger = buildLoopCoverageLedger(analysis, state.Coverage, papers, plannedQueries)
	state = mergeSourceAcquisitionLedger(state, firstNonEmpty(plannedQueries...), papers, firstResearchExecutionPlane(planes...))
	return state
}

func containsNormalizedLoopQuery(queries []string, query string) bool {
	key := strings.ToLower(strings.TrimSpace(query))
	if key == "" {
		return false
	}
	for _, candidate := range queries {
		if strings.ToLower(strings.TrimSpace(candidate)) == key {
			return true
		}
	}
	return false
}

func deriveLoopFollowUpQueries(originalQuery string, analysis *sufficiencyAnalysis, papers []search.Paper) []string {
	candidates := make([]string, 0, 6)
	seen := make(map[string]struct{}, 6)
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, trimmed)
	}

	query := strings.TrimSpace(originalQuery)
	if analysis != nil {
		for _, aspect := range analysis.MissingAspects {
			if trimmed := summarizeLoopGapTerms(aspect); trimmed != "" {
				add(strings.TrimSpace(firstNonEmpty(query+" "+trimmed, query)))
			}
		}
		if len(analysis.MissingSourceTypes) > 0 && query != "" {
			add(strings.TrimSpace(query + " " + strings.Join(limitStrings(analysis.MissingSourceTypes, 2), " ")))
		}
		for _, contradiction := range analysis.Contradictions {
			if trimmed := summarizeLoopGapTerms(contradiction); trimmed != "" {
				add(strings.TrimSpace(query + " contradiction " + trimmed))
			}
		}
	}

	for _, term := range collectInformativeTerms(searchPapersToLoopSources(papers), 4) {
		if query == "" {
			add(term)
			continue
		}
		add(strings.TrimSpace(query + " " + term))
	}

	if len(candidates) > 4 {
		candidates = candidates[:4]
	}
	return candidates
}

func summarizeLoopGapTerms(value string) string {
	tokens := loopEvidenceTokens(value)
	if len(tokens) == 0 {
		return ""
	}
	if len(tokens) > 4 {
		tokens = tokens[:4]
	}
	return strings.Join(tokens, " ")
}

func searchPapersToLoopSources(papers []search.Paper) []Source {
	if len(papers) == 0 {
		return nil
	}
	out := make([]Source, 0, len(papers))
	for _, paper := range papers {
		out = append(out, mapPaperToSource(paper))
	}
	return out
}

func (l *AutonomousLoop) proposeLoopHypotheses(ctx context.Context, primary string, seeds []string, findings []EvidenceFinding, queryCoverage map[string][]search.Paper, totalConfidence float64, disableHypothesisGeneration bool) []Hypothesis {
	if disableHypothesisGeneration {
		slog.Info("autonomous loop skipped hypothesis generation by request policy",
			"component", "wisdev.autonomous",
			"operation", "proposeHypotheses",
			"query", strings.TrimSpace(primary),
		)
		return nil
	}
	querySourceIndex := buildLoopQuerySourceIndex(queryCoverage)
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		if shouldLogWisDevCooldownFallback("wisdev.autonomous.proposeHypotheses", time.Now()) {
			slog.Warn("autonomous hypothesis proposal skipped during provider cooldown; using query fallback",
				"component", "wisdev.autonomous",
				"operation", "proposeHypotheses",
				"query", strings.TrimSpace(primary),
				"retry_after_ms", remaining.Milliseconds(),
			)
			EmitLoopDegraded(ctx, "propose_hypotheses", "Hypothesis proposal skipped during cooldown; using query fallback", map[string]any{
				"query":          strings.TrimSpace(primary),
				"retry_after_ms": remaining.Milliseconds(),
				"fallback":       "query",
			})
		}
		return buildAutonomousFallbackHypotheses(primary, seeds, findings, querySourceIndex, totalConfidence)
	}
	if l != nil && l.brainCaps != nil {
		if hypotheses := l.buildCapabilityHypotheses(ctx, primary, findings, querySourceIndex, totalConfidence); len(hypotheses) > 0 {
			return hypotheses
		}
	}
	return buildAutonomousFallbackHypotheses(primary, seeds, findings, querySourceIndex, totalConfidence)
}

func (l *AutonomousLoop) buildCapabilityHypotheses(ctx context.Context, primary string, findings []EvidenceFinding, querySourceIndex map[string]map[string]struct{}, totalConfidence float64) (hypotheses []Hypothesis) {
	query := strings.TrimSpace(primary)
	if query == "" || l == nil || l.brainCaps == nil {
		return nil
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Warn("autonomous hypothesis proposal panicked; using query fallback",
				"component", "wisdev.autonomous",
				"operation", "proposeHypotheses",
				"query", query,
				"error", fmt.Sprint(recovered),
			)
			EmitLoopDegraded(ctx, "propose_hypotheses", "Hypothesis proposal panicked; using query fallback", map[string]any{
				"query":    query,
				"error":    fmt.Sprint(recovered),
				"fallback": "query",
			})
			hypotheses = nil
		}
	}()

	proposed, err := l.brainCaps.ProposeHypotheses(ctx, query, "autonomous_research", "")
	if err != nil {
		slog.Warn("autonomous hypothesis proposal failed; using query fallback",
			"component", "wisdev.autonomous",
			"operation", "proposeHypotheses",
			"query", query,
			"error", err.Error(),
		)
		EmitLoopDegraded(ctx, "propose_hypotheses", "Hypothesis proposal failed; using query fallback", map[string]any{
			"query":    query,
			"error":    err.Error(),
			"fallback": "query",
		})
		return nil
	}

	return normalizeAutonomousCapabilityHypotheses(query, proposed, findings, querySourceIndex, totalConfidence)
}

func buildAutonomousFallbackHypotheses(primary string, seeds []string, findings []EvidenceFinding, querySourceIndex map[string]map[string]struct{}, _ float64) []Hypothesis {
	queries := normalizeLoopQueries(primary, seeds)
	if len(queries) == 0 {
		return nil
	}
	hypotheses := make([]Hypothesis, 0, len(queries))
	for _, query := range queries {
		evidence := selectLoopHypothesisEvidence(query, query, query, findings, querySourceIndex, 3)
		confidence := averageLoopEvidenceConfidence(evidence, 0.55)
		status := "candidate"
		if len(evidence) > 0 {
			status = "validated"
		}
		hypotheses = append(hypotheses, Hypothesis{
			ID:              stableWisDevID("loop_hyp", query),
			Query:           query,
			Text:            query,
			Claim:           query,
			Category:        "autonomous",
			Status:          status,
			ConfidenceScore: confidence,
			Evidence:        evidence,
			EvidenceCount:   len(evidence),
		})
	}
	return hypotheses
}

func normalizeAutonomousCapabilityHypotheses(primary string, proposed []Hypothesis, findings []EvidenceFinding, querySourceIndex map[string]map[string]struct{}, totalConfidence float64) []Hypothesis {
	if len(proposed) == 0 {
		return nil
	}

	confidence := 0.55
	if len(findings) > 0 {
		confidence = ClampFloat(totalConfidence/float64(len(findings)), 0.45, 0.95)
	}
	normalized := make([]Hypothesis, 0, len(proposed))
	seen := make(map[string]struct{}, len(proposed))
	for _, hypothesis := range proposed {
		claim := strings.TrimSpace(firstNonEmpty(hypothesis.Claim, hypothesis.Text, hypothesis.Query))
		if claim == "" {
			continue
		}
		key := strings.ToLower(claim)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		evidence := selectLoopHypothesisEvidence(
			buildLoopHypothesisSupportText(claim, hypothesis.FalsifiabilityCondition),
			"",
			primary,
			findings,
			querySourceIndex,
			3,
		)
		status := strings.TrimSpace(firstNonEmpty(hypothesis.Status, inferredLoopHypothesisStatus(evidence)))
		normalized = append(normalized, Hypothesis{
			ID:                      strings.TrimSpace(firstNonEmpty(hypothesis.ID, stableWisDevID("loop_hyp", claim))),
			Query:                   primary,
			Text:                    claim,
			Claim:                   claim,
			Category:                strings.TrimSpace(firstNonEmpty(hypothesis.Category, "autonomous")),
			FalsifiabilityCondition: strings.TrimSpace(hypothesis.FalsifiabilityCondition),
			ConfidenceThreshold:     ClampFloat(firstNonEmptyFloat(hypothesis.ConfidenceThreshold, confidence), 0, 1),
			ConfidenceScore:         ClampFloat(firstNonEmptyFloat(hypothesis.ConfidenceScore, hypothesis.ConfidenceThreshold, averageLoopEvidenceConfidence(evidence, confidence)), 0, 1),
			Status:                  status,
			Evidence:                evidence,
			EvidenceCount:           len(evidence),
			UpdatedAt:               NowMillis(),
		})
	}
	return normalized
}

func firstNonEmptyFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func inferredLoopHypothesisStatus(evidence []*EvidenceFinding) string {
	if len(evidence) > 0 {
		return "validated"
	}
	return "candidate"
}

func buildLoopHypothesisSupportText(claim string, falsifiabilityCondition string) string {
	return strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(claim),
		strings.TrimSpace(falsifiabilityCondition),
	}, " "))
}

func averageLoopEvidenceConfidence(evidence []*EvidenceFinding, fallback float64) float64 {
	if len(evidence) == 0 {
		if fallback <= 0 {
			fallback = 0.55
		}
		return ClampFloat(fallback, 0.45, 0.95)
	}
	total := 0.0
	for _, finding := range evidence {
		if finding == nil {
			continue
		}
		total += finding.Confidence
	}
	return ClampFloat(total/float64(len(evidence)), 0.45, 0.95)
}

func buildLoopQuerySourceIndex(queryCoverage map[string][]search.Paper) map[string]map[string]struct{} {
	if len(queryCoverage) == 0 {
		return nil
	}
	index := make(map[string]map[string]struct{}, len(queryCoverage))
	for query, papers := range queryCoverage {
		queryKey := strings.ToLower(strings.TrimSpace(query))
		if queryKey == "" {
			continue
		}
		sourceIDs := make(map[string]struct{}, len(papers))
		for _, paper := range papers {
			sourceKey := loopEvidenceSourceKey(paper.ID)
			if sourceKey == "" {
				continue
			}
			sourceIDs[sourceKey] = struct{}{}
		}
		if len(sourceIDs) > 0 {
			index[queryKey] = sourceIDs
		}
	}
	if len(index) == 0 {
		return nil
	}
	return index
}

type scoredLoopEvidence struct {
	finding EvidenceFinding
	score   float64
}

func selectLoopHypothesisEvidence(claim string, sourceQuery string, contextQuery string, findings []EvidenceFinding, querySourceIndex map[string]map[string]struct{}, limit int) []*EvidenceFinding {
	if len(findings) == 0 || limit <= 0 {
		return nil
	}
	sourceIDs := querySourceIndex[strings.ToLower(strings.TrimSpace(sourceQuery))]
	claimTokens := loopEvidenceTokenSet(claim)
	contextTokens := loopEvidenceTokenSet(contextQuery)
	minClaimOverlap := 1
	if len(claimTokens) >= 3 {
		minClaimOverlap = 2
	}
	scored := make([]scoredLoopEvidence, 0, len(findings))
	for _, finding := range findings {
		sourceMatch := false
		if len(sourceIDs) > 0 {
			_, sourceMatch = sourceIDs[loopEvidenceSourceKey(finding.SourceID)]
		}
		claimOverlap := loopEvidenceOverlap(claimTokens, finding.Claim, finding.Snippet, finding.PaperTitle)
		if !sourceMatch && claimOverlap < minClaimOverlap {
			continue
		}
		score := finding.Confidence + float64(claimOverlap*12)
		if sourceMatch {
			score += 100
		}
		score += float64(loopEvidenceOverlap(contextTokens, finding.Claim, finding.Snippet, finding.PaperTitle) * 3)
		scored = append(scored, scoredLoopEvidence{
			finding: finding,
			score:   score,
		})
	}
	if len(scored) == 0 {
		return nil
	}
	sort.SliceStable(scored, func(i int, j int) bool {
		if scored[i].score == scored[j].score {
			if scored[i].finding.Confidence == scored[j].finding.Confidence {
				return strings.Compare(
					strings.TrimSpace(firstNonEmpty(scored[i].finding.ID, scored[i].finding.SourceID, scored[i].finding.Claim)),
					strings.TrimSpace(firstNonEmpty(scored[j].finding.ID, scored[j].finding.SourceID, scored[j].finding.Claim)),
				) < 0
			}
			return scored[i].finding.Confidence > scored[j].finding.Confidence
		}
		return scored[i].score > scored[j].score
	})
	evidence := make([]*EvidenceFinding, 0, minInt(len(scored), limit))
	seen := make(map[string]struct{}, len(scored))
	for _, item := range scored {
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(item.finding.ID, item.finding.SourceID, item.finding.Claim)))
		if key != "" {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
		}
		copyFinding := item.finding
		evidence = append(evidence, &copyFinding)
		if len(evidence) >= limit {
			break
		}
	}
	if len(evidence) == 0 {
		return nil
	}
	return evidence
}

func loopEvidenceOverlap(keywords map[string]struct{}, values ...string) int {
	if len(keywords) == 0 {
		return 0
	}
	candidates := loopEvidenceTokenSet(values...)
	if len(candidates) == 0 {
		return 0
	}
	count := 0
	for token := range keywords {
		if _, exists := candidates[token]; exists {
			count++
		}
	}
	return count
}

func loopEvidenceTokenSet(values ...string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, value := range values {
		for _, token := range loopEvidenceTokens(value) {
			tokens[token] = struct{}{}
		}
	}
	return tokens
}

func loopEvidenceTokens(value string) []string {
	normalized := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			return unicode.ToLower(r)
		default:
			return ' '
		}
	}, value)
	if strings.TrimSpace(normalized) == "" {
		return nil
	}
	fields := strings.Fields(normalized)
	tokens := make([]string, 0, len(fields))
	for _, token := range fields {
		if len(token) <= 2 {
			continue
		}
		if _, stopword := loopEvidenceStopwords[token]; stopword {
			continue
		}
		tokens = append(tokens, token)
	}
	return tokens
}

func loopEvidenceSourceKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

var loopEvidenceStopwords = map[string]struct{}{
	"and":   {},
	"are":   {},
	"for":   {},
	"from":  {},
	"into":  {},
	"its":   {},
	"not":   {},
	"that":  {},
	"the":   {},
	"their": {},
	"these": {},
	"this":  {},
	"with":  {},
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

// ReWeightEvidenceConfidence adjusts evidence confidence scores based on specificity,
// recency, and source diversity to prevent stale single-assignment scores from dominating.
func ReWeightEvidenceConfidence(findings []EvidenceFinding) []EvidenceFinding {
	if len(findings) == 0 {
		return findings
	}

	sourceCounts := make(map[string]int, len(findings))
	for i := range findings {
		sourceCounts[findings[i].SourceID]++
	}

	sourceIndex := make(map[string]int, len(findings))
	for i := range findings {
		sid := findings[i].SourceID
		sourceIndex[sid]++
		original := findings[i].Confidence

		wordCount := len(strings.Fields(findings[i].Snippet))
		specificity := float64(wordCount) / 200.0
		if specificity > 1.0 {
			specificity = 1.0
		}

		recencyBoost := search.RecencyNorm(findings[i].Year)

		diversityPenalty := 0.0
		if sourceIndex[sid] > 3 {
			diversityPenalty = float64(sourceIndex[sid]-3) * 0.05
		}

		score := original*0.55 + specificity*0.10 + recencyBoost*0.30 - diversityPenalty
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		findings[i].Confidence = score
	}
	return findings
}

func (l *AutonomousLoop) assembleDossier(ctx context.Context, query string, papers []search.Paper) ([]EvidenceItem, error) {
	if len(papers) == 0 {
		return nil, nil
	}
	safePapers := SanitizeRetrievedPapersForLLM(papers, "assembleDossier")
	if len(safePapers) == 0 {
		return nil, nil
	}
	if packetItems := buildEvidenceItemsFromRawMaterial(query, safePapers, 12); len(packetItems) > 0 {
		packetItems = SanitizeEvidenceItemsForLLM(packetItems, "assembleDossier.rawMaterial")
		return packetItems, nil
	}

	// For efficiency, we only extract evidence from the top 5 most relevant papers
	topPapers := safePapers
	if len(topPapers) > 5 {
		topPapers = topPapers[:5]
	}

	// LLM unavailable: every paper degrades to heuristic extraction, in order.
	if l.llmClient == nil {
		evidence := make([]EvidenceItem, 0)
		for _, p := range topPapers {
			heuristicItems := buildEvidenceItemsFromPaper(p, 3)
			heuristicItems = SanitizeEvidenceItemsForLLM(heuristicItems, "assembleDossier.heuristic")
			evidence = append(evidence, heuristicItems...)
		}
		return evidence, nil
	}

	// Cooldown pre-check runs once before the fan-out so the heuristic
	// fallback path stays identical to the historical sequential behavior.
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		if shouldLogWisDevCooldownFallback("wisdev.autonomous.assembleDossier", time.Now()) {
			slog.Warn("assembleDossier: LLM extraction skipped during provider cooldown; using heuristic extraction",
				"component", "wisdev.autonomous",
				"operation", "assembleDossier",
				"retry_after_ms", remaining.Milliseconds(),
				"paperCount", len(topPapers),
			)
			EmitLoopDegraded(ctx, "assemble_dossier", "Evidence extraction skipped during cooldown; using heuristic extraction", map[string]any{
				"retry_after_ms": remaining.Milliseconds(),
				"paperCount":     len(topPapers),
			})
		}
		evidence := make([]EvidenceItem, 0)
		for _, fallbackPaper := range topPapers {
			evidence = append(evidence, SanitizeEvidenceItemsForLLM(buildEvidenceItemsFromPaper(fallbackPaper, 3), "assembleDossier.heuristic")...)
		}
		return evidence, nil
	}

	// Bounded fan-out: one LLM extraction per paper with at most
	// assembleDossierMaxConcurrentExtractions in flight. Results are collected
	// into an index-addressed slice so the flattened evidence ordering is
	// identical to the historical sequential per-paper loop.
	perPaper := make([][]EvidenceItem, len(topPapers))
	extractionSlots := make(chan struct{}, assembleDossierMaxConcurrentExtractions)
	var wg sync.WaitGroup
	for idx := range topPapers {
		wg.Add(1)
		go func(slot int, paper search.Paper) {
			defer wg.Done()
			extractionSlots <- struct{}{}
			defer func() { <-extractionSlots }()
			perPaper[slot] = l.extractDossierEvidenceForPaper(ctx, query, paper)
		}(idx, topPapers[idx])
	}
	wg.Wait()

	evidence := make([]EvidenceItem, 0)
	for _, items := range perPaper {
		evidence = append(evidence, items...)
	}
	return evidence, nil
}

// extractDossierEvidenceForPaper performs the per-paper LLM evidence
// extraction for assembleDossier, preserving the sequential loop's fallback
// semantics: any transport error, JSON parse failure, or empty extraction
// degrades to the paper's heuristic extraction, and unsafe extracted items
// are dropped.
func (l *AutonomousLoop) extractDossierEvidenceForPaper(ctx context.Context, query string, p search.Paper) []EvidenceItem {
	heuristicItems := buildEvidenceItemsFromPaper(p, 3)
	heuristicItems = SanitizeEvidenceItemsForLLM(heuristicItems, "assembleDossier.heuristic")

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`Extract the top 2-3 most important factual claims from the following paper that directly address the research query.
Query: %s
Paper Title: %s
Abstract: %s

Each item must include claim, snippet, and confidence between 0 and 1.
`, query, p.Title, p.Abstract))

	reqCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	resp, err := l.llmClient.StructuredOutput(reqCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveStandardModel(),
		JsonSchema: `{"type":"array","items":{"type":"object","required":["claim","snippet","confidence"],"properties":{"claim":{"type":"string"},"snippet":{"type":"string"},"confidence":{"type":"number"}}}}`,
	}))
	cancel()
	if err != nil {
		slog.Warn("assembleDossier: LLM extraction failed for paper",
			"component", "wisdev.autonomous",
			"operation", "assembleDossier",
			"paper_id", p.ID,
			"paper_title_preview", func() string {
				if len(p.Title) > 60 {
					return p.Title[:60]
				}
				return p.Title
			}(),
			"error", err.Error(),
		)
		return heuristicItems
	}

	var items []struct {
		Claim      string  `json:"claim"`
		Snippet    string  `json:"snippet"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(resp.JsonResult), &items); err != nil {
		slog.Warn("assembleDossier: JSON parse failed for paper extraction — skipping paper",
			"component", "wisdev.autonomous",
			"operation", "assembleDossier",
			"paper_id", p.ID,
			"error", err.Error(),
		)
		return heuristicItems
	}
	if len(items) == 0 {
		return heuristicItems
	}
	extracted := make([]EvidenceItem, 0, len(items))
	for _, item := range items {
		if safe, reason := IsSafeRetrievedLLMInput(item.Claim, item.Snippet); !safe {
			slog.Warn("assembleDossier: dropping extracted evidence due to suspicious content",
				"component", "wisdev.autonomous",
				"operation", "assembleDossier",
				"paper_id", p.ID,
				"reason", reason,
			)
			continue
		}
		extracted = append(extracted, EvidenceItem{
			Claim:      item.Claim,
			Snippet:    item.Snippet,
			PaperTitle: p.Title,
			PaperID:    p.ID,
			Status:     "verified",
			Confidence: item.Confidence,
		})
	}
	return extracted
}

func (l *AutonomousLoop) evaluateSufficiency(ctx context.Context, originalQuery string, papers []search.Paper) (*sufficiencyAnalysis, error) {
	papers = SanitizeRetrievedPapersForLLM(papers, "evaluateSufficiency")
	if len(papers) == 0 {
		return normalizesufficiencyAnalysis(originalQuery, &sufficiencyAnalysis{
			Sufficient: false,
			NextQuery:  originalQuery,
			Reasoning:  "No evidence has been retrieved yet.",
		}, papers), nil
	}
	if l.llmClient == nil {
		EmitLoopDegraded(ctx, "evaluate_sufficiency", "LLM unavailable; using heuristic sufficiency", map[string]any{
			"paperCount": len(papers),
		})
		return heuristicsufficiencyAnalysisWithoutLLM(originalQuery, papers), nil
	}
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		if shouldLogWisDevCooldownFallback("wisdev.autonomous.evaluate_sufficiency", time.Now()) {
			slog.Warn("Sufficiency evaluation skipped during provider cooldown; using heuristic fallback",
				"component", "wisdev.autonomous",
				"operation", "evaluate_sufficiency",
				"retry_after_ms", remaining.Milliseconds(),
				"paperCount", len(papers),
			)
			EmitLoopDegraded(ctx, "evaluate_sufficiency", "Sufficiency evaluation skipped during cooldown; using heuristic fallback", map[string]any{
				"retry_after_ms": remaining.Milliseconds(),
				"paperCount":     len(papers),
			})
		}
		return heuristicsufficiencyAnalysisWithoutLLM(originalQuery, papers), nil
	}

	paperSummaries := make([]string, 0, len(papers))
	for _, p := range papers {
		summary := strings.TrimSpace(firstNonEmpty(p.Abstract, p.Title))
		if len(summary) > 180 {
			summary = strings.TrimSpace(summary[:180]) + "..."
		}
		paperSummaries = append(paperSummaries, fmt.Sprintf("- %s [%s/%s]: %s",
			strings.TrimSpace(firstNonEmpty(p.Title, p.ID)),
			strings.TrimSpace(firstNonEmpty(p.Source, "unknown")),
			strings.TrimSpace(firstNonEmpty(p.Venue, "unknown")),
			summary,
		))
	}
	evidenceItems := collectEvidenceItemsFromPapers(papers, 2, 8)
	evidenceSummaries := make([]string, 0, len(evidenceItems))
	for _, item := range evidenceItems {
		evidenceSummaries = append(evidenceSummaries, fmt.Sprintf("- [%s] %s", strings.TrimSpace(firstNonEmpty(item.PaperTitle, item.PaperID)), strings.TrimSpace(firstNonEmpty(item.Snippet, item.Claim))))
	}
	if len(evidenceSummaries) == 0 {
		evidenceSummaries = append(evidenceSummaries, paperSummaries...)
	}
	observedSourceFamilies := buildObservedSourceFamiliesFromPapers(papers)

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`Evaluate if the following papers provide enough evidence to fully answer the research query.
Query: %s
Observed Source Families: %s
Evidence Snippets:
%s
Papers Found:
%s

Return:
- sufficient: whether the current evidence is enough
- reasoning: concise explanation
- nextQuery: best single follow-up query if more research is needed
- nextQueries: up to 3 targeted follow-up queries
- missingAspects: key unanswered subtopics or gaps
- missingSourceTypes: source families or evidence types that are still missing
- contradictions: contradictory claims that need resolution
- confidence: confidence between 0 and 1

Leave nextQuery and nextQueries empty when the evidence is already sufficient.
`, originalQuery, strings.Join(observedSourceFamilies, ", "), strings.Join(evidenceSummaries, "\n"), strings.Join(paperSummaries, "\n")))

	reqCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	resp, err := l.llmClient.StructuredOutput(reqCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveStandardModel(),
		JsonSchema: `{"type":"object","required":["sufficient","reasoning","nextQuery"],"properties":{"sufficient":{"type":"boolean"},"reasoning":{"type":"string"},"nextQuery":{"type":"string"},"nextQueries":{"type":"array","items":{"type":"string"},"maxItems":3},"missingAspects":{"type":"array","items":{"type":"string"},"maxItems":5},"missingSourceTypes":{"type":"array","items":{"type":"string"},"maxItems":4},"contradictions":{"type":"array","items":{"type":"string"},"maxItems":4},"confidence":{"type":"number"}}}`,
	}))
	cancel()
	if err != nil {
		return nil, err
	}

	var analysis sufficiencyAnalysis
	if err := json.Unmarshal([]byte(resp.JsonResult), &analysis); err != nil {
		return nil, err
	}
	return normalizesufficiencyAnalysis(originalQuery, &analysis, papers), nil
}

func heuristicsufficiencyAnalysisWithoutLLM(originalQuery string, papers []search.Paper) *sufficiencyAnalysis {
	evidenceItems := collectEvidenceItemsFromPapers(papers, 2, 8)
	sourceFamilies := buildObservedSourceFamiliesFromPapers(papers)
	analysis := &sufficiencyAnalysis{
		Sufficient: false,
		Reasoning:  "Structured sufficiency checkpoint unavailable; heuristic evidence coverage used without declaring the research complete.",
		Confidence: 0.45,
	}
	if len(papers) == 0 || len(evidenceItems) == 0 {
		analysis.NextQuery = strings.TrimSpace(originalQuery)
		analysis.MissingAspects = []string{"No grounded evidence snippets were extracted from retrieved papers."}
	} else {
		analysis.MissingAspects = []string{"Structured sufficiency confirmation is unavailable; continue retrieval until search budgets or explicit coverage gates stop the loop."}
	}
	if len(sourceFamilies) < 2 {
		analysis.MissingSourceTypes = []string{"independent source diversity"}
	}
	return normalizesufficiencyAnalysis(originalQuery, analysis, papers)
}

type sufficiencyAnalysis struct {
	Sufficient         bool     `json:"sufficient"`
	Reasoning          string   `json:"reasoning"`
	NextQuery          string   `json:"nextQuery"`
	NextQueries        []string `json:"nextQueries,omitempty"`
	MissingAspects     []string `json:"missingAspects,omitempty"`
	MissingSourceTypes []string `json:"missingSourceTypes,omitempty"`
	Contradictions     []string `json:"contradictions,omitempty"`
	Confidence         float64  `json:"confidence,omitempty"`
}

func normalizesufficiencyAnalysis(originalQuery string, analysis *sufficiencyAnalysis, papers []search.Paper) *sufficiencyAnalysis {
	if analysis == nil {
		analysis = &sufficiencyAnalysis{}
	}
	analysis.Reasoning = strings.TrimSpace(analysis.Reasoning)
	analysis.MissingAspects = dedupeTrimmedStrings(append([]string(nil), analysis.MissingAspects...))
	analysis.MissingSourceTypes = dedupeTrimmedStrings(append([]string(nil), analysis.MissingSourceTypes...))
	analysis.Contradictions = dedupeTrimmedStrings(append([]string(nil), analysis.Contradictions...))

	queries := normalizeLoopQueries("", append([]string{analysis.NextQuery}, analysis.NextQueries...))
	if !analysis.Sufficient && len(queries) == 0 {
		queries = deriveLoopFollowUpQueries(originalQuery, analysis, papers)
	}
	if len(queries) > 4 {
		queries = queries[:4]
	}
	analysis.NextQueries = queries
	if strings.TrimSpace(analysis.NextQuery) == "" && len(analysis.NextQueries) > 0 {
		analysis.NextQuery = analysis.NextQueries[0]
	}
	if analysis.Confidence <= 0 {
		analysis.Confidence = map[bool]float64{true: 0.82, false: 0.45}[analysis.Sufficient]
	}
	analysis.Confidence = ClampFloat(analysis.Confidence, 0, 1)
	return analysis
}

func (l *AutonomousLoop) synthesizeWithEvidence(ctx context.Context, query string, papers []search.Paper, evidence []EvidenceItem) (*rag.StructuredAnswer, error) {
	papers = SanitizeRetrievedPapersForLLM(papers, "synthesizeWithEvidence")
	evidence = SanitizeEvidenceItemsForLLM(evidence, "synthesizeWithEvidence")
	if l.llmClient == nil || l.brainCaps == nil {
		EmitLoopDegraded(ctx, "synthesize_with_evidence", "LLM unavailable; using heuristic synthesis", map[string]any{
			"paperCount":    len(papers),
			"evidenceCount": len(evidence),
		})
		return heuristicStructuredSynthesisWithoutLLM(query, papers, evidence), nil
	}
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		if shouldLogWisDevCooldownFallback("wisdev.autonomous.synthesizeWithEvidence", time.Now()) {
			slog.Warn("synthesis skipped during provider cooldown; using heuristic synthesis",
				"component", "wisdev.autonomous",
				"operation", "synthesizeWithEvidence",
				"retry_after_ms", remaining.Milliseconds(),
				"paperCount", len(papers),
				"evidenceCount", len(evidence),
			)
			EmitLoopDegraded(ctx, "synthesize_with_evidence", "Synthesis skipped during cooldown; using heuristic synthesis", map[string]any{
				"retry_after_ms": remaining.Milliseconds(),
				"paperCount":     len(papers),
				"evidenceCount":  len(evidence),
			})
		}
		return heuristicStructuredSynthesisWithoutLLM(query, papers, evidence), nil
	}

	sources := make([]Source, len(papers))
	for i, p := range papers {
		sources[i] = mapPaperToSource(p)
	}

	ans, err := safeSynthesizeStructuredAnswer(ctx, l.brainCaps, query, sources)
	if err != nil {
		if shouldAbortAutonomousLoop(err) || ctx.Err() != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("structured synthesis rate limited; using heuristic synthesis",
				"component", "wisdev.autonomous",
				"operation", "synthesizeWithEvidence",
				"error", err.Error(),
				"paperCount", len(papers),
				"evidenceCount", len(evidence),
			)
			EmitLoopDegraded(ctx, "synthesize_with_evidence", "Structured synthesis rate limited; using heuristic synthesis", map[string]any{
				"error":         err.Error(),
				"paperCount":    len(papers),
				"evidenceCount": len(evidence),
				"fallback":      "rate_limit",
			})
			return heuristicStructuredSynthesisWithoutLLM(query, papers, evidence), nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "llm client is not configured") {
			EmitLoopDegraded(ctx, "synthesize_with_evidence", "LLM not configured; using heuristic synthesis", map[string]any{
				"paperCount":    len(papers),
				"evidenceCount": len(evidence),
			})
			return heuristicStructuredSynthesisWithoutLLM(query, papers, evidence), nil
		}
		fallbackText, fallbackErr := l.synthesizePlainTextFallback(ctx, query, papers, evidence)
		if fallbackErr != nil {
			if shouldAbortAutonomousLoop(fallbackErr) || ctx.Err() != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				return nil, fallbackErr
			}
			slog.Warn("synthesis LLM fallback failed; using heuristic synthesis",
				"component", "wisdev.autonomous",
				"operation", "synthesizeWithEvidence",
				"structured_error", err.Error(),
				"fallback_error", fallbackErr.Error(),
				"paperCount", len(papers),
				"evidenceCount", len(evidence),
			)
			EmitLoopDegraded(ctx, "synthesize_with_evidence", "Synthesis LLM fallback failed; using heuristic synthesis", map[string]any{
				"structured_error": err.Error(),
				"fallback_error":   fallbackErr.Error(),
				"paperCount":       len(papers),
				"evidenceCount":    len(evidence),
			})
			return heuristicStructuredSynthesisWithoutLLM(query, papers, evidence), nil
		}
		return &rag.StructuredAnswer{
			Text: fallbackText,
			Sections: []rag.AnswerSection{{
				Heading: "Synthesis",
				Sentences: []rag.AnswerClaim{{
					Text:        fallbackText,
					EvidenceIDs: evidenceItemIDs(evidence),
					Unsupported: len(evidence) == 0,
				}},
			}},
		}, nil
	}
	return ans, nil
}

func safeSynthesizeStructuredAnswer(ctx context.Context, caps *BrainCapabilities, query string, sources []Source) (answer *rag.StructuredAnswer, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("structured synthesis panic: %v", recovered)
		}
	}()
	return caps.SynthesizeAnswer(ctx, query, sources, "")
}

func (l *AutonomousLoop) synthesizePlainTextFallback(ctx context.Context, query string, papers []search.Paper, evidence []EvidenceItem) (string, error) {
	papers = SanitizeRetrievedPapersForLLM(papers, "synthesizePlainTextFallback")
	evidence = SanitizeEvidenceItemsForLLM(evidence, "synthesizePlainTextFallback")
	if l == nil || l.llmClient == nil {
		return heuristicSynthesisWithoutLLM(query, papers, evidence), nil
	}
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		if shouldLogWisDevCooldownFallback("wisdev.autonomous.synthesizePlainTextFallback", time.Now()) {
			slog.Warn("plain-text synthesis skipped during provider cooldown; using heuristic synthesis",
				"component", "wisdev.autonomous",
				"operation", "synthesizePlainTextFallback",
				"retry_after_ms", remaining.Milliseconds(),
				"paperCount", len(papers),
				"evidenceCount", len(evidence),
			)
			EmitLoopDegraded(ctx, "synthesize_plain_text", "Plain-text synthesis skipped during cooldown; using heuristic synthesis", map[string]any{
				"retry_after_ms": remaining.Milliseconds(),
				"paperCount":     len(papers),
				"evidenceCount":  len(evidence),
			})
		}
		return heuristicSynthesisWithoutLLM(query, papers, evidence), nil
	}
	var sourceText strings.Builder
	for _, p := range papers {
		summary := strings.TrimSpace(firstNonEmpty(p.Abstract, p.FullText, p.Venue))
		sourceText.WriteString(fmt.Sprintf("- [%s] %s%s: %s\n", p.ID, p.Title, formatSynthesisPaperMeta(p), summary))
	}
	var evidenceText strings.Builder
	for _, item := range evidence {
		evidenceText.WriteString(fmt.Sprintf("- [%s] %s: %s\n", item.PaperID, item.Claim, item.Snippet))
	}
	prompt := fmt.Sprintf(`Synthesize a comprehensive research report for the query: "%s"
Based on %d sources found. Write for working researchers: explanatory, insight-driven, and critical rather than encyclopedic.

Requirements:
- Ground every factual claim in the Sources and Verified Evidence lists only. Do not invent studies, statistics, or author names.
- Use inline citations in author-year form (e.g., Smith et al., 2021) whenever attributing a finding.
- Include citation counts when available.
- If evidence is insufficient for a claim, say so explicitly instead of guessing.
- Do not end sentences or bullets with ellipsis ("..."); write complete thoughts.
- Structure with headings: Research landscape, Synthesis, Key literature, Grounded evidence, Implications for researchers, Questions worth investigating.
- End with a short methodological note on evidence limits.

Sources:
%s

Verified Evidence:
%s`, query, len(papers), sourceText.String(), evidenceText.String())
	resp, err := l.llmClient.Generate(ctx, &llmv1.GenerateRequest{
		Prompt: prompt,
		Model:  llm.ResolveHeavyModel(),
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.GetText()), nil
}

func evidenceItemIDs(evidence []EvidenceItem) []string {
	ids := make([]string, 0, len(evidence))
	for _, item := range evidence {
		if id := strings.TrimSpace(item.PaperID); id != "" {
			ids = append(ids, id)
		}
	}
	return uniqueTrimmedStrings(ids)
}

func heuristicSynthesisWithoutLLM(query string, papers []search.Paper, evidence []EvidenceItem) string {
	relevantEvidence := filterEvidenceByQueryRelevance(query, evidence)
	relevantPapers := filterPapersByQueryRelevance(query, papers)
	clauses := queryTopicClauses(query)
	landscape := buildSynthesisLandscape(query, relevantPapers, papers)
	registry := buildCitationRegistry(relevantPapers)

	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Provisional Research Synthesis: %s\n\n", strings.TrimSpace(query)))
	sb.WriteString("> Heuristic synthesis (LLM unavailable or degraded). Numbered references [n] map to the bibliography below — verify claims against primary sources before citing. Set WISDEV_LLM_PROVIDER=ollama|cloud|hybrid, run Ollama, configure Vertex, or start the orchestrator sidecar for LLM prose.\n\n")

	sb.WriteString("## Research landscape\n")
	sb.WriteString(formatSynthesisLandscape(landscape))
	sb.WriteString("\n\n")

	sb.WriteString("## Executive takeaway\n")
	for _, line := range buildSynthesisExecutiveTakeaway(query, relevantPapers, relevantEvidence, registry) {
		sb.WriteString("- " + line + "\n")
	}
	sb.WriteString("\n")

	if mix := buildSynthesisEvidenceMix(relevantPapers); mix != "" {
		sb.WriteString("## Evidence mix\n")
		sb.WriteString(mix)
		sb.WriteString("\n\n")
	}

	if themes := extractSynthesisThemes(query, relevantPapers, 5); len(themes) > 0 {
		sb.WriteString("## Emerging themes\n")
		for _, theme := range themes {
			sb.WriteString("- " + theme + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Synthesis\n")
	sb.WriteString(buildSynthesisNarrative(query, relevantPapers, clauses, registry))
	sb.WriteString("\n\n")
	if corroboration := buildSynthesisCorroboration(query, relevantPapers, registry); corroboration != "" {
		sb.WriteString(corroboration)
		sb.WriteString("\n\n")
	}

	if len(clauses) > 0 {
		sb.WriteString("## Thematic threads\n")
		for _, clause := range clauses {
			sb.WriteString(fmt.Sprintf("- **%s**: Literature admitted for this thread should be read for mechanisms, endpoints, and limitations specific to %s.\n", titleCasePhrase(clause), clause))
		}
		sb.WriteString("\n")
	}

	orderedSections := synthesisOrderedTopicSections(query, relevantPapers)
	if bridge := buildSynthesisCrossThemeBridge(clauses, orderedSections); bridge != "" {
		sb.WriteString("## Cross-theme bridge\n")
		sb.WriteString(bridge)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Key literature\n")
	if len(orderedSections) > 1 {
		for _, section := range orderedSections {
			if len(section.Papers) == 0 {
				continue
			}
			sb.WriteString(fmt.Sprintf("### %s\n", section.Label))
			for _, bullet := range formatSynthesisPaperBullets(query, section.Papers, relevantEvidence, 3, registry) {
				sb.WriteString(bullet + "\n")
			}
			sb.WriteString("\n")
		}
	} else {
		for _, bullet := range formatSynthesisPaperBullets(query, relevantPapers, relevantEvidence, 6, registry) {
			sb.WriteString(bullet + "\n")
		}
		sb.WriteString("\n")
	}

	evidenceLines := formatSynthesisEvidenceBullets(relevantEvidence, relevantPapers, 6, registry)
	if len(evidenceLines) > 0 {
		sb.WriteString("## Grounded evidence claims\n")
		sb.WriteString("Extracted snippets with numbered inline citations (author, year, citations where available):\n")
		for _, line := range evidenceLines {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Corroboration & tensions\n")
	sb.WriteString(buildSynthesisConsensusNote(relevantPapers, relevantEvidence))
	sb.WriteString("\n")
	for _, line := range buildSynthesisTensions(query, relevantPapers) {
		sb.WriteString("- " + line + "\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## Retrieval gaps to address\n")
	for _, line := range buildSynthesisRetrievalGaps(query, relevantPapers, papers, relevantEvidence) {
		sb.WriteString("- " + line + "\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## Implications for researchers\n")
	for _, line := range buildSynthesisImplications(query, relevantPapers, relevantEvidence) {
		sb.WriteString("- " + line + "\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## Questions worth investigating\n")
	for idx, prompt := range buildSynthesisResearchPrompts(query, clauses, relevantPapers) {
		sb.WriteString(fmt.Sprintf("%d. %s\n", idx+1, prompt))
	}
	sb.WriteString("\n")

	bibLines := formatSynthesisBibliography(relevantPapers, 8, registry)
	if len(bibLines) > 0 {
		sb.WriteString("## References cited in this synthesis\n")
		for _, line := range bibLines {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Methodological note\n")
	if len(relevantPapers) > 0 && len(relevantPapers) < len(papers) {
		sb.WriteString(fmt.Sprintf("- Filtered %d off-topic source(s) to keep synthesis focused.\n", len(papers)-len(relevantPapers)))
	}
	sb.WriteString("- Press `E` in the TUI to re-run with deeper retrieval, or increase max iterations in configuration.\n")

	return polishSynthesisText(sb.String())
}

func synthesisTopicSections(query string, papers []search.Paper) map[string][]search.Paper {
	clauses := queryTopicClauses(query)
	if len(clauses) < 2 || len(papers) == 0 {
		return nil
	}
	sections := make(map[string][]search.Paper, len(clauses))
	for _, clause := range clauses {
		label := synthesisSectionLabel(clause)
		for _, paper := range papers {
			if paperMatchesSingleTopicRelevance(clause, paper) {
				sections[label] = append(sections[label], paper)
			}
		}
		if len(sections[label]) > 0 {
			sections[label] = search.SortPapersByPreferenceWithQuery(sections[label], query)
		}
	}
	if len(sections) < 2 {
		return nil
	}
	return sections
}

func heuristicStructuredSynthesisWithoutLLM(query string, papers []search.Paper, evidence []EvidenceItem) *rag.StructuredAnswer {
	plain := heuristicSynthesisWithoutLLM(query, papers, evidence)
	relevantPapers := filterPapersByQueryRelevance(query, papers)
	relevantEvidence := filterEvidenceByQueryRelevance(query, evidence)
	registry := buildCitationRegistry(relevantPapers)

	sections := []rag.AnswerSection{{
		Heading: "Research landscape",
		Sentences: []rag.AnswerClaim{{
			Text:       formatSynthesisLandscape(buildSynthesisLandscape(query, relevantPapers, papers)),
			Confidence: 0.65,
		}},
	}}

	takeaway := rag.AnswerSection{Heading: "Executive takeaway"}
	for _, line := range buildSynthesisExecutiveTakeaway(query, relevantPapers, relevantEvidence, registry) {
		takeaway.Sentences = append(takeaway.Sentences, rag.AnswerClaim{
			Text:       line,
			Confidence: 0.7,
		})
	}
	if len(takeaway.Sentences) > 0 {
		sections = append(sections, takeaway)
	}

	if mix := buildSynthesisEvidenceMix(relevantPapers); mix != "" {
		sections = append(sections, rag.AnswerSection{
			Heading: "Evidence mix",
			Sentences: []rag.AnswerClaim{{
				Text:       mix,
				Confidence: 0.6,
			}},
		})
	}

	narrative := buildSynthesisNarrative(query, relevantPapers, queryTopicClauses(query), registry)
	if corroboration := buildSynthesisCorroboration(query, relevantPapers, registry); corroboration != "" {
		narrative += " " + corroboration
	}
	sections = append(sections, rag.AnswerSection{
		Heading: "Synthesis",
		Sentences: []rag.AnswerClaim{{
			Text:       narrative,
			Confidence: 0.68,
		}},
	})

	literature := rag.AnswerSection{Heading: "Key literature"}
	topPapers := search.SortPapersByPreferenceWithQuery(relevantPapers, query)
	for idx, paper := range topPapers {
		if idx >= 4 {
			break
		}
		bullets := formatSynthesisPaperBullets(query, []search.Paper{paper}, relevantEvidence, 1, registry)
		if len(bullets) == 0 {
			continue
		}
		text := strings.TrimPrefix(strings.TrimSpace(bullets[0]), "- ")
		paperID := strings.TrimSpace(paper.ID)
		literature.Sentences = append(literature.Sentences, rag.AnswerClaim{
			Text:        text,
			EvidenceIDs: []string{paperID},
			Confidence:  0.62,
		})
	}
	if len(literature.Sentences) > 0 {
		sections = append(sections, literature)
	}

	if claim := firstEvidenceClaim(relevantEvidence); claim != "" {
		sections = append(sections, rag.AnswerSection{
			Heading: "Grounded evidence",
			Sentences: []rag.AnswerClaim{{
				Text:        claim,
				EvidenceIDs: evidenceItemIDs(relevantEvidence),
				Confidence:  0.7,
			}},
		})
	}

	return &rag.StructuredAnswer{Text: plain, Sections: sections}
}

func firstEvidenceClaim(evidence []EvidenceItem) string {
	for _, item := range evidence {
		if claim := strings.TrimSpace(item.Claim); claim != "" {
			return claim
		}
		if snippet := strings.TrimSpace(item.Snippet); snippet != "" {
			return snippet
		}
	}
	return ""
}

func (l *AutonomousLoop) refineDraftWithCritique(ctx context.Context, query string, draft string, critique *LoopDraftCritique, evidence []EvidenceItem) (string, error) {
	evidence = SanitizeEvidenceItemsForLLM(evidence, "refineDraftWithCritique")
	if critique == nil || !critique.NeedsRevision {
		return draft, nil
	}
	if l.llmClient == nil {
		EmitLoopDegraded(ctx, "refine_draft", "LLM unavailable; using heuristic draft refinement", nil)
		return heuristicRefinedDraftWithoutLLM(query, draft, critique), nil
	}
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		if shouldLogWisDevCooldownFallback("wisdev.autonomous.refineDraftWithCritique", time.Now()) {
			slog.Warn("draft critique refinement skipped during provider cooldown; using heuristic refinement",
				"component", "wisdev.autonomous",
				"operation", "refineDraftWithCritique",
				"stage", "cooldown_fallback",
				"retry_after_ms", remaining.Milliseconds(),
			)
			EmitLoopDegraded(ctx, "refine_draft", "Draft refinement skipped during cooldown; using heuristic refinement", map[string]any{
				"retry_after_ms": remaining.Milliseconds(),
				"fallback":       "cooldown",
			})
		}
		return heuristicRefinedDraftWithoutLLM(query, draft, critique), nil
	}
	evidenceText := ""
	for i, item := range evidence {
		evidenceText += fmt.Sprintf("%d. [%s] %s (Evidence: %s)\n", i+1, item.PaperTitle, item.Claim, item.Snippet)
	}
	prompt := fmt.Sprintf(`Revise the research draft for query "%s" using the critique and verified evidence.

Draft:
%s

Critique:
- Reasoning: %s
- Missing aspects: %s
- Missing source types: %s
- Contradictions: %s

Verified Evidence:
%s

Instructions:
1. Remove or qualify unsupported claims.
2. Explicitly mark unresolved gaps and contradictions.
3. Every paragraph MUST end with numbered inline citations [n] (Author, Year; citations) tied to the verified evidence list.
4. Do not end sentences with ellipsis ("...") or stray "   ." artifacts; write complete thoughts.
5. Do not invent new evidence beyond the verified evidence list.
`, query, draft, critique.Reasoning, strings.Join(critique.MissingAspects, "; "), strings.Join(critique.MissingSourceTypes, "; "), strings.Join(critique.Contradictions, "; "), evidenceText)
	refineCtx, cancel := context.WithTimeout(ctx, optionalCritiqueRefinementLatencyBudget)
	defer cancel()
	req := llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{Prompt: prompt}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier:   "standard",
		RequestClass:    string(llm.RequestClassStandard),
		LatencyBudgetMs: int(optionalCritiqueRefinementLatencyBudget / time.Millisecond),
	}))
	resp, err := l.llmClient.Generate(refineCtx, req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(refineCtx.Err(), context.DeadlineExceeded) {
			slog.Warn("draft critique refinement timed out; using heuristic refinement",
				"component", "wisdev.autonomous",
				"operation", "refineDraftWithCritique",
				"stage", "timeout_fallback",
				"latency_budget_ms", req.GetLatencyBudgetMs(),
			)
			EmitLoopDegraded(ctx, "refine_draft", "Draft refinement timed out; using heuristic refinement", map[string]any{
				"latency_budget_ms": req.GetLatencyBudgetMs(),
				"fallback":          "timeout",
			})
			return heuristicRefinedDraftWithoutLLM(query, draft, critique), nil
		}
		if shouldAbortAutonomousLoop(err) {
			return "", err
		}
		if strings.Contains(strings.ToLower(err.Error()), "llm client is not configured") {
			EmitLoopDegraded(ctx, "refine_draft", "LLM not configured; using heuristic draft refinement", nil)
			return heuristicRefinedDraftWithoutLLM(query, draft, critique), nil
		}
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("draft critique refinement rate limited; using heuristic refinement",
				"component", "wisdev.autonomous",
				"operation", "refineDraftWithCritique",
				"stage", "rate_limit_fallback",
				"error", err.Error(),
			)
			EmitLoopDegraded(ctx, "refine_draft", "Draft refinement rate limited; using heuristic refinement", map[string]any{
				"error":    err.Error(),
				"fallback": "rate_limit",
			})
			return heuristicRefinedDraftWithoutLLM(query, draft, critique), nil
		}
		slog.Warn("draft critique refinement LLM failed; using heuristic refinement",
			"component", "wisdev.autonomous",
			"operation", "refineDraftWithCritique",
			"error", err.Error(),
		)
		EmitLoopDegraded(ctx, "refine_draft", "Draft refinement LLM failed; using heuristic refinement", map[string]any{
			"error": err.Error(),
		})
		return heuristicRefinedDraftWithoutLLM(query, draft, critique), nil
	}
	if resp == nil || strings.TrimSpace(resp.GetText()) == "" {
		return draft, nil
	}
	return resp.GetText(), nil
}

func (l *AutonomousLoop) refreshLoopReasoning(ctx context.Context, req LoopRequest, papers []search.Paper, queryCoverage map[string][]search.Paper, gap *LoopGapState, queryID string) ([]EvidenceFinding, []Hypothesis) {
	evidenceItems, _ := l.assembleDossier(ctx, req.Query, papers)
	findings := make([]EvidenceFinding, 0, len(evidenceItems))
	totalConfidence := 0.0
	for idx, item := range evidenceItems {
		findings = append(findings, EvidenceFinding{
			ID:         stableWisDevID("finding", item.PaperID, item.Claim, fmt.Sprintf("%d", idx)),
			Claim:      item.Claim,
			Snippet:    item.Snippet,
			PaperTitle: item.PaperTitle,
			SourceID:   item.PaperID,
			Confidence: item.Confidence,
			Status:     firstNonEmpty(strings.TrimSpace(item.Status), "verified"),
		})
		totalConfidence += item.Confidence
	}
	findings = ReWeightEvidenceConfidence(findings)

	hypotheses := l.proposeLoopHypotheses(ctx, req.Query, req.SeedQueries, findings, queryCoverage, totalConfidence, req.DisableHypothesisGeneration)

	// R1 & R2: Evaluate hypotheses and update belief state
	if l.evaluator != nil && len(hypotheses) > 0 {
		providerCooldown := autonomousLLMCooldownRemaining(l)
		evaluator := l.evaluator
		if providerCooldown > 0 || gap == nil || strings.TrimSpace(gap.Reasoning) == "" || req.ResearchPlane != ResearchExecutionPlaneMultiAgent {
			evaluator = NewHypothesisEvaluator(nil)
		}
		hypothesisPtrs := toHypothesisPtrs(hypotheses)
		evaluationResults, branchedHypotheses := evaluator.EvaluateAllBatched(ctx, hypothesisPtrs, findings, 8)

		// R2: Update belief state with evaluated hypotheses and provenance
		if l.beliefManager != nil {
			l.beliefManager.BuildBeliefsFromHypotheses(hypothesisPtrs, findings, gap, queryID)

			// R4: Cross-source triangulation (Phase 5)
			l.beliefManager.TriangulateBeliefs(papers)

			findings = l.beliefManager.RecalibrateEvidenceConfidence(findings)
		}

		// D2/Phase 5: Inter-agent debate for high uncertainty or contradictions
		var committee *ResearchCommittee
		if providerCooldown <= 0 {
			committee = NewResearchCommittee(l.llmClient)
		} else {
			if shouldLogWisDevCooldownFallback("wisdev.autonomous.hypothesis_committee", time.Now()) {
				slog.Warn("autonomous hypothesis committee skipped during provider cooldown",
					"component", "wisdev.autonomous",
					"operation", "hypothesis_committee",
					"retry_after_ms", providerCooldown.Milliseconds(),
					"hypothesisCount", len(hypothesisPtrs),
				)
			}
		}
		for _, h := range hypothesisPtrs {
			if h.IsTerminated {
				continue
			}
			highDepthPlane := isHighDepthResearchPlane(req.ResearchPlane)
			needsCommittee := highDepthPlane && (len(findings) >= 2 ||
				(h.ConfidenceScore > 0 && h.ConfidenceScore < 0.65) ||
				h.ContradictionCount > 0)
			if needsCommittee && committee != nil {
				if verdict, err := committee.Deliberate(ctx, h, findings); err == nil && verdict != nil {
					// Translate committee verdict back to EvaluationResult
					score := h.ConfidenceScore
					switch verdict.Verdict {
					case "approve":
						score = 0.8
					case "reject":
						score = 0.1
					case "revise":
						score = 0.4
					}

					debateResult := EvaluationResult{
						HypothesisID: h.ID,
						Score:        score,
						Verdict:      verdict.Verdict,
						Reasoning:    fmt.Sprintf("Committee [%s]: %s", verdict.Role, verdict.Reason),
						EvaluatedAt:  NowMillis(),
					}
					h.ConfidenceScore = debateResult.Score
					h.Status = debateResult.Verdict
					if h.EvaluationHistory == nil {
						h.EvaluationHistory = make([]EvaluationResult, 0)
					}
					h.EvaluationHistory = append(h.EvaluationHistory, debateResult)
					if debateResult.Verdict == "reject" {
						h.IsTerminated = true
					}
				}
			}
		}

		// Prune low-confidence hypotheses (score < 0.3)
		prunedPtrs := evaluator.PruneHypothesesByScore(hypothesisPtrs, 0.3)

		// Add new branched hypotheses (R1: Tree of Thoughts)
		for _, bh := range branchedHypotheses {
			if bh != nil {
				prunedPtrs = append(prunedPtrs, bh)
			}
		}

		// Convert back to value slice
		hypotheses = make([]Hypothesis, len(prunedPtrs))
		for i, h := range prunedPtrs {
			hypotheses[i] = *h
		}

		slog.Info("Hypothesis evaluation, debate, and branching completed",
			"originalCount", len(evaluationResults),
			"branchedCount", len(branchedHypotheses),
			"finalCount", len(hypotheses))
	}

	return findings, hypotheses
}

func (l *AutonomousLoop) executeSwarmInterjections(ctx context.Context, req LoopRequest, papers []search.Paper, analysis *sufficiencyAnalysis, hypotheses []Hypothesis) []string {
	var queries []string

	// Contradiction Critic interjection
	if analysis != nil && len(analysis.Contradictions) > 0 {
		for _, c := range analysis.Contradictions {
			queries = append(queries, buildResearchWorkerQuery(req.Query, "contradiction resolution: "+c))
		}
	}

	// Source Diversifier interjection
	if analysis != nil && len(analysis.MissingSourceTypes) > 0 {
		for _, t := range analysis.MissingSourceTypes {
			queries = append(queries, buildResearchWorkerQuery(req.Query, "source family: "+t))
		}
	}

	// Hypothesis-driven interjection
	for _, h := range hypotheses {
		if !h.IsTerminated && h.ConfidenceScore < 0.5 && h.ConfidenceScore > 0 {
			queries = append(queries, buildResearchWorkerQuery(req.Query, "evidence for sub-claim: "+h.Claim))
		}
	}

	return normalizeLoopQueries("", queries)
}

// intermediateSynthesis performs a light synthesis to identify nuanced gaps (R1 refinement).
func (l *AutonomousLoop) intermediateSynthesis(ctx context.Context, query string, papers []search.Paper, evidence []EvidenceItem) (*sufficiencyAnalysis, error) {
	papers = SanitizeRetrievedPapersForLLM(papers, "intermediateSynthesis")
	evidence = SanitizeEvidenceItemsForLLM(evidence, "intermediateSynthesis")
	if l.llmClient == nil || len(papers) < 3 {
		return nil, nil
	}
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		if shouldLogWisDevCooldownFallback("wisdev.autonomous.intermediateSynthesis", time.Now()) {
			slog.Warn("intermediate synthesis skipped during provider cooldown",
				"component", "wisdev.autonomous",
				"operation", "intermediateSynthesis",
				"retry_after_ms", remaining.Milliseconds(),
				"paperCount", len(papers),
			)
		}
		return heuristicsufficiencyAnalysisWithoutLLM(query, papers), nil
	}

	slog.Info("Performing intermediate qualitative synthesis", "paperCount", len(papers))

	// 1. Perform light synthesis
	draft, err := l.synthesizeWithEvidence(ctx, query, papers, evidence)
	if err != nil {
		return nil, err
	}

	// 2. Evaluate draft quality and find nuanced gaps
	prompt := fmt.Sprintf(`Analyze this preliminary research draft for qualitative gaps.
Query: %s

Draft:
%v

Identify specific missing perspectives, methodological details, or counter-arguments that would make this research more rigorous.
Provide sufficiency, reasoning, missing aspects, and targeted next queries using the supplied structured output schema.`, query, draft)

	resp, err := l.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     appendWisdevStructuredOutputInstruction(prompt),
		Model:      llm.ResolveStandardModel(),
		JsonSchema: `{"type":"object","properties":{"sufficient":{"type":"boolean"},"reasoning":{"type":"string"},"missingAspects":{"type":"array","items":{"type":"string"}},"nextQueries":{"type":"array","items":{"type":"string"}}},"required":["sufficient","reasoning"]}`,
	}, "standard", false))

	if err != nil {
		return nil, err
	}

	var result sufficiencyAnalysis
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// shouldConvergeByBeliefState returns true when all active beliefs have reached
// high confidence (≥0.75) and there are at least 3 beliefs — indicating the loop
// has sufficient evidence to stop searching.
func shouldConvergeByBeliefState(bs *BeliefState) bool {
	if bs == nil {
		return false
	}
	active := bs.GetActiveBeliefs()
	if len(active) < 3 {
		return false
	}
	for _, b := range active {
		if b.Confidence < 0.75 {
			return false
		}
	}
	return true
}

// deprioritizeHighConfidenceHypotheses filters out hypotheses whose corresponding
// beliefs are already high-confidence so the explorer focuses on uncertain areas.
func (l *AutonomousLoop) deprioritizeHighConfidenceHypotheses(hypotheses []Hypothesis, bs *BeliefState) []Hypothesis {
	if bs == nil || len(hypotheses) == 0 {
		return hypotheses
	}
	uncertain := make([]Hypothesis, 0, len(hypotheses))
	confident := make([]Hypothesis, 0, len(hypotheses))
	for _, h := range hypotheses {
		if h.IsTerminated {
			continue
		}
		beliefID := stableWisDevID("belief", h.Claim, "", "")
		if b, exists := bs.Beliefs[beliefID]; exists && b.Confidence >= 0.75 {
			confident = append(confident, h)
			continue
		}
		uncertain = append(uncertain, h)
	}
	// Keep at least one high-confidence hypothesis for cross-validation.
	if len(uncertain) == 0 {
		return hypotheses
	}
	if len(confident) > 0 {
		uncertain = append(uncertain, confident[0])
	}
	return uncertain
}

// pruneObsoleteQueries removes pending queries that target already settled beliefs.
func (l *AutonomousLoop) pruneObsoleteQueries(pending *[]string, beliefs *BeliefState) int {
	if beliefs == nil || len(beliefs.Beliefs) == 0 || pending == nil || len(*pending) == 0 {
		return 0
	}

	prunedCount := 0
	activePending := make([]string, 0, len(*pending))

	for _, q := range *pending {
		obsolete := false
		lowerQ := strings.ToLower(q)

		for _, b := range beliefs.Beliefs {
			// If belief is refuted or highly triangulated, we might not need more queries for it
			if (b.Status == BeliefStatusRefuted || (b.Triangulated && b.Confidence > 0.9)) &&
				strings.Contains(lowerQ, strings.ToLower(b.Claim)) {
				obsolete = true
				break
			}
		}

		if obsolete {
			prunedCount++
			continue
		}
		activePending = append(activePending, q)
	}

	*pending = activePending
	return prunedCount
}

// coordinateAgentDebate implements an inter-agent debate protocol to resolve uncertainty.
func (l *AutonomousLoop) coordinateAgentDebate(ctx context.Context, hypothesis *Hypothesis, evidence []EvidenceFinding) (*EvaluationResult, error) {
	if l.llmClient == nil {
		return nil, fmt.Errorf("LLM client not available for debate")
	}

	slog.Info("Initiating inter-agent debate", "hypothesis", hypothesis.Claim)

	prompt := fmt.Sprintf(`You are facilitating a debate between two specialized research agents.

Agent A (Proposer): Supports the hypothesis.
Agent B (Critic): Searches for contradictions and methodological flaws.

Hypothesis: %s
Falsifiability Condition: %s

Collected Evidence:
%s

Debate structure:
1. Agent A presents supporting evidence and interprets it.
2. Agent B identifies contradictions, limitations, or alternative explanations.
3. Facilitator (You) synthesizes a consensus verdict.

Provide consensus confidence, verdict, reasoning, branching decision, and suggested tie-breaker queries using the supplied structured output schema.`,
		hypothesis.Claim,
		hypothesis.FalsifiabilityCondition,
		formatEvidenceForDebate(evidence))

	resp, err := l.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     appendWisdevStructuredOutputInstruction(prompt),
		Model:      llm.ResolveHeavyModel(), // Use heavy model for complex debate
		JsonSchema: `{"type":"object","properties":{"score":{"type":"number"},"verdict":{"type":"string"},"reasoning":{"type":"string"},"branchingDecision":{"type":"string"},"suggestedQueries":{"type":"array","items":{"type":"string"}}},"required":["score","verdict","reasoning"]}`,
	}, "heavy", false))

	if err != nil {
		return nil, err
	}

	var result EvaluationResult
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	result.HypothesisID = hypothesis.ID
	result.EvaluatedAt = NowMillis()
	return &result, nil
}

func formatEvidenceForDebate(evidence []EvidenceFinding) string {
	var sb strings.Builder
	for idx, ev := range evidence {
		sb.WriteString(fmt.Sprintf("%d. [Confidence: %.2f] %s (Source: %s)\n", idx+1, ev.Confidence, ev.Claim, ev.PaperTitle))
	}
	return sb.String()
}

func heuristicRefinedDraftWithoutLLM(query string, draft string, critique *LoopDraftCritique) string {
	body := strings.TrimSpace(draft)
	if critique == nil {
		return body
	}
	reasons := dedupeTrimmedStrings(append(append([]string{}, critique.MissingAspects...), critique.MissingSourceTypes...))
	reasons = append(reasons, critique.Contradictions...)
	reasons = dedupeTrimmedStrings(reasons)
	if len(reasons) == 0 {
		reasons = []string{firstNonEmpty(strings.TrimSpace(critique.Reasoning), "additional verification required")}
	}
	return strings.TrimSpace(fmt.Sprintf("%s\n\nVerification note for %q: %s", body, strings.TrimSpace(query), strings.Join(reasons, "; ")))
}

func shouldAbortAutonomousLoop(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}

	raw := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(raw, "context canceled")
}

func autonomousLLMCooldownRemaining(loop *AutonomousLoop) time.Duration {
	if loop == nil || loop.llmClient == nil {
		return 0
	}
	return loop.llmClient.ProviderCooldownRemaining()
}

func unmarshalLLMJSON(raw string, target any) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("empty llm json payload")
	}
	return json.Unmarshal([]byte(trimmed), target)
}

func calculateInitialConfidence(p search.Paper) float64 {
	conf := 0.6

	// Source reputation (journal vs preprint)
	venue := strings.ToLower(p.Venue)
	if strings.Contains(venue, "arxiv") || strings.Contains(venue, "preprint") || strings.Contains(venue, "medrxiv") || strings.Contains(venue, "biorxiv") {
		conf -= 0.05
	} else if venue != "" {
		conf += 0.05
	}

	// Recency (newer publications preferred for fast-moving fields)
	currentYear := time.Now().Year()
	switch {
	case p.Year >= currentYear-3:
		conf += 0.12
	case p.Year >= currentYear-8:
		conf += 0.04
	case p.Year > 1900 && p.Year < currentYear-12:
		conf -= 0.10
	}

	// Query relevance
	if p.Score > 0 {
		if p.Score > 0.8 {
			conf += 0.05
		}
	}

	if conf < 0.3 {
		return 0.3
	}
	if conf > 0.85 {
		return 0.85
	}
	return conf
}

type LoopDecision struct {
	ShouldContinue bool
	Reason         string
	TargetBeliefs  []string
	QueryStrategy  string
}

type SteeringSignal struct {
	Type      string // "redirect", "focus", "exclude", "approve", "reject"
	Payload   string
	Queries   []string
	Timestamp int64
}

func (l *AutonomousLoop) beliefDrivenContinuation(bs *BeliefState, budget int, used int, iteration int) LoopDecision {
	hardBudgetExhausted := budget > 0 && used >= budget

	if bs == nil || len(bs.GetActiveBeliefs()) == 0 {
		if hardBudgetExhausted {
			return LoopDecision{ShouldContinue: false, Reason: "search budget exhausted before beliefs formed"}
		}
		return LoopDecision{ShouldContinue: true, Reason: "initial belief discovery", QueryStrategy: "breadth"}
	}

	active := bs.GetActiveBeliefs()
	if len(active) == 0 {
		if hardBudgetExhausted {
			return LoopDecision{ShouldContinue: false, Reason: "search budget exhausted with no active beliefs"}
		}
		return LoopDecision{ShouldContinue: true, Reason: "refresh active beliefs", QueryStrategy: "breadth"}
	}

	allConfident := true
	totalConf := 0.0
	contradictedTargets := make([]string, 0)
	for _, b := range active {
		totalConf += b.Confidence
		if b.Confidence < 0.75 {
			allConfident = false
		}
		if len(b.ContradictingEvidence) > 0 {
			contradictedTargets = append(contradictedTargets, b.ID)
		}
	}

	avgConf := 0.0
	if len(active) > 0 {
		avgConf = totalConf / float64(len(active))
	}

	contradictionPressure := 0.0
	if l.beliefManager != nil {
		contradictionPressure = l.beliefManager.GetContradictionPressure()
	}

	if allConfident && contradictionPressure < 0.2 {
		return LoopDecision{ShouldContinue: false, Reason: "belief convergence"}
	}

	if contradictionPressure > 0.5 {
		if hardBudgetExhausted {
			return LoopDecision{ShouldContinue: false, Reason: "search budget exhausted before contradiction resolved", TargetBeliefs: contradictedTargets, QueryStrategy: "reconciliation"}
		}
		return LoopDecision{ShouldContinue: true, Reason: "high contradiction", TargetBeliefs: contradictedTargets, QueryStrategy: "reconciliation"}
	}

	var uncertain []*Belief
	if l != nil && l.beliefManager != nil {
		uncertain = l.beliefManager.GetUncertainBeliefs(0.4)
	} else {
		for _, belief := range active {
			if belief.Confidence < 0.4 {
				uncertain = append(uncertain, belief)
			}
		}
	}
	if len(uncertain) > 0 {
		if hardBudgetExhausted {
			return LoopDecision{ShouldContinue: false, Reason: "search budget exhausted with uncertain beliefs", QueryStrategy: "focus"}
		}
		targets := activeBeliefIDsByConfidence(uncertain, 4)
		return LoopDecision{ShouldContinue: true, Reason: "focus on uncertain beliefs", TargetBeliefs: targets, QueryStrategy: "focus"}
	}

	if hardBudgetExhausted {
		return LoopDecision{ShouldContinue: false, Reason: "search budget exhausted after belief review"}
	}

	if avgConf >= 0.5 && avgConf < 0.75 {
		return LoopDecision{ShouldContinue: true, Reason: "moderate confidence requires falsification", TargetBeliefs: activeBeliefIDsByConfidence(active, 4), QueryStrategy: "falsification"}
	}

	return LoopDecision{ShouldContinue: true, Reason: "default continuation", QueryStrategy: "breadth"}
}

func activeBeliefIDsByConfidence(beliefs []*Belief, limit int) []string {
	if limit <= 0 || len(beliefs) == 0 {
		return nil
	}
	filtered := make([]*Belief, 0, len(beliefs))
	for _, belief := range beliefs {
		if belief == nil || belief.Status != BeliefStatusActive || strings.TrimSpace(belief.ID) == "" {
			continue
		}
		filtered = append(filtered, belief)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Confidence == filtered[j].Confidence {
			return strings.TrimSpace(filtered[i].ID) < strings.TrimSpace(filtered[j].ID)
		}
		return filtered[i].Confidence < filtered[j].Confidence
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	ids := make([]string, 0, len(filtered))
	for _, belief := range filtered {
		ids = append(ids, strings.TrimSpace(belief.ID))
	}
	return ids
}

type WorkingMemoryManager struct {
	llmClient *llm.Client
}

func NewWorkingMemoryManager(llmClient *llm.Client) *WorkingMemoryManager {
	return &WorkingMemoryManager{llmClient: llmClient}
}

func (w *WorkingMemoryManager) ShouldCompact(evidenceCount int) bool {
	return evidenceCount > 50
}

func (w *WorkingMemoryManager) Compact(ctx context.Context, evidence []EvidenceFinding, bs *BeliefState) []EvidenceFinding {
	if !w.ShouldCompact(len(evidence)) || bs == nil {
		return evidence
	}

	// 1. Group evidence by belief it supports
	// 3. Drop evidence for pruned/refuted beliefs
	supportedByBelief := make(map[string][]EvidenceFinding)
	unassigned := make([]EvidenceFinding, 0)

	evidenceMap := make(map[string]EvidenceFinding)
	for _, ev := range evidence {
		evidenceMap[ev.ID] = ev
	}

	activeBeliefs := bs.GetActiveBeliefs()
	beliefIDs := make(map[string]struct{})
	for _, b := range activeBeliefs {
		beliefIDs[b.ID] = struct{}{}
		for _, evID := range b.SupportingEvidence {
			if ev, ok := evidenceMap[evID]; ok {
				supportedByBelief[b.ID] = append(supportedByBelief[b.ID], ev)
				delete(evidenceMap, evID) // Mark as assigned
			}
		}
	}

	// Add unassigned evidence
	for _, ev := range evidenceMap {
		unassigned = append(unassigned, ev)
	}

	var compacted []EvidenceFinding

	// 2. Keep top-3 by confidence, summarize rest into single item
	for beliefID, evs := range supportedByBelief {
		sort.Slice(evs, func(i, j int) bool {
			return evs[i].Confidence > evs[j].Confidence
		})

		if len(evs) <= 3 {
			compacted = append(compacted, evs...)
			continue
		}

		compacted = append(compacted, evs[:3]...)

		// Summarize the rest
		rest := evs[3:]

		// 4. Preserve all evidence IDs and provenance chains
		var mergedIDs []string
		var mergedProvenance []ProvenanceEntry
		for _, e := range rest {
			mergedIDs = append(mergedIDs, e.ID)
			mergedProvenance = append(mergedProvenance, e.ProvenanceChain...)
		}
		summaryID := strings.Join(mergedIDs, ",")
		if len(summaryID) > 100 {
			summaryID = "summary-" + rest[0].ID
		}

		summaryEv := EvidenceFinding{
			ID:              summaryID,
			Claim:           "Summarized evidence for belief: " + bs.Beliefs[beliefID].Claim,
			Snippet:         fmt.Sprintf("%d additional supporting items compacted.", len(rest)),
			SourceID:        rest[0].SourceID,
			Confidence:      rest[0].Confidence,
			Year:            rest[0].Year,
			ProvenanceChain: mergedProvenance,
		}
		compacted = append(compacted, summaryEv)
	}

	// Keep a few top unassigned ones just in case
	sort.Slice(unassigned, func(i, j int) bool {
		return unassigned[i].Confidence > unassigned[j].Confidence
	})
	if len(unassigned) > 5 {
		compacted = append(compacted, unassigned[:5]...)
	} else {
		compacted = append(compacted, unassigned...)
	}

	return compacted
}

func (w *WorkingMemoryManager) CompactItems(ctx context.Context, evidence []EvidenceItem, bs *BeliefState) []EvidenceItem {
	if !w.ShouldCompact(len(evidence)) || bs == nil {
		return evidence
	}

	// Just convert to EvidenceFinding, compact, and convert back
	var findings []EvidenceFinding
	for idx, item := range evidence {
		findings = append(findings, EvidenceFinding{
			ID:         stableWisDevID("finding", item.PaperID, item.Claim, fmt.Sprintf("%d", idx)),
			Claim:      item.Claim,
			Snippet:    item.Snippet,
			PaperTitle: item.PaperTitle,
			SourceID:   item.PaperID,
			Confidence: item.Confidence,
			Status:     firstNonEmpty(strings.TrimSpace(item.Status), "verified"),
		})
	}

	compactedFindings := w.Compact(ctx, findings, bs)

	var compacted []EvidenceItem
	for _, cf := range compactedFindings {
		compacted = append(compacted, EvidenceItem{
			Claim:      cf.Claim,
			Snippet:    cf.Snippet,
			PaperTitle: cf.PaperTitle,
			PaperID:    cf.SourceID,
			Status:     cf.Status,
			Confidence: cf.Confidence,
		})
	}
	return compacted
}
