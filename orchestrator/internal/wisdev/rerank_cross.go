package wisdev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
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
	apiKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	if apiKey == "" {
		return scores, false
	}

	model := strings.TrimSpace(os.Getenv("AI_MODEL_RERANK_ID"))
	if model == "" {
		model = llm.ResolveStandardModel()
	}

	prompt := buildRerankPrompt(query, papers)
	requestBody := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"temperature":      0.1,
			"responseMimeType": "application/json",
		},
	}
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return scores, false
	}

	endpoint := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		model,
		apiKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return scores, false
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return scores, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return scores, false
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return scores, false
	}
	text := extractGenerateContentText(payload)
	if text == "" {
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
	b.WriteString("Return strict JSON: {\"scores\":[{\"index\":0,\"score\":0.0}]}\n")
	b.WriteString("Score each paper 0.0 to 1.0 by relevance to the query.\n")
	b.WriteString("Account for exact topic match, method alignment, and contradiction/negation language.\n")
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

func extractGenerateContentText(payload map[string]any) string {
	candidates, ok := payload["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		return ""
	}
	first, ok := candidates[0].(map[string]any)
	if !ok {
		return ""
	}
	content, ok := first["content"].(map[string]any)
	if !ok {
		return ""
	}
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) == 0 {
		return ""
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := part["text"].(string)
	return strings.TrimSpace(text)
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
