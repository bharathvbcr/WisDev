// Package wisdev exposes the stable public API for embedding the WisDev agent.
package wisdev

import (
	"context"
	"errors"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	internal "github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

const (
	defaultMaxIterations   = 3
	defaultMaxSearchTerms  = 6
	defaultHitsPerSearch   = 5
	defaultMaxUniquePapers = 20
)

// Agent runs WisDev tasks without requiring callers to import internal packages.
type Agent struct {
	searchRegistry *search.ProviderRegistry
	llmClient      *llm.Client
}

// Option configures a local Agent.
type Option func(*Agent)

// WithNoSearchProviders disables network-backed search. It is useful for dry
// runs, tests, and offline CLI smoke checks.
func WithNoSearchProviders() Option {
	return func(a *Agent) {
		a.searchRegistry = search.NewProviderRegistry()
	}
}

// WithProviderNames limits the default registry to specific provider names.
func WithProviderNames(names ...string) Option {
	return func(a *Agent) {
		a.searchRegistry = search.BuildRegistry(names...)
	}
}

// WithSearchProviders replaces the default registry with caller-supplied
// providers. This is the preferred open-source integration point for custom
// retrieval without importing internal packages.
func WithSearchProviders(providers ...SearchProvider) Option {
	return func(a *Agent) {
		registry := search.NewProviderRegistry()
		for _, provider := range providers {
			if provider != nil {
				registry.Register(searchProviderAdapter{provider: provider})
			}
		}
		a.searchRegistry = registry
	}
}

// NewAgent creates a local WisDev agent using the open-source defaults.
func NewAgent(opts ...Option) *Agent {
	a := &Agent{
		searchRegistry: search.BuildRegistry(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}
	return a
}

// YOLORequest describes a single autonomous WisDev task.
type YOLORequest struct {
	Task              string
	Domain            string
	ProjectID         string
	MaxIterations     int
	MaxSearchTerms    int
	HitsPerSearch     int
	MaxUniquePapers   int
	BudgetCents       int
	DisablePlanning   bool
	DisableHypotheses bool
}

// YOLOResult is the stable public result returned by RunYOLO.
type YOLOResult struct {
	FinalAnswer     string       `json:"finalAnswer"`
	Iterations      int          `json:"iterations"`
	Converged       bool         `json:"converged"`
	StopReason      string       `json:"stopReason,omitempty"`
	PapersFound     int          `json:"papersFound"`
	Papers          []Paper      `json:"papers,omitempty"`
	ExecutedQueries []string     `json:"executedQueries,omitempty"`
	PlannedQueries  []string     `json:"plannedQueries,omitempty"`
	BranchPlans     []BranchPlan `json:"branchPlans,omitempty"`
	Hypotheses      []Hypothesis `json:"hypotheses,omitempty"`
}

// BranchPlan describes a decomposed research branch exposed by the public API.
type BranchPlan struct {
	ID                      string   `json:"id"`
	Query                   string   `json:"query"`
	Hypothesis              string   `json:"hypothesis,omitempty"`
	RetrievalPlan           []string `json:"retrievalPlan,omitempty"`
	ReasoningStrategy       string   `json:"reasoningStrategy,omitempty"`
	FalsifiabilityCondition string   `json:"falsifiabilityCondition,omitempty"`
	ClosureCondition        string   `json:"closureCondition,omitempty"`
	Depth                   int      `json:"depth,omitempty"`
	SearchWeight            float64  `json:"searchWeight,omitempty"`
	Status                  string   `json:"status,omitempty"`
	StopReason              string   `json:"stopReason,omitempty"`
}

// Hypothesis describes a generated claim candidate and its verification state.
type Hypothesis struct {
	ID                      string  `json:"id"`
	Query                   string  `json:"query,omitempty"`
	Claim                   string  `json:"claim"`
	FalsifiabilityCondition string  `json:"falsifiabilityCondition,omitempty"`
	ConfidenceScore         float64 `json:"confidenceScore,omitempty"`
	Status                  string  `json:"status,omitempty"`
	EvidenceCount           int     `json:"evidenceCount,omitempty"`
}

// RunYOLO executes the current WisDev autonomous loop through the public API.
func (a *Agent) RunYOLO(ctx context.Context, req YOLORequest) (*YOLOResult, error) {
	task := strings.TrimSpace(req.Task)
	if task == "" {
		return nil, errors.New("wisdev yolo: task is required")
	}
	if a == nil {
		a = NewAgent()
	}
	registry := a.searchRegistry
	if registry == nil {
		registry = search.BuildRegistry()
	}

	loop := internal.NewAutonomousLoop(registry, a.llmClient)
	result, err := loop.Run(ctx, internal.LoopRequest{
		Query:                       task,
		Domain:                      strings.TrimSpace(req.Domain),
		ProjectID:                   strings.TrimSpace(req.ProjectID),
		MaxIterations:               defaultInt(req.MaxIterations, defaultMaxIterations),
		MaxSearchTerms:              defaultInt(req.MaxSearchTerms, defaultMaxSearchTerms),
		HitsPerSearch:               defaultInt(req.HitsPerSearch, defaultHitsPerSearch),
		MaxUniquePapers:             defaultInt(req.MaxUniquePapers, defaultMaxUniquePapers),
		BudgetCents:                 req.BudgetCents,
		Mode:                        string(internal.WisDevModeYOLO),
		DisableProgrammaticPlanning: req.DisablePlanning,
		DisableHypothesisGeneration: req.DisableHypotheses,
	})
	if err != nil {
		return nil, err
	}

	return &YOLOResult{
		FinalAnswer:     result.FinalAnswer,
		Iterations:      result.Iterations,
		Converged:       result.Converged,
		StopReason:      result.StopReason,
		PapersFound:     len(result.Papers),
		Papers:          fromInternalPapers(result.Papers),
		ExecutedQueries: append([]string(nil), result.ExecutedQueries...),
		PlannedQueries:  plannedQueriesFromInternalBranchPlans(result.BranchPlans),
		BranchPlans:     fromInternalBranchPlans(result.BranchPlans),
		Hypotheses:      fromInternalHypotheses(result.Hypotheses),
	}, nil
}

func defaultInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func plannedQueriesFromInternalBranchPlans(plans []internal.ResearchBranchPlan) []string {
	if len(plans) == 0 {
		return nil
	}
	queries := make([]string, 0, len(plans))
	seen := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		query := strings.TrimSpace(plan.Query)
		if query == "" {
			continue
		}
		key := strings.ToLower(query)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		queries = append(queries, query)
	}
	return queries
}

func fromInternalBranchPlans(plans []internal.ResearchBranchPlan) []BranchPlan {
	if len(plans) == 0 {
		return nil
	}
	out := make([]BranchPlan, 0, len(plans))
	for _, plan := range plans {
		out = append(out, BranchPlan{
			ID:                      plan.ID,
			Query:                   plan.Query,
			Hypothesis:              plan.Hypothesis,
			RetrievalPlan:           append([]string(nil), plan.RetrievalPlan...),
			ReasoningStrategy:       plan.ReasoningStrategy,
			FalsifiabilityCondition: plan.FalsifiabilityCondition,
			ClosureCondition:        plan.ClosureCondition,
			Depth:                   plan.Depth,
			SearchWeight:            plan.SearchWeight,
			Status:                  plan.Status,
			StopReason:              plan.StopReason,
		})
	}
	return out
}

func fromInternalHypotheses(hypotheses []internal.Hypothesis) []Hypothesis {
	if len(hypotheses) == 0 {
		return nil
	}
	out := make([]Hypothesis, 0, len(hypotheses))
	for _, hypothesis := range hypotheses {
		claim := strings.TrimSpace(hypothesis.Claim)
		if claim == "" {
			claim = strings.TrimSpace(hypothesis.Text)
		}
		if claim == "" {
			claim = strings.TrimSpace(hypothesis.Query)
		}
		if claim == "" {
			continue
		}
		out = append(out, Hypothesis{
			ID:                      strings.TrimSpace(hypothesis.ID),
			Query:                   strings.TrimSpace(hypothesis.Query),
			Claim:                   claim,
			FalsifiabilityCondition: strings.TrimSpace(hypothesis.FalsifiabilityCondition),
			ConfidenceScore:         hypothesis.ConfidenceScore,
			Status:                  strings.TrimSpace(hypothesis.Status),
			EvidenceCount:           hypothesis.EvidenceCount,
		})
	}
	return out
}
