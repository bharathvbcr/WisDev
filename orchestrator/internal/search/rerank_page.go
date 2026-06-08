package search

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
)

type pageIndexRanking struct {
	Index  int     `json:"index"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

type pageIndexResponse struct {
	Rankings []pageIndexRanking `json:"rankings"`
}

type pageIndexStructuredGenerator interface {
	GenerateStructured(ctx context.Context, modelID, prompt, systemPrompt string, jsonSchemaStr string, temperature float32, maxTokens int32) (string, error)
}

var pageIndexResponseSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"rankings": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"index":  map[string]any{"type": "integer", "minimum": 0},
					"score":  map[string]any{"type": "number", "minimum": 0, "maximum": 100},
					"reason": map[string]any{"type": "string"},
				},
				"required": []string{"index", "score", "reason"},
			},
		},
	},
	"required": []string{"rankings"},
}

var jsonMarshalFn = json.Marshal
var newPageIndexStructuredGenerator = func(ctx context.Context) (pageIndexStructuredGenerator, error) {
	location := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_LOCATION"))
	projectID := resilience.ResolveGoogleCloudProjectID()
	return llm.NewVertexClient(ctx, projectID, location)
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
	schemaBytes, err := jsonMarshalFn(pageIndexResponseSchema)
	if err != nil {
		return nil, false
	}
	client, err := newPageIndexStructuredGenerator(ctx)
	if err != nil {
		return nil, false
	}

	model := strings.TrimSpace(os.Getenv("AI_MODEL_RERANK_ID"))
	if model == "" {
		model = llm.ResolveStandardModel()
	}

	text, err := client.GenerateStructured(
		ctx,
		model,
		buildPageIndexPrompt(query, papers, topK),
		"",
		string(schemaBytes),
		0.1,
		512,
	)
	if err != nil || strings.TrimSpace(text) == "" {
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
	b.WriteString("Rank papers by true relevance to the query.\n")
	b.WriteString("Consider semantic alignment, methodological fit, evidence strength, and whether the paper directly answers the query.\n")
	b.WriteString("Provide a ranked justification for the best candidates using the supplied schema.\n")
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

func clampFloat(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
