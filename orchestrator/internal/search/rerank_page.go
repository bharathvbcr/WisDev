package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
)

type pageIndexRanking struct {
	Index  int     `json:"index"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

type pageIndexResponse struct {
	Rankings []pageIndexRanking `json:"rankings"`
}

func shouldEnablePageIndexRerank() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("GO_PAGE_INDEX_RERANK_ENABLED")))
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func ShouldRunPageIndexRerank(requested bool) bool {
	return requested || shouldEnablePageIndexRerank()
}

func PageIndexRerankPapers(ctx context.Context, query string, papers []Paper, topK int) []Paper {
	if len(papers) == 0 {
		return papers
	}
	if topK <= 0 || topK > len(papers) {
		topK = len(papers)
	}

	working := make([]Paper, len(papers))
	copy(working, papers)
	candidates := working[:topK]

	rankings, ok := fetchGeminiPageIndexRankings(ctx, query, candidates, topK)
	if !ok || len(rankings) == 0 {
		return working
	}

	ordered := make([]Paper, 0, len(candidates))
	used := make(map[int]struct{}, len(rankings))
	for _, ranking := range rankings {
		if ranking.Index < 0 || ranking.Index >= len(candidates) {
			continue
		}
		used[ranking.Index] = struct{}{}
		paper := candidates[ranking.Index]
		paper.Score = clampFloat(ranking.Score/100.0, 0.0, 1.0)
		ordered = append(ordered, paper)
	}

	if len(ordered) == 0 {
		return working
	}

	for idx, paper := range candidates {
		if _, exists := used[idx]; exists {
			continue
		}
		ordered = append(ordered, paper)
	}

	if len(working) > topK {
		ordered = append(ordered, working[topK:]...)
	}

	return ordered
}

func fetchGeminiPageIndexRankings(ctx context.Context, query string, papers []Paper, topK int) ([]pageIndexRanking, bool) {
	apiKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	if apiKey == "" {
		return nil, false
	}

	model := strings.TrimSpace(os.Getenv("AI_MODEL_RERANK_ID"))
	if model == "" {
		model = llm.ResolveStandardModel()
	}

	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": buildPageIndexPrompt(query, papers, topK)},
				},
			},
		},
		"generationConfig": map[string]any{
			"temperature":      0.1,
			"responseMimeType": "application/json",
		},
	})
	if err != nil {
		return nil, false
	}

	endpoint := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		model,
		apiKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, false
	}
	text := extractGenerateContentText(payload)
	if text == "" {
		return nil, false
	}

	var parsed pageIndexResponse
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, false
	}

	sort.SliceStable(parsed.Rankings, func(i, j int) bool {
		return parsed.Rankings[i].Score > parsed.Rankings[j].Score
	})
	return parsed.Rankings, true
}

func buildPageIndexPrompt(query string, papers []Paper, topK int) string {
	var b strings.Builder
	b.WriteString("You are an academic PageIndex reranker.\n")
	b.WriteString("Return strict JSON: {\"rankings\":[{\"index\":0,\"score\":95,\"reason\":\"...\"}]}\n")
	b.WriteString("Rank papers by true relevance to the query.\n")
	b.WriteString("Consider semantic alignment, methodological fit, evidence strength, and whether the paper directly answers the query.\n")
	b.WriteString(fmt.Sprintf("Return up to %d papers.\n", topK))
	b.WriteString("Query: ")
	b.WriteString(query)
	b.WriteString("\nCandidates:\n")
	for idx, paper := range papers {
		b.WriteString(fmt.Sprintf(
			"- index=%d | title=%q | abstract=%q | citations=%d\n",
			idx,
			truncateForPrompt(paper.Title, 220),
			truncateForPrompt(paper.Abstract, 420),
			paper.CitationCount,
		))
	}
	return b.String()
}

func truncateForPrompt(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func extractGenerateContentText(payload map[string]any) string {
	candidates, ok := payload["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		return ""
	}
	candidate := candidates[0].(map[string]any)
	content, ok := candidate["content"].(map[string]any)
	if !ok {
		return ""
	}
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) == 0 {
		return ""
	}
	part := parts[0].(map[string]any)
	text, _ := part["text"].(string)
	return text
}

func clampFloat(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
