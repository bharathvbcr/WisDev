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
	Terminated        bool   // hypothesis was refuted; applied to the hypothesis after the worker pool joins
	RefinedClaim      string // evidence-driven claim rewrite; applied after the worker pool joins
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

	// Apply hypothesis mutations on this goroutine only, after all workers have
	// joined — workers must not write hypothesis state concurrently.
	filteredResults := make([]ExplorationResult, 0, len(results))
	for _, r := range results {
		if r.Hypothesis == nil {
			continue
		}
		if refined := strings.TrimSpace(r.RefinedClaim); refined != "" && !strings.EqualFold(refined, strings.TrimSpace(r.Hypothesis.Claim)) {
			slog.Info("Hypothesis claim refined from evidence",
				"hypothesisID", r.Hypothesis.ID,
				"previousClaim", r.Hypothesis.Claim,
				"refinedClaim", refined)
			r.Hypothesis.Claim = refined
			if strings.TrimSpace(r.Hypothesis.Text) != "" {
				r.Hypothesis.Text = refined
			}
			r.Hypothesis.UpdatedAt = NowMillis()
		}
		if r.Terminated {
			r.Hypothesis.IsTerminated = true
		}
		filteredResults = append(filteredResults, r)
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

	// Ground verification in paper bodies, not just abstracts: fetch extracted
	// full text for the top papers that expose an open-access PDF source.
	AcquireFullTextForPapers(ctx, allPapers, 3)

	// Step 3: Convert papers to evidence findings
	evidenceFindings := make([]EvidenceFinding, 0, len(allPapers))
	for idx, paper := range allPapers {
		evidenceFindings = append(evidenceFindings, EvidenceFinding{
			ID:         fmt.Sprintf("hyp_ev_%s_%d", hypothesis.ID, idx),
			Claim:      paper.Title,
			Snippet:    evidenceTextFromPaper(paper),
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
	refinedClaim := ""
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
						Snippet:    evidenceTextFromPaper(paper),
						PaperTitle: paper.Title,
						SourceID:   paper.ID,
						Confidence: 0.5,
						Year:       paper.Year,
					})
				}
			}
		}

		// Evidence-driven refinement: rewrite the claim from accumulated evidence
		// before re-evaluating, so refinement can change the hypothesis itself
		// rather than only re-scoring the original text.
		refinedClaim = he.refineHypothesisClaim(ctx, hypothesis, evidenceFindings, evalResult)
		evaluationTarget := hypothesis
		if refinedClaim != "" {
			refined := *hypothesis
			refined.Claim = refinedClaim
			if strings.TrimSpace(refined.Text) != "" {
				refined.Text = refinedClaim
			}
			evaluationTarget = &refined
		}

		// Re-evaluate with new evidence (and the refined claim when available)
		if he.evaluator != nil {
			newEval, err := he.evaluator.Evaluate(ctx, evaluationTarget, evidenceFindings)
			if err != nil {
				refinedClaim = ""
				slog.Warn("Hypothesis re-evaluation after refinement failed; keeping previous evaluation",
					"error", err, "claim", hypothesis.Claim)
			} else {
				evalResult = newEval
				slog.Debug("Hypothesis explorer refinement completed",
					"claim", evaluationTarget.Claim,
					"newVerdict", evalResult.Verdict,
					"newScore", evalResult.Score)
			}
		}
	}

	// Step 6: Determine exploration status. Hypothesis state is mutated by the
	// caller (ExploreAll) after all workers join, never inside this worker.
	explorationStatus := "completed"
	terminated := false
	if len(allPapers) < 3 {
		explorationStatus = "insufficient_evidence"
	} else if evalResult.Verdict == "refuted" {
		explorationStatus = "refuted"
		terminated = true
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
		Terminated:        terminated,
		RefinedClaim:      refinedClaim,
	}
}

// refineHypothesisClaim asks the LLM to rewrite an uncertain hypothesis into a
// sharper falsifiable claim consistent with the accumulated evidence. Returns
// an empty string when no meaningful rewrite is available.
func (he *HypothesisExplorer) refineHypothesisClaim(
	ctx context.Context,
	hypothesis *Hypothesis,
	evidence []EvidenceFinding,
	eval *EvaluationResult,
) string {
	if he.brainCaps == nil || he.brainCaps.llmClient == nil {
		return ""
	}

	var sb strings.Builder
	limit := 8
	for idx, ev := range evidence {
		if idx >= limit {
			break
		}
		snippet := ev.Snippet
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		sb.WriteString(fmt.Sprintf("%d. %s — %s\n", idx+1, ev.PaperTitle, snippet))
	}
	if sb.Len() == 0 {
		return ""
	}

	reasoning := ""
	if eval != nil {
		reasoning = strings.TrimSpace(eval.Reasoning)
	}

	prompt := fmt.Sprintf(`A research hypothesis was evaluated as UNCERTAIN against the evidence below.

Hypothesis: %s
Evaluator reasoning: %s

Evidence:
%s

Rewrite the hypothesis into a sharper, falsifiable claim that is consistent with this evidence:
- Keep the original research subject and scope.
- Narrow or qualify the claim where the evidence demands it (population, mechanism, conditions).
- If the original claim is already as precise as the evidence allows, set "changed" to false.

Return JSON with the refined claim using the provided schema.`,
		hypothesis.Claim, reasoning, sb.String())

	response, err := he.generateStructuredWithTier(ctx, prompt, TierLight,
		`{"type":"object","properties":{"refinedClaim":{"type":"string"},"changed":{"type":"boolean"}},"required":["refinedClaim","changed"]}`)
	if err != nil {
		slog.Warn("Hypothesis claim refinement failed; keeping original claim",
			"error", err, "claim", hypothesis.Claim)
		return ""
	}

	var parsed struct {
		RefinedClaim string `json:"refinedClaim"`
		Changed      bool   `json:"changed"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(response)), &parsed); err != nil {
		slog.Warn("Hypothesis claim refinement returned unparseable output; keeping original claim",
			"error", err, "claim", hypothesis.Claim)
		return ""
	}

	refined := strings.TrimSpace(parsed.RefinedClaim)
	if !parsed.Changed || refined == "" || strings.EqualFold(refined, strings.TrimSpace(hypothesis.Claim)) {
		return ""
	}
	if len(refined) > 500 {
		refined = refined[:500]
	}
	return refined
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
