package wisdev

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

var (
	answerNumberedCitationRe = regexp.MustCompile(`\[\d+\]`)
	answerYearCitationRe     = regexp.MustCompile(`\([A-Za-z][^)]*,\s*(?:19|20)\d{2}`)
	whitespaceBeforePeriodRe = regexp.MustCompile(`\s+\.`)
)

const (
	minCitationOverlapScore = 2
	groundingWarningTag     = "[requires verification against retrieved sources]"
)

// SynthesisPipelineVersion is shown in the TUI status line so users can confirm they run a build with inline citation enrichment.
const SynthesisPipelineVersion = "inline-citations-v5"

func answerHasInlineCitations(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if answerNumberedCitationRe.MatchString(text) {
		return true
	}
	if answerYearCitationRe.MatchString(text) {
		return true
	}
	lower := strings.ToLower(text)
	return strings.Contains(lower, "references cited in this synthesis")
}

func formatParagraphCitationSuffix(registry *citationRegistry, paper search.Paper) string {
	parenthetical := formatInlineParentheticalCitation(paper)
	if registry == nil {
		return " " + parenthetical
	}
	if marker := registry.marker(paper); marker != "" {
		return " " + marker + " " + parenthetical
	}
	return " " + parenthetical
}

func paragraphAlreadyCitesPaper(paragraph string, paper search.Paper, registry *citationRegistry) bool {
	paragraph = strings.TrimSpace(paragraph)
	if paragraph == "" {
		return false
	}
	if registry != nil {
		if marker := registry.marker(paper); marker != "" && strings.Contains(paragraph, marker) {
			return true
		}
	}
	if paper.Year > 0 {
		year := fmt.Sprintf("%d", paper.Year)
		if strings.Contains(paragraph, "("+year) || strings.Contains(paragraph, ", "+year) {
			return true
		}
	}
	if authors := formatSynthesisAuthors(paper.Authors, 1); authors != "" {
		lastName := strings.TrimSpace(strings.Split(authors, ",")[0])
		if idx := strings.IndexByte(lastName, ' '); idx >= 0 {
			lastName = strings.TrimSpace(lastName[idx+1:])
		}
		if len(lastName) > 2 && strings.Contains(paragraph, lastName) {
			return true
		}
	}
	title := strings.TrimSpace(paper.Title)
	if title != "" && len(title) > 16 && strings.Contains(paragraph, title) {
		return true
	}
	return false
}

func paragraphPaperOverlapScore(paragraph string, paper search.Paper) int {
	paragraph = strings.ToLower(strings.TrimSpace(paragraph))
	if paragraph == "" {
		return 0
	}
	paperText := strings.ToLower(strings.TrimSpace(paper.Title + " " + paper.Abstract))
	if paperText == "" {
		return 0
	}
	score := 0
	for _, token := range loopEvidenceTokens(paragraph) {
		if len(token) < 4 {
			continue
		}
		if strings.Contains(paperText, token) {
			score++
		}
	}
	for _, word := range strings.Fields(strings.ToLower(paper.Title)) {
		clean := strings.Trim(word, ".,;:")
		if len(clean) > 4 && strings.Contains(paragraph, clean) {
			score += 3
		}
	}
	return score
}

func splitMarkdownBlockForEnrichment(block string) []string {
	trimmed := strings.TrimSpace(block)
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	first := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(first, "#") {
		return []string{trimmed}
	}
	parts := []string{first}
	body := strings.TrimSpace(strings.Join(lines[1:], "\n"))
	if body != "" {
		parts = append(parts, body)
	}
	return parts
}

func appendParagraphCitation(paragraph string, paper search.Paper, score int, registry *citationRegistry) string {
	paragraph = strings.TrimSpace(paragraph)
	if paragraph == "" {
		return ""
	}
	if paragraphAlreadyCitesPaper(paragraph, paper, registry) {
		return paragraph
	}
	if score < minCitationOverlapScore {
		if !strings.Contains(paragraph, groundingWarningTag) {
			paragraph = strings.TrimRight(paragraph, ".") + ". " + groundingWarningTag
		}
		return paragraph
	}
	return strings.TrimRight(paragraph, ".") + "." + formatParagraphCitationSuffix(registry, paper)
}

func bestPaperForParagraph(paragraph string, papers []search.Paper, registry *citationRegistry, used map[string]struct{}) search.Paper {
	best := search.Paper{}
	bestScore := -1
	for _, paper := range papers {
		if paragraphAlreadyCitesPaper(paragraph, paper, registry) {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(paper.Title, paper.ID)))
		score := paragraphPaperOverlapScore(paragraph, paper)
		if _, seen := used[key]; seen {
			score--
		}
		if score > bestScore {
			bestScore = score
			best = paper
		}
	}
	if strings.TrimSpace(firstNonEmpty(best.Title, best.ID)) != "" {
		return best
	}
	for _, paper := range papers {
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(paper.Title, paper.ID)))
		if key == "" {
			continue
		}
		if _, seen := used[key]; seen {
			continue
		}
		return paper
	}
	return search.Paper{}
}

func enrichProseAnswerWithInlineCitations(text string, papers []search.Paper) string {
	return enrichProseAnswerWithInlineCitationsForQuery("", text, papers, nil)
}
