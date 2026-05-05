package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
)

var (
	negationPattern = regexp.MustCompile(`(?i)\b(no effect|not significant|non-significant|did not|fails to|without improvement|null result)\b`)
	tokenPattern    = regexp.MustCompile(`[a-z0-9]+`)
)

type rerankScore struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

type rerankModelResponse struct {
	Scores []rerankScore `json:"scores"`
}

type scoredPaper struct {
	Paper Source
	Score float64
}

type rerankStructuredGenerator interface {
	GenerateStructured(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32) (string, error)
}

var rerankResponseSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"scores": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"index": map[string]any{"type": "integer", "minimum": 0},
					"score": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
				},
				"required": []string{"index", "score"},
			},
		},
	},
	"required": []string{"scores"},
}

var newCrossRerankStructuredGenerator = func(ctx context.Context) (rerankStructuredGenerator, error) {
	location := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_LOCATION"))
	projectID := resilience.ResolveGoogleCloudProjectID()
	return llm.NewVertexClient(ctx, projectID, location)
}

func shouldRunStage2Rerank(requested bool) bool {
	return requested
}

func rerankPapersStage2(ctx context.Context, query string, papers []Source, domain string, topK int) []Source {
	if len(papers) == 0 {
		return papers
	}
	if topK <= 0 || topK > len(papers) {
		topK = len(papers)
	}

	working := make([]Source, len(papers))
	copy(working, papers)
	candidates := working[:topK]

	llmScores, hasLLM := fetchGeminiRerankScores(ctx, query, candidates)
	scored := make([]scoredPaper, 0, len(candidates))

	for i, paper := range candidates {
		llmScore := llmScores[i]
		if !hasLLM {
			llmScore = lexicalRelevance(query, paper)
		}
		qualitySignal := computePaperQualitySignal(paper)
		negationPenalty := computeNegationPenalty(paper)
		domainBoost := computeDomainBoost(domain, paper)

		combined := (0.65 * llmScore) + (0.25 * qualitySignal) + (0.10 * domainBoost) - negationPenalty
		combined = ClampFloat(combined, 0.0, 1.0)

		updated := paper
		updated.Score = combined
		scored = append(scored, scoredPaper{Paper: updated, Score: combined})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return scored[i].Paper.Title < scored[j].Paper.Title
		}
		return scored[i].Score > scored[j].Score
	})

	reranked := make([]Source, 0, len(working))
	for _, item := range scored {
		reranked = append(reranked, item.Paper)
	}
	if len(working) > topK {
		reranked = append(reranked, working[topK:]...)
	}
	return reranked
}

func fetchGeminiRerankScores(ctx context.Context, query string, papers []Source) ([]float64, bool) {
	scores := make([]float64, len(papers))
	schemaBytes, err := json.Marshal(rerankResponseSchema)
	if err != nil {
		return scores, false
	}
	client, err := newCrossRerankStructuredGenerator(ctx)
	if err != nil {
		return scores, false
	}

	model := strings.TrimSpace(os.Getenv("AI_MODEL_RERANK_ID"))
	if model == "" {
		model = llm.ResolveStandardModel()
	}

	prompt := buildRerankPrompt(query, papers)
	text, err := client.GenerateStructured(
		ctx,
		model,
		prompt,
		"",
		string(schemaBytes),
		0.1,
		512,
	)
	if err != nil || strings.TrimSpace(text) == "" {
		return scores, false
	}

	var parsed rerankModelResponse
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return scores, false
	}
	if len(parsed.Scores) == 0 {
		return scores, false
	}

	for _, row := range parsed.Scores {
		if row.Index >= 0 && row.Index < len(scores) {
			scores[row.Index] = ClampFloat(row.Score, 0.0, 1.0)
		}
	}
	return scores, true
}

func buildRerankPrompt(query string, papers []Source) string {
	var b strings.Builder
	b.WriteString("You are an academic cross-encoder reranker.\n")
	b.WriteString("Score each paper 0.0 to 1.0 by relevance to the query.\n")
	b.WriteString("Account for exact topic match, method alignment, and contradiction/negation language.\n")
	b.WriteString("Return a score for every candidate using the supplied schema.\n")
	b.WriteString("Query: ")
	b.WriteString(query)
	b.WriteString("\nPapers:\n")
	for idx, paper := range papers {
		b.WriteString(fmt.Sprintf(
			"- index=%d | title=%q | abstract=%q | citations=%d\n",
			idx,
			truncateForPrompt(paper.Title, 220),
			truncateForPrompt(paper.Summary, 420),
			paper.CitationCount,
		))
	}
	return b.String()
}

func lexicalRelevance(query string, paper Source) float64 {
	queryTokens := tokenizeText(query)
	if len(queryTokens) == 0 {
		return 0.0
	}
	content := strings.ToLower(strings.TrimSpace(paper.Title + " " + paper.Summary))
	match := 0
	for token := range queryTokens {
		if strings.Contains(content, token) {
			match++
		}
	}
	return ClampFloat(float64(match)/float64(len(queryTokens)), 0.0, 1.0)
}

func tokenizeText(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, token := range tokenPattern.FindAllString(strings.ToLower(text), -1) {
		if len(token) < 3 {
			continue
		}
		out[token] = struct{}{}
	}
	return out
}

func computePaperQualitySignal(paper Source) float64 {
	citationNorm := ClampFloat(float64(paper.CitationCount)/500.0, 0.0, 1.0)
	summaryBoost := 0.0
	if strings.TrimSpace(paper.Summary) != "" {
		summaryBoost = 0.25
	}
	doiBoost := 0.0
	if strings.TrimSpace(paper.DOI) != "" {
		doiBoost = 0.15
	}
	return ClampFloat((0.6*citationNorm)+summaryBoost+doiBoost, 0.0, 1.0)
}

func computeNegationPenalty(paper Source) float64 {
	text := paper.Title + " " + paper.Summary
	if negationPattern.MatchString(text) {
		return 0.08
	}
	return 0.0
}

func computeDomainBoost(domain string, paper Source) float64 {
	d := strings.TrimSpace(strings.ToLower(domain))
	if d == "" {
		return 0.5
	}
	content := strings.ToLower(paper.Title + " " + paper.Summary)
	switch d {
	case "medicine", "biology", "neuro":
		if strings.Contains(content, "trial") || strings.Contains(content, "cohort") || strings.Contains(content, "meta-analysis") {
			return 0.8
		}
	case "cs", "computer science", "ai":
		if strings.Contains(content, "benchmark") || strings.Contains(content, "architecture") || strings.Contains(content, "algorithm") {
			return 0.8
		}
	case "economics":
		if strings.Contains(content, "regression") || strings.Contains(content, "panel data") || strings.Contains(content, "identification") {
			return 0.8
		}
	case "humanities":
		if strings.Contains(content, "historical") || strings.Contains(content, "interpretive") || strings.Contains(content, "archival") {
			return 0.8
		}
	}
	return 0.5
}

func truncateForPrompt(input string, maxLen int) string {
	value := strings.TrimSpace(input)
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}
