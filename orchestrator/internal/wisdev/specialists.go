package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

var specialistConfidencePattern = regexp.MustCompile(`(?i)\bconfidence(?:\s+score)?\s*[:=]\s*([0-9]+(?:\.[0-9]+)?)(\s*%)?`)

// SpecialistPersona represents a focused research role.
type SpecialistPersona string

const (
	PersonaHypothesisGenerator SpecialistPersona = "hypothesis_generator"
	PersonaEvidenceGatherer    SpecialistPersona = "evidence_gatherer"
	PersonaMethodologyCritic   SpecialistPersona = "methodology_critic"
	PersonaSynthesizer         SpecialistPersona = "synthesizer"
	PersonaVerificationAgent   SpecialistPersona = "verification_agent"
)

// SpecialistResult carries the output of a specialist agent.
type SpecialistResult struct {
	Persona    SpecialistPersona
	Confidence float64
	Findings   []string
	RawOutput  string
}

// ResearchSpecialist performs a focused research task.
type ResearchSpecialist struct {
	persona SpecialistPersona
	client  *llm.Client
	model   string
}

// NewResearchSpecialist creates a new specialist.
func NewResearchSpecialist(persona SpecialistPersona, client *llm.Client, model string) *ResearchSpecialist {
	return &ResearchSpecialist{
		persona: persona,
		client:  client,
		model:   model,
	}
}

// Execute performs the specialist's assigned task.
func (s *ResearchSpecialist) Execute(ctx context.Context, query string, contextDocs []string) (*SpecialistResult, error) {
	if s.client == nil {
		return nil, fmt.Errorf("specialist client unavailable")
	}
	if remaining := s.client.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("research specialist execution skipped during provider cooldown",
			"component", "wisdev.specialist",
			"operation", "execute",
			"persona", s.persona,
			"retry_after_ms", remaining.Milliseconds(),
		)
		return s.parseResponse(heuristicSpecialistResponse(s.persona, query, contextDocs)), nil
	}

	prompt := s.buildPrompt(query, contextDocs)

	resp, err := s.client.Generate(ctx, applyWisdevStandardGeneratePolicy(&llmv1.GenerateRequest{
		Prompt: prompt,
		Model:  s.model,
	}))
	if err != nil {
		if wisdevLLMCallIsCoolingDown(err) {
			slog.Warn("research specialist execution fell back during provider cooldown",
				"component", "wisdev.specialist",
				"operation", "execute",
				"persona", s.persona,
				"error", err.Error(),
			)
			return s.parseResponse(heuristicSpecialistResponse(s.persona, query, contextDocs)), nil
		}
		return nil, fmt.Errorf("specialist execution failed: %w", err)
	}

	text, err := normalizeWisdevGeneratedText("specialist execution", resp)
	if err != nil {
		return nil, err
	}

	return s.parseResponse(text), nil
}

func heuristicSpecialistResponse(persona SpecialistPersona, query string, docs []string) string {
	findings := make([]string, 0, minInt(len(docs), 3)+1)
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		findings = append(findings, fmt.Sprintf("%s fallback analysis for %s", persona, trimmed))
	}
	for _, doc := range docs {
		trimmed := strings.TrimSpace(doc)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > 180 {
			trimmed = strings.TrimSpace(trimmed[:180])
		}
		findings = append(findings, trimmed)
		if len(findings) >= 4 {
			break
		}
	}
	if len(findings) == 0 {
		findings = append(findings, fmt.Sprintf("%s fallback analysis completed with no context documents.", persona))
	}
	return strings.Join(findings, "\n") + "\nConfidence: 0.55"
}

func (s *ResearchSpecialist) buildPrompt(query string, docs []string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("You are a specialized research agent acting as a %s.\n\n", s.persona))
	sb.WriteString(fmt.Sprintf("Objective: %s\n\n", query))
	sb.WriteString("Context from academic papers:\n")
	for i, doc := range docs {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i+1, doc))
	}
	sb.WriteString("\nProvide your expert analysis, findings, and a confidence score (0.0-1.0).")
	return sb.String()
}

func (s *ResearchSpecialist) parseResponse(text string) *SpecialistResult {
	trimmed := strings.TrimSpace(text)
	findings := parseSpecialistFindings(trimmed)
	return &SpecialistResult{
		Persona:    s.persona,
		Confidence: parseSpecialistConfidence(trimmed, 0.8),
		RawOutput:  trimmed,
		Findings:   findings,
	}
}

func parseSpecialistConfidence(text string, fallback float64) float64 {
	match := specialistConfidencePattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return ClampFloat(fallback, 0, 1)
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return ClampFloat(fallback, 0, 1)
	}
	if strings.TrimSpace(match[2]) == "%" || value > 1 {
		value = value / 100
	}
	return ClampFloat(value, 0, 1)
}

func parseSpecialistFindings(text string) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}

	lines := strings.Split(trimmed, "\n")
	findings := make([]string, 0, len(lines))
	for _, line := range lines {
		clean := strings.TrimSpace(line)
		if clean == "" {
			continue
		}
		if specialistConfidencePattern.MatchString(clean) && len(strings.Fields(clean)) <= 5 {
			continue
		}
		clean = strings.TrimLeft(clean, "-*• \t")
		clean = strings.TrimLeft(clean, "0123456789.) ")
		clean = strings.TrimSpace(clean)
		if clean == "" {
			continue
		}
		if isSpecialistFindingHeading(clean) {
			continue
		}
		findings = append(findings, clean)
	}
	if len(findings) == 0 {
		return []string{trimmed}
	}
	return dedupeTrimmedStrings(findings)
}

func isSpecialistFindingHeading(line string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.TrimSuffix(line, ":"))) {
	case "finding", "findings", "analysis", "expert analysis", "key findings":
		return true
	default:
		return false
	}
}

// SpecialistAgent provides specialized analysis of evidence findings.
// It maps to personas like methodologist, skeptic, synthesizer, and curator.
type SpecialistAgent struct {
	persona SpecialistType
	client  *llm.Client
}

// NewMethodologist creates a methodologist specialist for critique of research design.
func NewMethodologist(client *llm.Client) *SpecialistAgent {
	return &SpecialistAgent{
		persona: SpecialistTypeMethodologist,
		client:  client,
	}
}

// NewSkeptic creates a skeptic specialist for challenging assumptions and evidence.
func NewSkeptic(client *llm.Client) *SpecialistAgent {
	return &SpecialistAgent{
		persona: SpecialistTypeSkeptic,
		client:  client,
	}
}

// NewSynthesizer creates a synthesizer specialist for combining and integrating findings.
func NewSynthesizer(client *llm.Client) *SpecialistAgent {
	return &SpecialistAgent{
		persona: SpecialistTypeSynthesizer,
		client:  client,
	}
}

// NewCurator creates a curator specialist for multi-modal content analysis.
func NewCurator(client *llm.Client) *SpecialistAgent {
	return &SpecialistAgent{
		persona: SpecialistTypeCurator,
		client:  client,
	}
}

// Analyze performs specialized analysis on an evidence finding.
func (s *SpecialistAgent) Analyze(ctx context.Context, query string, finding EvidenceFinding) (*SpecialistStatus, error) {
	if s.client == nil {
		return &SpecialistStatus{
			Type:         s.persona,
			DeepAnalysis: fmt.Sprintf("%s analysis: %s", s.persona, finding.Snippet),
			Verification: 0,
		}, nil
	}
	if remaining := s.client.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("specialist analysis skipped during provider cooldown",
			"component", "wisdev.specialist",
			"operation", "analyze",
			"persona", s.persona,
			"retry_after_ms", remaining.Milliseconds(),
		)
		return specialistFallbackStatus(s.persona, finding), nil
	}

	prompt := s.buildAnalysisPrompt(query, finding)

	resp, err := s.client.Generate(ctx, applyWisdevLightGeneratePolicy(&llmv1.GenerateRequest{
		Prompt: prompt,
		Model:  llm.ResolveLightModel(), // Fast specialist analysis uses the light tier
	}))
	if err != nil {
		if wisdevLLMCallIsCoolingDown(err) {
			slog.Warn("specialist analysis fell back during provider cooldown",
				"component", "wisdev.specialist",
				"operation", "analyze",
				"persona", s.persona,
				"error", err.Error(),
			)
			return specialistFallbackStatus(s.persona, finding), nil
		}
		return nil, fmt.Errorf("specialist analysis failed for %s: %w", s.persona, err)
	}

	text, err := normalizeWisdevGeneratedText(fmt.Sprintf("%s specialist analysis", s.persona), resp)
	if err != nil {
		return nil, err
	}

	return s.parseAnalysisResponse(text), nil
}

func specialistFallbackStatus(persona SpecialistType, finding EvidenceFinding) *SpecialistStatus {
	return &SpecialistStatus{
		Type:         persona,
		DeepAnalysis: fmt.Sprintf("%s analysis: %s", persona, strings.TrimSpace(finding.Snippet)),
		Verification: 0,
		Reasoning:    "Provider cooldown active; deterministic specialist fallback used.",
	}
}

func (s *SpecialistAgent) buildAnalysisPrompt(query string, finding EvidenceFinding) string {
	var sb strings.Builder

	switch s.persona {
	case SpecialistTypeMethodologist:
		sb.WriteString("Role: Academic Methodologist\n")
		sb.WriteString("You are a research methodology expert. Critique the research design and methodology of the following evidence:\n\n")
	case SpecialistTypeSkeptic:
		sb.WriteString("Role: Scientific Skeptic\n")
		sb.WriteString("You are a critical skeptic. Identify potential weaknesses, biases, and gaps in the following evidence:\n\n")
	case SpecialistTypeSynthesizer:
		sb.WriteString("Role: Research Synthesizer\n")
		sb.WriteString("You are a knowledge synthesizer. Integrate and synthesize the key insights from the following evidence:\n\n")
	case SpecialistTypeCurator:
		sb.WriteString("Role: Multi-modal Curator\n")
		sb.WriteString("You are a multi-modal content curator. Analyze and describe tables, figures, and visual elements in the following evidence:\n\n")
	default:
		sb.WriteString("You are a research specialist. Analyze the following evidence:\n\n")
	}

	sb.WriteString(fmt.Sprintf("Original Query: %s\n\n", query))
	sb.WriteString(fmt.Sprintf("Paper Title: %s\n", finding.PaperTitle))
	sb.WriteString(fmt.Sprintf("Snippet: %s\n\n", finding.Snippet))
	sb.WriteString("Provide:\n")
	sb.WriteString("1. Deep analysis relevant to your role\n")
	sb.WriteString("2. Verification assessment (positive, negative, or neutral)\n")
	sb.WriteString("3. Any citation gaps or missing references\n")

	return sb.String()
}

func (s *SpecialistAgent) parseAnalysisResponse(text string) *SpecialistStatus {
	verification := 0 // Neutral by default
	lower := strings.ToLower(strings.TrimSpace(text))
	if hasExplicitVerificationPolarity(lower, "positive") ||
		strings.Contains(lower, "support") ||
		strings.Contains(lower, "confirm") {
		verification = 1 // Verified
	} else if hasExplicitVerificationPolarity(lower, "negative") ||
		strings.Contains(lower, "contradict") ||
		strings.Contains(lower, "reject") {
		verification = -1 // Rejected
	}

	return &SpecialistStatus{
		Type:         s.persona,
		DeepAnalysis: strings.TrimSpace(text),
		Verification: verification,
	}
}

func hasExplicitVerificationPolarity(text string, polarity string) bool {
	return strings.Contains(text, "verification: "+polarity) ||
		strings.Contains(text, "verification assessment: "+polarity) ||
		strings.Contains(text, "verification - "+polarity) ||
		strings.Contains(text, "verification="+polarity)
}
