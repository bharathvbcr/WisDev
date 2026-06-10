package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

// answerBibliographyHeading is the section the synthesis pipeline appends to
// final answers; its "- [n] Author. Title. …" lines define the [n] numbering
// used by inline citation markers.
const answerBibliographyHeading = "references cited in this synthesis"

var (
	answerBibliographyLineRe = regexp.MustCompile(`^-\s*\[(\d+)\]\s*(.+)$`)
	citationTokenSplitRe     = regexp.MustCompile(`[^a-z0-9]+`)
)

// answerBibliographyEntry is one parsed "- [n] …" bibliography line.
type answerBibliographyEntry struct {
	Number int
	Text   string
}

// parseAnswerBibliography extracts the numbered bibliography entries from the
// "## References cited in this synthesis" section of a final answer. Garbled
// or unnumbered lines are skipped; a missing section returns nil.
func parseAnswerBibliography(answer string) []answerBibliographyEntry {
	if strings.TrimSpace(answer) == "" {
		return nil
	}
	var entries []answerBibliographyEntry
	inSection := false
	for _, raw := range strings.Split(answer, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#") {
			heading := strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "# ")))
			inSection = heading == answerBibliographyHeading
			continue
		}
		if !inSection || line == "" {
			continue
		}
		match := answerBibliographyLineRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		number, err := strconv.Atoi(match[1])
		if err != nil || number <= 0 {
			continue
		}
		text := strings.TrimSpace(match[2])
		if text == "" {
			continue
		}
		entries = append(entries, answerBibliographyEntry{Number: number, Text: text})
	}
	return entries
}

// normalizeCitationText lowercases and reduces text to space-separated
// alphanumeric tokens so punctuation and markdown emphasis never block a match.
func normalizeCitationText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	return strings.TrimSpace(citationTokenSplitRe.ReplaceAllString(text, " "))
}

// matchCitationEntryToPaper finds the index of the paper whose title best
// matches a bibliography entry line. It first tries normalized title
// containment, then token-overlap scoring (≥ 60% of title tokens present).
// Returns -1 when nothing matches confidently.
func matchCitationEntryToPaper(entryText string, papers []agent.Paper) int {
	normEntry := normalizeCitationText(entryText)
	if normEntry == "" {
		return -1
	}
	paddedEntry := " " + normEntry + " "

	bestIdx := -1
	bestScore := 0.0
	for idx, paper := range papers {
		normTitle := normalizeCitationText(paper.Title)
		if normTitle == "" {
			continue
		}
		if strings.Contains(paddedEntry, " "+normTitle+" ") {
			// Exact normalized containment: prefer the longest contained title.
			score := 1.0 + float64(len(normTitle))
			if score > bestScore {
				bestScore = score
				bestIdx = idx
			}
			continue
		}
		titleTokens := strings.Fields(normTitle)
		if len(titleTokens) == 0 {
			continue
		}
		entryTokens := make(map[string]struct{})
		for _, token := range strings.Fields(normEntry) {
			entryTokens[token] = struct{}{}
		}
		matched := 0
		for _, token := range titleTokens {
			if _, ok := entryTokens[token]; ok {
				matched++
			}
		}
		overlap := float64(matched) / float64(len(titleTokens))
		if overlap >= 0.6 && overlap > bestScore {
			bestScore = overlap
			bestIdx = idx
		}
	}
	return bestIdx
}

// buildCitationPaperMap maps inline citation numbers [n] from the answer's
// bibliography section to indexes into result.Papers. Entries whose titles
// cannot be matched against any paper are omitted.
func buildCitationPaperMap(result *agent.YOLOResult) map[int]int {
	if result == nil || len(result.Papers) == 0 {
		return nil
	}
	entries := parseAnswerBibliography(result.FinalAnswer)
	if len(entries) == 0 {
		return nil
	}
	mapping := make(map[int]int, len(entries))
	for _, entry := range entries {
		if _, exists := mapping[entry.Number]; exists {
			continue
		}
		if idx := matchCitationEntryToPaper(entry.Text, result.Papers); idx >= 0 {
			mapping[entry.Number] = idx
		}
	}
	if len(mapping) == 0 {
		return nil
	}
	return mapping
}

// formatGroundingScorecard renders the one-line grounding summary, e.g.
// "Grounding: 14/16 claims cited · 5 sources · 2 unsupported". Returns ""
// when no claim-level stats are available.
func formatGroundingScorecard(grounding *agent.GroundingStats) string {
	if grounding == nil || grounding.TotalClaims <= 0 {
		return ""
	}
	card := fmt.Sprintf("Grounding: %d/%d claims cited · %d source(s)",
		grounding.GroundedClaims, grounding.TotalClaims, grounding.CitedSources)
	if grounding.UnsupportedClaims > 0 {
		card += fmt.Sprintf(" · %d unsupported", grounding.UnsupportedClaims)
	}
	return card
}

// groundingNeedsAttention reports whether the scorecard should render in the
// warning style: any unsupported claims, or under 70% of claims cited.
func groundingNeedsAttention(grounding *agent.GroundingStats) bool {
	if grounding == nil || grounding.TotalClaims <= 0 {
		return false
	}
	if grounding.UnsupportedClaims > 0 {
		return true
	}
	return float64(grounding.GroundedClaims)/float64(grounding.TotalClaims) < 0.7
}

// sectionGroundingSuffix returns a per-section citation coverage suffix like
// "(3/4 cited)" when the rendered "## heading" matches a structured-answer
// section by normalized title. Returns "" when stats are unavailable.
func sectionGroundingSuffix(grounding *agent.GroundingStats, heading string) string {
	if grounding == nil {
		return ""
	}
	normHeading := normalizeCitationText(heading)
	if normHeading == "" {
		return ""
	}
	for _, section := range grounding.Sections {
		if section.TotalClaims <= 0 {
			continue
		}
		if normalizeCitationText(section.Heading) == normHeading {
			return fmt.Sprintf("(%d/%d cited)", section.GroundedClaims, section.TotalClaims)
		}
	}
	return ""
}
