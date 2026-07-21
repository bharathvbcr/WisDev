package wisdev

import (
	"context"
	"sort"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

var genericTopicTerms = map[string]struct{}{
	"clinical": {}, "engineering": {}, "evidence": {}, "material": {}, "materials": {},
	"repair": {}, "scaffold": {}, "scaffolds": {}, "study": {}, "studies": {},
	"tissue": {}, "trial": {}, "trials": {},
}

// curatedAnchorSynonyms maps a normalized anchor token to additional surface
// forms (synonyms and morphological stems matched as substrings) that denote the
// same concept. This mirrors the synonym expansion the search layer uses to
// retrieve papers, so the relevance gate does not reject on-topic papers that the
// search legitimately found under a synonym (e.g. "microphysiological" for
// "organ on a chip", "allograft" for "rejection"). Entries are activated only
// when the query literally contains the key, so scope stays tight.
var curatedAnchorSynonyms = map[string][]string{
	"chip":       {"microfluidic", "microphysiolog", "organ-on", "lab-on-chip", "lab-on-a-chip", "organoid", "organotypic", "biochip"},
	"organ":      {"organoid", "organotypic", "microphysiolog"},
	"immune":     {"immunolog", "immunity", "immunogenic", "immunoreact", "immunomod"},
	"immunity":   {"immune", "immunolog", "immunogenic"},
	"rejection":  {"allograft", "allogeneic", "allogenic", "transplant", "graft", "graft-versus-host", "host-versus-graft"},
	"transplant": {"transplantation", "allograft", "allogeneic", "graft"},
	"cancer":     {"carcinoma", "tumor", "tumour", "neoplas", "malignan", "oncolog"},
	"tumor":      {"tumour", "carcinoma", "neoplas", "cancer", "malignan"},
	"tumour":     {"tumor", "carcinoma", "neoplas", "cancer", "malignan"},
}

var queryRelevanceStopwords = map[string]struct{}{
	"about": {}, "after": {}, "also": {}, "among": {}, "been": {}, "being": {},
	"between": {}, "both": {}, "could": {}, "current": {}, "does": {}, "each": {},
	"evidence": {}, "from": {}, "have": {}, "into": {}, "latest": {}, "more": {},
	"most": {}, "near": {}, "new": {}, "other": {}, "over": {}, "recent": {},
	"research": {}, "review": {}, "should": {}, "some": {}, "such": {}, "than": {},
	"that": {}, "their": {}, "there": {}, "these": {}, "they": {}, "this": {},
	"those": {}, "through": {}, "under": {}, "using": {}, "very": {}, "what": {},
	"when": {}, "where": {}, "which": {}, "while": {}, "with": {}, "within": {},
	"without": {}, "would": {},
}

func queryAnchorTerms(query string) []string {
	tokens := loopEvidenceTokens(query)
	anchors := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if _, stop := queryRelevanceStopwords[token]; stop {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		anchors = append(anchors, token)
	}
	sort.Slice(anchors, func(i, j int) bool {
		if len(anchors[i]) == len(anchors[j]) {
			return anchors[i] < anchors[j]
		}
		return len(anchors[i]) > len(anchors[j])
	})
	return anchors
}

func paperQueryRelevanceScore(query string, paper search.Paper) float64 {
	anchors := queryAnchorTerms(query)
	if len(anchors) == 0 {
		return 1
	}
	title := strings.ToLower(strings.TrimSpace(paper.Title))
	abstract := strings.ToLower(strings.TrimSpace(paper.Abstract))
	body := title + " " + abstract
	if strings.TrimSpace(body) == "" {
		return 0
	}

	matched := 0
	weighted := 0.0
	for _, anchor := range anchors {
		if bodyContainsAnchor(body, anchor) {
			matched++
			weight := 1.0
			if bodyContainsAnchor(title, anchor) {
				weight = 2.5
			}
			weighted += weight
		}
	}
	if matched == 0 {
		return 0
	}

	denominator := float64(len(anchors)) * 2.5
	score := weighted / denominator
	if matched == len(anchors) {
		score += 0.15
	}
	if score > 1 {
		score = 1
	}
	return score
}

func splitQueryAnchors(anchors []string) (specific []string, generic []string) {
	for _, anchor := range anchors {
		if _, ok := genericTopicTerms[anchor]; ok {
			generic = append(generic, anchor)
			continue
		}
		specific = append(specific, anchor)
	}
	return specific, generic
}

// requiredSpecificMatches returns how many of a topic's specific anchor terms a
// paper must mention to be admitted. Short queries (1-2 anchors) demand all of
// them to preserve precision; multi-concept queries (3+) require a 60% majority
// so a paper phrased with synonyms for one concept is not rejected for missing a
// single literal token, while still demanding most concepts be present. The
// per-anchor score floor in paperMatchesSingleTopicRelevance is the precision backstop.
func requiredSpecificMatches(specificCount int) int {
	if specificCount <= 2 {
		return specificCount
	}
	return (3*specificCount + 4) / 5 // ceil(0.6n): 3->2, 4->3, 5->3, 6->4
}

func queryTopicClauses(query string) []string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(query)), " and ")
	if len(parts) < 2 {
		return nil
	}
	clauses := make([]string, 0, len(parts))
	for _, part := range parts {
		if clause := strings.TrimSpace(part); clause != "" {
			clauses = append(clauses, clause)
		}
	}
	if len(clauses) < 2 {
		return nil
	}
	return clauses
}

func paperMatchesQueryRelevance(query string, paper search.Paper) bool {
	query = applyResearchQueryCorrections(normalizeResearchQueryText(query))
	if clauses := queryTopicClauses(query); len(clauses) >= 2 {
		for _, clause := range clauses {
			if paperMatchesSingleTopicRelevance(clause, paper) {
				return true
			}
		}
		return false
	}
	return paperMatchesSingleTopicRelevance(query, paper)
}

func paperMatchesSingleTopicRelevance(query string, paper search.Paper) bool {
	anchors := queryAnchorTerms(query)
	if len(anchors) == 0 {
		return true
	}
	body := strings.ToLower(strings.TrimSpace(paper.Title + " " + paper.Abstract))
	if body == "" {
		return false
	}
	specific, generic := splitQueryAnchors(anchors)
	if len(specific) == 0 {
		specific = append(specific, anchors...)
		generic = nil
	}
	matchedSpecific := 0
	for _, anchor := range specific {
		if bodyContainsAnchor(body, anchor) {
			matchedSpecific++
		}
	}
	required := requiredSpecificMatches(len(specific))
	coveredByAnchors := matchedSpecific >= required
	if !coveredByAnchors {
		// A paper must share at least one of the query's own anchors before the
		// LLM-prepared synonyms/keywords for this query cover the shortfall. This
		// admits synonym-retrieved papers against the narrower literal root query
		// without opening the gate to off-topic noise.
		if matchedSpecific == 0 {
			return false
		}
		if matchedSpecific+matchedExpansionTerms(query, body, specific) < required {
			return false
		}
	}
	if len(generic) > 0 {
		matchedGeneric := false
		for _, anchor := range generic {
			if bodyContainsAnchor(body, anchor) {
				matchedGeneric = true
				break
			}
		}
		if !matchedGeneric {
			return false
		}
	}
	// When coverage comes purely from the query's own anchors, keep the score
	// backstop for precision. When LLM synonyms supplied the shortfall, those
	// query-specific matches are themselves the precision signal.
	if coveredByAnchors {
		return paperQueryRelevanceScore(query, paper) >= relevanceScoreFloor(len(specific))
	}
	return true
}

// relevanceScoreFloor is the precision backstop applied after anchor coverage is
// satisfied. Short queries keep a firm floor; multi-concept queries relax it so a
// paper that legitimately matches the fractional anchor majority is not rejected
// merely for expressing those concepts in the abstract rather than the title.
func relevanceScoreFloor(specificCount int) float64 {
	if specificCount <= 2 {
		return 0.35
	}
	return 0.18
}

// relevanceExpansionTerms returns anchor tokens drawn from the LLM-prepared
// synonyms and keywords cached for the query. These let the gate recognise a
// concept expressed with the model's own synonym vocabulary, not only the curated
// map, so it generalises to arbitrary research topics.
func relevanceExpansionTerms(query string) []string {
	prep, ok := lookupPreparedQuery(query)
	if !ok {
		return nil
	}
	phrases := make([]string, 0, len(prep.Synonyms)+len(prep.Keywords))
	phrases = append(phrases, prep.Synonyms...)
	phrases = append(phrases, prep.Keywords...)
	out := make([]string, 0, len(phrases)*2)
	for _, phrase := range phrases {
		out = append(out, queryAnchorTerms(phrase)...)
	}
	return dedupeTrimmedStrings(out)
}

// matchedExpansionTerms counts how many distinct LLM-prepared synonym/keyword
// terms for the query appear in the paper body, excluding terms already counted
// as specific anchors.
func matchedExpansionTerms(query, body string, specific []string) int {
	terms := relevanceExpansionTerms(query)
	if len(terms) == 0 {
		return 0
	}
	skip := make(map[string]struct{}, len(specific)+len(terms))
	for _, s := range specific {
		skip[s] = struct{}{}
	}
	hits := 0
	for _, term := range terms {
		if term == "" {
			continue
		}
		if _, dup := skip[term]; dup {
			continue
		}
		skip[term] = struct{}{}
		if bodyContainsAnchor(body, term) {
			hits++
		}
	}
	return hits
}

func shouldSkipQueryRelevanceFilter(papers []search.Paper) bool {
	rich := 0
	for _, paper := range papers {
		if len(strings.TrimSpace(paper.Title+" "+paper.Abstract)) >= 24 {
			rich++
		}
	}
	return rich == 0
}

func filterPapersByQueryRelevance(query string, papers []search.Paper) []search.Paper {
	if len(papers) == 0 {
		return nil
	}
	if shouldSkipQueryRelevanceFilter(papers) {
		return search.SortPapersByPreferenceWithQuery(papers, query)
	}
	filtered := make([]search.Paper, 0, len(papers))
	for _, paper := range papers {
		if paperMatchesQueryRelevance(query, paper) {
			filtered = append(filtered, paper)
		}
	}
	return search.SortPapersByPreferenceWithQuery(filtered, query)
}

func filterEvidenceByQueryRelevance(query string, evidence []EvidenceItem) []EvidenceItem {
	if len(evidence) == 0 {
		return nil
	}
	filtered := make([]EvidenceItem, 0, len(evidence))
	for _, item := range evidence {
		paper := search.Paper{
			ID:       item.PaperID,
			Title:    firstNonEmpty(item.PaperTitle, item.Claim),
			Abstract: firstNonEmpty(item.Snippet, item.Claim),
		}
		if paperMatchesQueryRelevance(query, paper) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// BuildTopicFocusedQueries returns LLM-prepared seed queries when available.
func BuildTopicFocusedQueries(root string) []string {
	if prep, ok := lookupPreparedQuery(root); ok && len(prep.SeedQueries) > 0 {
		return append([]string(nil), prep.SeedQueries...)
	}
	prep := PrepareResearchQueryWithContext(context.Background(), root, ResearchQueryPrepareOptions{
		LLMClient: GlobalLLMClient,
	})
	if len(prep.SeedQueries) > 0 {
		return append([]string(nil), prep.SeedQueries...)
	}
	return offlineSeedQueries(root)
}

func admitSearchPapersForQuery(existing []search.Paper, query string, incoming []search.Paper, maxUniquePapers int) ([]search.Paper, []search.Paper) {
	return appendUniqueSearchPapersWithinBudget(existing, filterPapersByQueryRelevance(query, incoming), maxUniquePapers)
}

// filterPapersByRetrievalRelevance admits a paper when it is relevant to either
// the branch query that actually retrieved it or the root research query. This
// keeps synonym/expansion branches productive: papers found via a broadened query
// are judged against that query, not only against the narrower root terms.
func filterPapersByRetrievalRelevance(rootQuery, retrievalQuery string, papers []search.Paper) []search.Paper {
	if len(papers) == 0 {
		return nil
	}
	retrievalQuery = strings.TrimSpace(retrievalQuery)
	if retrievalQuery == "" || strings.EqualFold(retrievalQuery, strings.TrimSpace(rootQuery)) {
		return filterPapersByQueryRelevance(rootQuery, papers)
	}
	if shouldSkipQueryRelevanceFilter(papers) {
		return search.SortPapersByPreferenceWithQuery(papers, rootQuery)
	}
	filtered := make([]search.Paper, 0, len(papers))
	for _, paper := range papers {
		if paperMatchesQueryRelevance(retrievalQuery, paper) || paperMatchesQueryRelevance(rootQuery, paper) {
			filtered = append(filtered, paper)
		}
	}
	return search.SortPapersByPreferenceWithQuery(filtered, rootQuery)
}

// admitSearchPapersForRetrievalQuery admits papers judged against the branch
// query that retrieved them, falling back to the root query, so synonym-driven
// retrieval branches are not silently discarded by the narrower root terms.
func admitSearchPapersForRetrievalQuery(existing []search.Paper, rootQuery, retrievalQuery string, incoming []search.Paper, maxUniquePapers int) ([]search.Paper, []search.Paper) {
	return appendUniqueSearchPapersWithinBudget(existing, filterPapersByRetrievalRelevance(rootQuery, retrievalQuery, incoming), maxUniquePapers)
}

func anchorVariants(anchor string) []string {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return nil
	}
	variants := []string{anchor}
	if strings.HasSuffix(anchor, "s") && len(anchor) > 3 {
		variants = append(variants, strings.TrimSuffix(anchor, "s"))
	} else {
		variants = append(variants, anchor+"s")
	}
	return variants
}

// anchorMatchTerms returns every surface form whose presence in a paper body
// should count as a match for anchor: the anchor itself, its naive singular/plural
// variant, and any curated concept synonyms.
func anchorMatchTerms(anchor string) []string {
	terms := anchorVariants(anchor)
	if syns, ok := curatedAnchorSynonyms[anchor]; ok {
		terms = append(terms, syns...)
	}
	return terms
}

// relevanceMorphologySuffixes are stripped (longest-first) to reduce a word to a
// stem for inflectional/derivational matching. Suffixes like "ic", "al", "ize"
// and "ization" are deliberately omitted because they collapse unrelated words
// (organ↔organic/organize/organization); the substring path already covers those
// when they are literally present.
var relevanceMorphologySuffixes = []string{
	"ogenicity", "ological", "ologies", "ology", "ogenic",
	"ations", "ation", "ities", "ity", "ions", "ion",
	"ings", "ing", "encies", "ency", "ies", "ed", "es", "s", "e",
}

// relevanceStem reduces a word to a coarse morphological stem so that, e.g.,
// "immune", "immunity", "immunological", and "immunogenicity" all collapse to
// "immun", and "rejection"/"rejecting" collapse to "reject".
func relevanceStem(word string) string {
	w := strings.ToLower(strings.TrimSpace(word))
	if len(w) <= 4 {
		return w
	}
	for _, suffix := range relevanceMorphologySuffixes {
		if strings.HasSuffix(w, suffix) && len(w)-len(suffix) >= 4 {
			return w[:len(w)-len(suffix)]
		}
	}
	return w
}

// stemMatchesBody reports whether any body token shares a morphological stem with
// the anchor, catching inflected/derived forms the literal substring path misses.
func stemMatchesBody(body, anchor string) bool {
	stem := relevanceStem(anchor)
	if len(stem) < 4 {
		return false
	}
	for _, token := range loopEvidenceTokens(body) {
		if relevanceStem(token) == stem {
			return true
		}
	}
	return false
}

func bodyContainsAnchor(body, anchor string) bool {
	body = strings.ToLower(body)
	anchorLower := strings.ToLower(anchor)

	for _, variant := range anchorMatchTerms(anchorLower) {
		if variant != "" && strings.Contains(body, variant) {
			return true
		}
	}
	// Morphological fallback: match inflected/derived forms the literal substring
	// path misses (immune↔immunity↔immunological, rejection↔rejecting).
	return stemMatchesBody(body, anchorLower)
}
