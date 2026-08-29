package search

import (
	"context"
	"errors"
	"testing"
)

// A registry holding the owning authority must not report the capability as
// absent, and a provider outage must not be reported as a missing capability.

type stubLookupProvider struct {
	BaseProvider
	name  string
	paper *Paper
	err   error
	calls *[]string
}

func (s *stubLookupProvider) Name() string      { return s.name }
func (s *stubLookupProvider) Domains() []string { return []string{"cs"} }
func (s *stubLookupProvider) Tools() []string   { return []string{"paper_lookup"} }
func (s *stubLookupProvider) Search(ctx context.Context, q string, o SearchOpts) ([]Paper, error) {
	return nil, nil
}
func (s *stubLookupProvider) SearchByPaperID(ctx context.Context, id string) (*Paper, error) {
	if s.calls != nil {
		*s.calls = append(*s.calls, s.name)
	}
	return s.paper, s.err
}

func regWith(providers ...SearchProvider) *ProviderRegistry {
	reg := NewProviderRegistry()
	for _, p := range providers {
		reg.Register(p)
	}
	return reg
}

func TestDetectIdentifierKind(t *testing.T) {
	cases := map[string]string{
		"2401.07324":              IdentifierKindArxiv,
		"2606.09498v2":            IdentifierKindArxiv,
		"arXiv:2401.07324":        IdentifierKindArxiv,
		"https://arxiv.org/abs/2401.07324": IdentifierKindArxiv,
		"10.48550/arXiv.2401.07324":        IdentifierKindArxiv,
		"10.1007/s10664-022-10277-5":       IdentifierKindDOI,
		"https://doi.org/10.1145/3715754":  IdentifierKindDOI,
		"CorpusId:12345":                   IdentifierKindS2,
		"not-an-identifier":                IdentifierKindUnknown,
	}
	for in, want := range cases {
		if got := DetectIdentifierKind(in); got != want {
			t.Errorf("DetectIdentifierKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidDOIRejectsNonDOI(t *testing.T) {
	// NormalizeDOI returns its input unchanged for a non-DOI, so routing on it
	// alone would send arbitrary strings to Crossref.
	if got := ValidDOI("not-a-doi"); got != "" {
		t.Fatalf("ValidDOI(non-DOI) = %q, want empty", got)
	}
	if got := ValidDOI("https://doi.org/10.1145/3715754"); got != "10.1145/3715754" {
		t.Fatalf("ValidDOI = %q", got)
	}
}

func TestLookupFailureIsNotReportedAsAbsence(t *testing.T) {
	// The defect: a capable provider that errors used to fall through to
	// "no provider found", turning an outage into a settled negative.
	boom := errors.New("429 rate limited")
	reg := regWith(&stubLookupProvider{name: "semantic_scholar", err: boom})
	res := LookupPaperByID(context.Background(), reg, "CorpusId:1")

	if res.Outcome != PaperLookupAllProvidersFailed {
		t.Fatalf("outcome = %q, want %q", res.Outcome, PaperLookupAllProvidersFailed)
	}
	if !errors.Is(res.Err(), ErrPaperLookupAllProvidersFailed) {
		t.Fatalf("Err() = %v, want ErrPaperLookupAllProvidersFailed", res.Err())
	}
	if errors.Is(res.Err(), ErrPaperLookupCapabilityAbsent) {
		t.Fatal("a provider failure must never render as capability absence")
	}
	if res.ProviderErrors["semantic_scholar"] != boom.Error() {
		t.Fatalf("provider error discarded: %#v", res.ProviderErrors)
	}
}

func TestLookupCapabilityAbsentOnlyWhenNoProviderCan(t *testing.T) {
	res := LookupPaperByID(context.Background(), regWith(), "2401.07324")
	if res.Outcome != PaperLookupCapabilityAbsent {
		t.Fatalf("outcome = %q, want capability_absent", res.Outcome)
	}
	if !errors.Is(res.Err(), ErrPaperLookupCapabilityAbsent) {
		t.Fatalf("Err() = %v", res.Err())
	}
}

func TestLookupCleanMissIsNotFailure(t *testing.T) {
	// Provider answered and did not hold it: not_found, distinct from an outage.
	reg := regWith(&stubLookupProvider{name: "arxiv", paper: nil, err: nil})
	res := LookupPaperByID(context.Background(), reg, "2401.07324")
	if res.Outcome != PaperLookupNotFound {
		t.Fatalf("outcome = %q, want not_found", res.Outcome)
	}
	if !errors.Is(res.Err(), ErrPaperLookupNotFound) {
		t.Fatalf("Err() = %v", res.Err())
	}
}

func TestLookupRoutesToOwningAuthorityFirst(t *testing.T) {
	var calls []string
	found := &Paper{Title: "resolved", ArxivID: "2401.07324"}
	reg := regWith(
		&stubLookupProvider{name: "semantic_scholar", paper: found, calls: &calls},
		&stubLookupProvider{name: "arxiv", paper: found, calls: &calls},
	)
	res := LookupPaperByID(context.Background(), reg, "2401.07324")
	if res.Outcome != PaperLookupFound {
		t.Fatalf("outcome = %q", res.Outcome)
	}
	if len(calls) == 0 || calls[0] != "arxiv" {
		t.Fatalf("arXiv owns arXiv IDs and must be tried first; call order = %v", calls)
	}
}

func TestLookupFallsBackPastAFailingAuthority(t *testing.T) {
	var calls []string
	found := &Paper{Title: "resolved"}
	reg := regWith(
		&stubLookupProvider{name: "arxiv", err: errors.New("timeout"), calls: &calls},
		&stubLookupProvider{name: "semantic_scholar", paper: found, calls: &calls},
	)
	res := LookupPaperByID(context.Background(), reg, "2401.07324")
	if res.Outcome != PaperLookupFound || res.Provider != "semantic_scholar" {
		t.Fatalf("outcome=%q provider=%q", res.Outcome, res.Provider)
	}
	// The authority's failure is still reported even though the lookup succeeded.
	if res.ProviderErrors["arxiv"] == "" {
		t.Fatal("a failure on the way to success must still be recorded")
	}
}

func TestArxivAndCrossrefAdvertisePaperLookup(t *testing.T) {
	// The registry held the owning authorities; they advertised no tools, so
	// the dispatch never offered them the identifier.
	for _, p := range []SearchProvider{NewArXivProvider(), NewCrossrefProvider()} {
		if _, ok := p.(PaperLookupProvider); !ok {
			t.Fatalf("%s does not implement PaperLookupProvider", p.Name())
		}
		advertises := false
		for _, tool := range p.Tools() {
			if tool == "paper_lookup" {
				advertises = true
			}
		}
		if !advertises {
			t.Fatalf("%s implements lookup but does not advertise paper_lookup", p.Name())
		}
	}
}
