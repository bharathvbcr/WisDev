package search

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Identifier-routed paper lookup with typed outcomes.
//
// The dispatch this replaces scanned the registry for any provider
// implementing PaperLookupProvider, called it, and on error did `continue`.
// Two defects followed from that shape:
//
//  1. Exactly one provider (Semantic Scholar) satisfied the predicate, so a
//     registry without it answered every identifier with "no provider found
//     for paper_lookup" even when it held the owning authority -- arXiv for an
//     arXiv ID, Crossref for a DOI.
//  2. When a capable provider did exist and failed -- rate limit, timeout,
//     upstream 5xx -- the loop swallowed the error and fell through to the same
//     "no provider found" message. A capability FAILURE was reported as a
//     capability ABSENCE, and the cause was discarded.
//
// The second is the more corrosive: a caller cannot distinguish "this paper
// does not exist" from "the provider was throttled", so a retryable outage
// reads as a settled negative result.

// PaperLookupOutcome classifies why a lookup did not return a paper.
type PaperLookupOutcome string

const (
	// PaperLookupFound: a provider returned the paper.
	PaperLookupFound PaperLookupOutcome = "found"
	// PaperLookupCapabilityAbsent: no registered provider can service this
	// identifier type at all.
	PaperLookupCapabilityAbsent PaperLookupOutcome = "capability_absent"
	// PaperLookupAllProvidersFailed: capable providers existed and every one
	// errored. An availability failure, not a statement about the paper.
	PaperLookupAllProvidersFailed PaperLookupOutcome = "all_providers_failed"
	// PaperLookupNotFound: capable providers responded and none held the id.
	PaperLookupNotFound PaperLookupOutcome = "not_found"
)

// PaperLookupResult is the typed result of an identifier lookup.
type PaperLookupResult struct {
	Outcome PaperLookupOutcome `json:"outcome"`
	Paper   *Paper             `json:"paper,omitempty"`
	// Provider that resolved the identifier, when found.
	Provider string `json:"provider,omitempty"`
	// AttemptedProviders lists every capable provider that was called.
	AttemptedProviders []string `json:"attemptedProviders,omitempty"`
	// ProviderErrors maps provider name to its error. Populated when providers
	// failed, so a caller can tell a rate limit from a genuine miss.
	ProviderErrors map[string]string `json:"providerErrors,omitempty"`
	// IdentifierKind is the detected identifier space.
	IdentifierKind string `json:"identifierKind,omitempty"`
}

var (
	// ErrPaperLookupCapabilityAbsent: no provider supports this identifier type.
	ErrPaperLookupCapabilityAbsent = errors.New("paper_lookup: no registered provider supports this identifier type")
	// ErrPaperLookupAllProvidersFailed: every capable provider errored.
	ErrPaperLookupAllProvidersFailed = errors.New("paper_lookup: all capable providers failed")
	// ErrPaperLookupNotFound: capable providers responded; none held the id.
	ErrPaperLookupNotFound = errors.New("paper_lookup: identifier not found")
)

// Err renders the outcome as an error, preserving which providers were tried
// and why each failed. Never collapses a failure into an absence.
func (r PaperLookupResult) Err() error {
	switch r.Outcome {
	case PaperLookupFound:
		return nil
	case PaperLookupCapabilityAbsent:
		return fmt.Errorf("%w (identifier kind=%s; register a provider that owns this identifier space)",
			ErrPaperLookupCapabilityAbsent, r.IdentifierKind)
	case PaperLookupAllProvidersFailed:
		return fmt.Errorf("%w (kind=%s, attempted=%s): %s",
			ErrPaperLookupAllProvidersFailed, r.IdentifierKind,
			strings.Join(r.AttemptedProviders, ","), r.errorDetail())
	default:
		return fmt.Errorf("%w (kind=%s, attempted=%s)",
			ErrPaperLookupNotFound, r.IdentifierKind,
			strings.Join(r.AttemptedProviders, ","))
	}
}

func (r PaperLookupResult) errorDetail() string {
	names := make([]string, 0, len(r.ProviderErrors))
	for name := range r.ProviderErrors {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+": "+r.ProviderErrors[name])
	}
	return strings.Join(parts, "; ")
}

// modernArxivID and legacyArxivID already exist in doi_normalize.go; reused
// here so the two files cannot disagree on what an arXiv ID looks like.
var doiPattern = regexp.MustCompile(`^10\.\d{4,9}/\S+$`)

// Identifier kinds.
const (
	IdentifierKindArxiv   = "arxiv"
	IdentifierKindDOI     = "doi"
	IdentifierKindS2      = "s2"
	IdentifierKindUnknown = "unknown"
)

// NormalizeArxivID returns the bare arXiv ID for anything that denotes one --
// a bare ID, an abs/pdf URL, or an "arXiv:" prefix -- and "" otherwise.
func NormalizeArxivID(raw string) string {
	id := strings.TrimSpace(raw)
	if id == "" {
		return ""
	}
	lower := strings.ToLower(id)
	for _, pref := range []string{"arxiv:", "arxiv.org/abs/", "arxiv.org/pdf/"} {
		if idx := strings.Index(lower, pref); idx >= 0 {
			id = id[idx+len(pref):]
			break
		}
	}
	id = strings.TrimPrefix(id, "http://")
	id = strings.TrimPrefix(id, "https://")
	id = strings.TrimSuffix(strings.TrimSpace(id), ".pdf")
	if modernArxivID.MatchString(id) || legacyArxivID.MatchString(id) {
		return id
	}
	// A 10.48550/arXiv.* DOI denotes an arXiv paper; the package already knows
	// how to unwrap one.
	if inner := ExtractArxivIDFromDOI(raw); inner != "" {
		return inner
	}
	return ""
}

// ValidDOI strips the usual prefixes via the package's NormalizeDOI and then
// checks the result actually is a DOI. NormalizeDOI alone returns its input
// unchanged for a non-DOI, so routing on it would send arbitrary strings to
// Crossref.
func ValidDOI(raw string) string {
	id := NormalizeDOI(raw)
	if doiPattern.MatchString(id) {
		return id
	}
	return ""
}

// DetectIdentifierKind classifies an identifier so lookup can be routed to the
// authority that owns that space, rather than to whichever provider happens to
// implement the interface.
func DetectIdentifierKind(raw string) string {
	if NormalizeArxivID(raw) != "" {
		return IdentifierKindArxiv
	}
	if ValidDOI(raw) != "" {
		return IdentifierKindDOI
	}
	if id := strings.TrimSpace(raw); id != "" {
		// Semantic Scholar corpus ids are 40-hex SHA1s or "CorpusId:N".
		if strings.HasPrefix(strings.ToLower(id), "corpusid:") {
			return IdentifierKindS2
		}
		if len(id) == 40 && strings.Trim(strings.ToLower(id), "0123456789abcdef") == "" {
			return IdentifierKindS2
		}
	}
	return IdentifierKindUnknown
}

// providerOwnsIdentifier reports whether a provider is an authority for a kind.
// An unknown kind is offered to every capable provider, which is the old
// behaviour and the right fallback: it is a guess, and it is labelled as one.
func providerOwnsIdentifier(providerName, kind string) bool {
	// The registrar for the identifier space, and only it. Other capable
	// providers stay in the candidate list as fallbacks -- they are just not
	// tried first. Listing a good resolver here (Semantic Scholar for arXiv,
	// OpenAlex for DOIs) makes every candidate an "owner" and the ordering
	// collapses back to registry order, which is what it is meant to override.
	switch kind {
	case IdentifierKindArxiv:
		return providerName == "arxiv"
	case IdentifierKindDOI:
		return providerName == "crossref"
	case IdentifierKindS2:
		return providerName == "semantic_scholar"
	default:
		return false
	}
}

// LookupPaperByID resolves one identifier, routed by identifier space, and
// reports a typed outcome that distinguishes absence from failure from a miss.
func LookupPaperByID(ctx context.Context, reg *ProviderRegistry, paperID string) PaperLookupResult {
	kind := DetectIdentifierKind(paperID)
	res := PaperLookupResult{IdentifierKind: kind}
	if reg == nil {
		res.Outcome = PaperLookupCapabilityAbsent
		return res
	}

	type candidate struct {
		name   string
		lookup PaperLookupProvider
		owns   bool
	}
	var candidates []candidate
	for _, p := range reg.All() {
		lookup, ok := p.(PaperLookupProvider)
		if !ok {
			continue
		}
		supports := false
		for _, t := range p.Tools() {
			if t == "paper_lookup" {
				supports = true
				break
			}
		}
		if !supports {
			continue
		}
		candidates = append(candidates, candidate{
			name: p.Name(), lookup: lookup,
			owns: providerOwnsIdentifier(p.Name(), kind),
		})
	}
	if len(candidates) == 0 {
		res.Outcome = PaperLookupCapabilityAbsent
		return res
	}
	// The owning authority first; others remain as fallback so a registry
	// without the authority still answers.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].owns && !candidates[j].owns
	})

	failures := map[string]string{}
	responded := false
	for _, c := range candidates {
		res.AttemptedProviders = append(res.AttemptedProviders, c.name)
		paper, err := c.lookup.SearchByPaperID(ctx, paperID)
		if err != nil {
			failures[c.name] = err.Error()
			continue
		}
		responded = true
		if paper != nil {
			res.Outcome = PaperLookupFound
			res.Paper = paper
			res.Provider = c.name
			if len(failures) > 0 {
				res.ProviderErrors = failures
			}
			return res
		}
	}

	if len(failures) > 0 {
		res.ProviderErrors = failures
	}
	if responded {
		// At least one provider answered cleanly and did not hold it.
		res.Outcome = PaperLookupNotFound
		return res
	}
	res.Outcome = PaperLookupAllProvidersFailed
	return res
}
