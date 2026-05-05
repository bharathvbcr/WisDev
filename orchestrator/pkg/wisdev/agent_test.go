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

	result, err := NewAgent(WithNoSearchProviders()).RunYOLO(ctx, YOLORequest{
		Task:              "map evidence for open source research agents",
		MaxIterations:     1,
		MaxSearchTerms:    1,
		HitsPerSearch:     1,
		MaxUniquePapers:   2,
		DisablePlanning:   true,
		DisableHypotheses: true,
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
}

func TestRunYOLOUsesPublicSearchProvider(t *testing.T) {
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
