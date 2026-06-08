package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
	"golang.org/x/sync/semaphore"
)

// HypothesisExplorer explores hypotheses concurrently with per-hypothesis search contexts
type HypothesisExplorer struct {
	searchReg *search.ProviderRegistry
	evaluator *HypothesisEvaluator
	brainCaps *BrainCapabilities
	poolSize  int
}

// NewHypothesisExplorer creates a new hypothesis explorer with a worker pool
func NewHypothesisExplorer(
	searchReg *search.ProviderRegistry,
	evaluator *HypothesisEvaluator,
	brainCaps *BrainCapabilities,
	poolSize int,
) *HypothesisExplorer {
	if poolSize <= 0 {
		poolSize = 3 // Default concurrent hypothesis exploration limit
	}

	return &HypothesisExplorer{
		searchReg: searchReg,
		evaluator: evaluator,
		brainCaps: brainCaps,
		poolSize:  poolSize,
	}
}

// ExplorationResult captures the outcome of exploring a single hypothesis
type ExplorationResult struct {
	Hypothesis        *Hypothesis
	NewEvidence       []search.Paper
	EvaluationResult  *EvaluationResult
	SuggestedQueries  []string
	Queries           []string
	Confidence        float64
	ExplorationStatus string // "completed", "insufficient_evidence", "refuted"
}

// ExploreAll explores all hypotheses concurrently, each with its own search context
func (he *HypothesisExplorer) ExploreAll(
	ctx context.Context,
	hypotheses []*Hypothesis,
	searchOpts search.SearchOpts,
	queriesPerHypothesis int,
) []ExplorationResult {
	if len(hypotheses) == 0 {
		return nil
	}

	if queriesPerHypothesis <= 0 {
		queriesPerHypothesis = 2 // Default: 2 queries per hypothesis
	}

	// Sort hypotheses by confidence descending to prioritize promising ones in the pool
	ordered := append([]*Hypothesis(nil), hypotheses...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i] == nil || ordered[j] == nil {
			return ordered[j] != nil
		}
		return ordered[i].ConfidenceScore > ordered[j].ConfidenceScore
	})

	results := make([]ExplorationResult, len(ordered))
	resultsMu := sync.Mutex{}

	// Use semaphore for backpressure
	sem := semaphore.NewWeighted(int64(he.poolSize))
	var wg sync.WaitGroup

	for idx, hypothesis := range ordered {
		if hypothesis == nil || hypothesis.IsTerminated {
			continue
		}

		wg.Add(1)
		go func(idx int, h *Hypothesis) {
			defer wg.Done()

			// Acquire semaphore slot
			if err := sem.Acquire(ctx, 1); err != nil {
				slog.Warn("Failed to acquire semaphore for hypothesis exploration", "error", err)
				return
			}
			defer sem.Release(1)

			result := he.exploreHypothesis(ctx, h, searchOpts, queriesPerHypothesis)

			resultsMu.Lock()
			results[idx] = result
			resultsMu.Unlock()
		}(idx, hypothesis)
	}

	wg.Wait()

	// Filter out empty results (from terminated hypotheses)
	filteredResults := make([]ExplorationResult, 0, len(results))
	for _, r := range results {
		if r.Hypothesis != nil {
			filteredResults = append(filteredResults, r)
		}
	}

	return filteredResults
}

// exploreHypothesis explores a single hypothesis: generates queries, searches, evaluates
func (he *HypothesisExplorer) exploreHypothesis(
	ctx context.Context,
	hypothesis *Hypothesis,
	searchOpts search.SearchOpts,
	queriesPerHypothesis int,
) ExplorationResult {
	slog.Debug("Exploring hypothesis",
		"claim", hypothesis.Claim,
		"currentConfidence", hypothesis.ConfidenceScore)

	// Step 1: Generate hypothesis-specific search queries
	budget := queriesPerHypothesis
	if hypothesis.AllocatedQueryBudget > budget {
		budget = hypothesis.AllocatedQueryBudget
	}
	queries := he.generateHypothesisQueries(ctx, hypothesis, budget)
	if len(queries) == 0 {
		// Fallback: use the hypothesis claim itself
		queries = []string{hypothesis.Claim}
	}

	// Step 2: Execute search for these queries
	allPapers := make([]search.Paper, 0)
	for _, query := range queries {
		papers, _, err := retrieveCanonicalSearchPapers(ctx, he.searchReg, query, searchOpts)
		if err != nil {
			slog.Warn("Hypothesis explorer paper retrieval failed", "error", err, "query", query)
			continue
		}
		if len(papers) > 0 {
			allPapers = append(allPapers, papers...)
		}
	}

	// Deduplicate papers
	allPapers = dedupePapers(allPapers)

	// Step 3: Convert papers to evidence findings
	evidenceFindings := make([]EvidenceFinding, 0, len(allPapers))
	for idx, paper := range allPapers {
		evidenceFindings = append(evidenceFindings, EvidenceFinding{
			ID:         fmt.Sprintf("hyp_ev_%s_%d", hypothesis.ID, idx),
			Claim:      paper.Title,
			Snippet:    paper.Abstract,
			PaperTitle: paper.Title,
			SourceID:   paper.ID,
			Confidence: 0.5, // Neutral until evaluated
			Year:       paper.Year,
		})
	}

	// Step 4: Evaluate hypothesis against collected evidence
	var evalResult *EvaluationResult
	if he.evaluator != nil {
		var err error
		evalResult, err = he.evaluator.Evaluate(ctx, hypothesis, evidenceFindings)
		if err != nil {
			slog.Warn("Hypothesis evaluation failed during exploration", "error", err, "hypothesis", hypothesis.Claim)
			evalResult = &EvaluationResult{
				Score:   0.5,
				Verdict: "uncertain",
			}
		}
	} else {
		evalResult = &EvaluationResult{
			Score:   0.5,
			Verdict: "uncertain",
		}
	}

	// Step 5: Iterative Refinement (R1)
	// If the result is uncertain and we have suggested queries, perform a second pass.
	if evalResult.Verdict == "uncertain" && len(evalResult.SuggestedQueries) > 0 {
		slog.Debug("Hypothesis explorer performing second pass refinement", "claim", hypothesis.Claim)
		secondPassQueries := evalResult.SuggestedQueries
		if len(secondPassQueries) > 2 {
			secondPassQueries = secondPassQueries[:2]
		}

		for _, query := range secondPassQueries {
			papers, _, err := retrieveCanonicalSearchPapers(ctx, he.searchReg, query, searchOpts)
			if err != nil {
				slog.Warn("Hypothesis explorer refinement retrieval failed", "error", err, "query", query)
				continue
			}
			if len(papers) > 0 {
				allPapers = append(allPapers, papers...)
				// Update findings for re-evaluation
				for _, paper := range papers {
					evidenceFindings = append(evidenceFindings, EvidenceFinding{
						ID:         fmt.Sprintf("hyp_ev_ref_%s_%s", hypothesis.ID, paper.ID),
						Claim:      paper.Title,
						Snippet:    paper.Abstract,
						PaperTitle: paper.Title,
						SourceID:   paper.ID,
						Confidence: 0.5,
						Year:       paper.Year,
					})
				}
			}
		}

		// Re-evaluate with new evidence
		if he.evaluator != nil {
			newEval, err := he.evaluator.Evaluate(ctx, hypothesis, evidenceFindings)
			if err == nil {
				evalResult = newEval
				slog.Debug("Hypothesis explorer refinement completed",
					"claim", hypothesis.Claim,
					"newVerdict", evalResult.Verdict,
					"newScore", evalResult.Score)
			}
		}
	}

	// Step 6: Determine exploration status
	explorationStatus := "completed"
	if len(allPapers) < 3 {
		explorationStatus = "insufficient_evidence"
	} else if evalResult.Verdict == "refuted" {
		explorationStatus = "refuted"
		hypothesis.IsTerminated = true
	}

	slog.Debug("Hypothesis exploration completed",
		"claim", hypothesis.Claim,
		"queriesUsed", len(queries),
		"papersFound", len(allPapers),
		"evaluationScore", evalResult.Score,
		"verdict", evalResult.Verdict,
		"status", explorationStatus)

	return ExplorationResult{
		Hypothesis:        hypothesis,
		NewEvidence:       allPapers,
		EvaluationResult:  evalResult,
		SuggestedQueries:  evalResult.SuggestedQueries,
		Queries:           queries,
		Confidence:        evalResult.Score,
		ExplorationStatus: explorationStatus,
	}
}

// generateHypothesisQueries generates targeted search queries for a specific hypothesis
func (he *HypothesisExplorer) generateHypothesisQueries(
	ctx context.Context,
	hypothesis *Hypothesis,
	count int,
) []string {
	if he.brainCaps == nil || he.brainCaps.llmClient == nil {
		// Fallback: heuristic query generation
		return he.heuristicGenerateQueries(hypothesis, count)
	}

	prompt := fmt.Sprintf(`Generate %d specific search queries to find evidence for this hypothesis:

Hypothesis: %s
Falsifiability Condition: %s

Requirements:
- Each query should target a different aspect or implication of the hypothesis
- Use academic/scientific terminology
- Be specific enough to find relevant papers

Return the query strings using the provided JSON schema.`,
		count,
		hypothesis.Claim,
		hypothesis.FalsifiabilityCondition)

	response, err := he.generateStructuredWithTier(ctx, prompt, TierLight, `{"type":"array","items":{"type":"string"}}`)
	if err != nil {
		slog.Warn("Failed to generate hypothesis queries, using fallback", "error", err)
		return he.heuristicGenerateQueries(hypothesis, count)
	}

	queries := parseQueryArray(response)
	if len(queries) == 0 {
		return he.heuristicGenerateQueries(hypothesis, count)
	}

	return queries
}

// heuristicGenerateQueries provides a fallback for query generation
func (he *HypothesisExplorer) heuristicGenerateQueries(hypothesis *Hypothesis, count int) []string {
	if hypothesis == nil {
		return nil
	}
	if count <= 0 {
		count = 2
	}
	queries := make([]string, 0, count)
	addQuery := func(query string) {
		trimmed := strings.TrimSpace(query)
		if trimmed == "" {
			return
		}
		for _, existing := range queries {
			if strings.EqualFold(strings.TrimSpace(existing), trimmed) {
				return
			}
		}
		queries = append(queries, trimmed)
	}

	claim := strings.TrimSpace(hypothesis.Claim)
	if claim != "" {
		addQuery(claim)
	}
	if condition := strings.TrimSpace(hypothesis.FalsifiabilityCondition); condition != "" {
		addQuery(condition)
	}
	if category := strings.TrimSpace(hypothesis.Category); category != "" {
		addQuery(fmt.Sprintf("%s evidence", category))
	}
	if len(queries) < count && claim != "" {
		addQuery(fmt.Sprintf("%s replication", claim))
	}
	if len(queries) < count && claim != "" {
		addQuery(fmt.Sprintf("%s contradiction", claim))
	}
	if len(queries) < count {
		addQuery(fmt.Sprintf("evidence for %s", claim))
	}
	if len(queries) > count {
		queries = queries[:count]
	}
	return queries
}

// generateStructuredWithTier is a helper to generate schema-backed output with a specific model tier.
func (he *HypothesisExplorer) generateStructuredWithTier(ctx context.Context, prompt string, tier ModelTier, jsonSchema string) (string, error) {
	if he.brainCaps == nil || he.brainCaps.llmClient == nil {
		return "", fmt.Errorf("LLM client not available")
	}
	if remaining := he.brainCaps.llmClient.ProviderCooldownRemaining(); remaining > 0 {
		return "", fmt.Errorf("hypothesis explorer LLM generation skipped during provider cooldown; retry after %s", remaining.Round(time.Millisecond))
	}

	// Resolve model name based on tier
	var modelName string
	switch tier {
	case TierHeavy:
		modelName = llm.ResolveHeavyModel()
	case TierLight:
		modelName = llm.ResolveLightModel()
	default:
		modelName = llm.ResolveStandardModel()
	}

	resp, err := he.brainCaps.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     appendWisdevStructuredOutputInstruction(prompt),
		Model:      modelName,
		JsonSchema: jsonSchema,
	}, string(tier), false))
	if err != nil {
		return "", err
	}

	return resp.JsonResult, nil
}

// dedupePapers removes duplicate papers by ID, DOI, or title
func dedupePapers(papers []search.Paper) []search.Paper {
	seen := make(map[string]struct{})
	unique := make([]search.Paper, 0, len(papers))

	for _, paper := range papers {
		key := paper.ID
		if key == "" {
			key = paper.DOI
		}
		if key == "" {
			key = paper.Title
		}

		if key != "" {
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				unique = append(unique, paper)
			}
		}
	}

	return unique
}

// parseQueryArray parses the exact schema-backed array of query strings.
func parseQueryArray(response string) []string {
	response = strings.TrimSpace(response)
	if response == "" {
		return nil
	}

	var queries []string
	if err := json.Unmarshal([]byte(response), &queries); err != nil {
		return nil
	}

	filtered := queries[:0]
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query != "" {
			filtered = append(filtered, query)
		}
	}

	if len(filtered) == 0 {
		return nil
	}
	return filtered
}
