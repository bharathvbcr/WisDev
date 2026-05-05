package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

func RerankPlanCandidatesWithVerifier(_ context.Context, _ *llm.Client, _ *AgentSession, _ string, candidates []PlanCandidate) []PlanCandidate {
	if len(candidates) == 0 {
		return nil
	}
	ranked := append([]PlanCandidate(nil), candidates...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return ranked[i].Hypothesis < ranked[j].Hypothesis
		}
		return ranked[i].Score > ranked[j].Score
	})
	return ranked
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
	if err := m.generateStructuredValue(ctx, "claim verification", prompt, `{"type":"object","required":["supported","confidence"],"properties":{"supported":{"type":"boolean"},"confidence":{"type":"number"}}}`, &payload); err == nil {
		return payload.Supported, clampUnitConfidence(payload.Confidence), nil
	}

	return true, 0.7, nil
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
		slog.Warn("compat model text generation skipped during provider cooldown",
			"component", "wisdev.compat_model",
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
	return normalizeWisdevGeneratedText("compat model generation", resp)
}

func (m *googleGenAIModel) generateStructuredValue(ctx context.Context, operation string, prompt string, schema string, out any) error {
	if m == nil || m.client == nil {
		return fmt.Errorf("model client unavailable")
	}
	if remaining := m.client.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("compat model structured output skipped during provider cooldown",
			"component", "wisdev.compat_model",
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
