package wisdev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
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

const optionalCritiqueRefinementLatencyBudget = 8 * time.Second

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
	SeedQueries                    []string                  `json:"seedQueries,omitempty"`
	InitialPapers                  []search.Paper            `json:"initialPapers,omitempty"`
	InitialQueryCoverage           map[string][]search.Paper `json:"initialQueryCoverage,omitempty"`
	InitialExecutedQueries         []string                  `json:"initialExecutedQueries,omitempty"`
	ResearchPlane                  ResearchExecutionPlane    `json:"researchPlane,omitempty"`
	Domain                         string                    `json:"domain"`
	ProjectID                      string                    `json:"projectId"`
	MaxIterations                  int                       `json:"maxIterations"`
	MaxSearchTerms                 int                       `json:"maxSearchTerms,omitempty"`
	BudgetCents                    int                       `json:"budgetCents"`
	HitsPerSearch                  int                       `json:"hitsPerSearch,omitempty"`
	MaxUniquePapers                int                       `json:"maxUniquePapers,omitempty"`
	AllocatedTokens                int                       `json:"allocatedTokens,omitempty"`
	DurableJobID                   string                    `json:"durableJobId,omitempty"`
	Mode                           string                    `json:"mode,omitempty"`
	EnableDynamicProviderSelection bool                      `json:"enableDynamicProviderSelection,omitempty"`
	DisableProgrammaticPlanning    bool                      `json:"disableProgrammaticPlanning,omitempty"`
	DisableHypothesisGeneration    bool                      `json:"disableHypothesisGeneration,omitempty"`
	SteeringChan                   <-chan SteeringSignal     `json:"-"`
	SteeringJournal                *RuntimeJournal           `json:"-"`
}

type LoopResult struct {
	FinalAnswer      string                    `json:"finalAnswer"`
	StructuredAnswer *rag.StructuredAnswer     `json:"structuredAnswer,omitempty"`
	Papers           []search.Paper            `json:"papers"`
	Evidence         []EvidenceFinding         `json:"evidence"`
	Branches         []ResearchBranch          `json:"branches,omitempty"`
	Iterations       int                       `json:"iterations"`
	Converged        bool                      `json:"converged"`
	BranchPlans      []ResearchBranchPlan      `json:"branchPlans,omitempty"`
	ExecutedQueries  []string                  `json:"executedQueries,omitempty"`
	QueryCoverage    map[string][]search.Paper `json:"queryCoverage,omitempty"`
	GapAnalysis      *LoopGapState             `json:"gapAnalysis,omitempty"`
	DraftCritique    *LoopDraftCritique        `json:"draftCritique,omitempty"`
	FinalizationGate *ResearchFinalizationGate `json:"finalizationGate,omitempty"`
	StopReason       string                    `json:"stopReason,omitempty"`
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
	plannedQueries := normalizeLoopQueries(req.Query, req.SeedQueries)
	slog.Info("Starting autonomous research loop", "query", req.Query, "maxIterations", req.MaxIterations, "seedQueryCount", maxInt(len(plannedQueries)-1, 0))
	var emit func(PlanExecutionEvent)
	if len(onEvent) > 0 {
		emit = onEvent[0]
	}
	if emit != nil {
		emit(PlanExecutionEvent{
			Type:      EventProgress,
			SessionID: strings.TrimSpace(req.ProjectID),
			Message:   "autonomous loop started",
			CreatedAt: NowMillis(),
		})
	}

	papers, _ := appendUniqueSearchPapersWithinBudget(nil, req.InitialPapers, maxInt(req.MaxUniquePapers, 0))
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
			slog.Info("belief-driven continuation stopped loop", "reason", decision.Reason)
			if decision.Reason == "belief convergence" {
				converged = true
			}
			break
		}
		iterations++
		remainingTerms := searchTermBudget - len(executedQueries)

		// Consume belief-driven query strategy
		switch decision.QueryStrategy {
		case "reconciliation":
			for _, bID := range decision.TargetBeliefs {
				if bs != nil {
					if b, ok := bs.Beliefs[bID]; ok {
						queueCandidate("reconcile contradiction: " + strings.TrimSpace(b.Claim))
					}
				}
			}
		case "focus":
			for _, bID := range decision.TargetBeliefs {
				if bs != nil {
					if b, ok := bs.Beliefs[bID]; ok {
						queueCandidate("evidence for: " + strings.TrimSpace(b.Claim))
					}
				}
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
			QualitySort:      true,
			DynamicProviders: shouldUseDynamicProviderSelection(req.Mode, req.ResearchPlane, req.EnableDynamicProviderSelection, l.llmClient),
			SkipCache:        true,
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
				for _, branch := range treeResult.Branches {
					for _, query := range branch.ExecutedQueries {
						plannedQueries = appendUniqueLoopQuery(plannedQueries, query)
						executedQueries = appendUniqueLoopQuery(executedQueries, query)
					}
				}
				if treeResult.MergeCandidate != nil {
					beforeTreeMerge := len(papers)
					papers, _ = appendUniqueSearchPapersWithinBudget(papers, treeResult.MergeCandidate.Papers, maxUniquePapers)
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
					slog.Info("Belief state convergence: all active beliefs high-confidence, stopping loop early",
						"iteration", i+1)
					converged = true
					break
				}
			}
		}

		// If newCount is 0, it means we either didn't do Phase 2 or Phase 2 found nothing new.
		// Fall back to Phase 1 (Broad search) for the current batch.
		if newCount == 0 {
			batch := nextLoopQueryBatch(&pendingQueries, minInt(currentParallelism, remainingTerms))
			if len(batch) == 0 {
				break
			}
			slog.Info("Loop iteration (Phase 1)", "index", i+1, "queryCount", len(batch), "queries", batch)

			batchResults := l.executeLoopSearchBatch(ctx, batch, searchOpts, currentParallelism)
			phase1Findings := make([]EvidenceFinding, 0)
			for _, batchResult := range batchResults {
				executedQueries = appendUniqueLoopQuery(executedQueries, batchResult.Query)
				beforeCount := len(papers)
				var acceptedPapers []search.Paper
				papers, acceptedPapers = appendUniqueSearchPapersWithinBudget(papers, batchResult.Result.Papers, maxUniquePapers)
				recordLoopQueryCoverage(queryCoverage, batchResult.Query, acceptedPapers)
				newCount += len(papers) - beforeCount
				for _, p := range acceptedPapers {
					phase1Findings = append(phase1Findings, EvidenceFinding{
						ID:         stableWisDevID("p1finding", batchResult.Query, p.ID),
						Claim:      p.Title,
						Snippet:    p.Abstract,
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
					slog.Info("Evidence saturation detected, skipping remaining retrieval", "diversity", saturation.DiversityScore)
					converged = true
					break
				} else if saturation.Recommendation == "expand-diversity" {
					slog.Info("Evidence concentrated, expanding diversity in next queries", "diversity", saturation.DiversityScore)
					queueCandidate(req.Query + " alternative perspectives")
				}
			}
		}

		slog.Debug("After iteration retrieval", "total", len(papers), "newCount", newCount)
		paperBudgetReached := maxUniquePapers > 0 && len(papers) >= maxUniquePapers

		// Belief-driven finalization gate: if all active beliefs are high-confidence,
		// override the sufficiency check outcome rather than burning an LLM call.
		if l.beliefManager != nil && shouldConvergeByBeliefState(l.beliefManager.GetState()) {
			slog.Info("Belief finalization gate: skipping LLM sufficiency check — all beliefs converged",
				"iteration", i+1,
				"avgConfidence", l.beliefManager.GetAverageConfidence(),
				"contradictionPressure", l.beliefManager.GetContradictionPressure())
			converged = true
			break
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
		} else {
			analysis, err = l.evaluateSufficiency(ctx, req.Query, papers)
		}
		if err == nil {
			lastAnalysis = analysis
			if analysis.Sufficient || i == maxLoopIterations-1 || paperBudgetReached {
				converged = analysis.Sufficient
				break
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
			gapState := buildLoopGapState(plannedQueries, executedQueries, queryCoverage, papers, analysis, false, req.ResearchPlane)
			enqueued := false
			for _, candidate := range buildFollowUpQueriesFromLedger(req.Query, gapState.Ledger, 4) {
				if queueCandidate(candidate) {
					enqueued = true
				}
			}
			if !enqueued {
				for _, candidate := range analysis.NextQueries {
					if queueCandidate(candidate) {
						enqueued = true
					}
				}
			}
			if !enqueued {
				for _, candidate := range deriveLoopFollowUpQueries(req.Query, analysis, papers) {
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
			)
			if analysis.Sufficient || paperBudgetReached || len(papers) >= 20 {
				converged = true
				break
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
	findings, hypotheses = l.refreshLoopReasoning(ctx, req, papers, queryCoverage, gapAnalysis, "")
	gapAnalysis = mergeHypothesisBranchLedger(gapAnalysis, req.Query, hypotheses)
	evidenceItems, _ := l.assembleDossier(ctx, req.Query, papers)

	// Context Compaction / Working Memory
	if l.beliefManager != nil {
		wmm := NewWorkingMemoryManager(l.llmClient)
		evidenceItems = wmm.CompactItems(ctx, evidenceItems, l.beliefManager.GetState())
		findings = wmm.Compact(ctx, findings, l.beliefManager.GetState())
	}

	mode := NormalizeWisDevMode(req.Mode)
	serviceTier := ResolveServiceTier(mode, false)
	session := &AgentSession{
		SessionID:      strings.TrimSpace(req.ProjectID),
		Query:          strings.TrimSpace(req.Query),
		CorrectedQuery: strings.TrimSpace(req.Query),
		DetectedDomain: strings.TrimSpace(req.Domain),
		Mode:           mode,
		ServiceTier:    serviceTier,
		MemoryTiers:    &MemoryTierState{},
	}
	if l.beliefManager != nil {
		session.BeliefState = l.beliefManager.GetState()
	}
	UpdateSessionReasoningGraph(session, hypotheses, findings, papers...)

	// 5. Draft Synthesis (Using Heavy Brain), then mandatory critique-driven retrieval reopening.
	reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
		Timestamp: NowMillis(),
		Phase:     "synthesis",
		Decision:  "draft",
		Reasoning: fmt.Sprintf("Synthesizing draft from %d papers and %d evidence items", len(papers), len(evidenceItems)),
	})
	structuredAnswer, err := l.synthesizeWithEvidence(ctx, req.Query, papers, evidenceItems)
	if err != nil {
		return nil, err
	}
	finalAnswer := structuredAnswer.PlainText
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
			slog.Warn("autonomous critique retrieval deferred during provider cooldown",
				"component", "wisdev.autonomous",
				"operation", "critique_retrieval",
				"stage", "cooldown_defer",
				"retry_after_ms", remaining.Milliseconds(),
				"candidateCount", len(critiqueCandidates),
			)
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
		currentSearchLimit := remainingLoopSearchLimit(len(papers), hitsPerSearch, maxUniquePapers)
		if currentSearchLimit > 0 && len(critiqueCandidates) > 0 {
			retrievalReopened = true
		}
		var batchResults []loopSearchBatchResult
		if retrievalReopened {
			batchResults = l.executeLoopSearchBatch(ctx, critiqueCandidates, search.SearchOpts{
				Limit:            currentSearchLimit,
				Domain:           req.Domain,
				QualitySort:      true,
				DynamicProviders: shouldUseDynamicProviderSelection(req.Mode, req.ResearchPlane, req.EnableDynamicProviderSelection, l.llmClient),
				SkipCache:        true,
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
			papers, acceptedPapers = appendUniqueSearchPapersWithinBudget(papers, batchResult.Result.Papers, maxUniquePapers)
			recordLoopQueryCoverage(queryCoverage, candidate, acceptedPapers)
			if len(papers) > beforeCount {
				retrievedMore = true
			}
		}
		critique.RetrievalReopened = retrievalReopened
		critique.AdditionalEvidenceFound = retrievedMore
		if retrievedMore {
			if analysis, err := l.evaluateSufficiency(ctx, req.Query, papers); err == nil {
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
				)
				if analysis.Sufficient || len(papers) >= 20 || (maxUniquePapers > 0 && len(papers) >= maxUniquePapers) {
					converged = true
				}
			}
			gapAnalysis = buildLoopGapState(plannedQueries, executedQueries, queryCoverage, papers, lastAnalysis, converged, req.ResearchPlane)
			evidenceItems, _ = l.assembleDossier(ctx, req.Query, papers)
			findings, hypotheses = l.refreshLoopReasoning(ctx, req, papers, queryCoverage, gapAnalysis, "")
			gapAnalysis = mergeHypothesisBranchLedger(gapAnalysis, req.Query, hypotheses)
			if l.beliefManager != nil {
				session.BeliefState = l.beliefManager.GetState()
			}
			UpdateSessionReasoningGraph(session, hypotheses, findings, papers...)
			structuredAnswer, err = l.synthesizeWithEvidence(ctx, req.Query, papers, evidenceItems)
			if err != nil {
				return nil, err
			}
			finalAnswer = structuredAnswer.PlainText
		}
		if !retrievedMore {
			finalAnswer, err = l.refineDraftWithCritique(ctx, req.Query, finalAnswer, critique, evidenceItems)
			if err != nil {
				return nil, err
			}
			structuredAnswer.PlainText = finalAnswer
		}
	}
	gapAnalysis = mergeDraftCritiqueIntoGapState(gapAnalysis, critique, req.Query)

	if emit != nil {
		emit(PlanExecutionEvent{
			Type:      EventCompleted,
			SessionID: strings.TrimSpace(req.ProjectID),
			Message:   "autonomous loop completed",
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
	reasoningTrace = append(reasoningTrace, ReasoningTraceEntry{
		Timestamp: NowMillis(),
		Phase:     "finalization",
		Decision:  "stop",
		Reasoning: stopReason,
	})

	return &LoopResult{
		FinalAnswer:      finalAnswer,
		StructuredAnswer: structuredAnswer,
		Papers:           papers,
		Evidence:         findings,
		Branches:         completedBranches,
		Iterations:       iterations,
		Converged:        converged,
		BranchPlans:      researchBranchPlansFromQueries(req.Query, plannedQueries),
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
		out[trimmed] = appendUniqueSearchPapers(nil, papers)
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
		if isHighDepthResearchPlane(plane) {
			return 6
		}
		return 4
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
			return 5
		}
		return 4
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
			return 6
		}
		return 4
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
	case ResearchExecutionPlaneAutonomous, ResearchExecutionPlaneDeep, ResearchExecutionPlaneMultiAgent, ResearchExecutionPlaneQuest:
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
	if NormalizeWisDevMode(mode) == WisDevModeYOLO {
		return true
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
			result, err := retrieveCanonicalSearchResult(ctx, l.searchReg, query, opts)
			if err != nil {
				result.Warnings = append(result.Warnings, search.ProviderWarning{
					Provider: "wisdev_core_mcp_tool",
					Message:  err.Error(),
				})
			}
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
				Snippet:    paper.Abstract,
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
			QualitySort:      true,
			DynamicProviders: shouldUseDynamicProviderSelection(req.Mode, req.ResearchPlane, req.EnableDynamicProviderSelection, l.llmClient),
			SkipCache:        true,
			LLMClient:        l.llmClient,
		}, currentParallelism)
		for _, batchResult := range batchResults {
			result.ExecutedQueries = appendUniqueLoopQuery(result.ExecutedQueries, batchResult.Query)
			var acceptedPapers []search.Paper
			result.Papers, acceptedPapers = appendUniqueSearchPapersWithinBudget(result.Papers, batchResult.Result.Papers, maxUniquePapers)
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
			)
			if analysis.Sufficient || len(result.Papers) >= 20 || (maxUniquePapers > 0 && len(result.Papers) >= maxUniquePapers) {
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
	emit(PlanExecutionEvent{
		Type:            EventProgress,
		TraceID:         NewTraceID(),
		SessionID:       strings.TrimSpace(req.ProjectID),
		Message:         strings.TrimSpace(message),
		Payload:         payload,
		Owner:           "go",
		OwningComponent: "wisdev-agent-os/orchestrator/internal/wisdev",
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
		trimmed := strings.TrimSpace(query)
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
		slog.Warn("autonomous hypothesis proposal skipped during provider cooldown; using query fallback",
			"component", "wisdev.autonomous",
			"operation", "proposeHypotheses",
			"query", strings.TrimSpace(primary),
			"retry_after_ms", remaining.Milliseconds(),
		)
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

		recencyBoost := 0.0
		if findings[i].Year > 0 {
			recencyBoost = float64(findings[i].Year-2015) * 0.02
			if recencyBoost < 0 {
				recencyBoost = 0
			}
			if recencyBoost > 0.15 {
				recencyBoost = 0.15
			}
		}

		diversityPenalty := 0.0
		if sourceIndex[sid] > 3 {
			diversityPenalty = float64(sourceIndex[sid]-3) * 0.05
		}

		score := original*0.7 + specificity*0.15 + recencyBoost*0.15 - diversityPenalty
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

	evidence := make([]EvidenceItem, 0)

	for _, p := range topPapers {
		heuristicItems := buildEvidenceItemsFromPaper(p, 3)
		heuristicItems = SanitizeEvidenceItemsForLLM(heuristicItems, "assembleDossier.heuristic")
		if l.llmClient == nil {
			evidence = append(evidence, heuristicItems...)
			continue
		}
		if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
			slog.Warn("assembleDossier: LLM extraction skipped during provider cooldown; using heuristic extraction",
				"component", "wisdev.autonomous",
				"operation", "assembleDossier",
				"retry_after_ms", remaining.Milliseconds(),
				"paperCount", len(topPapers),
			)
			for _, fallbackPaper := range topPapers {
				evidence = append(evidence, SanitizeEvidenceItemsForLLM(buildEvidenceItemsFromPaper(fallbackPaper, 3), "assembleDossier.heuristic")...)
			}
			return evidence, nil
		}

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
			evidence = append(evidence, heuristicItems...)
			continue
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
			evidence = append(evidence, heuristicItems...)
			continue
		}
		if len(items) == 0 {
			evidence = append(evidence, heuristicItems...)
			continue
		}
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
			evidence = append(evidence, EvidenceItem{
				Claim:      item.Claim,
				Snippet:    item.Snippet,
				PaperTitle: p.Title,
				PaperID:    p.ID,
				Status:     "verified",
				Confidence: item.Confidence,
			})
		}
	}
	return evidence, nil
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
		return heuristicsufficiencyAnalysisWithoutLLM(originalQuery, papers), nil
	}
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		slog.Warn("Sufficiency evaluation skipped during provider cooldown; using heuristic fallback",
			"component", "wisdev.autonomous",
			"operation", "evaluate_sufficiency",
			"retry_after_ms", remaining.Milliseconds(),
			"paperCount", len(papers),
		)
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
	sufficient := len(papers) > 0 && len(evidenceItems) > 0
	analysis := &sufficiencyAnalysis{
		Sufficient: sufficient,
		Reasoning:  "Structured sufficiency checkpoint unavailable; heuristic evidence coverage used.",
		Confidence: map[bool]float64{true: 0.66, false: 0.38}[sufficient],
	}
	if !sufficient {
		analysis.NextQuery = strings.TrimSpace(originalQuery)
		analysis.MissingAspects = []string{"No grounded evidence snippets were extracted from retrieved papers."}
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
		return heuristicStructuredSynthesisWithoutLLM(query, papers, evidence), nil
	}
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		slog.Warn("synthesis skipped during provider cooldown; using heuristic synthesis",
			"component", "wisdev.autonomous",
			"operation", "synthesizeWithEvidence",
			"retry_after_ms", remaining.Milliseconds(),
			"paperCount", len(papers),
			"evidenceCount", len(evidence),
		)
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
			return heuristicStructuredSynthesisWithoutLLM(query, papers, evidence), nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "llm client is not configured") {
			return heuristicStructuredSynthesisWithoutLLM(query, papers, evidence), nil
		}
		legacy, legacyErr := l.legacySynthesizePlainText(ctx, query, papers, evidence)
		if legacyErr != nil {
			if shouldAbortAutonomousLoop(legacyErr) || ctx.Err() != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				return nil, legacyErr
			}
			slog.Warn("synthesis LLM fallback failed; using heuristic synthesis",
				"component", "wisdev.autonomous",
				"operation", "synthesizeWithEvidence",
				"structured_error", err.Error(),
				"fallback_error", legacyErr.Error(),
				"paperCount", len(papers),
				"evidenceCount", len(evidence),
			)
			return heuristicStructuredSynthesisWithoutLLM(query, papers, evidence), nil
		}
		return &rag.StructuredAnswer{
			PlainText: legacy,
			Sections: []rag.AnswerSection{{
				Heading: "Synthesis",
				Sentences: []rag.AnswerClaim{{
					Text:        legacy,
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

func (l *AutonomousLoop) legacySynthesizePlainText(ctx context.Context, query string, papers []search.Paper, evidence []EvidenceItem) (string, error) {
	papers = SanitizeRetrievedPapersForLLM(papers, "legacySynthesizePlainText")
	evidence = SanitizeEvidenceItemsForLLM(evidence, "legacySynthesizePlainText")
	if l == nil || l.llmClient == nil {
		return heuristicSynthesisWithoutLLM(query, papers, evidence), nil
	}
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		slog.Warn("legacy synthesis skipped during provider cooldown; using heuristic synthesis",
			"component", "wisdev.autonomous",
			"operation", "legacySynthesizePlainText",
			"retry_after_ms", remaining.Milliseconds(),
			"paperCount", len(papers),
			"evidenceCount", len(evidence),
		)
		return heuristicSynthesisWithoutLLM(query, papers, evidence), nil
	}
	var sourceText strings.Builder
	for _, p := range papers {
		sourceText.WriteString(fmt.Sprintf("- [%s] %s: %s\n", p.ID, p.Title, p.Abstract))
	}
	var evidenceText strings.Builder
	for _, item := range evidence {
		evidenceText.WriteString(fmt.Sprintf("- [%s] %s: %s\n", item.PaperID, item.Claim, item.Snippet))
	}
	prompt := fmt.Sprintf(`Synthesize a comprehensive research report for the query: "%s"
Based on %d sources found.

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
	lines := []string{
		fmt.Sprintf("Provisional research synthesis for %q based on %d retrieved source(s).", strings.TrimSpace(query), len(papers)),
	}
	for i, item := range evidence {
		if i >= 4 {
			break
		}
		title := strings.TrimSpace(firstNonEmpty(item.PaperTitle, item.PaperID, fmt.Sprintf("source %d", i+1)))
		claim := strings.TrimSpace(firstNonEmpty(item.Claim, item.Snippet))
		snippet := strings.TrimSpace(item.Snippet)
		if claim == "" && snippet == "" {
			continue
		}
		if snippet != "" && snippet != claim {
			lines = append(lines, fmt.Sprintf("[%s] supports the working claim %q with evidence: %s", title, claim, snippet))
		} else {
			lines = append(lines, fmt.Sprintf("[%s] supports the working claim: %s", title, claim))
		}
	}
	if len(lines) == 1 {
		for i, paper := range papers {
			if i >= 4 {
				break
			}
			title := strings.TrimSpace(firstNonEmpty(paper.Title, paper.ID, fmt.Sprintf("source %d", i+1)))
			summary := strings.TrimSpace(firstNonEmpty(paper.Abstract, paper.FullText, paper.Venue))
			if summary == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("[%s] was retrieved as relevant evidence: %s", title, trimEvidenceText(summary, 220)))
		}
	}
	if len(lines) == 1 {
		lines = append(lines, "No grounded evidence snippets were available for synthesis.")
	}
	return strings.Join(lines, "\n\n")
}

func heuristicStructuredSynthesisWithoutLLM(query string, papers []search.Paper, evidence []EvidenceItem) *rag.StructuredAnswer {
	plain := heuristicSynthesisWithoutLLM(query, papers, evidence)
	evidenceIDs := evidenceItemIDs(evidence)
	unsupported := len(evidenceIDs) == 0
	sections := []rag.AnswerSection{
		{
			Heading: "Evidence Summary",
			Sentences: []rag.AnswerClaim{{
				Text:        fmt.Sprintf("The query %q is grounded in %d retrieved source(s).", strings.TrimSpace(query), len(papers)),
				EvidenceIDs: evidenceIDs,
				Unsupported: unsupported,
				Confidence:  0.65,
			}},
		},
		{
			Heading: "Key Findings",
			Sentences: []rag.AnswerClaim{{
				Text:        firstNonEmpty(firstEvidenceClaim(evidence), "No verified evidence claim was assembled."),
				EvidenceIDs: evidenceIDs,
				Unsupported: unsupported,
				Confidence:  0.62,
			}},
		},
		{
			Heading: "Coverage Notes",
			Sentences: []rag.AnswerClaim{{
				Text:        "Additional retrieval should focus on unresolved contradictions, missing source families, and citation precision.",
				EvidenceIDs: evidenceIDs,
				Unsupported: unsupported,
				Confidence:  0.55,
			}},
		},
	}
	return &rag.StructuredAnswer{PlainText: plain, Sections: sections}
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
		return heuristicRefinedDraftWithoutLLM(query, draft, critique), nil
	}
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		slog.Warn("draft critique refinement skipped during provider cooldown; using heuristic refinement",
			"component", "wisdev.autonomous",
			"operation", "refineDraftWithCritique",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
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
3. Preserve source-grounded claims using [Title] citations.
4. Do not invent new evidence beyond the verified evidence list.
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
			return heuristicRefinedDraftWithoutLLM(query, draft, critique), nil
		}
		if shouldAbortAutonomousLoop(err) {
			return "", err
		}
		if strings.Contains(strings.ToLower(err.Error()), "llm client is not configured") {
			return heuristicRefinedDraftWithoutLLM(query, draft, critique), nil
		}
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("draft critique refinement rate limited; using heuristic refinement",
				"component", "wisdev.autonomous",
				"operation", "refineDraftWithCritique",
				"stage", "rate_limit_fallback",
				"error", err.Error(),
			)
			return heuristicRefinedDraftWithoutLLM(query, draft, critique), nil
		}
		slog.Warn("draft critique refinement LLM failed; using heuristic refinement",
			"component", "wisdev.autonomous",
			"operation", "refineDraftWithCritique",
			"error", err.Error(),
		)
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
			slog.Warn("autonomous hypothesis committee skipped during provider cooldown",
				"component", "wisdev.autonomous",
				"operation", "hypothesis_committee",
				"retry_after_ms", providerCooldown.Milliseconds(),
				"hypothesisCount", len(hypothesisPtrs),
			)
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
		slog.Warn("intermediate synthesis skipped during provider cooldown",
			"component", "wisdev.autonomous",
			"operation", "intermediateSynthesis",
			"retry_after_ms", remaining.Milliseconds(),
			"paperCount", len(papers),
		)
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

	// Recency (newer = slight boost for evolving fields)
	currentYear := time.Now().Year()
	if p.Year >= currentYear-2 {
		conf += 0.05
	} else if p.Year < currentYear-10 && p.Year > 1900 {
		conf -= 0.05
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
		targets := make([]string, len(uncertain))
		for i, b := range uncertain {
			targets[i] = b.ID
		}
		return LoopDecision{ShouldContinue: true, Reason: "focus on uncertain beliefs", TargetBeliefs: targets, QueryStrategy: "focus"}
	}

	if hardBudgetExhausted {
		return LoopDecision{ShouldContinue: false, Reason: "search budget exhausted after belief review"}
	}

	if avgConf >= 0.5 && avgConf < 0.75 {
		return LoopDecision{ShouldContinue: true, Reason: "moderate confidence", QueryStrategy: "breadth"}
	}

	return LoopDecision{ShouldContinue: true, Reason: "default continuation", QueryStrategy: "breadth"}
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
