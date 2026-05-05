package wisdev

import (
	"context"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"log/slog"
)

// Activity Inputs/Outputs

type ResearchSearchInput struct {
	SessionID   string            `json:"sessionId"`
	Queries     []string          `json:"queries"`
	SearchOpts  search.SearchOpts `json:"searchOpts"`
	Parallelism int               `json:"parallelism"`
}

type ResearchSearchOutput struct {
	Papers        []search.Paper            `json:"papers"`
	QueryCoverage map[string][]search.Paper `json:"queryCoverage"`
}

type SufficiencyInput struct {
	SessionID     string         `json:"sessionId"`
	OriginalQuery string         `json:"originalQuery"`
	Papers        []search.Paper `json:"papers"`
}

type SufficiencyOutput struct {
	Analysis *sufficiencyAnalysis `json:"analysis"`
}

type ReasoningRefreshInput struct {
	SessionID     string                    `json:"sessionId"`
	Request       LoopRequest               `json:"request"`
	Papers        []search.Paper            `json:"papers"`
	QueryCoverage map[string][]search.Paper `json:"queryCoverage"`
	Gap           *LoopGapState             `json:"gap"`
	QueryID       string                    `json:"queryId"`
}

type ReasoningRefreshOutput struct {
	Findings   []EvidenceFinding `json:"findings"`
	Hypotheses []Hypothesis      `json:"hypotheses"`
}

// Activity Implementations

func (a *temporalActivities) ResearchSearchActivity(ctx context.Context, input ResearchSearchInput) (*ResearchSearchOutput, error) {
	slog.Info("Temporal Activity: ResearchSearch", "sessionID", input.SessionID, "queryCount", len(input.Queries))

	loop := NewAutonomousLoop(a.gateway.SearchRegistry, a.gateway.LLMClient)
	batchResults := loop.executeLoopSearchBatch(ctx, input.Queries, input.SearchOpts, input.Parallelism)

	papers := make([]search.Paper, 0)
	queryCoverage := make(map[string][]search.Paper)

	for _, res := range batchResults {
		papers = append(papers, res.Result.Papers...)
		queryCoverage[res.Query] = res.Result.Papers
	}

	return &ResearchSearchOutput{
		Papers:        papers,
		QueryCoverage: queryCoverage,
	}, nil
}

func (a *temporalActivities) SufficiencyActivity(ctx context.Context, input SufficiencyInput) (*SufficiencyOutput, error) {
	slog.Info("Temporal Activity: Sufficiency", "sessionID", input.SessionID, "paperCount", len(input.Papers))

	loop := NewAutonomousLoop(a.gateway.SearchRegistry, a.gateway.LLMClient)
	analysis, err := loop.evaluateSufficiency(ctx, input.OriginalQuery, input.Papers)
	if err != nil {
		return nil, err
	}

	return &SufficiencyOutput{
		Analysis: analysis,
	}, nil
}

func (a *temporalActivities) ReasoningRefreshActivity(ctx context.Context, input ReasoningRefreshInput) (*ReasoningRefreshOutput, error) {
	slog.Info("Temporal Activity: ReasoningRefresh", "sessionID", input.SessionID)

	loop := NewAutonomousLoop(a.gateway.SearchRegistry, a.gateway.LLMClient)
	findings, hypotheses := loop.refreshLoopReasoning(ctx, input.Request, input.Papers, input.QueryCoverage, input.Gap, input.QueryID)

	return &ReasoningRefreshOutput{
		Findings:   findings,
		Hypotheses: hypotheses,
	}, nil
}

func (a *temporalActivities) SynthesisActivity(ctx context.Context, input SufficiencyInput) (string, error) {
	slog.Info("Temporal Activity: Synthesis", "sessionID", input.SessionID)

	loop := NewAutonomousLoop(a.gateway.SearchRegistry, a.gateway.LLMClient)
	evidence, _ := loop.assembleDossier(ctx, input.OriginalQuery, input.Papers)

	ans, err := loop.synthesizeWithEvidence(ctx, input.OriginalQuery, input.Papers, evidence)
	if err != nil {
		return "", err
	}
	return ans.PlainText, nil
}
