package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
)

// AutonomousLoop handles the budgeted research iteration.
type AutonomousLoop struct {
	searchReg *search.ProviderRegistry
	llmClient *llm.Client
}

func NewAutonomousLoop(reg *search.ProviderRegistry, llm *llm.Client) *AutonomousLoop {
	return &AutonomousLoop{
		searchReg: reg,
		llmClient: llm,
	}
}

type LoopRequest struct {
	Query         string `json:"query"`
	Domain        string `json:"domain"`
	ProjectID     string `json:"projectId"`
	MaxIterations int    `json:"maxIterations"`
	BudgetCents   int    `json:"budgetCents"`
}

type LoopResult struct {
	FinalAnswer string         `json:"finalAnswer"`
	Papers      []search.Paper `json:"papers"`
	Evidence    []EvidenceItem `json:"evidence"`
	Iterations  int            `json:"iterations"`
	Converged   bool           `json:"converged"`
}

type EvidenceItem struct {
	Claim      string  `json:"claim"`
	Snippet    string  `json:"snippet"`
	PaperTitle string  `json:"paperTitle"`
	PaperID    string  `json:"paperId"`
	Confidence float64 `json:"confidence"`
}

func (l *AutonomousLoop) Run(ctx context.Context, req LoopRequest) (*LoopResult, error) {
	slog.Info("Starting autonomous research loop", "query", req.Query, "maxIterations", req.MaxIterations)

	papers := make([]search.Paper, 0)
	iterations := 0
	converged := false
	currentQuery := req.Query

	for i := 0; i < req.MaxIterations; i++ {
		iterations++
		slog.Info("Loop iteration", "index", i+1, "query", currentQuery)

		// 1. Retrieval (Using Light workers for parallel search)
		searchOpts := search.SearchOpts{
			Limit:            10,
			Domain:           req.Domain,
			QualitySort:      true,
			DynamicProviders: false,
			SkipCache:        true,
			LLMClient:        l.llmClient,
		}
		res := search.ParallelSearch(ctx, l.searchReg, currentQuery, searchOpts)
		slog.Debug("ParallelSearch results", "count", len(res.Papers), "query", currentQuery)

		// Dedup and add new papers
		newCount := 0
		for _, p := range res.Papers {
			isNew := true
			for _, existing := range papers {
				if p.ID == existing.ID || (p.DOI != "" && p.DOI == existing.DOI) {
					isNew = false
					break
				}
			}
			if isNew {
				papers = append(papers, p)
				newCount++
			}
		}
		slog.Debug("After dedup", "total", len(papers), "newCount", newCount)

		// 2. Verification & Convergence Check (standard model)
		analysis, err := l.evaluateSufficiency(ctx, req.Query, papers)
		if err == nil {
			if analysis.Sufficient || i == req.MaxIterations-1 {
				converged = analysis.Sufficient
				break
			}
			// 3. Refine query based on gaps
			if analysis.NextQuery != "" {
				currentQuery = analysis.NextQuery
			}
		} else {
			slog.Warn("Sufficiency evaluation failed", "error", err)
			if len(papers) >= 20 {
				converged = true
				break
			}
		}
	}

	// 4. Evidence Assembly (standard model)
	evidence, _ := l.assembleDossier(ctx, req.Query, papers)

	// 5. Final Synthesis (Using Heavy Brain)
	finalAnswer, err := l.synthesizeWithEvidence(ctx, req.Query, papers, evidence)
	if err != nil {
		return nil, err
	}

	return &LoopResult{
		FinalAnswer: finalAnswer,
		Papers:      papers,
		Evidence:    evidence,
		Iterations:  iterations,
		Converged:   converged,
	}, nil
}

func (l *AutonomousLoop) assembleDossier(ctx context.Context, query string, papers []search.Paper) ([]EvidenceItem, error) {
	if len(papers) == 0 {
		return nil, nil
	}

	// For efficiency, we only extract evidence from the top 5 most relevant papers
	topPapers := papers
	if len(topPapers) > 5 {
		topPapers = topPapers[:5]
	}

	evidence := make([]EvidenceItem, 0)
	provider := llm.NewModelProvider(l.llmClient)

	for _, p := range topPapers {
		prompt := fmt.Sprintf(`Extract the top 2-3 most important factual claims from the following paper that directly address the research query.
Query: %s
Paper Title: %s
Abstract: %s

Return ONLY a JSON array of objects:
[{"claim": "...", "snippet": "...", "confidence": 0.0-1.0}]
`, query, p.Title, p.Abstract)

		// Using Standard model for extraction
		resp, err := provider.Call(ctx, "standard", prompt)
		if err != nil {
			continue
		}

		var items []struct {
			Claim      string  `json:"claim"`
			Snippet    string  `json:"snippet"`
			Confidence float64 `json:"confidence"`
		}
		// Try to parse the JSON response
		if err := json.Unmarshal([]byte(resp), &items); err == nil {
			for _, item := range items {
				evidence = append(evidence, EvidenceItem{
					Claim:      item.Claim,
					Snippet:    item.Snippet,
					PaperTitle: p.Title,
					PaperID:    p.ID,
					Confidence: item.Confidence,
				})
			}
		}
	}
	return evidence, nil
}

func (l *AutonomousLoop) evaluateSufficiency(ctx context.Context, originalQuery string, papers []search.Paper) (*sufficiencyAnalysis, error) {
	if len(papers) == 0 {
		return &sufficiencyAnalysis{Sufficient: false, NextQuery: originalQuery}, nil
	}

	titles := make([]string, 0, len(papers))
	for _, p := range papers {
		titles = append(titles, p.Title)
	}

	provider := llm.NewModelProvider(l.llmClient)
	prompt := fmt.Sprintf(`Evaluate if the following papers provide enough evidence to fully answer the research query.
Query: %s
Papers Found: %v

Return ONLY a JSON object with:
- sufficient (boolean)
- reasoning (string explaining the decision)
- nextQuery (string suggesting a refined search query if not sufficient)
`, originalQuery, titles)

	resp, err := provider.Call(ctx, "standard", prompt)
	if err != nil {
		return nil, err
	}

	var analysis sufficiencyAnalysis
	if err := json.Unmarshal([]byte(resp), &analysis); err != nil {
		return nil, err
	}
	return &analysis, nil
}

type sufficiencyAnalysis struct {
	Sufficient bool   `json:"sufficient"`
	Reasoning  string `json:"reasoning"`
	NextQuery  string `json:"nextQuery"`
}

func (l *AutonomousLoop) synthesizeWithEvidence(ctx context.Context, query string, papers []search.Paper, evidence []EvidenceItem) (string, error) {
	evidenceText := ""
	for i, e := range evidence {
		evidenceText += fmt.Sprintf("%d. [%s] %s (Evidence: %s)\n", i+1, e.PaperTitle, e.Claim, e.Snippet)
	}

	prompt := fmt.Sprintf(`Synthesize a comprehensive research report for the query: "%s"
Based on %d papers found.

Primary Evidence:
%s

Instructions:
1. Write a 3-4 paragraph technical report.
2. Use the provided evidence to ground your claims.
3. Cite the papers using [Title] format.
4. If there are contradictions in the evidence, highlight them.
`, query, len(papers), evidenceText)

	provider := llm.NewModelProvider(l.llmClient)
	resp, err := provider.Call(ctx, "heavy", prompt)
	if err != nil {
		return "", err
	}
	return resp, nil
}
