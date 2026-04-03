package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
	"sort"
	"strings"
)

func RerankPlanCandidatesWithVerifier(
	ctx context.Context,
	llmClient *llm.Client,
	session *AgentSession,
	query string,
	candidates []PlanCandidate,
) []PlanCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	reranked := make([]PlanCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		verifierScore := 0.0
		if llmClient != nil && candidate.Plan != nil {
			prompt := fmt.Sprintf(`Evaluate the following research plan for the query: "%s"
Plan Steps: %v

Return ONLY a JSON object: {"score": 0.0-1.0, "reasoning": "..."}`, query, candidate.Plan.Steps)

			resp, err := llmClient.Generate(ctx, &llmv1.GenerateRequest{
				Prompt: prompt,
				Model:  llm.ResolveHeavyModel(),
			})
			if err == nil {
				var result struct {
					Score float64 `json:"score"`
				}
				// Robust parsing: find first '{' and last '}'
				clean := resp.Text
				if start := strings.Index(clean, "{"); start != -1 {
					if end := strings.LastIndex(clean, "}"); end != -1 && end > start {
						clean = clean[start : end+1]
					}
				}
				if err := json.Unmarshal([]byte(clean), &result); err == nil {
					verifierScore = result.Score
				} else {
					verifierScore = 0.5 // Default on parse error
				}
			}
		}
		updated := candidate
		updated.Score = ClampFloat((candidate.Score*0.75)+(verifierScore*0.25), 0, 0.99)
		updated.Rationale = candidate.Rationale + " [verifier_reranked]"
		reranked = append(reranked, updated)
	}
	sort.SliceStable(reranked, func(i, j int) bool {
		if reranked[i].Score == reranked[j].Score {
			return reranked[i].Hypothesis < reranked[j].Hypothesis
		}
		return reranked[i].Score > reranked[j].Score
	})
	return reranked
}
