package wisdev

import (
	"sort"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// EvidenceSpan anchors a claim to a specific sentence of source text, so that
// confidence in a hypothesis can be traced back to the exact wording that
// supports it rather than to paper-level metadata.
type EvidenceSpan struct {
	PaperID    string  `json:"paperId,omitempty"`
	PaperTitle string  `json:"paperTitle,omitempty"`
	Quote      string  `json:"quote"`
	MatchScore float64 `json:"matchScore"`
}

const (
	maxEvidenceSpansPerFinding = 3
	minSpanMatchScore          = 0.15
	maxSpanQuoteLength         = 320
	maxEvidenceFullTextChars   = 6000
)

// evidenceTextFromPaper returns the richest grounding text available for a
// paper: the extracted full text (bounded) when the document pipeline has
// populated it, otherwise the abstract. Findings built from this text give
// span anchoring real body sentences to quote instead of abstract-only.
func evidenceTextFromPaper(p search.Paper) string {
	if full := strings.TrimSpace(p.FullText); full != "" {
		if len(full) > maxEvidenceFullTextChars {
			return full[:maxEvidenceFullTextChars]
		}
		return full
	}
	return p.Abstract
}

// extractEvidenceSpans selects the sentences of a finding's snippet (abstract)
// that best support the given claim, scored by keyword overlap. Returns at
// most maxEvidenceSpansPerFinding spans, best first.
func extractEvidenceSpans(claim string, finding *EvidenceFinding) []EvidenceSpan {
	if finding == nil {
		return nil
	}
	keywords := claimKeywords(claim)
	if len(keywords) == 0 {
		return nil
	}
	sentences := splitSpanSentences(finding.Snippet)
	if len(sentences) == 0 {
		return nil
	}

	spans := make([]EvidenceSpan, 0, len(sentences))
	for _, sentence := range sentences {
		score := spanMatchScore(keywords, sentence)
		if score < minSpanMatchScore {
			continue
		}
		quote := sentence
		if len(quote) > maxSpanQuoteLength {
			quote = strings.TrimSpace(quote[:maxSpanQuoteLength]) + "…"
		}
		spans = append(spans, EvidenceSpan{
			PaperID:    finding.SourceID,
			PaperTitle: finding.PaperTitle,
			Quote:      quote,
			MatchScore: score,
		})
	}

	sort.SliceStable(spans, func(i, j int) bool {
		return spans[i].MatchScore > spans[j].MatchScore
	})
	if len(spans) > maxEvidenceSpansPerFinding {
		spans = spans[:maxEvidenceSpansPerFinding]
	}
	return spans
}

// claimKeywords extracts the meaningful lowercase tokens (length >= 4) of a claim.
func claimKeywords(claim string) []string {
	var keywords []string
	for _, token := range strings.Fields(strings.ToLower(claim)) {
		token = strings.Trim(token, ".,;:!?()[]\"'")
		if len(token) >= 4 {
			keywords = append(keywords, token)
		}
	}
	return keywords
}

// spanMatchScore is the fraction of claim keywords present in the sentence.
func spanMatchScore(keywords []string, sentence string) float64 {
	if len(keywords) == 0 {
		return 0
	}
	body := strings.ToLower(sentence)
	matched := 0
	for _, kw := range keywords {
		if strings.Contains(body, kw) {
			matched++
		}
	}
	return float64(matched) / float64(len(keywords))
}

// splitSpanSentences splits abstract text into trimmed sentences, dropping
// fragments too short to anchor a claim.
func splitSpanSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var sentences []string
	var current strings.Builder
	for _, r := range text {
		current.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			sentence := strings.TrimSpace(current.String())
			if len(sentence) >= 30 {
				sentences = append(sentences, sentence)
			}
			current.Reset()
		}
	}
	if tail := strings.TrimSpace(current.String()); len(tail) >= 30 {
		sentences = append(sentences, tail)
	}
	return sentences
}
