package wisdev

import (
	"sort"
	"strings"
	"unicode"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// Claim-overlap ranking for the evidence tool.
//
// The tool cannot verify entailment, and pretending otherwise is the defect
// this replaces. What it can do honestly is report how much of the claim's
// vocabulary appears in a candidate, so a caller can see that a result sharing
// two stopwords with the claim is not the same as one sharing its technical
// terms. The number is labelled lexical wherever it is shown.

type claimScoredPaper struct {
	paper   search.Paper
	overlap float64
}

// claimStopwords are the terms whose presence says nothing about topical match.
var claimStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "but": {},
	"by": {}, "can": {}, "do": {}, "does": {}, "for": {}, "from": {}, "has": {},
	"have": {}, "how": {}, "in": {}, "is": {}, "it": {}, "its": {}, "of": {}, "on": {},
	"or": {}, "that": {}, "the": {}, "their": {}, "them": {}, "there": {}, "these": {},
	"they": {}, "this": {}, "to": {}, "was": {}, "were": {}, "when": {}, "which": {},
	"while": {}, "with": {}, "not": {}, "than": {}, "then": {}, "we": {}, "our": {},
}

func claimTerms(text string) map[string]struct{} {
	terms := map[string]struct{}{}
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	}) {
		field = strings.Trim(field, "-")
		if len(field) < 3 {
			continue
		}
		if _, stop := claimStopwords[field]; stop {
			continue
		}
		terms[field] = struct{}{}
	}
	return terms
}

// ClaimOverlap is the fraction of the claim's content terms that appear in the
// candidate's title or abstract. 0 means no shared vocabulary at all, which is
// a strong signal the candidate was retrieved for something other than this
// claim. It is not a measure of support.
func ClaimOverlap(claim string, p search.Paper) float64 {
	want := claimTerms(claim)
	if len(want) == 0 {
		return 0
	}
	have := claimTerms(p.Title + " " + p.Abstract)
	hits := 0
	for term := range want {
		if _, ok := have[term]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(want))
}

// rankByClaimOverlap orders candidates by shared claim vocabulary, descending,
// keeping retrieval order as the tiebreak so the ranking stays deterministic.
func rankByClaimOverlap(claim string, papers []search.Paper) []claimScoredPaper {
	out := make([]claimScoredPaper, 0, len(papers))
	for _, p := range papers {
		out = append(out, claimScoredPaper{paper: p, overlap: ClaimOverlap(claim, p)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].overlap > out[j].overlap })
	return out
}
