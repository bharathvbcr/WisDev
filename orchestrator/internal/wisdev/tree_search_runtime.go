package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

type TreeSearchConfig struct {
	MaxBranches      int
	PruneBelow       float64
	MaxDepth         int
	MaxUniquePapers  int
	Parallelism      int
	QueriesPerBranch int
	DisableAdvance   bool
}

type TreeSearchRuntime struct {
	loop     *AutonomousLoop
	explorer *HypothesisExplorer
	manager  *BranchManager
	config   TreeSearchConfig
}

type TreeSearchResult struct {
	Branches          []ResearchBranch
	MergeCandidate    *ResearchBranch
	CommittedPapers   []search.Paper
	Findings          []EvidenceFinding
	SpawnedHypotheses []Hypothesis
	Trace             []ReasoningTraceEntry
}

func NewTreeSearchRuntime(loop *AutonomousLoop, explorer *HypothesisExplorer, config TreeSearchConfig) *TreeSearchRuntime {
	if config.MaxBranches <= 0 {
		config.MaxBranches = 5
	}
	if config.PruneBelow <= 0 {
		config.PruneBelow = 0.3
	}
	if config.MaxDepth <= 0 {
		config.MaxDepth = 2
	}
	if config.QueriesPerBranch <= 0 {
		config.QueriesPerBranch = 2
	}
	return &TreeSearchRuntime{
		loop:     loop,
		explorer: explorer,
		manager:  NewBranchManager(config.MaxBranches, config.PruneBelow),
		config:   config,
	}
}

func (rt *TreeSearchRuntime) Run(
	ctx context.Context,
	req LoopRequest,
	hypotheses []Hypothesis,
	searchOpts search.SearchOpts,
	queryCoverage map[string][]search.Paper,
) TreeSearchResult {
	result := TreeSearchResult{}
	if rt == nil || rt.loop == nil || rt.explorer == nil || len(hypotheses) == 0 {
		return result
	}

	explorationResults := rt.explorer.ExploreAll(ctx, toHypothesisPtrs(hypotheses), searchOpts, rt.config.QueriesPerBranch)
	for _, res := range explorationResults {
		if res.Hypothesis == nil {
			continue
		}
		branch := rt.manager.Fork("", res.Hypothesis)
		branch.ExecutedQueries = appendUniqueLoopQueries(branch.ExecutedQueries, res.Queries)
		branch.PendingQueries = appendUniqueLoopQueries(branch.PendingQueries, res.SuggestedQueries)
		branch.Confidence = ClampFloat(res.Confidence, 0, 1)
		if res.ExplorationStatus != "" {
			branch.Status = res.ExplorationStatus
			if branch.Status == "completed" || branch.Status == "insufficient_evidence" {
				branch.Status = "active"
			}
		}

		acceptedPapers := rt.attachInitialExploration(branch, res, queryCoverage)
		result.CommittedPapers = appendUniqueSearchPapers(result.CommittedPapers, acceptedPapers)

		if res.EvaluationResult != nil {
			rt.applyBranchDecision(branch, res, req, &result)
		}
	}

	rt.advanceBranches(ctx, searchOpts, queryCoverage, &result)
	rt.manager.Prune()
	active := rt.manager.ActiveBranches()
	for _, branch := range active {
		if branch == nil {
			continue
		}
		result.Branches = append(result.Branches, *branch)
	}
	result.MergeCandidate = rt.manager.Merge(active)
	if result.MergeCandidate != nil {
		result.CommittedPapers = appendUniqueSearchPapers(result.CommittedPapers, result.MergeCandidate.Papers)
		result.Findings = append(result.Findings, result.MergeCandidate.Evidence...)
	}
	return result
}

func (rt *TreeSearchRuntime) attachInitialExploration(branch *ResearchBranch, res ExplorationResult, queryCoverage map[string][]search.Paper) []search.Paper {
	if branch == nil || res.Hypothesis == nil {
		return nil
	}
	safePapers := SanitizeRetrievedPapersForLLM(res.NewEvidence, "treeSearch.attachInitialExploration")
	var accepted []search.Paper
	branch.Papers, accepted = appendUniqueSearchPapersWithinBudget(branch.Papers, safePapers, rt.config.MaxUniquePapers)
	if len(accepted) > 0 {
		recordLoopQueryCoverage(queryCoverage, "tree:"+branch.ID, accepted)
		for _, query := range res.Queries {
			recordLoopQueryCoverage(queryCoverage, "tree:"+branch.ID+":"+query, accepted)
		}
	}
	for _, paper := range accepted {
		finding := EvidenceFinding{
			ID:         stableWisDevID("treefinding", res.Hypothesis.ID, paper.ID),
			Claim:      paper.Title,
			Snippet:    paper.Abstract,
			PaperTitle: paper.Title,
			SourceID:   paper.ID,
			Confidence: calculateInitialConfidence(paper),
			Year:       paper.Year,
		}
		branch.Evidence = append(branch.Evidence, finding)
	}
	attachBranchEvidence(branch)
	return accepted
}

func (rt *TreeSearchRuntime) applyBranchDecision(branch *ResearchBranch, res ExplorationResult, req LoopRequest, result *TreeSearchResult) {
	if branch == nil || res.Hypothesis == nil || res.EvaluationResult == nil || result == nil {
		return
	}
	switch res.EvaluationResult.BranchingDecision {
	case "branch":
		for _, subClaim := range res.EvaluationResult.SubHypotheses {
			subClaim = strings.TrimSpace(subClaim)
			if subClaim == "" {
				continue
			}
			subHyp := Hypothesis{
				ID:                      stableWisDevID("branch-hyp", res.Hypothesis.ID, subClaim),
				ParentID:                res.Hypothesis.ID,
				Query:                   req.Query,
				Text:                    subClaim,
				Claim:                   subClaim,
				Category:                "branch",
				FalsifiabilityCondition: "Specific evidence refutes this sub-claim.",
				ConfidenceScore:         0.5,
				Status:                  "active",
				CreatedAt:               NowMillis(),
			}
			child := rt.manager.Fork(branch.ID, &subHyp)
			child.PendingQueries = appendUniqueLoopQueries(child.PendingQueries, res.EvaluationResult.SuggestedQueries)
			result.SpawnedHypotheses = append(result.SpawnedHypotheses, subHyp)
			result.Trace = append(result.Trace, ReasoningTraceEntry{
				Timestamp: NowMillis(),
				Phase:     "tree_search_branch",
				Decision:  "branch",
				Reasoning: fmt.Sprintf("Spawned isolated sub-branch %s from %s", subHyp.ID, branch.ID),
			})
		}
	case "prune":
		branch.Status = "pruned"
		res.Hypothesis.IsTerminated = true
		res.Hypothesis.Status = "pruned"
		result.Trace = append(result.Trace, ReasoningTraceEntry{
			Timestamp: NowMillis(),
			Phase:     "tree_search_prune",
			Decision:  "prune",
			Reasoning: fmt.Sprintf("Pruned isolated branch %s for hypothesis %s", branch.ID, res.Hypothesis.ID),
		})
	case "backtrack":
		branch.PendingQueries = appendUniqueLoopQuery(branch.PendingQueries, "contradicting evidence: "+strings.TrimSpace(res.Hypothesis.Claim))
		branch.PendingQueries = appendUniqueLoopQueries(branch.PendingQueries, res.EvaluationResult.SuggestedQueries)
	}
}

func (rt *TreeSearchRuntime) advanceBranches(ctx context.Context, searchOpts search.SearchOpts, queryCoverage map[string][]search.Paper, result *TreeSearchResult) {
	if rt.config.DisableAdvance {
		return
	}
	for depth := 0; depth < rt.config.MaxDepth; depth++ {
		active := rt.manager.ActiveBranches()
		if len(active) == 0 {
			return
		}
		advanced := 0
		for _, branch := range active {
			before := len(branch.Evidence)
			findings := rt.loop.advanceBranchSession(ctx, branch, searchOpts, rt.config.MaxUniquePapers, queryCoverage)
			if len(findings) > 0 {
				result.Findings = append(result.Findings, findings...)
				advanced++
			}
			branch.Confidence = rt.manager.Score(branch)
			if len(branch.Evidence) == before && len(branch.PendingQueries) == 0 {
				continue
			}
			slog.Debug("advanced isolated tree-search branch",
				"component", "wisdev.tree_search",
				"branch_id", branch.ID,
				"depth", depth+1,
				"evidence_count", len(branch.Evidence),
				"pending_queries", len(branch.PendingQueries),
			)
		}
		if advanced == 0 {
			return
		}
	}
}

func appendUniqueLoopQueries(existing []string, queries []string) []string {
	out := append([]string(nil), existing...)
	for _, query := range queries {
		out = appendUniqueLoopQuery(out, query)
	}
	return out
}
