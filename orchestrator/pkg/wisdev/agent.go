// Package wisdev exposes the stable public API for embedding the WisDev agent.
package wisdev

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	internal "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
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

// WithLLMClient configures the LLM client used for query grammar correction and loop reasoning.
func WithLLMClient(client *llm.Client) Option {
	return func(a *Agent) {
		a.llmClient = client
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
		llmClient:      llm.NewClient(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}
	return a
}

// ProgressEvent is a structured stage update emitted during RunYOLO.
type ProgressEvent struct {
	Type     string
	Stage    string
	Message  string
	Payload  map[string]any
	Degraded bool
}

// YOLORequest describes a single autonomous WisDev task.
type YOLORequest struct {
	Task              string
	OriginalQuery     string
	PreparedQuery     string
	Domain            string
	ProjectID         string
	SeedQueries       []string
	MaxIterations     int
	MinIterations     int
	MaxSearchTerms    int
	HitsPerSearch     int
	MaxUniquePapers   int
	BudgetCents       int
	DisablePlanning     bool
	DisableHypotheses   bool
	DisableQueryEnhance bool
	BypassSearchCache   bool
	OnProgress          func(ProgressEvent)
}

// YOLOResult is the stable public result returned by RunYOLO.
type YOLOResult struct {
	FinalAnswer         string          `json:"finalAnswer"`
	OriginalQuery       string          `json:"originalQuery,omitempty"`
	PreparedQuery       string          `json:"preparedQuery,omitempty"`
	DetectedDomain      string          `json:"detectedDomain,omitempty"`
	RequestedIterations int             `json:"requestedIterations,omitempty"`
	Iterations          int             `json:"iterations"`
	Converged           bool            `json:"converged"`
	StopReason          string          `json:"stopReason,omitempty"`
	SynthesisMode       string          `json:"synthesisMode,omitempty"`
	PapersFound         int             `json:"papersFound"`
	Papers              []Paper         `json:"papers,omitempty"`
	ExecutedQueries     []string        `json:"executedQueries,omitempty"`
	PlannedQueries      []string        `json:"plannedQueries,omitempty"`
	BranchPlans         []BranchPlan    `json:"branchPlans,omitempty"`
	Hypotheses          []Hypothesis    `json:"hypotheses,omitempty"`
	ReasoningTrace      []ReasoningStep `json:"reasoningTrace,omitempty"`
	Grounding           *GroundingStats `json:"grounding,omitempty"`
	Beliefs             []Belief        `json:"beliefs,omitempty"`
	Gaps                *CoverageGaps   `json:"gaps,omitempty"`
}

// Belief is a minimal public view of one entry in the agent's belief ledger:
// claim, confidence, lifecycle status, and evidence tallies only — never the
// internal provenance chains.
type Belief struct {
	Claim              string  `json:"claim"`
	Confidence         float64 `json:"confidence,omitempty"`
	Status             string  `json:"status,omitempty"` // active | revised | refuted
	SupportCount       int     `json:"supportCount,omitempty"`
	ContradictionCount int     `json:"contradictionCount,omitempty"`
}

// CoverageGaps summarizes the loop's final gap analysis: planned-versus-
// executed query coverage plus open aspects flagged by the critique step.
type CoverageGaps struct {
	Sufficient               bool     `json:"sufficient"`
	Reasoning                string   `json:"reasoning,omitempty"`
	MissingAspects           []string `json:"missingAspects,omitempty"`
	PlannedQueryCount        int      `json:"plannedQueryCount,omitempty"`
	ExecutedQueryCount       int      `json:"executedQueryCount,omitempty"`
	UnexecutedPlannedQueries []string `json:"unexecutedPlannedQueries,omitempty"`
	QueriesWithoutCoverage   []string `json:"queriesWithoutCoverage,omitempty"`
}

// GroundingStats summarizes per-claim evidence coverage of the synthesized
// answer. It is a minimal aggregate derived from the internal structured
// answer — counts only, never the full claim/evidence structure.
type GroundingStats struct {
	GroundedClaims    int                `json:"groundedClaims"`
	TotalClaims       int                `json:"totalClaims"`
	UnsupportedClaims int                `json:"unsupportedClaims"`
	CitedSources      int                `json:"citedSources"`
	Sections          []SectionGrounding `json:"sections,omitempty"`
}

// SectionGrounding carries grounded/total claim counts for one answer section.
type SectionGrounding struct {
	Heading        string `json:"heading"`
	GroundedClaims int    `json:"groundedClaims"`
	TotalClaims    int    `json:"totalClaims"`
}

// ReasoningStep is one chronological entry of the agent's ReAct reasoning
// trace (plan → act → observe → reflect → replan → synthesis).
type ReasoningStep struct {
	Timestamp    int64    `json:"timestamp,omitempty"` // unix milliseconds
	Phase        string   `json:"phase,omitempty"`     // e.g. "planning", "retrieval", "evaluation", "replan", "synthesis"
	Decision     string   `json:"decision,omitempty"`  // e.g. "react_action_retrieve"
	Reasoning    string   `json:"reasoning,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
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

	maxIterations := defaultInt(req.MaxIterations, defaultMaxIterations)
	maxSearchTerms := req.MaxSearchTerms
	if maxSearchTerms <= 0 {
		maxSearchTerms = maxInt(maxIterations, defaultMaxSearchTerms)
	}
	originalQuery := strings.TrimSpace(req.OriginalQuery)
	if originalQuery == "" {
		originalQuery = task
	}
	preparedQuery := strings.TrimSpace(req.PreparedQuery)
	domain := strings.TrimSpace(req.Domain)
	seedQueries := append([]string(nil), req.SeedQueries...)

	prep := internal.EarlyPrepareResearchQuery(ctx, originalQuery, a.llmClient, req.DisableQueryEnhance)
	if corrected := strings.TrimSpace(prep.Corrected); corrected != "" {
		preparedQuery = corrected
	} else if preparedQuery == "" {
		preparedQuery = task
	}
	if search := strings.TrimSpace(prep.SearchQuery); search != "" {
		task = search
	}
	if domain == "" {
		domain = prep.Domain
	}
	if len(prep.SeedQueries) > 0 {
		seedQueries = append([]string(nil), prep.SeedQueries...)
	}
	if domain == "" {
		domain = internal.InferResearchDomain(task)
	}

	var onEvent func(internal.PlanExecutionEvent)
	if req.OnProgress != nil {
		onEvent = func(event internal.PlanExecutionEvent) {
			stage := ""
			degraded := false
			if event.Payload != nil {
				stage = strings.TrimSpace(internal.AsOptionalString(event.Payload["stage"]))
				if v, ok := event.Payload["degraded"].(bool); ok && v {
					degraded = true
				}
				if strings.TrimSpace(internal.AsOptionalString(event.Payload["fallback"])) != "" {
					degraded = true
				}
			}
			req.OnProgress(ProgressEvent{
				Type:     string(event.Type),
				Stage:    stage,
				Message:  strings.TrimSpace(event.Message),
				Payload:  event.Payload,
				Degraded: degraded,
			})
		}
	}
	if onEvent != nil && prep.Changed && preparedQuery != "" && preparedQuery != originalQuery {
		onEvent(internal.PlanExecutionEvent{
			Type:    internal.EventProgress,
			Message: fmt.Sprintf("Query corrected: %s → %s", internal.QueryPreview(originalQuery), internal.QueryPreview(preparedQuery)),
			Payload: map[string]any{
				"component":       "wisdev.autonomous",
				"operation":       "query_prepare",
				"stage":           "query_prepared",
				"original_query":  originalQuery,
				"corrected_query": preparedQuery,
			},
			CreatedAt: internal.NowMillis(),
		})
	}

	loop := internal.NewAutonomousLoop(registry, a.llmClient)
	result, err := loop.Run(ctx, internal.LoopRequest{
		Query:                       task,
		OriginalQuery:               originalQuery,
		DisableQueryEnhance:         req.DisableQueryEnhance,
		SeedQueries:                 seedQueries,
		Domain:                      domain,
		ProjectID:                   strings.TrimSpace(req.ProjectID),
		MaxIterations:               maxIterations,
		MinIterations:               maxInt(0, req.MinIterations),
		MaxSearchTerms:              maxSearchTerms,
		HitsPerSearch:               defaultInt(req.HitsPerSearch, defaultHitsPerSearch),
		MaxUniquePapers:             defaultInt(req.MaxUniquePapers, defaultMaxUniquePapers),
		BudgetCents:                 req.BudgetCents,
		Mode:                        string(internal.WisDevModeYOLO),
		DisableProgrammaticPlanning: req.DisablePlanning,
		DisableHypothesisGeneration: req.DisableHypotheses,
		BypassSearchCache:           req.BypassSearchCache,
	}, onEvent)
	if err != nil {
		return nil, err
	}

	papers := PreferCitedPapers(SortPapersByCitations(fromInternalPapers(result.Papers)))
	return &YOLOResult{
		FinalAnswer:         result.FinalAnswer,
		OriginalQuery:       originalQuery,
		PreparedQuery:       preparedQuery,
		DetectedDomain:      domain,
		RequestedIterations: maxIterations,
		Iterations:          result.Iterations,
		Converged:           result.Converged,
		StopReason:          result.StopReason,
		SynthesisMode:       strings.TrimSpace(result.SynthesisMode),
		PapersFound:         len(papers),
		Papers:              papers,
		ExecutedQueries:     append([]string(nil), result.ExecutedQueries...),
		PlannedQueries:      plannedQueriesFromInternalBranchPlans(result.BranchPlans),
		BranchPlans:         fromInternalBranchPlans(result.BranchPlans),
		Hypotheses:          fromInternalHypotheses(result.Hypotheses),
		ReasoningTrace:      fromInternalReasoningTrace(result.ReasoningTrace),
		Grounding:           groundingStatsFromStructuredAnswer(result.StructuredAnswer),
		Beliefs:             fromInternalBeliefState(result.BeliefState),
		Gaps:                fromInternalGapState(result.GapAnalysis),
	}, nil
}

// fromInternalBeliefState reduces the internal belief ledger to the minimal
// public belief view, ordered by confidence (highest first) for determinism.
func fromInternalBeliefState(bs *internal.BeliefState) []Belief {
	if bs == nil || len(bs.Beliefs) == 0 {
		return nil
	}
	out := make([]Belief, 0, len(bs.Beliefs))
	for _, belief := range bs.Beliefs {
		if belief == nil {
			continue
		}
		claim := strings.TrimSpace(belief.Claim)
		if claim == "" {
			continue
		}
		out = append(out, Belief{
			Claim:              claim,
			Confidence:         belief.Confidence,
			Status:             strings.TrimSpace(string(belief.Status)),
			SupportCount:       len(belief.SupportingEvidence),
			ContradictionCount: len(belief.ContradictingEvidence),
		})
	}
	if len(out) == 0 {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Claim < out[j].Claim
	})
	return out
}

// fromInternalGapState reduces the internal gap analysis to coverage counts
// and open-gap strings — never the full coverage ledger.
func fromInternalGapState(gap *internal.LoopGapState) *CoverageGaps {
	if gap == nil {
		return nil
	}
	return &CoverageGaps{
		Sufficient:               gap.Sufficient,
		Reasoning:                strings.TrimSpace(gap.Reasoning),
		MissingAspects:           compactTrimmedStrings(gap.MissingAspects),
		PlannedQueryCount:        gap.Coverage.PlannedQueryCount,
		ExecutedQueryCount:       gap.Coverage.ExecutedQueryCount,
		UnexecutedPlannedQueries: compactTrimmedStrings(gap.Coverage.UnexecutedPlannedQueries),
		QueriesWithoutCoverage:   compactTrimmedStrings(gap.Coverage.QueriesWithoutCoverage),
	}
}

func compactTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// groundingStatsFromStructuredAnswer reduces the internal structured answer to
// claim-level grounding counts for the public result.
func groundingStatsFromStructuredAnswer(answer *rag.StructuredAnswer) *GroundingStats {
	if answer == nil || len(answer.Sections) == 0 {
		return nil
	}
	stats := &GroundingStats{}
	sources := make(map[string]struct{})
	for _, section := range answer.Sections {
		sectionStats := SectionGrounding{Heading: strings.TrimSpace(section.Heading)}
		for _, claim := range section.Sentences {
			if strings.TrimSpace(claim.Text) == "" {
				continue
			}
			sectionStats.TotalClaims++
			grounded := false
			for _, id := range claim.EvidenceIDs {
				id = strings.TrimSpace(id)
				if id == "" {
					continue
				}
				grounded = true
				sources[strings.ToLower(id)] = struct{}{}
			}
			if grounded {
				sectionStats.GroundedClaims++
			}
			if claim.Unsupported {
				stats.UnsupportedClaims++
			}
		}
		if sectionStats.TotalClaims == 0 {
			continue
		}
		stats.TotalClaims += sectionStats.TotalClaims
		stats.GroundedClaims += sectionStats.GroundedClaims
		stats.Sections = append(stats.Sections, sectionStats)
	}
	if stats.TotalClaims == 0 {
		return nil
	}
	stats.CitedSources = len(sources)
	return stats
}

func fromInternalReasoningTrace(trace []internal.ReasoningTraceEntry) []ReasoningStep {
	if len(trace) == 0 {
		return nil
	}
	out := make([]ReasoningStep, 0, len(trace))
	for _, entry := range trace {
		out = append(out, ReasoningStep{
			Timestamp:    entry.Timestamp,
			Phase:        strings.TrimSpace(entry.Phase),
			Decision:     strings.TrimSpace(entry.Decision),
			Reasoning:    strings.TrimSpace(entry.Reasoning),
			Alternatives: append([]string(nil), entry.Alternatives...),
		})
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
