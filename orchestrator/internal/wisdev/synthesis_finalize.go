package wisdev

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// citationAbbreviations are lowercased word forms that precede a period without
// ending a sentence, so the prose sentence splitter must not break on them.
var citationAbbreviations = map[string]struct{}{
	"al": {}, "et": {}, "eg": {}, "ie": {}, "vs": {}, "cf": {}, "fig": {}, "figs": {},
	"no": {}, "nos": {}, "dr": {}, "prof": {}, "ed": {}, "eds": {}, "pp": {}, "vol": {},
	"est": {}, "approx": {}, "ca": {}, "inc": {}, "ltd": {}, "jr": {}, "sr": {}, "st": {},
}

// trailingAlphaWord returns the run of letters immediately preceding index i.
func trailingAlphaWord(runes []rune, i int) string {
	start := i
	for start > 0 && unicode.IsLetter(runes[start-1]) {
		start--
	}
	return string(runes[start:i])
}

// isProseSentenceBoundary reports whether the terminator rune at index i ends a
// sentence rather than sitting inside a citation, abbreviation, initial, or
// decimal number. depth is the current parenthesis/bracket nesting.
func isProseSentenceBoundary(runes []rune, i, depth int) bool {
	if depth > 0 {
		return false
	}
	j := i + 1
	for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
		j++
	}
	if j >= len(runes) {
		return true
	}
	if runes[i] == '.' {
		if i > 0 && unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[j]) {
			return false // decimal like 5.2
		}
		word := trailingAlphaWord(runes, i)
		if _, ok := citationAbbreviations[strings.ToLower(word)]; ok {
			return false
		}
		if r := []rune(word); len(r) == 1 && unicode.IsUpper(r[0]) {
			return false // author initial like "F."
		}
	}
	next := runes[j]
	return unicode.IsUpper(next) || unicode.IsDigit(next) ||
		next == '"' || next == '\'' || next == '(' || next == '[' || next == '#' || next == '-'
}

// citationSafeProseSentences splits prose into sentences without breaking inside
// parenthetical citations ("(Author, 1999; 12 citations)"), author initials
// ("Philip F."), "et al.", common abbreviations, or decimals — the period sites
// that previously mangled inline citations into dangling fragments. A limit <= 0
// means no cap (the enrichment path must not drop trailing sentences).
func citationSafeProseSentences(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	var sentences []string
	var current strings.Builder
	depth := 0
	flush := func() {
		s := strings.TrimSpace(current.String())
		current.Reset()
		if s == "" {
			return
		}
		if len(s) < 24 && len(sentences) > 0 {
			// Re-attach a too-short tail to the previous sentence rather than emit
			// it as a standalone fragment that would get its own citation.
			sentences[len(sentences)-1] = strings.TrimSpace(sentences[len(sentences)-1] + " " + s)
			return
		}
		sentences = append(sentences, s)
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		current.WriteRune(r)
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '.', '!', '?':
			if isProseSentenceBoundary(runes, i, depth) {
				flush()
			}
		}
	}
	flush()
	if limit > 0 && len(sentences) > limit {
		sentences = sentences[:limit]
	}
	return sentences
}

const sentenceCitationMinParagraphLen = 280

func isHeuristicSynthesisAnswer(text string) bool {
	return strings.Contains(text, "Provisional Research Synthesis")
}

func answerHasSection(text, heading string) bool {
	needle := "## " + strings.TrimSpace(heading)
	return strings.Contains(text, needle)
}

func prependResearcherFrontMatter(query string, text string, papers []search.Paper, evidence []EvidenceItem) string {
	if isHeuristicSynthesisAnswer(text) {
		return text
	}
	relevant := filterPapersByQueryRelevance(query, papers)
	if len(relevant) == 0 {
		relevant = papers
	}
	registry := buildCitationRegistry(relevant)

	var prefix strings.Builder
	if !answerHasSection(text, "Executive takeaway") {
		takeaways := buildSynthesisExecutiveTakeaway(query, relevant, evidence, registry)
		if len(takeaways) > 0 {
			prefix.WriteString("## Executive takeaway\n")
			for _, line := range takeaways {
				prefix.WriteString("- " + line + "\n")
			}
			prefix.WriteString("\n")
		}
	}
	if !answerHasSection(text, "Research landscape") {
		landscape := buildSynthesisLandscape(query, relevant, papers)
		prefix.WriteString("## Research landscape\n")
		prefix.WriteString(formatSynthesisLandscape(landscape))
		prefix.WriteString("\n\n")
	}
	if !answerHasSection(text, "Evidence mix") {
		if mix := buildSynthesisEvidenceMix(relevant); mix != "" {
			prefix.WriteString("## Evidence mix\n")
			prefix.WriteString(mix)
			prefix.WriteString("\n\n")
		}
	}
	if prefix.Len() == 0 {
		return text
	}
	return prefix.String() + strings.TrimSpace(text)
}

func buildLoopAwareRetrievalGaps(query string, relevant, all []search.Paper, evidence []EvidenceItem, gap *LoopGapState) []string {
	gaps := make([]string, 0, 8)
	if gap != nil {
		for _, aspect := range gap.MissingAspects {
			aspect = strings.TrimSpace(aspect)
			if aspect == "" {
				continue
			}
			gaps = append(gaps, "Coverage gap: "+aspect)
		}
		for _, sourceType := range gap.MissingSourceTypes {
			sourceType = strings.TrimSpace(sourceType)
			if sourceType == "" {
				continue
			}
			gaps = append(gaps, "Missing source type: "+sourceType)
		}
		for _, contradiction := range gap.Contradictions {
			contradiction = strings.TrimSpace(contradiction)
			if contradiction == "" {
				continue
			}
			gaps = append(gaps, "Unresolved contradiction: "+contradiction)
		}
		for _, nextQuery := range gap.NextQueries {
			nextQuery = strings.TrimSpace(nextQuery)
			if nextQuery == "" {
				continue
			}
			gaps = append(gaps, fmt.Sprintf("Suggested follow-up retrieval: %q", nextQuery))
		}
	}
	gaps = dedupeTrimmedStrings(append(gaps, buildSynthesisRetrievalGaps(query, relevant, all, evidence)...))
	if len(gaps) > 6 {
		gaps = gaps[:6]
	}
	return gaps
}

func filterNovelGapLines(text string, gaps []string) []string {
	if len(gaps) == 0 {
		return nil
	}
	lower := strings.ToLower(text)
	out := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		gap = strings.TrimSpace(gap)
		if gap == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(gap)) {
			continue
		}
		out = append(out, gap)
	}
	return out
}

func appendHeuristicSupplementalMatter(query string, text string, papers []search.Paper, evidence []EvidenceItem, gap *LoopGapState) string {
	relevant := filterPapersByQueryRelevance(query, papers)
	if len(relevant) == 0 {
		relevant = papers
	}
	loopOnly := filterNovelGapLines(text, buildLoopAwareRetrievalGaps(query, relevant, papers, evidence, gap))
	var suffix strings.Builder
	if len(loopOnly) > 0 {
		suffix.WriteString("\n\n## Loop critique gaps\n")
		for _, gapLine := range loopOnly {
			suffix.WriteString("- " + gapLine + "\n")
		}
	}
	if suffix.Len() == 0 {
		return text
	}
	return strings.TrimSpace(text) + suffix.String()
}

func appendResearcherBackMatter(query string, text string, papers []search.Paper, evidence []EvidenceItem, gap *LoopGapState) string {
	if isHeuristicSynthesisAnswer(text) {
		return appendHeuristicSupplementalMatter(query, text, papers, evidence, gap)
	}
	relevant := filterPapersByQueryRelevance(query, papers)
	if len(relevant) == 0 {
		relevant = papers
	}
	var suffix strings.Builder
	if !answerHasSection(text, "Retrieval gaps to address") {
		gaps := buildLoopAwareRetrievalGaps(query, relevant, papers, evidence, gap)
		if len(gaps) > 0 {
			suffix.WriteString("\n\n## Retrieval gaps to address\n")
			for _, gapLine := range gaps {
				suffix.WriteString("- " + gapLine + "\n")
			}
		}
	}
	if !answerHasSection(text, "Questions worth investigating") {
		prompts := buildSynthesisResearchPrompts(query, queryTopicClauses(query), relevant)
		if len(prompts) > 0 {
			suffix.WriteString("\n\n## Questions worth investigating\n")
			for idx, prompt := range prompts {
				suffix.WriteString(fmt.Sprintf("%d. %s\n", idx+1, prompt))
			}
		}
	}
	if suffix.Len() == 0 {
		return text
	}
	return strings.TrimSpace(text) + suffix.String()
}

func enrichParagraphWithCitations(paragraph string, papers []search.Paper, evidence []EvidenceItem, registry *citationRegistry, used map[string]struct{}, query string) string {
	paragraph = strings.TrimSpace(paragraph)
	if paragraph == "" {
		return ""
	}
	if answerNumberedCitationRe.MatchString(paragraph) && answerYearCitationRe.MatchString(paragraph) {
		return paragraph
	}
	// Trust the synthesizer's own grounding: when the model already attached an
	// author-year citation (rendered from its per-sentence evidenceIds), the
	// paragraph IS source-linked. Re-deriving citations by lexical token overlap
	// here previously stamped a false "requires verification" warning over
	// semantically (not lexically) grounded prose and piled on duplicate
	// citations. Leave the model's grounding intact instead.
	if answerYearCitationRe.MatchString(paragraph) {
		return paragraph
	}
	if len(paragraph) >= sentenceCitationMinParagraphLen {
		sentences := citationSafeProseSentences(paragraph, 0)
		if len(sentences) >= 2 {
			parts := make([]string, 0, len(sentences))
			for _, sentence := range sentences {
				sentence = strings.TrimSpace(sentence)
				if sentence == "" {
					continue
				}
				// Leave sentences that already carry a citation or marker intact so
				// enrichment never stacks a second citation onto them.
				if answerNumberedCitationRe.MatchString(sentence) || answerYearCitationRe.MatchString(sentence) {
					parts = append(parts, sentence)
					continue
				}
				if !strings.HasSuffix(sentence, ".") && !strings.HasSuffix(sentence, "!") && !strings.HasSuffix(sentence, "?") {
					sentence += "."
				}
				paper, score := bestPaperForParagraphWithQuery(sentence, papers, evidence, registry, used, query)
				key := strings.ToLower(strings.TrimSpace(firstNonEmpty(paper.Title, paper.ID)))
				if key != "" {
					used[key] = struct{}{}
					sentence = appendParagraphCitation(sentence, paper, score, registry)
				} else if !strings.Contains(sentence, groundingWarningTag) {
					sentence = strings.TrimRight(sentence, ".") + ". " + groundingWarningTag
				}
				parts = append(parts, sentence)
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	paper, score := bestPaperForParagraphWithQuery(paragraph, papers, evidence, registry, used, query)
	key := strings.ToLower(strings.TrimSpace(firstNonEmpty(paper.Title, paper.ID)))
	if key != "" {
		used[key] = struct{}{}
		return appendParagraphCitation(paragraph, paper, score, registry)
	}
	if !strings.Contains(paragraph, groundingWarningTag) {
		paragraph = strings.TrimRight(paragraph, ".") + ". " + groundingWarningTag
	}
	return paragraph
}

func bestPaperForParagraphWithQuery(paragraph string, papers []search.Paper, evidence []EvidenceItem, registry *citationRegistry, used map[string]struct{}, query string) (search.Paper, int) {
	best := search.Paper{}
	bestScore := -1
	for _, paper := range papers {
		if paragraphAlreadyCitesPaper(paragraph, paper, registry) {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(paper.Title, paper.ID)))
		score := paragraphPaperOverlapScoreWithQuery(paragraph, paper, query)
		score += evidencePaperOverlapBoost(paragraph, paper, evidence)
		if _, seen := used[key]; seen {
			score--
		}
		if score > bestScore {
			bestScore = score
			best = paper
		}
	}
	if strings.TrimSpace(firstNonEmpty(best.Title, best.ID)) != "" {
		return best, bestScore
	}
	fallback := bestPaperForParagraph(paragraph, papers, registry, used)
	if strings.TrimSpace(firstNonEmpty(fallback.Title, fallback.ID)) == "" {
		return search.Paper{}, -1
	}
	return fallback, paragraphPaperOverlapScoreWithQuery(paragraph, fallback, query) + evidencePaperOverlapBoost(paragraph, fallback, evidence)
}

func paragraphPaperOverlapScoreWithQuery(paragraph string, paper search.Paper, query string) int {
	score := paragraphPaperOverlapScore(paragraph, paper)
	for _, anchor := range queryAnchorTerms(query) {
		if len(anchor) < 3 {
			continue
		}
		if strings.Contains(strings.ToLower(paragraph), anchor) &&
			strings.Contains(strings.ToLower(paper.Title+" "+paper.Abstract), anchor) {
			score += 2
		}
	}
	return score
}

func enrichProseAnswerWithInlineCitationsForQuery(query string, text string, papers []search.Paper, evidence []EvidenceItem) string {
	text = polishSynthesisText(text)
	text = whitespaceBeforePeriodRe.ReplaceAllString(text, ".")
	if strings.TrimSpace(text) == "" || len(papers) == 0 {
		return text
	}
	if isHeuristicSynthesisAnswer(text) {
		return text
	}
	if answerNumberedCitationRe.MatchString(text) && strings.Contains(strings.ToLower(text), "references cited in this synthesis") {
		return text
	}

	relevant := filterPapersByQueryRelevance(query, papers)
	if len(relevant) == 0 {
		relevant = papers
	}
	registry := buildCitationRegistry(relevant)
	relevant = registry.papers
	blocks := strings.Split(text, "\n\n")
	enriched := make([]string, 0, len(blocks)+2)
	usedPapers := make(map[string]struct{}, len(relevant))

	for _, block := range blocks {
		for _, piece := range splitMarkdownBlockForEnrichment(block) {
			trimmed := strings.TrimSpace(piece)
			if trimmed == "" {
				continue
			}
			lower := strings.ToLower(trimmed)
			if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "- ") {
				enriched = append(enriched, trimmed)
				continue
			}
			if strings.HasPrefix(lower, "references cited in this synthesis") {
				enriched = append(enriched, trimmed)
				continue
			}
			if isNumberedResearchListItem(trimmed) {
				enriched = append(enriched, trimmed)
				continue
			}

			enriched = append(enriched, enrichParagraphWithCitations(trimmed, relevant, evidence, registry, usedPapers, query))
		}
	}

	result := strings.Join(enriched, "\n\n")
	if !strings.Contains(strings.ToLower(result), "references cited in this synthesis") {
		bibLines := formatSynthesisBibliography(relevant, 12, registry)
		if len(bibLines) > 0 {
			result += "\n\n## References cited in this synthesis\n"
			for _, line := range bibLines {
				result += line + "\n"
			}
		}
	}
	if !strings.Contains(result, "> Inline citations") {
		result = "> Inline citations use numbered references [n] with author-year metadata; bibliography at end.\n\n" + result
	}
	return polishSynthesisText(dedupeCitationArtifacts(result))
}

func isNumberedResearchListItem(line string) bool {
	dot := strings.Index(line, ". ")
	if dot <= 0 || dot > 3 {
		return false
	}
	for _, r := range line[:dot] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func finalizeResearchAnswer(query string, text string, papers []search.Paper, evidence []EvidenceItem, gap *LoopGapState) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = prependResearcherFrontMatter(query, text, papers, evidence)
	text = enrichProseAnswerWithInlineCitationsForQuery(query, text, papers, evidence)
	text = appendGroundingAuditSection(text, evidence)
	text = appendResearcherBackMatter(query, text, papers, evidence, gap)
	return polishSynthesisText(text)
}

func detectSynthesisMode(answer string) string {
	if isHeuristicSynthesisAnswer(answer) {
		return "heuristic"
	}
	return "llm"
}

func renderStructuredAnswerWithInlineCitations(query string, answer *rag.StructuredAnswer, papers []search.Paper, evidence []EvidenceItem, gap *LoopGapState) string {
	if answer == nil {
		return ""
	}
	index := buildPaperCitationIndex(papers)
	resolve := func(evidenceIDs []string) string {
		return resolveInlineCitationsParenthetical(evidenceIDs, index)
	}
	var rendered string
	if text := strings.TrimSpace(answer.Text); text != "" {
		rendered = text
	} else if hasEvidenceLinkedSections(answer.Sections) {
		rendered = strings.TrimSpace(rag.RenderAnswerSectionsWithCitations(answer.Sections, resolve))
	} else {
		rendered = strings.TrimSpace(rag.RenderAnswerSectionsWithCitations(answer.Sections, resolve))
	}
	if rendered == "" {
		return ""
	}
	return finalizeResearchAnswer(query, rendered, papers, evidence, gap)
}
