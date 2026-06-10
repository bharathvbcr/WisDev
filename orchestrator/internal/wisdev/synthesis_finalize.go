package wisdev

import (
	"fmt"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

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
	if len(paragraph) >= sentenceCitationMinParagraphLen {
		sentences := splitEvidenceSentences(paragraph, 6)
		if len(sentences) >= 2 {
			parts := make([]string, 0, len(sentences))
			for _, sentence := range sentences {
				sentence = strings.TrimSpace(sentence)
				if sentence == "" {
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
	return polishSynthesisText(result)
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
