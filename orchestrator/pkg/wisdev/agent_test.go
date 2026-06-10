package wisdev

import (
	"context"
	"strings"
	"testing"
	"time"
)

type stubSearchProvider struct {
	queries []string
	opts    []SearchOptions
}

func (p *stubSearchProvider) Name() string {
	return "stub"
}

func (p *stubSearchProvider) Search(_ context.Context, query string, opts SearchOptions) ([]Paper, error) {
	p.queries = append(p.queries, query)
	p.opts = append(p.opts, opts)
	return []Paper{{
		ID:     "stub-1",
		Title:  "Stub evidence for " + query,
		Source: "stub",
		Year:   2026,
	}}, nil
}

func (p *stubSearchProvider) Domains() []string {
	return nil
}

func TestRunYOLOEnhancesQueryByDefault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	llmClient, _ := allowGrammarCorrectionClient(t, "meniscus scaffolds and ACL anterior cruciate ligament reconstruction strategies")
	result, err := NewAgent(WithNoSearchProviders(), WithLLMClient(llmClient)).RunYOLO(ctx, YOLORequest{
		Task:              "meniscus scaffolds and acl re constricution stratigies",
		MaxIterations:     1,
		MaxSearchTerms:    1,
		HitsPerSearch:     1,
		DisablePlanning:   true,
		DisableHypotheses: true,
	})
	if err != nil {
		t.Fatalf("RunYOLO returned error: %v", err)
	}
	if result.PreparedQuery == result.OriginalQuery {
		t.Fatalf("expected prepared query to differ from original, got %q", result.PreparedQuery)
	}
	for _, want := range []string{"meniscus", "scaffolds", "strategies"} {
		if !strings.Contains(strings.ToLower(result.PreparedQuery), want) {
			t.Fatalf("expected prepared query to contain %q: %s", want, result.PreparedQuery)
		}
	}
	if strings.Contains(result.PreparedQuery, "constricution") || strings.Contains(result.PreparedQuery, "stratigies") {
		t.Fatalf("expected typos corrected in prepared query: %s", result.PreparedQuery)
	}
}

func TestRunYOLORequiresTask(t *testing.T) {
	_, err := NewAgent(WithNoSearchProviders()).RunYOLO(context.Background(), YOLORequest{})
	if err == nil {
		t.Fatal("expected missing task error")
	}
	if !strings.Contains(err.Error(), "task is required") {
		t.Fatalf("expected task validation error, got %v", err)
	}
}

func TestRunYOLOOfflineNoProviders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	result, err := NewAgent(WithNoSearchProviders(), WithLLMClient(nil)).RunYOLO(ctx, YOLORequest{
		Task:              "map evidence for open source research agents",
		MaxIterations:     1,
		MaxSearchTerms:    1,
		HitsPerSearch:     1,
		MaxUniquePapers:   2,
		DisablePlanning:   true,
		DisableHypotheses: true,
		DisableQueryEnhance: true,
	})
	if err != nil {
		t.Fatalf("RunYOLO returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
	if result.Iterations != 1 {
		t.Fatalf("expected one iteration, got %d", result.Iterations)
	}
	if result.PapersFound != 0 {
		t.Fatalf("expected offline run to avoid provider results, got %d papers", result.PapersFound)
	}
	if len(result.ReasoningTrace) == 0 {
		t.Fatal("expected public result to expose the reasoning trace")
	}
}

func TestRunYOLOAdmitsMeniciusTypoResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	llmClient, _ := allowGrammarCorrectionClient(t, "meniscus reconstruction strategies")
	provider := &stubSearchProvider{}
	result, err := NewAgent(WithSearchProviders(provider), WithLLMClient(llmClient)).RunYOLO(ctx, YOLORequest{
		Task:              "Menicius reconstruction stratiges",
		MaxIterations:     1,
		MaxSearchTerms:    1,
		HitsPerSearch:     3,
		DisablePlanning:   true,
		DisableHypotheses: true,
	})
	if err != nil {
		t.Fatalf("RunYOLO returned error: %v", err)
	}
	if result.PapersFound < 1 {
		t.Fatalf("expected typo query to admit provider paper, got %d papers", result.PapersFound)
	}
	if !strings.Contains(strings.ToLower(result.PreparedQuery), "meniscus") {
		t.Fatalf("expected prepared query to correct menicius typo: %q", result.PreparedQuery)
	}
}

func TestRunYOLOUsesPublicSearchProvider(t *testing.T) {
	// Hermetic: never reach a real sidecar that may be running locally.
	t.Setenv("PYTHON_SIDECAR_GRPC_ADDR", "127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	provider := &stubSearchProvider{}
	result, err := NewAgent(WithSearchProviders(provider)).RunYOLO(ctx, YOLORequest{
		Task:              "map evidence for open source research agents",
		Domain:            "cs",
		MaxIterations:     1,
		MaxSearchTerms:    1,
		HitsPerSearch:     3,
		MaxUniquePapers:   3,
		DisablePlanning:   true,
		DisableHypotheses: true,
	})
	if err != nil {
		t.Fatalf("RunYOLO returned error: %v", err)
	}
	if len(provider.queries) == 0 {
		t.Fatal("expected custom provider to be called")
	}
	if len(provider.opts) == 0 || provider.opts[0].Domain != "cs" || provider.opts[0].Limit != 3 {
		t.Fatalf("expected public search options to be forwarded, got %#v", provider.opts)
	}
	if result.PapersFound != 1 {
		t.Fatalf("expected one paper from custom provider, got %d", result.PapersFound)
	}
	if len(result.Papers) != 1 || result.Papers[0].Title == "" || result.Papers[0].Source != "stub" {
		t.Fatalf("expected public paper in result, got %#v", result.Papers)
	}
}

func TestRunYOLOExposesResearchAgentAgenda(t *testing.T) {
	// Hermetic: never reach a real sidecar that may be running locally.
	t.Setenv("PYTHON_SIDECAR_GRPC_ADDR", "127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	provider := &stubSearchProvider{}
	// YOLO mode seeds pre-retrieval hypothesis branches (evidence, falsification,
	// and contradiction probes) plus belief-feedback queries ahead of the topic
	// agenda, and they consume search-term budget first. The budget below leaves
	// enough headroom for both the hypothesis probes and the topic-focused
	// agenda queries to execute.
	result, err := NewAgent(WithSearchProviders(provider)).RunYOLO(ctx, YOLORequest{
		Task:            "map evidence for open source research agents",
		Domain:          "cs",
		MaxIterations:   8,
		MaxSearchTerms:  24,
		HitsPerSearch:   1,
		MaxUniquePapers: 8,
	})
	if err != nil {
		t.Fatalf("RunYOLO returned error: %v", err)
	}
	if !containsFragment(result.PlannedQueries, "agents source") {
		t.Fatalf("expected topic-focused query decomposition, got %#v", result.PlannedQueries)
	}
	if !containsFragment(result.PlannedQueries, "systematic review meta analysis") {
		t.Fatalf("expected research agenda planning, got %#v", result.PlannedQueries)
	}
	if !containsFragment(result.PlannedQueries, "independent replication") {
		t.Fatalf("expected replication branch planning, got %#v", result.PlannedQueries)
	}
	if len(result.BranchPlans) == 0 {
		t.Fatalf("expected public branch plans")
	}
	hasHypothesisBranch := false
	for _, plan := range result.BranchPlans {
		if plan.ReasoningStrategy == "pre_retrieval_hypothesis_test" {
			hasHypothesisBranch = true
			break
		}
	}
	if !hasHypothesisBranch {
		t.Fatalf("expected pre-retrieval hypothesis branch plans, got %#v", result.BranchPlans)
	}
	if len(result.Hypotheses) == 0 {
		t.Fatalf("expected public hypotheses")
	}
	if !containsFragment(provider.queries, "agents source") {
		t.Fatalf("expected provider to execute topic-focused query, got %#v", provider.queries)
	}
	if !containsFragment(provider.queries, "falsification check") {
		t.Fatalf("expected provider to execute belief-feedback falsification query, got %#v", provider.queries)
	}
}

func containsFragment(values []string, fragment string) bool {
	fragment = strings.ToLower(strings.TrimSpace(fragment))
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), fragment) {
			return true
		}
	}
	return false
}

func TestRunYOLOOnProgressEmitsStages(t *testing.T) {
	// Hermetic: never reach a real sidecar that may be running locally.
	t.Setenv("PYTHON_SIDECAR_GRPC_ADDR", "127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	provider := &stubSearchProvider{}
	var stages []string
	result, err := NewAgent(WithSearchProviders(provider)).RunYOLO(ctx, YOLORequest{
		Task:              "meniscus repair strategies",
		MaxIterations:     1,
		MaxSearchTerms:    1,
		HitsPerSearch:     1,
		DisablePlanning:   true,
		DisableHypotheses: true,
		OnProgress: func(event ProgressEvent) {
			if stage := strings.TrimSpace(event.Stage); stage != "" {
				stages = append(stages, stage)
			}
		},
	})
	if err != nil {
		t.Fatalf("RunYOLO returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
	if !containsFragment(stages, "loop_started") {
		t.Fatalf("expected loop_started in stages, got %v", stages)
	}
}
