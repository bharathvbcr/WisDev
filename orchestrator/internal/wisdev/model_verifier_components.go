package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

const wisdevPlanVerifierRerankSchema = `{"type":"object","required":["scores"],"properties":{"scores":{"type":"array","items":{"type":"object","required":["index","score"],"properties":{"index":{"type":"integer"},"score":{"type":"number"},"reason":{"type":"string"}}}}}}`

type planCandidateVerifierScore struct {
	Index  int     `json:"index"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

func sortPlanCandidatesByScoreDesc(ranked []PlanCandidate) {
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return ranked[i].Hypothesis < ranked[j].Hypothesis
		}
		return ranked[i].Score > ranked[j].Score
	})
}

func logPlanVerifierRerankDegraded(reason string, err error) {
	if !shouldLogWisDevCooldownFallback("wisdev.verifier.rerank_plan_candidates."+reason, time.Now()) {
		return
	}
	attrs := []any{
		"component", "wisdev.verifier",
		"operation", "rerank_plan_candidates",
		"stage", "degraded_fallback",
		"reason", reason,
	}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	slog.Warn("plan candidate reranking degraded to deterministic score sort", attrs...)
}

// RerankPlanCandidatesWithVerifier reranks plan candidates with one batched
// verifier model call, blending the model score with the deterministic prior
// (0.5/0.5). When the model is unavailable (nil client, provider cooldown,
// transport error, or unparseable output) it degrades to the deterministic
// prior-score sort. It never returns an error and never mutates the input
// slice; candidates are copied by value.
func RerankPlanCandidatesWithVerifier(ctx context.Context, client *llm.Client, _ *AgentSession, researchGoal string, candidates []PlanCandidate) []PlanCandidate {
	if len(candidates) == 0 {
		return nil
	}
	ranked := append([]PlanCandidate(nil), candidates...)
	if client == nil || len(ranked) <= 1 {
		sortPlanCandidatesByScoreDesc(ranked)
		return ranked
	}
	if remaining := client.ProviderCooldownRemaining(); remaining > 0 {
		logPlanVerifierRerankDegraded("provider_cooldown", fmt.Errorf("provider cooldown active; retry after %s", remaining.Round(time.Millisecond)))
		sortPlanCandidatesByScoreDesc(ranked)
		return ranked
	}

	scores, err := requestPlanCandidateVerifierScores(ctx, client, researchGoal, ranked)
	if err != nil {
		logPlanVerifierRerankDegraded("verifier_error", err)
		sortPlanCandidatesByScoreDesc(ranked)
		return ranked
	}

	seen := make(map[int]struct{}, len(scores))
	for _, entry := range scores {
		if entry.Index < 0 || entry.Index >= len(ranked) {
			continue
		}
		if _, dup := seen[entry.Index]; dup {
			continue
		}
		seen[entry.Index] = struct{}{}
		blended := clampUnitConfidence(0.5*ranked[entry.Index].Score + 0.5*clampUnitConfidence(entry.Score))
		ranked[entry.Index].Score = blended
		if reason := strings.TrimSpace(entry.Reason); reason != "" {
			if strings.TrimSpace(ranked[entry.Index].Rationale) == "" {
				ranked[entry.Index].Rationale = "Verifier: " + reason
			} else {
				ranked[entry.Index].Rationale += " | Verifier: " + reason
			}
		}
	}
	sortPlanCandidatesByScoreDesc(ranked)
	return ranked
}

func requestPlanCandidateVerifierScores(ctx context.Context, client *llm.Client, researchGoal string, candidates []PlanCandidate) ([]planCandidateVerifierScore, error) {
	goal := strings.TrimSpace(researchGoal)
	if goal == "" {
		goal = "(unspecified)"
	}

	var prompt strings.Builder
	prompt.WriteString("You are a research-plan verifier. Score how well each candidate plan advances the research goal on a 0..1 scale and give a one-line reason per candidate. Reference candidates by their zero-based index.\n\nResearch goal: ")
	prompt.WriteString(goal)
	prompt.WriteString("\n\nCandidates:")
	for i, candidate := range candidates {
		prompt.WriteString(fmt.Sprintf("\n[%d] hypothesis: %s", i, strings.TrimSpace(candidate.Hypothesis)))
		if rationale := strings.TrimSpace(candidate.Rationale); rationale != "" {
			prompt.WriteString("; rationale: " + rationale)
		}
		if summary := summarizePlanStepsForVerifier(candidate.Plan); summary != "" {
			prompt.WriteString("; steps: " + summary)
		}
	}

	reqCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	resp, err := client.StructuredOutput(reqCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:      appendWisdevStructuredOutputInstruction(prompt.String()),
		JsonSchema:  wisdevPlanVerifierRerankSchema,
		Temperature: 0.1,
	}))
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("plan candidate reranking returned nil response")
	}

	var payload struct {
		Scores []planCandidateVerifierScore `json:"scores"`
	}
	if err := json.Unmarshal([]byte(resp.GetJsonResult()), &payload); err != nil {
		return nil, fmt.Errorf("plan candidate reranking returned invalid structured output: %w", err)
	}
	if len(payload.Scores) == 0 {
		return nil, fmt.Errorf("plan candidate reranking returned no scores")
	}
	return payload.Scores, nil
}

func summarizePlanStepsForVerifier(plan *PlanState) string {
	if plan == nil || len(plan.Steps) == 0 {
		return ""
	}
	actions := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		action := strings.TrimSpace(step.Action)
		if action == "" {
			continue
		}
		actions = append(actions, action)
	}
	return strings.Join(actions, " -> ")
}

type googleGenAIModel struct {
	client *llm.Client
	name   string
	tier   ModelTier
}

func NewGoogleGenAIModel(client *llm.Client, name string, tier ModelTier) Model {
	if strings.TrimSpace(name) == "" {
		name = ResolveModelNameForTier(tier)
	}
	if tier == "" {
		tier = ModelTierStandard
	}
	return &googleGenAIModel{client: client, name: name, tier: tier}
}

func (m *googleGenAIModel) Generate(ctx context.Context, prompt string) (string, error) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return "", nil
	}

	text, err := m.generateText(ctx, trimmed, 0.2)
	if err == nil && text != "" {
		return text, nil
	}
	return trimmed, nil
}

func (m *googleGenAIModel) GenerateHypotheses(ctx context.Context, query string) ([]string, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, nil
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Generate 3 to 5 concise, evidence-testable research hypotheses for this query.\n\nQuery: %s", trimmed))
	if hypotheses, err := m.generateStringList(ctx, "hypothesis generation", prompt, `{"type":"object","required":["hypotheses"],"properties":{"hypotheses":{"type":"array","items":{"type":"string"}}}}`, "hypotheses"); err == nil && len(hypotheses) > 0 {
		return hypotheses, nil
	}

	return []string{
		trimmed,
		trimmed + " with supporting evidence",
		trimmed + " with counter-evidence considered",
	}, nil
}

func (m *googleGenAIModel) ExtractClaims(ctx context.Context, text string) ([]string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, nil
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Extract the core scientific claims from the following text.\n\nText:\n%s", trimmed))
	if claims, err := m.generateStringList(ctx, "claim extraction", prompt, `{"type":"object","required":["claims"],"properties":{"claims":{"type":"array","items":{"type":"string"}}}}`, "claims"); err == nil && len(claims) > 0 {
		return claims, nil
	}

	return []string{trimmed}, nil
}

func (m *googleGenAIModel) VerifyClaim(ctx context.Context, claim, evidence string) (bool, float64, error) {
	if strings.TrimSpace(claim) == "" || strings.TrimSpace(evidence) == "" {
		return false, 0.25, nil
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf("Assess whether the evidence supports the claim.\n\nClaim: %s\n\nEvidence:\n%s", strings.TrimSpace(claim), strings.TrimSpace(evidence)))
	var payload struct {
		Supported  bool    `json:"supported"`
		Confidence float64 `json:"confidence"`
	}
	err := m.generateStructuredValue(ctx, "claim verification", prompt, `{"type":"object","required":["supported","confidence"],"properties":{"supported":{"type":"boolean"},"confidence":{"type":"number"}}}`, &payload)
	if err == nil {
		return payload.Supported, clampUnitConfidence(payload.Confidence), nil
	}

	supported, confidence := lexicalClaimSupportHeuristic(claim, evidence)
	if shouldLogWisDevCooldownFallback("wisdev.verifier.verify_claim", time.Now()) {
		modelName := ""
		if m != nil {
			modelName = strings.TrimSpace(m.name)
		}
		slog.Warn("claim verification degraded to lexical overlap heuristic",
			"component", "wisdev.verifier",
			"operation", "verify_claim",
			"stage", "heuristic_fallback",
			"model", modelName,
			"supported", supported,
			"confidence", confidence,
			"error", err,
		)
	}
	return supported, confidence, nil
}

var wisdevLexicalStopwords = map[string]struct{}{
	"the": {}, "and": {}, "are": {}, "was": {}, "were": {}, "been": {}, "being": {},
	"has": {}, "have": {}, "had": {}, "does": {}, "did": {}, "not": {}, "but": {},
	"can": {}, "could": {}, "will": {}, "would": {}, "should": {}, "may": {}, "might": {},
	"must": {}, "shall": {}, "for": {}, "with": {}, "that": {}, "this": {}, "these": {},
	"those": {}, "from": {}, "into": {}, "onto": {}, "over": {}, "under": {}, "about": {},
	"than": {}, "then": {}, "them": {}, "they": {}, "its": {}, "his": {}, "her": {},
	"our": {}, "your": {}, "their": {}, "all": {}, "any": {}, "each": {}, "more": {},
	"most": {}, "other": {}, "some": {}, "such": {}, "only": {}, "own": {}, "same": {},
	"too": {}, "very": {}, "also": {}, "when": {}, "where": {}, "which": {}, "while": {},
	"who": {}, "whom": {}, "why": {}, "how": {}, "what": {}, "between": {}, "both": {},
	"because": {}, "after": {}, "before": {}, "during": {}, "above": {}, "below": {},
	"again": {}, "further": {}, "here": {}, "there": {}, "against": {},
}

// wisdevLexicalTokens lowercases the text, splits on non-alphanumeric runes,
// and drops stopwords and tokens of two or fewer characters.
func wisdevLexicalTokens(text string) map[string]struct{} {
	tokens := map[string]struct{}{}
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		token := current.String()
		current.Reset()
		if utf8.RuneCountInString(token) <= 2 {
			return
		}
		if _, stop := wisdevLexicalStopwords[token]; stop {
			return
		}
		tokens[token] = struct{}{}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

// lexicalClaimSupportHeuristic is the deterministic degraded path for claim
// verification when the model is unavailable: overlap is the fraction of
// distinct claim tokens present in the evidence, supported when >= 0.3, and
// confidence = clamp(0.30 + 0.5*overlap, 0.25, 0.80).
func lexicalClaimSupportHeuristic(claim, evidence string) (bool, float64) {
	claimTokens := wisdevLexicalTokens(claim)
	overlap := 0.0
	if len(claimTokens) > 0 {
		evidenceTokens := wisdevLexicalTokens(evidence)
		matched := 0
		for token := range claimTokens {
			if _, ok := evidenceTokens[token]; ok {
				matched++
			}
		}
		overlap = float64(matched) / float64(len(claimTokens))
	}
	supported := overlap >= 0.3
	confidence := 0.30 + 0.5*overlap
	if confidence < 0.25 {
		confidence = 0.25
	}
	if confidence > 0.80 {
		confidence = 0.80
	}
	return supported, confidence
}

func (m *googleGenAIModel) SynthesizeFindings(ctx context.Context, hypotheses []string, evidence map[string]interface{}) (string, error) {
	if len(hypotheses) == 0 {
		return "No hypotheses available.", nil
	}

	prompt := fmt.Sprintf("Synthesize the following research findings into a concise, evidence-grounded summary.\n\nHypotheses:\n%s\n\nEvidence:\n%v", strings.Join(hypotheses, "\n"), evidence)
	if summary, err := m.generateText(ctx, prompt, 0.2); err == nil && summary != "" {
		return summary, nil
	}

	return "Synthesis: " + hypotheses[0], nil
}

func (m *googleGenAIModel) CritiqueFindings(ctx context.Context, findings []string) (string, error) {
	if len(findings) == 0 {
		return "No findings available for critique.", nil
	}

	prompt := fmt.Sprintf("Critique the following research findings. Focus on methodological weaknesses, unsupported inferences, and missing evidence.\n\nFindings:\n%s", strings.Join(findings, "\n"))
	if critique, err := m.generateText(ctx, prompt, 0.2); err == nil && critique != "" {
		return critique, nil
	}

	return "Critique: evidence should be strengthened.", nil
}

func (m *googleGenAIModel) Name() string    { return m.name }
func (m *googleGenAIModel) Tier() ModelTier { return m.tier }

func (m *googleGenAIModel) resolvedTier() string {
	switch m.tier {
	case ModelTierHeavy:
		return "heavy"
	case ModelTierLight:
		return "light"
	default:
		return "standard"
	}
}

func (m *googleGenAIModel) generateText(ctx context.Context, prompt string, temperature float32) (string, error) {
	if m == nil || m.client == nil {
		return "", fmt.Errorf("model client unavailable")
	}
	if remaining := m.client.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("google genai model text generation skipped during provider cooldown",
			"component", "wisdev.google_genai_model",
			"operation", "generate_text",
			"model", strings.TrimSpace(m.name),
			"tier", m.resolvedTier(),
			"retry_after_ms", remaining.Milliseconds(),
		)
		return "", fmt.Errorf("provider cooldown active; retry after %s", remaining.Round(time.Millisecond))
	}
	resp, err := m.client.Generate(ctx, llm.ApplyGeneratePolicy(&llmv1.GenerateRequest{
		Prompt:      strings.TrimSpace(prompt),
		Model:       strings.TrimSpace(m.name),
		Temperature: temperature,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: m.resolvedTier(),
		TaskType:      "synthesis",
	})))
	if err != nil {
		return "", err
	}
	return normalizeWisdevGeneratedText("google genai model generation", resp)
}

func (m *googleGenAIModel) generateStructuredValue(ctx context.Context, operation string, prompt string, schema string, out any) error {
	if m == nil || m.client == nil {
		return fmt.Errorf("model client unavailable")
	}
	if remaining := m.client.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("google genai model structured output skipped during provider cooldown",
			"component", "wisdev.google_genai_model",
			"operation", operation,
			"model", strings.TrimSpace(m.name),
			"tier", m.resolvedTier(),
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fmt.Errorf("provider cooldown active; retry after %s", remaining.Round(time.Millisecond))
	}
	resp, err := m.client.StructuredOutput(ctx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:      prompt,
		Model:       strings.TrimSpace(m.name),
		JsonSchema:  schema,
		Temperature: 0.1,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: m.resolvedTier(),
		Structured:    true,
		HighValue:     true,
	})))
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(resp.GetJsonResult()), out); err != nil {
		return fmt.Errorf("%s returned invalid structured output: %w", operation, err)
	}
	return nil
}

func (m *googleGenAIModel) generateStringList(ctx context.Context, operation string, prompt string, schema string, field string) ([]string, error) {
	var payload map[string][]string
	if err := m.generateStructuredValue(ctx, operation, prompt, schema, &payload); err != nil {
		return nil, err
	}
	items := make([]string, 0, len(payload[field]))
	seen := make(map[string]struct{}, len(payload[field]))
	for _, value := range payload[field] {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		items = append(items, trimmed)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%s returned no usable values", operation)
	}
	return items, nil
}

func clampUnitConfidence(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

type YOLOState struct {
	Hypotheses []*Hypothesis
	Status     string
	Synthesis  *SynthesisResult
}

type questStateSaver interface {
	SaveQuestState(ctx context.Context, quest *QuestState) error
}

type YOLOOrchestrator struct {
	jobID     string
	query     string
	model     Model
	searchReg *search.ProviderRegistry
	ragEngine *rag.Engine
	store     questStateSaver
	db        DBProvider
	userID    string
}

func NewYOLOOrchestrator(jobID string, query string, model Model, searchReg *search.ProviderRegistry, ragEngine *rag.Engine, store questStateSaver, db DBProvider) *YOLOOrchestrator {
	return &YOLOOrchestrator{
		jobID:     strings.TrimSpace(jobID),
		query:     strings.TrimSpace(query),
		model:     model,
		searchReg: searchReg,
		ragEngine: ragEngine,
		store:     store,
		db:        db,
	}
}

func (o *YOLOOrchestrator) WithUserID(userID string) *YOLOOrchestrator {
	o.userID = strings.TrimSpace(userID)
	return o
}

func (o *YOLOOrchestrator) Run(ctx context.Context) (*YOLOState, error) {
	agent := NewHypothesisAgent(o.model)
	hypotheses, err := agent.Generate(ctx, o.query, 3)
	if err != nil {
		return nil, err
	}

	state := &YOLOState{
		Hypotheses: hypotheses,
		Status:     string(QuestStatusComplete),
		Synthesis: &SynthesisResult{
			Sections: map[string]string{
				"main": "YOLO synthesis for: " + o.query,
			},
			CreatedAt: time.Now(),
		},
	}

	if o.store != nil {
		quest := &QuestState{
			ID:                 firstNonEmpty(o.jobID, NewTraceID()),
			SessionID:          firstNonEmpty(o.jobID, NewTraceID()),
			QuestID:            firstNonEmpty(o.jobID, NewTraceID()),
			UserID:             o.userID,
			Query:              o.query,
			Domain:             "general",
			DetectedDomain:     "general",
			Status:             QuestStatusComplete,
			CurrentStage:       QuestStageComplete,
			Mode:               WisDevModeYOLO,
			ServiceTier:        ServiceTierFlex,
			Hypotheses:         hypotheses,
			Synthesis:          state.Synthesis,
			Artifacts:          map[string]any{},
			EvidenceDossiers:   map[string]*EvidenceDossier{},
			ResearchScratchpad: map[string]string{},
			CreatedAt:          NowMillis(),
			UpdatedAt:          NowMillis(),
		}
		_ = o.store.SaveQuestState(ctx, quest)
	}

	return state, nil
}
