package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
	"strings"
)

type BrainCapabilities struct {
	llmClient *llm.Client
}

func NewBrainCapabilities(client *llm.Client) *BrainCapabilities {
	return &BrainCapabilities{llmClient: client}
}

type ResearchHypothesis struct {
	Claim                  string  `json:"claim"`
	FalsifiabilityCondition string  `json:"falsifiability_condition"`
	ConfidenceThreshold    float64 `json:"confidence_threshold"`
}

type ResearchTask struct {
	ID        string         `json:"id"`
	Action    string         `json:"action"`
	Reason    string         `json:"reason"`
	Params    map[string]any `json:"params"`
	DependsOn []string       `json:"depends_on"`
	Tier      string         `json:"tier"`
}

func (c *BrainCapabilities) AssessResearchComplexity(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "standard", nil
	}

	// Heuristic Triage: Fast-path for very simple/short queries
	// Reduces latency by skipping LLM call for trivial cases.
	lower := strings.ToLower(query)
	if len(query) < 30 && !strings.Contains(lower, "effect") && !strings.Contains(lower, "impact") {
		return "standard", nil
	}

	prompt := fmt.Sprintf(`Analyze the following research query for technical complexity, domain specificity, and potential for conflicting evidence.
Query: "%s"

Based on this, should we use a 'heavy' reasoning model for the core analysis, or is a 'standard' model sufficient?
Heavy is recommended for:
- Novel/Frontier scientific topics
- Systematic reviews with many conflicting sources
- Highly technical methodology analysis

Standard is sufficient for:
- General knowledge overviews
- Well-established consensus topics
- Simple fact-finding

Output ONLY 'heavy' or 'standard'.`, query)

	resp, err := c.llmClient.Generate(ctx, &llmv1.GenerateRequest{
		Prompt:       prompt,
		SystemPrompt: "You are a research triage officer.",
		Model:        llm.ResolveStandardModel(),
	})
	if err != nil {
		return "standard", err
	}
	res := strings.ToLower(strings.TrimSpace(resp.Text))
	if strings.Contains(res, "heavy") {
		return "heavy", nil
	}
	return "standard", nil
}

func (c *BrainCapabilities) DecomposeTask(ctx context.Context, query string, domain string, model string) ([]ResearchTask, error) {
	if model == "" {
		model = llm.ResolveStandardModel() // Default to standard for planning
	}
	prompt := fmt.Sprintf(`You are a senior research architect specializing in systematic evidence synthesis.
Topic: "%s"
Domain: %s

Your goal is to decompose this research query into a highly efficient, parallelizable Directed Acyclic Graph (DAG) of research tasks.

Rules for DAG construction:
1. 'step-01' should always be 'research.queryDecompose' (Heavy) to build the formal PICO/SPIDER structure.
2. 'step-02' should always be 'research.proposeHypotheses' (Heavy) to define the testable claims.
3. Parallel Workers: Create multiple 'research.retrievePapers' or 'research.enhanceQuery' steps (Light) that can run concurrently.
4. Coordination: Use 'balanced' tier for intermediate relevance scoring or verification.
5. Final Synthesis: The last step should be 'research.finalDraft' (Heavy) depending on all previous evidence gathering.

Assign appropriate model tiers:
   - 'heavy': Deep planning, hypothesis formation, final synthesis.
   - 'balanced': Mediation, verification, complex scoring.
   - 'light': Query expansion, high-throughput retrieval, TL;DRs.

Output ONLY a JSON array of ResearchTask objects.`, query, domain)

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:      prompt,
		Model:       model,
		JsonSchema:  `{"type": "array", "items": {"type": "object", "properties": {"id": {"type": "string"}, "action": {"type": "string"}, "reason": {"type": "string"}, "params": {"type": "object"}, "depends_on": {"type": "array", "items": {"type": "string"}}, "tier": {"type": "string"}}, "required": ["id", "action", "reason", "params", "tier"]}}`,
	})
	if err != nil {
		return nil, err
	}

	var tasks []ResearchTask
	if err := json.Unmarshal([]byte(resp.JsonResult), &tasks); err != nil {
		return nil, fmt.Errorf("failed to parse tasks: %w", err)
	}

	return tasks, nil
}

func (c *BrainCapabilities) ProposeHypotheses(ctx context.Context, query string, intent string, model string) ([]ResearchHypothesis, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	prompt := fmt.Sprintf(`You are a research methodologist and philosopher of science.
Research Query: "%s"
Intent: "%s"

Propose 3-5 sophisticated, testable research hypotheses. For each hypothesis:
1. Define a clear, falsifiable claim.
2. Specify the precise condition (data point, contradiction, or gap) that would DISPROVE the hypothesis.
3. Set a confidence threshold for acceptance.

Output ONLY a JSON object: {"hypotheses": [{"claim": "...", "falsifiability_condition": "...", "confidence_threshold": 0.8}]}`, query, intent)

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:      prompt,
		Model:       model,
		JsonSchema:  `{"type": "object", "properties": {"hypotheses": {"type": "array", "items": {"type": "object", "properties": {"claim": {"type": "string"}, "falsifiability_condition": {"type": "string"}, "confidence_threshold": {"type": "number"}}, "required": ["claim", "falsifiability_condition", "confidence_threshold"]}}}, "required": ["hypotheses"]}`,
	})
	if err != nil {
		return nil, err
	}

	var result struct {
		Hypotheses []ResearchHypothesis `json:"hypotheses"`
	}
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, fmt.Errorf("failed to parse hypotheses: %w", err)
	}

	return result.Hypotheses, nil
}

func (c *BrainCapabilities) CoordinateReplan(ctx context.Context, failedStepID string, reason string, contextData map[string]any, model string) ([]ResearchTask, error) {
	if model == "" {
		model = llm.ResolveLightModel()
	}
	contextJSON, _ := json.Marshal(contextData)
	prompt := fmt.Sprintf(`You are a research coordinator. The task "%s" failed because: "%s".
Current Research Context: %s

Coordinate a recovery plan. Should we retry, skip, or pivot the search?
Output ONLY a JSON array of NEW tasks to be added to the DAG.`, failedStepID, reason, string(contextJSON))

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:      prompt,
		Model:       model,
		JsonSchema:  `{"type": "array", "items": {"type": "object", "properties": {"id": {"type": "string"}, "action": {"type": "string"}, "reason": {"type": "string"}, "params": {"type": "object"}, "depends_on": {"type": "array", "items": {"type": "string"}}, "tier": {"type": "string"}}, "required": ["id", "action", "reason", "params", "tier"]}}`,
	})
	if err != nil {
		return nil, err
	}

	var tasks []ResearchTask
	if err := json.Unmarshal([]byte(resp.JsonResult), &tasks); err != nil {
		return nil, fmt.Errorf("failed to parse replan tasks: %w", err)
	}

	return tasks, nil
}

func (c *BrainCapabilities) VerifyCitations(ctx context.Context, papers []Source, model string) (map[string]any, error) {
	if len(papers) == 0 {
		return map[string]any{"verified": true, "issues": []string{}}, nil
	}
	if model == "" {
		model = llm.ResolveStandardModel()
	}

	var contextBuilder strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&contextBuilder, "[%d] DOI: %s | Title: %s\n", i+1, p.DOI, p.Title)
	}

	prompt := fmt.Sprintf(`You are an academic metadata auditor. Verify the consistency and validity of the following citations.
Look for:
1. Missing or invalid DOIs.
2. Title/metadata mismatches.
3. Potentially predatory or non-scholarly sources.

Sources to Audit:
%s

Output ONLY a JSON object: {"verified": bool, "issues": ["issue 1", "..."], "confidence": float}`, contextBuilder.String())

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"verified": {"type": "boolean"}, "issues": {"type": "array", "items": {"type": "string"}}, "confidence": {"type": "number"}}, "required": ["verified", "issues", "confidence"]}`,
	})
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) GenerateSnowballQueries(ctx context.Context, seedPapers []Source, model string) ([]string, error) {
	if len(seedPapers) == 0 {
		return []string{}, nil
	}
	if model == "" {
		model = llm.ResolveLightModel()
	}

	var titles []string
	for _, p := range seedPapers {
		titles = append(titles, p.Title)
	}

	prompt := fmt.Sprintf(`You are a research librarian. Based on these seed papers, generate 3 highly specific technical search queries to find "frontier" or "missing" evidence that would expand this seed corpus via citation snowballing logic.

Seed Corpus:
%s

Output ONLY a JSON array of strings.`, strings.Join(titles, "\n"))

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "array", "items": {"type": "string"}}`,
	})
	if err != nil {
		return nil, err
	}

	var queries []string
	if err := json.Unmarshal([]byte(resp.JsonResult), &queries); err != nil {
		return nil, err
	}

	return queries, nil
}

func (c *BrainCapabilities) SnowballCitations(ctx context.Context, seedPapers []Source, model string) ([]Source, error) {
	queries, err := c.GenerateSnowballQueries(ctx, seedPapers, model)
	if err != nil {
		return nil, err
	}
	
	// Since BrainCapabilities doesn't have a search engine, 
	// we return the queries as "virtual sources" that the orchestrator 
	// can then use to trigger actual retrieval steps if needed, 
	// or we mock them if this is a high-level suggestion.
	// For build parity, we return them as sources with the query as title.
	results := make([]Source, 0, len(queries))
	for _, q := range queries {
		results = append(results, Source{
			ID:    fmt.Sprintf("snowball_%s", NewTraceID()[:8]),
			Title: q,
			Source: "snowball_suggestion",
		})
	}
	return results, nil
}

func (c *BrainCapabilities) BuildClaimEvidenceTable(ctx context.Context, query string, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	var contextBuilder strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&contextBuilder, "[Source %d] %s: %s\n", i+1, p.Title, p.Summary)
	}

	prompt := fmt.Sprintf(`You are a systematic review analyst. Build a claim-evidence table for the research query based on the provided sources.

Query: "%s"

Sources:
%s

Output ONLY a JSON object: {"claims": [{"claim": "...", "evidence": "...", "source_id": "Source N", "status": "supported|contradicted|unclear"}], "summary": "..."}`, query, contextBuilder.String())

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"claims": {"type": "array", "items": {"type": "object", "properties": {"claim": {"type": "string"}, "evidence": {"type": "string"}, "source_id": {"type": "string"}, "status": {"type": "string"}}, "required": ["claim", "evidence", "source_id", "status"]}}, "summary": {"type": "string"}}, "required": ["claims", "summary"]}`,
	})
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) GenerateThoughts(ctx context.Context, payload map[string]any, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	// Specialized thought generation for the Tree-of-Thought loop
	b, _ := json.Marshal(payload)
	prompt := fmt.Sprintf("Analyze this research state and generate 3 divergent 'thought' branches for further exploration. JSON State: %s", string(b))

	resp, err := c.llmClient.Generate(ctx, &llmv1.GenerateRequest{
		Prompt:       prompt,
		SystemPrompt: "You are a divergent research analyst. Output 3 numbered thoughts.",
		Model:        model,
		Temperature:  0.7,
	})
	if err != nil {
		return nil, err
	}

	return map[string]any{"thoughts": resp.Text}, nil
}

func (c *BrainCapabilities) DetectContradictions(ctx context.Context, papers []Source, model string) (map[string]any, error) {
	if len(papers) < 2 {
		return map[string]any{"contradictions": []string{}, "confidence": 1.0}, nil
	}
	if model == "" {
		model = llm.ResolveStandardModel()
	}

	var contextBuilder strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&contextBuilder, "[Source %d] %s: %s\n", i+1, p.Title, p.Summary)
	}

	prompt := fmt.Sprintf(`You are a research critic. Identify any direct contradictions or significant disagreements between the following papers.
Look for conflicting results on the same intervention, effect size discrepancies, or opposing conclusions.

Sources:
%s

Output ONLY a JSON object: {"contradictions": [{"claim": "...", "source_a": "Source X", "source_b": "Source Y", "explanation": "..."}], "confidence": float}`, contextBuilder.String())

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"contradictions": {"type": "array", "items": {"type": "object", "properties": {"claim": {"type": "string"}, "source_a": {"type": "string"}, "source_b": {"type": "string"}, "explanation": {"type": "string"}}, "required": ["claim", "source_a", "source_b", "explanation"]}}, "confidence": {"type": "number"}}, "required": ["contradictions", "confidence"]}`,
	})
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) VerifyClaims(ctx context.Context, synthesisText string, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	var contextBuilder strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&contextBuilder, "[Source %d] %s: %s\n", i+1, p.Title, p.Summary)
	}

	prompt := fmt.Sprintf(`Verify the factual claims in the synthesis text against the provided academic sources.

Synthesis Text:
"%s"

Sources:
%s

Output ONLY a JSON object: {"verifications": [{"claim": "...", "status": "verified|contradicted|unsupported", "evidence": "...", "source_id": "Source N"}], "overall_verdict": "string"}`, synthesisText, contextBuilder.String())

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"verifications": {"type": "array", "items": {"type": "object", "properties": {"claim": {"type": "string"}, "status": {"type": "string"}, "evidence": {"type": "string"}, "source_id": {"type": "string"}}, "required": ["claim", "status", "evidence", "source_id"]}}, "overall_verdict": {"type": "string"}}, "required": ["verifications", "overall_verdict"]}`,
	})
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) SystematicReviewPrisma(ctx context.Context, query string, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	var contextBuilder strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&contextBuilder, "[Source %d] %s: %s\n", i+1, p.Title, p.Summary)
	}

	prompt := fmt.Sprintf(`You are a systematic review expert following PRISMA guidelines. Analyze the provided sources for the research query.
Query: "%s"

Sources:
%s

Perform:
1. Screening based on relevance.
2. Risk of bias assessment (high-level).
3. Evidence synthesis.

Output ONLY a JSON object: {"eligible_count": int, "risk_of_bias": "low|medium|high", "synthesis": "...", "prisma_flow_data": {}}`, query, contextBuilder.String())

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"eligible_count": {"type": "integer"}, "risk_of_bias": {"type": "string"}, "synthesis": {"type": "string"}, "prisma_flow_data": {"type": "object"}}, "required": ["eligible_count", "risk_of_bias", "synthesis", "prisma_flow_data"]}`,
	})
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) EnhanceAcademicQuery(ctx context.Context, query string, model string) (string, error) {
	if model == "" {
		model = llm.ResolveLightModel()
	}
	prompt := fmt.Sprintf(`Enhance the following research query for professional academic search. 
Use formal terminology, add Boolean operators if helpful, and expand with relevant synonyms or sub-topics.

Original Query: "%s"

Output ONLY the enhanced string.`, query)

	resp, err := c.llmClient.Generate(ctx, &llmv1.GenerateRequest{
		Prompt:       prompt,
		SystemPrompt: "You are an expert academic research librarian.",
		Model:        model,
		Temperature:  0.3,
	})
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(resp.Text), nil
}

func (c *BrainCapabilities) SelectPrimarySource(ctx context.Context, query string, papers []Source, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveStandardModel()
	}
	var contextBuilder strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&contextBuilder, "[Source %d] %s: %s\n", i+1, p.Title, p.Summary)
	}

	prompt := fmt.Sprintf(`Select the single most authoritative and relevant primary source for the research query.
Query: "%s"

Sources:
%s

Output ONLY a JSON object: {"selected_id": "Source N", "rationale": "..."}`, query, contextBuilder.String())

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"selected_id": {"type": "string"}, "rationale": {"type": "string"}}, "required": ["selected_id", "rationale"]}`,
	})
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) AskFollowUpIfAmbiguous(ctx context.Context, query string, model string) (map[string]any, error) {
	if model == "" {
		model = llm.ResolveLightModel()
	}
	prompt := fmt.Sprintf(`Analyze this research query for ambiguity. If it is clear, return is_ambiguous=false. 
If it needs clarification, return is_ambiguous=true and a helpful follow-up question.

Query: "%s"

Output ONLY a JSON object: {"is_ambiguous": bool, "question": "..."}`, query)

	resp, err := c.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      model,
		JsonSchema: `{"type": "object", "properties": {"is_ambiguous": {"type": "boolean"}, "question": {"type": "string"}}, "required": ["is_ambiguous", "question"]}`,
	})
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *BrainCapabilities) SynthesizeAnswer(ctx context.Context, query string, papers []Source, model string) (string, error) {
	var contextBuilder strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&contextBuilder, "[%d] Title: %s\n", i+1, p.Title)
		if p.Summary != "" {
			fmt.Fprintf(&contextBuilder, "Abstract: %s\n", p.Summary)
		}
		contextBuilder.WriteString("\n")
	}

	systemPrompt := `You are ScholarLM, an AI research assistant. Your task is to provide a comprehensive, 
accurate answer to the user's query based ONLY on the provided academic paper abstracts. 

Instructions:
1. Use the provided papers to ground your answer.
2. Provide specific citations using [1], [2], etc.
3. If the papers don't contain enough information, state what is missing.
4. Maintain a professional, scientific tone.
5. Format your output as a clear explanation followed by a list of findings.`

	userPrompt := fmt.Sprintf("Query: %s\n\nContext:\n%s", query, contextBuilder.String())

	if model == "" {
		model = llm.ResolveStandardModel() // Default to standard for synthesis unless assessed otherwise
	}

	resp, err := c.llmClient.Generate(ctx, &llmv1.GenerateRequest{
		Prompt:       userPrompt,
		SystemPrompt: systemPrompt,
		Model:        model,
		Temperature:  0.3,
	})
	if err != nil {
		return "", err
	}

	return resp.Text, nil
}
