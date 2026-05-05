package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

const (
	brainInteractivePlanningTimeout = 45 * time.Second
	brainBatchStructuredTimeout     = 18 * time.Second
	questionValueSuggestionTimeout  = 20 * time.Second
)

type BrainCapabilities struct {
	llmClient *llm.Client
}

func NewBrainCapabilities(client *llm.Client) *BrainCapabilities {
	return &BrainCapabilities{
		llmClient: client,
	}
}

func (c *BrainCapabilities) interactiveStructuredRequest(ctx context.Context, timeout time.Duration) (context.Context, *llm.Client, context.CancelFunc) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	return reqCtx, c.llmClient, cancel
}

func (c *BrainCapabilities) ScoreHypothesisConfidence(
	ctx context.Context,
	hypothesis string,
	evidence []*EvidenceFinding,
	contradictions []*EvidenceFinding,
	terminated bool,
) float64 {
	evidenceCount := len(evidence)
	contradictionCount := len(contradictions)
	if evidenceCount == 0 && contradictionCount == 0 {
		return 0.5
	}
	score := 0.5 + (float64(evidenceCount) * 0.1) - (float64(contradictionCount) * 0.15)
	if contradictionCount > 0 {
		score *= 0.5
	}
	if terminated {
		score *= 0.25
	}
	return math.Max(0, math.Min(1, score))
}

func (c *BrainCapabilities) DecomposeTask(ctx context.Context, query string, domain string, model string) ([]ResearchTask, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain task decomposition using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "decompose_task",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackBrainResearchTasks(query, domain), nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Decompose query: %s in domain: %s into research tasks.", query, domain))
	resp, err := c.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"tasks": {"type": "array", "items": {"type": "object", "properties": {"id": {"type": "string"}, "name": {"type": "string"}, "action": {"type": "string"}, "dependsOnIds": {"type": "array", "items": {"type": "string"}}}}}}}`,
	}, "standard", true))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain task decomposition using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "decompose_task",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackBrainResearchTasks(query, domain), nil
		}
		return nil, err
	}
	var result struct {
		Tasks []ResearchTask `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result.Tasks, nil
}

func (c *BrainCapabilities) DecomposeTaskInteractive(ctx context.Context, query string, domain string, model string) ([]map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	tasks, err := c.DecomposeTask(requestCtx, query, domain, model)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(tasks))
	for i, t := range tasks {
		out[i] = map[string]any{
			"id":           t.ID,
			"name":         t.Name,
			"action":       t.Action,
			"dependsOnIds": t.DependsOnIDs,
		}
	}
	return out, nil
}

func (c *BrainCapabilities) ProposeHypotheses(ctx context.Context, query string, intent string, model string) ([]Hypothesis, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain hypothesis proposal using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "propose_hypotheses",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackBrainHypotheses(query, intent), nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Propose 3-5 hypotheses for query: %s. Intent: %s.", query, intent))
	resp, err := c.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"hypotheses": {"type": "array", "items": {"type": "object", "properties": {"claim": {"type": "string"}, "falsifiabilityCondition": {"type": "string"}, "confidenceThreshold": {"type": "number"}, "category": {"type": "string"}, "text": {"type": "string"}}}}}}`,
	}, "standard", true))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain hypothesis proposal using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "propose_hypotheses",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackBrainHypotheses(query, intent), nil
		}
		return nil, err
	}
	var result struct {
		Hypotheses []Hypothesis `json:"hypotheses"`
	}
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	for i := range result.Hypotheses {
		if result.Hypotheses[i].Claim == "" {
			result.Hypotheses[i].Claim = result.Hypotheses[i].Text
		}
		if result.Hypotheses[i].Text == "" {
			result.Hypotheses[i].Text = result.Hypotheses[i].Claim
		}
	}
	return result.Hypotheses, nil
}

func brainCapabilityCooldownRemaining(c *BrainCapabilities) time.Duration {
	if c == nil || c.llmClient == nil {
		return 0
	}
	return c.llmClient.ProviderCooldownRemaining()
}

func fallbackBrainHypotheses(query string, intent string) []Hypothesis {
	claim := firstNonEmpty(query, intent, "research question")
	category := firstNonEmpty(intent, "fallback")
	now := NowMillis()
	return []Hypothesis{{
		ID:                      stableWisDevID("brain_hypothesis", claim, category),
		Query:                   claim,
		Text:                    claim,
		Claim:                   claim,
		Category:                category,
		FalsifiabilityCondition: "Requires direct support or contradiction from retrieved evidence.",
		ConfidenceThreshold:     0.65,
		ConfidenceScore:         0.45,
		Status:                  "candidate",
		CreatedAt:               now,
		UpdatedAt:               now,
	}}
}

func fallbackBrainResearchTasks(query string, domain string) []ResearchTask {
	scope := firstNonEmpty(query, domain, "research question")
	searchID := stableWisDevID("brain_task", "search", scope)
	return []ResearchTask{
		{
			ID:     searchID,
			Name:   "Retrieve evidence",
			Action: "search",
			Reason: "Provider rate limiting prevented model-backed planning; use the original query as the evidence retrieval scope.",
		},
		{
			ID:           stableWisDevID("brain_task", "evaluate", scope),
			Name:         "Evaluate retrieved evidence",
			Action:       "evaluate_evidence",
			Reason:       "Fallback plan preserves a minimal evidence-first path until model-backed planning is available.",
			DependsOnIDs: []string{searchID},
		},
	}
}

func (c *BrainCapabilities) ProposeHypothesesInteractive(ctx context.Context, query string, domain string, model string) ([]Hypothesis, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.ProposeHypotheses(requestCtx, query, domain, model)
}

func (c *BrainCapabilities) GenerateHypotheses(ctx context.Context, query string, domain string, intent string, model string) ([]Hypothesis, error) {
	return c.ProposeHypotheses(ctx, query, intent, model)
}

func (c *BrainCapabilities) GenerateHypothesesInteractive(ctx context.Context, query string, domain string, intent string, model string) ([]Hypothesis, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.GenerateHypotheses(requestCtx, query, domain, intent, model)
}

func (c *BrainCapabilities) CoordinateReplan(ctx context.Context, failedID string, reason string, contextData map[string]any, model string) ([]ResearchTask, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain replan using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "coordinate_replan",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackBrainReplanTasks(failedID, reason, contextData), nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("A research step (%s) failed with reason: %s. Context: %v. Propose a recovery plan.", failedID, reason, contextData))
	resp, err := c.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"tasks": {"type": "array", "items": {"type": "object", "properties": {"id": {"type": "string"}, "name": {"type": "string"}, "action": {"type": "string"}, "dependsOnIds": {"type": "array", "items": {"type": "string"}}}}}}}`,
	}, "standard", true))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain replan using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "coordinate_replan",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackBrainReplanTasks(failedID, reason, contextData), nil
		}
		return nil, err
	}
	var result struct {
		Tasks []ResearchTask `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result.Tasks, nil
}

func fallbackBrainReplanTasks(failedID string, reason string, contextData map[string]any) []ResearchTask {
	scope := firstNonEmpty(failedID, reason, fmt.Sprint(contextData), "failed research step")
	dependsOn := []string{}
	if strings.TrimSpace(failedID) != "" {
		dependsOn = append(dependsOn, failedID)
	}
	return []ResearchTask{{
		ID:           stableWisDevID("brain_replan", scope),
		Name:         "Retry evidence collection",
		Action:       "retry_search",
		Reason:       firstNonEmpty(reason, "Provider rate limiting prevented model-backed replanning; retry the failed step with bounded fallback behavior."),
		DependsOnIDs: dependsOn,
	}}
}

func (c *BrainCapabilities) GenerateSnowballQueries(ctx context.Context, seedPapers []Source, model string) ([]string, error) {
	if len(seedPapers) == 0 {
		return []string{}, nil
	}
	if model == "" {
		model = llm.ResolveLightModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain snowball query generation using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "generate_snowball_queries",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackSnowballQueries(seedPapers), nil
	}
	requestCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	var titles []string
	for _, p := range seedPapers {
		titles = append(titles, p.Title)
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Based on these seed papers, generate 3 technical search queries: %s", strings.Join(titles, "\n")))
	resp, err := c.llmClient.StructuredOutput(requestCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"queries": {"type": "array", "items": {"type": "string"}}}}`,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain snowball query generation using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "generate_snowball_queries",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackSnowballQueries(seedPapers), nil
		}
		return nil, err
	}
	var result struct {
		Queries []string `json:"queries"`
	}
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result.Queries, nil
}

func fallbackSnowballQueries(seedPapers []Source) []string {
	queries := make([]string, 0, len(seedPapers))
	for _, paper := range seedPapers {
		title := strings.TrimSpace(paper.Title)
		if title == "" {
			continue
		}
		queries = append(queries, fmt.Sprintf("%s related evidence", title))
		if len(queries) >= 3 {
			break
		}
	}
	if len(queries) == 0 {
		return []string{"related evidence"}
	}
	return queries
}

func (c *BrainCapabilities) GenerateSnowballQueriesInteractive(ctx context.Context, seedPapers []Source, model string) ([]string, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.GenerateSnowballQueries(requestCtx, seedPapers, model)
}

func (c *BrainCapabilities) SnowballCitations(ctx context.Context, seedPapers []Source, model string) ([]Source, error) {
	queries, err := c.GenerateSnowballQueries(ctx, seedPapers, model)
	if err != nil {
		return nil, err
	}
	results := make([]Source, 0, len(queries))
	for _, q := range queries {
		results = append(results, Source{
			ID:     fmt.Sprintf("snowball_%s", NewTraceID()[:8]),
			Title:  q,
			Source: "snowball_suggestion",
		})
	}
	return results, nil
}

func (c *BrainCapabilities) VerifyCitations(ctx context.Context, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain citation verification using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "verify_citations",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackCitationVerification(papers), nil
	}
	requestCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Verify citations for these papers: %v.", papers))
	resp, err := c.llmClient.StructuredOutput(requestCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"validCount": {"type": "number"}, "issues": {"type": "array", "items": {"type": "string"}}}}`,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain citation verification using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "verify_citations",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackCitationVerification(papers), nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) VerifyCitationsInteractive(ctx context.Context, papers []Source, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.VerifyCitations(requestCtx, papers, model)
}

func fallbackCitationVerification(papers []Source) map[string]any {
	issues := make([]string, 0)
	validCount := 0
	seen := map[string]string{}
	for _, paper := range papers {
		identity := firstNonEmpty(paper.DOI, paper.ArxivID, paper.ID, paper.Title)
		if identity == "" {
			issues = append(issues, "citation_identity_missing")
			continue
		}
		if previousTitle, exists := seen[identity]; exists {
			issues = append(issues, fmt.Sprintf("duplicate citation identity %s between %s and %s", identity, previousTitle, paper.Title))
			continue
		}
		seen[identity] = paper.Title
		validCount++
	}
	if len(issues) == 0 {
		issues = append(issues, "model_verification_degraded")
	}
	return map[string]any{
		"validCount": float64(validCount),
		"issues":     issues,
		"degraded":   true,
	}
}

func (c *BrainCapabilities) BuildClaimEvidenceTable(ctx context.Context, query string, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain claim evidence table using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "build_claim_evidence_table",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackClaimEvidenceTable(query, papers), nil
	}
	requestCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Build a claim-evidence table for query: %s using papers: %v.", query, papers))
	resp, err := c.llmClient.StructuredOutput(requestCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"table": {"type": "string"}, "rowCount": {"type": "number"}}}`,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain claim evidence table using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "build_claim_evidence_table",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackClaimEvidenceTable(query, papers), nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func fallbackClaimEvidenceTable(query string, papers []Source) map[string]any {
	var table strings.Builder
	table.WriteString("| Claim | Evidence |\n| --- | --- |\n")
	if len(papers) == 0 {
		table.WriteString(fmt.Sprintf("| %s | model verification unavailable; no sources provided |\n", firstNonEmpty(query, "research question")))
	} else {
		for _, paper := range papers {
			title := firstNonEmpty(paper.Title, paper.ID, "source")
			evidence := firstNonEmpty(paper.ID, paper.DOI, paper.ArxivID, paper.Link, title)
			table.WriteString(fmt.Sprintf("| %s | %s |\n", title, evidence))
		}
	}
	return map[string]any{
		"table":    strings.TrimSpace(table.String()),
		"rowCount": float64(maxInt(1, len(papers))),
		"degraded": true,
	}
}

func (c *BrainCapabilities) BuildClaimEvidenceTableInteractive(ctx context.Context, query string, papers []Source, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.BuildClaimEvidenceTable(requestCtx, query, papers, model)
}

func (c *BrainCapabilities) GenerateThoughts(ctx context.Context, payload map[string]any, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain thought generation using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "generate_thoughts",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackThoughts(payload), nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Generate internal thoughts/reasoning based on context: %v.", payload))
	resp, err := c.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"thoughts": {"type": "string"}, "confidence": {"type": "number"}, "branches": {"type": "array", "items": {"type": "object", "properties": {"nodes": {"type": "array", "items": {"type": "object", "properties": {"label": {"type": "string"}, "reasoning": {"type": "string"}, "reasoning_strategy": {"type": "string"}}}}}}}}}`,
	}, "standard", false))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain thought generation using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "generate_thoughts",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackThoughts(payload), nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	if _, hasBranches := result["branches"]; !hasBranches {
		if thoughts, ok := result["thoughts"].(string); ok && thoughts != "" {
			result["branches"] = []any{
				map[string]any{
					"hypothesis": thoughts,
					"nodes": []any{
						map[string]any{"label": thoughts, "reasoning": thoughts},
					},
				},
			}
		}
	}
	return result, nil
}

func (c *BrainCapabilities) GenerateThoughtsInteractive(ctx context.Context, payload map[string]any, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainBatchStructuredTimeout)
	defer cancel()
	return c.GenerateThoughts(requestCtx, payload, model)
}

func (c *BrainCapabilities) DetectContradictions(ctx context.Context, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain contradiction detection using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "detect_contradictions",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackContradictions(), nil
	}
	requestCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Detect contradictions in source papers: %v.", papers))
	resp, err := c.llmClient.StructuredOutput(requestCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"contradictions": {"type": "array", "items": {"type": "object", "properties": {"finding_a": {"type": "string"}, "finding_b": {"type": "string"}, "severity": {"type": "string"}, "explanation": {"type": "string"}}}}}}`,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain contradiction detection using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "detect_contradictions",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackContradictions(), nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func fallbackThoughts(payload map[string]any) map[string]any {
	summary := "Model-backed reasoning was unavailable due to provider rate limiting."
	if payload != nil {
		if query := firstNonEmpty(AsOptionalString(payload["query"]), AsOptionalString(payload["prompt"])); query != "" {
			summary = fmt.Sprintf("Model-backed reasoning was unavailable; continue with evidence-first reasoning for %s.", query)
		}
	}
	return map[string]any{
		"thoughts":   summary,
		"confidence": 0.0,
		"degraded":   true,
		"branches": []any{
			map[string]any{
				"hypothesis": summary,
				"nodes": []any{
					map[string]any{"label": "degraded reasoning", "reasoning": summary},
				},
			},
		},
	}
}

func (c *BrainCapabilities) DetectContradictionsInteractive(ctx context.Context, papers []Source, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.DetectContradictions(requestCtx, papers, model)
}

func (c *BrainCapabilities) VerifyClaims(ctx context.Context, text string, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain claim verification using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "verify_claims",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackClaimVerification(text, papers), nil
	}
	requestCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Verify these claims in text: %s against sources: %v.", text, papers))
	resp, err := c.llmClient.StructuredOutput(requestCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"verified": {"type": "boolean"}, "report": {"type": "string"}}}`,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain claim verification using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "verify_claims",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackClaimVerification(text, papers), nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func fallbackContradictions() map[string]any {
	return map[string]any{
		"contradictions": []any{},
		"summary":        "Model-backed contradiction detection was unavailable due to provider rate limiting.",
		"degraded":       true,
	}
}

func (c *BrainCapabilities) VerifyClaimsInteractive(ctx context.Context, text string, papers []Source, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.VerifyClaims(requestCtx, text, papers, model)
}

func fallbackClaimVerification(text string, papers []Source) map[string]any {
	return map[string]any{
		"verified": false,
		"report":   fmt.Sprintf("Model-backed claim verification was unavailable due to provider rate limiting. Source count: %d. Claim text: %s", len(papers), strings.TrimSpace(text)),
		"degraded": true,
	}
}

func (c *BrainCapabilities) VerifyClaimsBatchInteractive(ctx context.Context, outputs []map[string]any, papers []Source, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainBatchStructuredTimeout)
	defer cancel()
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain claim batch verification using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "verify_claims_batch",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackClaimBatchVerification(outputs), nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Rank and verify these research findings: %v against sources: %v.", outputs, papers))
	resp, err := c.llmClient.StructuredOutput(requestCtx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"results": {"type": "array", "items": {"type": "object", "properties": {"verified": {"type": "boolean"}, "score": {"type": "number"}, "report": {"type": "string"}}}}}}`,
	}, "standard", false))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain claim batch verification using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "verify_claims_batch",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackClaimBatchVerification(outputs), nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func fallbackClaimBatchVerification(outputs []map[string]any) map[string]any {
	results := make([]map[string]any, 0, len(outputs))
	for i, output := range outputs {
		results = append(results, map[string]any{
			"id":       firstNonEmpty(AsOptionalString(output["id"]), fmt.Sprintf("output_%d", i+1)),
			"verified": false,
			"score":    0.0,
			"report":   "Model-backed claim verification was unavailable due to provider rate limiting.",
			"degraded": true,
		})
	}
	return map[string]any{
		"results":  results,
		"degraded": true,
	}
}

func (c *BrainCapabilities) SystematicReviewPrisma(ctx context.Context, query string, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain prisma report using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "systematic_review_prisma",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackPrismaReport(papers), nil
	}
	requestCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Create a PRISMA report for query: %s based on papers: %v.", query, papers))
	resp, err := c.llmClient.StructuredOutput(requestCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"records_identified": {"type": "integer"}, "records_screened": {"type": "integer"}, "full_text_assessed": {"type": "integer"}, "studies_included": {"type": "integer"}}}`,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain prisma report using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "systematic_review_prisma",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackPrismaReport(papers), nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) SystematicReviewPrismaInteractive(ctx context.Context, query string, papers []Source, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.SystematicReviewPrisma(requestCtx, query, papers, model)
}

func (c *BrainCapabilities) AssessResearchComplexity(ctx context.Context, query string) (string, error) {
	if c == nil || c.llmClient == nil {
		return "", fmt.Errorf("llm client is not configured")
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain research complexity using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "assess_research_complexity",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackResearchComplexity(query), nil
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Assess the complexity of this research query: %s. Classify as low, medium, or high.", query))
	resp, err := c.llmClient.StructuredOutput(ctx, applyWisdevLightStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveLightModel(),
		JsonSchema: wisdevResearchComplexitySchema,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain research complexity using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "assess_research_complexity",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackResearchComplexity(query), nil
		}
		return "", err
	}
	return parseResearchComplexity(resp.JsonResult)
}

func fallbackResearchComplexity(query string) string {
	terms := strings.Fields(query)
	switch {
	case len(terms) >= 18:
		return "high"
	case len(terms) <= 4:
		return "low"
	default:
		return "medium"
	}
}

func (c *BrainCapabilities) AssessResearchComplexityInteractive(ctx context.Context, query string) (string, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, questionValueSuggestionTimeout)
	defer cancel()
	return c.AssessResearchComplexity(requestCtx, query)
}

type StructuredAnswerResponse struct {
	Sections []struct {
		Heading   string `json:"heading"`
		Sentences []struct {
			Text        string   `json:"text"`
			EvidenceIDs []string `json:"evidenceIds"`
		} `json:"sentences"`
	} `json:"sections"`
}

func (c *BrainCapabilities) SynthesizeAnswer(ctx context.Context, query string, papers []Source, model string) (*rag.StructuredAnswer, error) {
	if model == "" {
		model = llm.ResolveHeavyModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain answer synthesis using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "synthesize_answer",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackStructuredAnswer(query, papers), nil
	}

	evidenceText := ""
	for _, p := range papers {
		evidenceText += fmt.Sprintf("ID: %s | Title: %s | Summary: %s\n", p.ID, p.Title, p.Summary)
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`Synthesize a comprehensive research report for the query: "%s"
Based on %d sources found.

Sources:
%s

Instructions:
1. Break the report into logical sections with headings.
2. For each sentence, specify which source IDs support it.
3. If a sentence has no supporting source, return empty evidenceIds.
4. Be precise and technical.
`, query, len(papers), evidenceText))

	resp, err := c.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type":"object","properties":{"sections":{"type":"array","items":{"type":"object","properties":{"heading":{"type":"string"},"sentences":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string"},"evidenceIds":{"type":"array","items":{"type":"string"}}},"required":["text","evidenceIds"]}}},"required":["heading","sentences"]}}},"required":["sections"]}`,
	}, "heavy", false))

	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain answer synthesis using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "synthesize_answer",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackStructuredAnswer(query, papers), nil
		}
		return nil, err
	}

	var raw StructuredAnswerResponse
	if err := json.Unmarshal([]byte(resp.JsonResult), &raw); err != nil {
		return nil, err
	}

	result := &rag.StructuredAnswer{
		Sections: make([]rag.AnswerSection, len(raw.Sections)),
	}

	var plainText strings.Builder
	for i, s := range raw.Sections {
		result.Sections[i].Heading = s.Heading
		plainText.WriteString("## " + s.Heading + "\n\n")
		for _, sent := range s.Sentences {
			claim := rag.AnswerClaim{
				Text:        sent.Text,
				EvidenceIDs: sent.EvidenceIDs,
			}
			result.Sections[i].Sentences = append(result.Sections[i].Sentences, claim)
			plainText.WriteString(sent.Text + " ")
		}
		plainText.WriteString("\n\n")
	}
	result.PlainText = strings.TrimSpace(plainText.String())

	return result, nil
}

func fallbackStructuredAnswer(query string, papers []Source) *rag.StructuredAnswer {
	answer := &rag.StructuredAnswer{
		Sections: []rag.AnswerSection{{
			Heading: "Evidence Summary",
		}},
	}
	section := &answer.Sections[0]
	queryText := firstNonEmpty(query, "the research question")
	if len(papers) == 0 {
		section.Sentences = append(section.Sentences, rag.AnswerClaim{
			Text:        fmt.Sprintf("Model-backed synthesis was unavailable, and no retrieved sources were provided for %s.", queryText),
			EvidenceIDs: []string{},
			Unsupported: true,
		})
	} else {
		for i, paper := range papers {
			title := firstNonEmpty(paper.Title, fmt.Sprintf("Source %d", i+1))
			summary := strings.TrimSpace(paper.Summary)
			text := fmt.Sprintf("%s is part of the retrieved evidence for %s.", title, queryText)
			if summary != "" {
				text = fmt.Sprintf("%s reports: %s", title, summary)
			}
			evidenceIDs := []string{}
			if id := strings.TrimSpace(paper.ID); id != "" {
				evidenceIDs = append(evidenceIDs, id)
			}
			section.Sentences = append(section.Sentences, rag.AnswerClaim{
				Text:        text,
				EvidenceIDs: evidenceIDs,
				Unsupported: len(evidenceIDs) == 0,
			})
		}
	}

	var plain strings.Builder
	for _, answerSection := range answer.Sections {
		plain.WriteString("## " + answerSection.Heading + "\n\n")
		for _, sentence := range answerSection.Sentences {
			plain.WriteString(sentence.Text + " ")
		}
		plain.WriteString("\n\n")
	}
	answer.PlainText = strings.TrimSpace(plain.String())
	return answer
}

func (c *BrainCapabilities) ResolveCanonicalCitations(ctx context.Context, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain canonical citation resolution using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "resolve_canonical_citations",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackCanonicalCitations(papers), nil
	}
	requestCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Resolve canonical citations for these papers: %v.", papers))
	resp, err := c.llmClient.StructuredOutput(requestCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"resolved": {"type": "array", "items": {"type": "object", "properties": {"id": {"type": "string"}, "canonicalId": {"type": "string"}}}}}}`,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain canonical citation resolution using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "resolve_canonical_citations",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackCanonicalCitations(papers), nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) ResolveCanonicalCitationsInteractive(ctx context.Context, papers []Source, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.ResolveCanonicalCitations(requestCtx, papers, model)
}

func fallbackCanonicalCitations(papers []Source) map[string]any {
	resolved := make([]map[string]any, 0, len(papers))
	for i, paper := range papers {
		id := firstNonEmpty(paper.ID, paper.DOI, paper.ArxivID, paper.Title, fmt.Sprintf("source_%d", i+1))
		canonicalID := firstNonEmpty(paper.DOI, paper.ArxivID, paper.ID, stableWisDevID("citation", paper.Title, paper.Link))
		resolved = append(resolved, map[string]any{
			"id":          id,
			"canonicalId": canonicalID,
			"degraded":    true,
		})
	}
	return map[string]any{
		"resolved": resolved,
		"degraded": true,
	}
}

func (c *BrainCapabilities) VerifyReasoningPaths(ctx context.Context, branches []map[string]any, model string) (map[string]any, error) {
	// Pre-LLM hardening: audit each branch for single-source evidence provenance
	// before invoking the language model. A branch with evidenceCount == 1 and no
	// source identifiers on any of its findings is ungrounded and must be rejected
	// without an LLM round-trip.
	auditedBranches := make([]map[string]any, 0, len(branches))
	hardRejectReasons := make([]string, 0)
	for _, branch := range branches {
		audited := make(map[string]any, len(branch))
		for k, v := range branch {
			audited[k] = v
		}
		reasons := verifyBranchProvenance(branch)
		if len(reasons) > 0 {
			audited["verificationReasons"] = reasons
			audited["verified"] = false
			hardRejectReasons = append(hardRejectReasons, reasons...)
		} else {
			audited["verified"] = true
		}
		auditedBranches = append(auditedBranches, audited)
	}
	result := map[string]any{
		"branches":          auditedBranches,
		"readyForSynthesis": len(hardRejectReasons) == 0,
	}
	if len(hardRejectReasons) > 0 {
		return result, fmt.Errorf("branch provenance verification failed: %s", strings.Join(dedupeTrimmedStrings(hardRejectReasons), "; "))
	}
	if c.llmClient == nil {
		return result, nil
	}
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain reasoning path verification using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "verify_reasoning_paths",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		for k, v := range fallbackReasoningPathVerification(auditedBranches) {
			result[k] = v
		}
		return result, nil
	}
	requestCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Verify these reasoning branches: %v.", branches))
	resp, err := c.llmClient.StructuredOutput(requestCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"results": {"type": "array", "items": {"type": "object", "properties": {"branchId": {"type": "string"}, "verified": {"type": "boolean"}, "score": {"type": "number"}}}}}}`,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain reasoning path verification using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "verify_reasoning_paths",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			for k, v := range fallbackReasoningPathVerification(auditedBranches) {
				result[k] = v
			}
			return result, nil
		}
		return result, err
	}
	var llmResult map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &llmResult); err != nil {
		return result, err
	}
	// Merge LLM results into the pre-audited result.
	for k, v := range llmResult {
		result[k] = v
	}
	return result, nil
}

// verifyBranchProvenance checks a branch map for ungrounded single-source evidence
// and returns a slice of rejection reason strings, or nil if the branch is clean.
// A branch is considered ungrounded single-source only when evidenceCount is
// explicitly set to 1 AND no finding carries a source identifier.
func verifyBranchProvenance(branch map[string]any) []string {
	evidenceCountRaw, evidenceCountSet := branch["evidenceCount"]
	if !evidenceCountSet {
		return nil
	}
	evidenceCount, _ := evidenceCountRaw.(int)
	if evidenceCount != 1 {
		return nil
	}
	findings, _ := branch["findings"].([]any)
	if len(findings) == 0 {
		return nil
	}
	// Single-source branch: check if any finding has a source ID.
	for _, f := range findings {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"sourceId", "source_id", "doi", "arxivId", "arxiv_id", "pmid"} {
			if v, ok := fm[key]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return nil // at least one finding has a source ID → grounded
				}
			}
		}
	}
	return []string{"evidence_provenance_unverified"}
}

func (c *BrainCapabilities) VerifyReasoningPathsInteractive(ctx context.Context, branches []map[string]any, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.VerifyReasoningPaths(requestCtx, branches, model)
}

func fallbackReasoningPathVerification(branches []map[string]any) map[string]any {
	results := make([]map[string]any, 0, len(branches))
	for i, branch := range branches {
		verified, _ := branch["verified"].(bool)
		results = append(results, map[string]any{
			"branchId": firstNonEmpty(AsOptionalString(branch["id"]), fmt.Sprintf("branch_%d", i+1)),
			"verified": verified,
			"score":    0.0,
			"report":   "Model-backed reasoning verification was unavailable due to provider rate limiting.",
			"degraded": true,
		})
	}
	return map[string]any{
		"results":  results,
		"degraded": true,
	}
}

func (c *BrainCapabilities) EnhanceAcademicQuery(ctx context.Context, query string, model string) (string, error) {
	if model == "" {
		model = llm.ResolveLightModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain query enhancement using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "enhance_academic_query",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return strings.TrimSpace(query), nil
	}
	prompt := fmt.Sprintf("Enhance this academic query for better retrieval: %s.", query)
	resp, err := c.llmClient.Generate(ctx, applyBrainGeneratePolicy(&llmv1.GenerateRequest{
		Prompt: prompt,
		Model:  model,
	}, "light"))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain query enhancement using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "enhance_academic_query",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return strings.TrimSpace(query), nil
		}
		return "", err
	}
	enhanced := strings.TrimSpace(resp.Text)
	if enhanced == "" {
		return "", fmt.Errorf("EnhanceAcademicQuery: LLM returned empty result")
	}
	return enhanced, nil
}

func (c *BrainCapabilities) SelectPrimarySource(ctx context.Context, query string, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain primary source selection using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "select_primary_source",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackPrimarySource(papers), nil
	}
	requestCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Select the most authoritative primary source for query: %s from these papers: %v.", query, papers))
	resp, err := c.llmClient.StructuredOutput(requestCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"primarySourceId": {"type": "string"}, "reason": {"type": "string"}}}`,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain primary source selection using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "select_primary_source",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackPrimarySource(papers), nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func fallbackPrimarySource(papers []Source) map[string]any {
	if len(papers) == 0 {
		return map[string]any{
			"primarySourceId": "",
			"reason":          "Model-backed source selection was unavailable and no sources were provided.",
			"degraded":        true,
		}
	}
	paper := papers[0]
	return map[string]any{
		"primarySourceId": firstNonEmpty(paper.ID, paper.DOI, paper.ArxivID, paper.Title),
		"reason":          "Model-backed source selection was unavailable; selected the first retrieved source as a deterministic fallback.",
		"degraded":        true,
	}
}

func fallbackPrismaReport(papers []Source) map[string]any {
	count := len(papers)
	return map[string]any{
		"records_identified": float64(count),
		"records_screened":   float64(count),
		"full_text_assessed": float64(count),
		"studies_included":   float64(count),
		"degraded":           true,
	}
}

func (c *BrainCapabilities) SelectPrimarySourceInteractive(ctx context.Context, query string, papers []Source, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.SelectPrimarySource(requestCtx, query, papers, model)
}

func (c *BrainCapabilities) AskFollowUpIfAmbiguous(ctx context.Context, query string, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain ambiguity check using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "ask_follow_up_if_ambiguous",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return map[string]any{
			"isAmbiguous": false,
			"question":    "",
			"degraded":    true,
		}, nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Analyze this query for ambiguity: %s. Determine if clarification is needed.", query))
	resp, err := c.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"isAmbiguous": {"type": "boolean"}, "question": {"type": "string"}}}`,
	}, "standard", false))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain ambiguity check using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "ask_follow_up_if_ambiguous",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return map[string]any{
				"isAmbiguous": false,
				"question":    "",
				"degraded":    true,
			}, nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) AskFollowUpIfAmbiguousInteractive(ctx context.Context, query string, model string) (map[string]any, error) {
	requestCtx, _, cancel := c.interactiveStructuredRequest(ctx, brainInteractivePlanningTimeout)
	defer cancel()
	return c.AskFollowUpIfAmbiguous(requestCtx, query, model)
}

func (c *BrainCapabilities) SuggestQuestionValues(ctx context.Context, query string, id string, name string, options []string, limit int, model string) ([]string, string, error) {
	if query == "" || len(options) == 0 {
		return nil, "", fmt.Errorf("query and options are required")
	}
	if model == "" {
		model = llm.ResolveLightModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain question value suggestion using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "suggest_question_values",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		values := fallbackQuestionValues(options, limit)
		return values, "Model-backed value suggestion was unavailable due to provider cooldown.", nil
	}
	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Suggest values for question: %s (id: %s) based on query: %s. Allowed options: %v.", name, id, query, options))
	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:       prompt,
		Model:        model,
		JsonSchema:   `{"type": "object", "properties": {"values": {"type": "array", "items": {"type": "string"}}, "explanation": {"type": "string"}}}`,
		RequestClass: "light",
		RetryProfile: "conservative",
		ServiceTier:  "standard",
	})
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain question value suggestion using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "suggest_question_values",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			values := fallbackQuestionValues(options, limit)
			return values, "Model-backed value suggestion was unavailable due to provider rate limiting.", nil
		}
		return nil, "", err
	}
	var result struct {
		Values      []string `json:"values"`
		Explanation string   `json:"explanation"`
	}
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, "", err
	}

	// Validate and normalise values
	var validated []string
	for _, v := range result.Values {
		trimV := strings.TrimSpace(v)
		matched := false
		for _, opt := range options {
			if strings.EqualFold(trimV, strings.TrimSpace(opt)) {
				validated = append(validated, opt)
				matched = true
				break
			}
		}
		if !matched {
			for _, opt := range options {
				trimOpt := strings.TrimSpace(opt)
				if strings.Contains(strings.ToLower(trimOpt), strings.ToLower(trimV)) ||
					strings.Contains(strings.ToLower(trimV), strings.ToLower(trimOpt)) {
					validated = append(validated, opt)
					break
				}
			}
		}
	}

	if len(validated) == 0 {
		return nil, "", fmt.Errorf("no valid options returned by LLM")
	}

	return validated, result.Explanation, nil
}

func fallbackQuestionValues(options []string, limit int) []string {
	maxValues := limit
	if maxValues <= 0 || maxValues > len(options) {
		maxValues = len(options)
	}
	values := make([]string, 0, maxValues)
	for _, option := range options {
		if trimmed := strings.TrimSpace(option); trimmed != "" {
			values = append(values, option)
		}
		if len(values) >= maxValues {
			break
		}
	}
	return values
}

func applyBrainStructuredPolicy(req *llmv1.StructuredRequest, tier string, highValue bool) *llmv1.StructuredRequest {
	return llm.ApplyStructuredPolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: tier,
		Structured:    true,
		HighValue:     highValue,
	}))
}

func applyBrainGeneratePolicy(req *llmv1.GenerateRequest, tier string) *llmv1.GenerateRequest {
	return llm.ApplyGeneratePolicy(req, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: tier,
	}))
}

func (c *BrainCapabilities) CritiqueEvidenceSet(ctx context.Context, query string, evidence []EvidenceItem, model string) (*sufficiencyAnalysis, error) {
	if model == "" {
		model = llm.ResolveLightModel()
	}
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain evidence critique using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "critique_evidence_set",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackSufficiencyAnalysis(query, evidence), nil
	}

	evidenceText := ""
	for i, e := range evidence {
		evidenceText += fmt.Sprintf("%d. [%s] %s: %s\n", i+1, e.PaperTitle, e.Claim, e.Snippet)
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`Perform a harsh, critical analysis of the current evidence for the query: "%s"

Evidence:
%s

Identify specific missing perspectives, methodological details, or counter-arguments that are missing.
Be highly critical. If the evidence is shallow, point it out.
Return:
- sufficient: whether the current evidence is enough
- reasoning: concise harsh critique
- nextQuery: best single follow-up query to address gaps
- missingAspects: key unanswered subtopics or gaps
- nextQueries: targeted follow-up queries
`, query, evidenceText))

	resp, err := c.llmClient.StructuredOutput(ctx, applyBrainStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type":"object","properties":{"sufficient":{"type":"boolean"},"reasoning":{"type":"string"},"nextQuery":{"type":"string"},"missingAspects":{"type":"array","items":{"type":"string"}},"nextQueries":{"type":"array","items":{"type":"string"}}},"required":["sufficient","reasoning"]}`,
	}, "standard", false))

	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain evidence critique using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "critique_evidence_set",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackSufficiencyAnalysis(query, evidence), nil
		}
		return nil, err
	}

	var result sufficiencyAnalysis
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func fallbackSufficiencyAnalysis(query string, evidence []EvidenceItem) *sufficiencyAnalysis {
	reasoning := "Model-backed evidence critique was unavailable due to provider rate limiting."
	sufficient := len(evidence) >= 3
	return &sufficiencyAnalysis{
		Sufficient:     sufficient,
		Reasoning:      reasoning,
		NextQuery:      firstNonEmpty(query, "follow-up evidence"),
		NextQueries:    []string{firstNonEmpty(query, "follow-up evidence")},
		MissingAspects: []string{"model_critique_unavailable"},
		Confidence:     0.0,
	}
}

func (c *BrainCapabilities) JudgeQuestExperience(ctx context.Context, quest *ResearchQuest, replayedPrimitives []string) (*ExperienceJudgeOutput, error) {
	if c == nil || c.llmClient == nil {
		return nil, nil
	}
	model := llm.ResolveLightModel()
	if remaining := brainCapabilityCooldownRemaining(c); remaining > 0 {
		slog.Warn("wisdev brain experience judging using cooldown fallback",
			"component", "wisdev.brain",
			"operation", "judge_quest_experience",
			"stage", "cooldown_fallback",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallbackExperienceJudge(quest), nil
	}

	replaySummary := "None"
	if len(replayedPrimitives) > 0 {
		replaySummary = strings.Join(replayedPrimitives, "\n---\n")
	}

	finalAnswer := quest.FinalAnswer
	if len(finalAnswer) > 500 {
		finalAnswer = finalAnswer[:500] + "..."
	}

	prompt := fmt.Sprintf(`You are a research quality judge. Evaluate this completed research quest and extract lessons.

## Quest Details
- Query: %s
- Domain: %s
- Papers retrieved: %d
- Accepted claims: %d
- Rejected branches: %d
- Iteration count: %d

## Final Answer (truncated to 500 chars):
%s

## Previously Injected Experience:
%s

## Instructions
1. Score overall outcome (0.0-1.0): evidence quality, coverage, efficiency
2. Classify: "success" (>=0.7), "partial" (0.4-0.7), "failure" (<0.4)
3. List 1-3 success factors, 1-3 failure factors
4. Extract 0-2 reusable lessons as {title, description, content, applicableWhen, queryPatterns}
`, quest.Query, quest.Domain, len(quest.Papers), len(quest.AcceptedClaims), len(quest.RejectedBranches), quest.CurrentIteration, finalAnswer, replaySummary)

	schema := `{"type": "object", "properties": {
		"score": {"type": "number"},
		"outcome": {"type": "string", "enum": ["success", "partial", "failure"]},
		"reasoning": {"type": "string"},
		"successFactors": {"type": "array", "items": {"type": "string"}},
		"failureFactors": {"type": "array", "items": {"type": "string"}},
		"lessons": {"type": "array", "items": {
			"type": "object",
			"properties": {
				"title": {"type": "string"},
				"description": {"type": "string"},
				"content": {"type": "string"},
				"applicableWhen": {"type": "string"},
				"queryPatterns": {"type": "array", "items": {"type": "string"}},
				"domainHints": {"type": "array", "items": {"type": "string"}}
			},
			"required": ["title", "description", "content", "applicableWhen"]
		}}
	}, "required": ["score", "outcome", "reasoning", "successFactors", "failureFactors"]}`

	reqCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()

	resp, err := c.llmClient.StructuredOutput(reqCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     appendWisdevStructuredOutputInstruction(prompt),
		Model:      model,
		JsonSchema: schema,
	}))
	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("wisdev brain experience judging using provider-rate-limit fallback",
				"component", "wisdev.brain",
				"operation", "judge_quest_experience",
				"stage", "rate_limit_fallback",
				"error_code", "provider_rate_limited",
				"error", err,
			)
			return fallbackExperienceJudge(quest), nil
		}
		return nil, err
	}

	var output ExperienceJudgeOutput
	if err := json.Unmarshal([]byte(resp.JsonResult), &output); err != nil {
		return nil, err
	}
	return &output, nil
}

func fallbackExperienceJudge(quest *ResearchQuest) *ExperienceJudgeOutput {
	if quest == nil {
		return nil
	}
	outcome := TrajectoryOutcomePartial
	score := 0.5
	if len(quest.AcceptedClaims) > 0 && len(quest.RejectedBranches) == 0 {
		outcome = TrajectoryOutcomeSuccess
		score = 0.7
	}
	if len(quest.AcceptedClaims) == 0 && len(quest.RejectedBranches) > 0 {
		outcome = TrajectoryOutcomeFailure
		score = 0.3
	}
	return &ExperienceJudgeOutput{
		Score:          score,
		Outcome:        outcome,
		Reasoning:      "Model-backed experience judging was unavailable due to provider rate limiting.",
		SuccessFactors: []string{"fallback_completed"},
		FailureFactors: []string{"model_judge_unavailable"},
	}
}
