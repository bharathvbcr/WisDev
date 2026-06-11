package wisdev

import (
	"fmt"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func evidenceItemMatchesPaper(item EvidenceItem, paper search.Paper) bool {
	itemID := strings.ToLower(strings.TrimSpace(item.PaperID))
	if itemID == "" {
		return false
	}
	for _, key := range paperCitationKeys(paper) {
		if strings.EqualFold(strings.TrimSpace(key), itemID) {
			return true
		}
	}
	return false
}

func paragraphEvidenceOverlapScore(paragraph string, item EvidenceItem) int {
	paragraph = strings.ToLower(strings.TrimSpace(paragraph))
	if paragraph == "" {
		return 0
	}
	score := 0
	for _, token := range loopEvidenceTokens(strings.TrimSpace(item.Claim + " " + item.Snippet)) {
		if len(token) < 4 {
			continue
		}
		if strings.Contains(paragraph, token) {
			score += 2
		}
	}
	return score
}

func evidencePaperOverlapBoost(paragraph string, paper search.Paper, evidence []EvidenceItem) int {
	boost := 0
	for _, item := range evidence {
		if !evidenceItemMatchesPaper(item, paper) {
			continue
		}
		boost += paragraphEvidenceOverlapScore(paragraph, item)
	}
	return boost
}

type synthesisGroundingStats struct {
	ProseParagraphs int
	CitedParagraphs int
	FlaggedParagraphs int
}

func collectSynthesisGroundingStats(text string) synthesisGroundingStats {
	stats := synthesisGroundingStats{}
	for _, block := range strings.Split(text, "\n\n") {
		for _, piece := range splitMarkdownBlockForEnrichment(block) {
			trimmed := strings.TrimSpace(piece)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "- ") {
				continue
			}
			lower := strings.ToLower(trimmed)
			if strings.HasPrefix(lower, "references cited in this synthesis") || strings.HasPrefix(lower, "grounding audit") {
				continue
			}
			if isNumberedResearchListItem(trimmed) {
				continue
			}
			stats.ProseParagraphs++
			switch {
			case strings.Contains(trimmed, groundingWarningTag):
				stats.FlaggedParagraphs++
			case answerYearCitationRe.MatchString(trimmed):
				// Author-year citation present (numbered marker optional): the
				// paragraph is tied to a retrieved source, either via the model's
				// per-sentence evidenceIds or numbered citation enrichment.
				stats.CitedParagraphs++
			}
		}
	}
	return stats
}

func buildSynthesisGroundingAudit(text string, evidence []EvidenceItem) string {
	stats := collectSynthesisGroundingStats(text)
	if stats.ProseParagraphs == 0 {
		return ""
	}
	lines := make([]string, 0, 4)
	lines = append(lines, fmt.Sprintf("%d prose paragraph(s) scanned for source linkage.", stats.ProseParagraphs))
	if stats.CitedParagraphs > 0 {
		lines = append(lines, fmt.Sprintf("%d paragraph(s) carry author-year citations tied to retrieved sources.", stats.CitedParagraphs))
	}
	if stats.FlaggedParagraphs > 0 {
		lines = append(lines, fmt.Sprintf("%d paragraph(s) are flagged %s because source overlap was weak — verify against full texts.", stats.FlaggedParagraphs, groundingWarningTag))
	}
	if len(evidence) > 0 {
		lines = append(lines, fmt.Sprintf("%d grounded evidence snippet(s) were available during synthesis; prefer snippet-backed claims over abstract-only inference.", len(evidence)))
	} else {
		lines = append(lines, "No grounded evidence snippets were extracted — treat uncited claims as provisional.")
	}
	if stats.CitedParagraphs == 0 && stats.FlaggedParagraphs == 0 {
		lines = append(lines, "Citation enrichment did not attach numbered references; read sources pane before citing this answer.")
	}
	return strings.Join(lines, "\n")
}

func appendGroundingAuditSection(text string, evidence []EvidenceItem) string {
	if answerHasSection(text, "Grounding audit") {
		return text
	}
	audit := strings.TrimSpace(buildSynthesisGroundingAudit(text, evidence))
	if audit == "" {
		return text
	}
	return strings.TrimSpace(text) + "\n\n## Grounding audit\n" + audit
}
