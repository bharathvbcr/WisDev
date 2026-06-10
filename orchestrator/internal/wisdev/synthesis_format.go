package wisdev

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

const (
	synthesisPaperSummaryLimit   = 360
	synthesisEvidenceSnippetLimit = 320
	synthesisSectionLabelLimit   = 56
)

func formatSourceBibliographicMeta(source Source) string {
	paper := search.Paper{
		Authors:       source.Authors,
		Year:          source.Year,
		CitationCount: source.CitationCount,
		Venue:         firstNonEmpty(source.Publication, source.SiteName),
	}
	return formatSynthesisPaperMeta(paper)
}

func paperSourceLabel(paper search.Paper) string {
	if authors := formatSynthesisAuthors(paper.Authors, 2); authors != "" {
		return authors
	}
	if venue := strings.TrimSpace(paper.Venue); venue != "" {
		return truncateAtWordBoundary(venue, 42)
	}
	if source := strings.TrimSpace(paper.Source); source != "" {
		return titleCasePhrase(source) + " source"
	}
	if title := strings.TrimSpace(paper.Title); title != "" {
		return truncateAtWordBoundary(title, 36)
	}
	return "Retrieved source"
}

func formatInlineNarrativeCitation(paper search.Paper) string {
	authors := paperSourceLabel(paper)
	switch {
	case paper.Year > 0 && paper.CitationCount > 0:
		return fmt.Sprintf("%s (%d, %d citations)", authors, paper.Year, paper.CitationCount)
	case paper.Year > 0:
		return fmt.Sprintf("%s (%d)", authors, paper.Year)
	case paper.CitationCount > 0:
		return fmt.Sprintf("%s (%d citations)", authors, paper.CitationCount)
	default:
		return authors
	}
}

func formatInlineParentheticalCitation(paper search.Paper) string {
	authors := paperSourceLabel(paper)
	switch {
	case paper.Year > 0 && paper.CitationCount > 0:
		return fmt.Sprintf("(%s, %d; %d citations)", authors, paper.Year, paper.CitationCount)
	case paper.Year > 0:
		return fmt.Sprintf("(%s, %d)", authors, paper.Year)
	case paper.CitationCount > 0:
		return fmt.Sprintf("(%s; %d citations)", authors, paper.CitationCount)
	default:
		return "(" + authors + ")"
	}
}

func buildPaperCitationIndex(papers []search.Paper) map[string]string {
	index := make(map[string]string, len(papers)*3)
	for _, paper := range papers {
		cite := formatInlineParentheticalCitation(paper)
		for _, key := range paperCitationKeys(paper) {
			if key != "" {
				index[key] = cite
			}
		}
	}
	return index
}

func buildSourceCitationIndex(sources []Source) map[string]string {
	papers := make([]search.Paper, 0, len(sources))
	for _, source := range sources {
		papers = append(papers, search.Paper{
			ID:            source.ID,
			Title:         source.Title,
			DOI:           source.DOI,
			ArxivID:       source.ArxivID,
			Authors:       source.Authors,
			Year:          source.Year,
			CitationCount: source.CitationCount,
			Venue:         firstNonEmpty(source.Publication, source.SiteName),
		})
	}
	return buildPaperCitationIndex(papers)
}

func paperCitationKeys(paper search.Paper) []string {
	keys := []string{
		strings.TrimSpace(paper.ID),
		strings.TrimSpace(paper.DOI),
		strings.TrimSpace(paper.ArxivID),
	}
	if title := strings.ToLower(strings.TrimSpace(paper.Title)); title != "" {
		keys = append(keys, title)
	}
	return keys
}

func resolveInlineCitationsParenthetical(evidenceIDs []string, index map[string]string) string {
	if len(evidenceIDs) == 0 || len(index) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(evidenceIDs))
	cites := make([]string, 0, len(evidenceIDs))
	for _, rawID := range evidenceIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		cite, ok := index[id]
		if !ok {
			cite, ok = index[strings.ToLower(id)]
		}
		if !ok || cite == "" {
			continue
		}
		if _, exists := seen[cite]; exists {
			continue
		}
		seen[cite] = struct{}{}
		cites = append(cites, cite)
	}
	if len(cites) == 0 {
		return ""
	}
	if len(cites) == 1 {
		return cites[0]
	}
	parts := make([]string, 0, len(cites))
	for _, cite := range cites {
		cite = strings.TrimSpace(cite)
		cite = strings.TrimPrefix(cite, "(")
		cite = strings.TrimSuffix(cite, ")")
		if cite != "" {
			parts = append(parts, cite)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, "; ") + ")"
}

func hasEvidenceLinkedSections(sections []rag.AnswerSection) bool {
	for _, section := range sections {
		for _, sentence := range section.Sentences {
			if len(sentence.EvidenceIDs) > 0 && strings.TrimSpace(sentence.Text) != "" {
				return true
			}
		}
	}
	return false
}

func formatSynthesisAuthors(authors []string, maxAuthors int) string {
	if maxAuthors <= 0 {
		maxAuthors = 2
	}
	clean := make([]string, 0, len(authors))
	for _, author := range authors {
		if trimmed := strings.TrimSpace(author); trimmed != "" {
			clean = append(clean, trimmed)
		}
	}
	switch len(clean) {
	case 0:
		return ""
	case 1:
		return clean[0]
	case 2:
		return clean[0] + " & " + clean[1]
	default:
		if len(clean) > maxAuthors {
			return strings.Join(clean[:maxAuthors], ", ") + ", et al."
		}
		return strings.Join(clean, ", ")
	}
}

func formatSynthesisPaperMeta(paper search.Paper) string {
	parts := make([]string, 0, 4)
	if authors := formatSynthesisAuthors(paper.Authors, 2); authors != "" {
		if paper.Year > 0 {
			parts = append(parts, fmt.Sprintf("%s (%d)", authors, paper.Year))
		} else {
			parts = append(parts, authors)
		}
	} else if paper.Year > 0 {
		parts = append(parts, fmt.Sprintf("(%d)", paper.Year))
	}
	if paper.CitationCount > 0 {
		parts = append(parts, fmt.Sprintf("%d citations", paper.CitationCount))
	}
	if venue := strings.TrimSpace(paper.Venue); venue != "" {
		parts = append(parts, venue)
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, " · ")
}

func formatSynthesisPaperBullets(query string, papers []search.Paper, evidence []EvidenceItem, limit int, registry *citationRegistry) []string {
	if limit <= 0 {
		limit = 4
	}
	papers = search.SortPapersByPreferenceWithQuery(papers, query)
	lines := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, paper := range papers {
		if len(lines) >= limit {
			break
		}
		title := strings.TrimSpace(firstNonEmpty(paper.Title, paper.ID))
		if title == "" {
			continue
		}
		key := strings.ToLower(title)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		summary := strings.TrimSpace(firstNonEmpty(paper.Abstract, paper.FullText, paper.Venue))
		inline := formatNumberedInlineCitation(registry, paper)
		design := detectStudyDesignLabel(paper)
		designTag := ""
		if design != "" {
			designTag = " [" + design + "]"
		}
		strengthTag := formatEvidenceStrengthTag(paper, paperHasGroundedEvidence(paper, evidence))
		if summary == "" {
			lines = append(lines, fmt.Sprintf("- %s%s%s: *%s*.", inline, designTag, strengthTag, title))
			continue
		}
		sentence := firstEvidenceSentence(summary)
		if sentence == "" {
			sentence = trimEvidenceText(summary, synthesisPaperSummaryLimit)
		}
		lines = append(lines, fmt.Sprintf("- %s%s%s %s that %s (*%s*).", inline, designTag, strengthTag, inlineReportVerb(paper), sentence, title))
	}
	return lines
}

func inlineReportVerb(paper search.Paper) string {
	if len(paper.Authors) > 1 || strings.Contains(formatSynthesisAuthors(paper.Authors, 2), "et al.") {
		return "report"
	}
	return "reports"
}

func formatSynthesisEvidenceBullets(evidence []EvidenceItem, papers []search.Paper, limit int, registry *citationRegistry) []string {
	if limit <= 0 {
		limit = 4
	}
	paperByTitle := make(map[string]search.Paper, len(papers))
	for _, paper := range papers {
		title := strings.ToLower(strings.TrimSpace(firstNonEmpty(paper.Title, paper.ID)))
		if title != "" {
			paperByTitle[title] = paper
		}
	}
	lines := make([]string, 0, limit)
	seenTitles := make(map[string]int, limit)
	for _, item := range evidence {
		if len(lines) >= limit {
			break
		}
		title := strings.TrimSpace(firstNonEmpty(item.PaperTitle, item.PaperID))
		if title == "" {
			continue
		}
		key := strings.ToLower(title)
		if seenTitles[key] >= 1 {
			continue
		}
		claim := strings.TrimSpace(firstNonEmpty(item.Claim, item.Snippet))
		snippet := strings.TrimSpace(item.Snippet)
		if strings.EqualFold(claim, title) {
			claim = ""
		}
		text := firstNonEmpty(snippet, claim)
		if text == "" {
			continue
		}
		seenTitles[key]++
		inline := ""
		verb := "reports"
		strengthTag := " [grounded evidence]"
		if paper, ok := paperByTitle[key]; ok {
			inline = formatNumberedInlineCitation(registry, paper)
			verb = inlineReportVerb(paper)
			strengthTag = formatEvidenceStrengthTag(paper, true)
		}
		sentence := firstEvidenceSentence(text)
		if sentence == "" {
			sentence = trimEvidenceText(text, synthesisEvidenceSnippetLimit)
		}
		if inline != "" {
			lines = append(lines, fmt.Sprintf("- %s%s %s that %s (*%s*).", inline, strengthTag, verb, sentence, title))
		} else {
			lines = append(lines, fmt.Sprintf("- %s: %s", title, sentence))
		}
	}
	return lines
}

func synthesisSectionLabel(clause string) string {
	clause = strings.TrimSpace(clause)
	if clause == "" {
		return ""
	}
	if len(clause) > synthesisSectionLabelLimit {
		return titleCasePhrase(truncateAtWordBoundary(clause, synthesisSectionLabelLimit))
	}
	return titleCasePhrase(clause)
}

func titleCasePhrase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	words := strings.Fields(value)
	for idx, word := range words {
		runes := []rune(word)
		if len(runes) == 0 {
			continue
		}
		runes[0] = unicode.ToUpper(runes[0])
		words[idx] = string(runes)
	}
	return strings.Join(words, " ")
}

func truncateAtWordBoundary(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" || limit <= 0 || len(text) <= limit {
		return text
	}
	cut := text[:limit]
	if idx := strings.LastIndexByte(cut, ' '); idx > limit/2 {
		return strings.TrimSpace(cut[:idx])
	}
	return strings.TrimSpace(cut)
}

type synthesisLandscape struct {
	YearMin          int
	YearMax          int
	MedianCites      int
	TotalCites       int
	TopVenue         string
	ProviderSummary  string
	OnTopicCount     int
	RetrievedCount   int
}

func buildSynthesisLandscape(query string, relevant, all []search.Paper) synthesisLandscape {
	landscape := synthesisLandscape{
		OnTopicCount:   len(relevant),
		RetrievedCount: len(all),
	}
	if len(relevant) == 0 {
		return landscape
	}
	years := make([]int, 0, len(relevant))
	cites := make([]int, 0, len(relevant))
	venueCounts := make(map[string]int)
	for _, paper := range relevant {
		if paper.Year > 0 {
			years = append(years, paper.Year)
		}
		if paper.CitationCount > 0 {
			cites = append(cites, paper.CitationCount)
			landscape.TotalCites += paper.CitationCount
		}
		if venue := strings.TrimSpace(paper.Venue); venue != "" {
			venueCounts[strings.ToLower(venue)]++
		}
	}
	if len(years) > 0 {
		sort.Ints(years)
		landscape.YearMin = years[0]
		landscape.YearMax = years[len(years)-1]
	}
	if len(cites) > 0 {
		sort.Ints(cites)
		landscape.MedianCites = cites[len(cites)/2]
	}
	if len(venueCounts) > 0 {
		type venueCount struct {
			venue string
			count int
		}
		ranked := make([]venueCount, 0, len(venueCounts))
		for venue, count := range venueCounts {
			ranked = append(ranked, venueCount{venue: venue, count: count})
		}
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].count == ranked[j].count {
				return ranked[i].venue < ranked[j].venue
			}
			return ranked[i].count > ranked[j].count
		})
		landscape.TopVenue = ranked[0].venue
	}
	if summary := formatProviderDiversity(relevant); summary != "" {
		landscape.ProviderSummary = strings.TrimPrefix(summary, "Provider mix: ")
		landscape.ProviderSummary = strings.TrimSuffix(landscape.ProviderSummary, ".")
	}
	_ = query
	return landscape
}

func formatSynthesisLandscape(landscape synthesisLandscape) string {
	if landscape.OnTopicCount == 0 {
		return "No on-topic papers were admitted for synthesis. Broaden the query or increase retrieval depth."
	}
	parts := make([]string, 0, 4)
	parts = append(parts, fmt.Sprintf("%d on-topic source(s) from %d retrieved", landscape.OnTopicCount, landscape.RetrievedCount))
	if landscape.YearMin > 0 && landscape.YearMax > 0 {
		if landscape.YearMin == landscape.YearMax {
			parts = append(parts, fmt.Sprintf("publication year %d", landscape.YearMin))
		} else {
			parts = append(parts, fmt.Sprintf("publication span %d–%d", landscape.YearMin, landscape.YearMax))
		}
	}
	if landscape.MedianCites > 0 {
		parts = append(parts, fmt.Sprintf("median citations %d", landscape.MedianCites))
	}
	if landscape.TopVenue != "" {
		parts = append(parts, fmt.Sprintf("most frequent venue family: %s", landscape.TopVenue))
	}
	if landscape.ProviderSummary != "" {
		parts = append(parts, landscape.ProviderSummary)
	}
	return strings.Join(parts, "; ") + "."
}

func buildSynthesisNarrative(query string, relevant []search.Paper, clauses []string, registry *citationRegistry) string {
	query = strings.TrimSpace(query)
	if len(relevant) == 0 {
		return fmt.Sprintf("The retrieved corpus does not yet provide enough on-topic evidence to answer %q with confidence. Consider refining anchor terms or expanding provider coverage.", query)
	}
	top := search.SortPapersByPreferenceWithQuery(relevant, query)
	lead := top[0]
	leadInline := formatNumberedInlineCitation(registry, lead)
	var sb strings.Builder
	if len(clauses) >= 2 {
		sb.WriteString(fmt.Sprintf("This synthesis connects %q across %d thematic threads", query, len(clauses)))
		for idx, clause := range clauses {
			if idx == 0 {
				sb.WriteString(": ")
			} else if idx == len(clauses)-1 {
				sb.WriteString(", and ")
			} else {
				sb.WriteString(", ")
			}
			sb.WriteString(titleCasePhrase(clause))
		}
		sb.WriteString(". ")
	} else {
		sb.WriteString(fmt.Sprintf("The literature retrieved for %q suggests an active but heterogeneous evidence base. ", query))
	}
	leadTitle := strings.TrimSpace(firstNonEmpty(lead.Title, lead.ID))
	if leadTitle != "" {
		sb.WriteString(fmt.Sprintf("The lead source %s (*%s*) anchors this thread. ", leadInline, leadTitle))
	} else {
		sb.WriteString(fmt.Sprintf("The lead source %s anchors this thread. ", leadInline))
	}
	if len(relevant) > 1 {
		sb.WriteString(fmt.Sprintf("Across %d additional sources, recurring emphasis appears on mechanisms, outcomes, and study-design limitations rather than a single consensus claim.", len(relevant)-1))
	} else {
		sb.WriteString("Additional corroborating sources were sparse in this run, so treat this as an exploratory map rather than a settled review.")
	}
	return sb.String()
}

func buildSynthesisResearchPrompts(query string, clauses []string, relevant []search.Paper) []string {
	prompts := make([]string, 0, 5)
	if len(clauses) >= 2 {
		prompts = append(prompts, fmt.Sprintf("How might %s and %s interact in the admitted literature — shared mechanisms, sequential interventions, or parallel evidence lines?",
			titleCasePhrase(clauses[0]), titleCasePhrase(clauses[1])))
	}
	prompts = append(prompts, "Which study designs dominate the admitted papers (RCT, cohort, in vitro, review), and how does that constrain causal inference?")
	if landscape := buildSynthesisLandscape(query, relevant, relevant); landscape.YearMax > 0 && landscape.YearMin > 0 && landscape.YearMax-landscape.YearMin >= 8 {
		prompts = append(prompts, fmt.Sprintf("How has the field shifted between %d and %d — are newer papers challenging earlier scaffold or surgical assumptions?", landscape.YearMin, landscape.YearMax))
	} else {
		prompts = append(prompts, "Are there population segments, comorbidities, or follow-up windows that remain underrepresented?")
	}
	prompts = append(prompts, "Which claims in this synthesis rely on abstracts only versus full-text verification?")
	prompts = append(prompts, fmt.Sprintf("What falsifiable prediction would most sharply test the working interpretation of %q?", strings.TrimSpace(query)))
	if len(prompts) > 5 {
		prompts = prompts[:5]
	}
	return prompts
}

func buildSynthesisImplications(query string, relevant []search.Paper, evidence []EvidenceItem) []string {
	implications := make([]string, 0, 4)
	if len(relevant) == 0 {
		return []string{"Treat this run as a retrieval diagnostic: refine query anchors, widen providers, or increase iterations before drawing scholarly conclusions."}
	}
	recent := 0
	highlyCited := 0
	for _, paper := range relevant {
		if paper.Year >= 2020 {
			recent++
		}
		if paper.CitationCount >= 50 {
			highlyCited++
		}
	}
	if recent > 0 {
		implications = append(implications, fmt.Sprintf("%d of %d admitted papers are from 2020 onward — weigh recent methods against older high-citation anchors.", recent, len(relevant)))
	}
	if highlyCited > 0 {
		implications = append(implications, fmt.Sprintf("%d highly cited source(s) (50+ citations) may define canonical terminology; verify whether newer studies replicate or revise those findings.", highlyCited))
	}
	if len(evidence) > 0 {
		implications = append(implications, fmt.Sprintf("%d grounded evidence snippet(s) support specific claims; cross-check each against the source abstract or full text before citing in your own work.", len(evidence)))
	} else {
		implications = append(implications, "No grounded evidence snippets were extracted — synthesis bullets summarize abstracts and should not be treated as verified quotations.")
	}
	implications = append(implications, fmt.Sprintf("For %q, prioritize sources that report effect sizes, failure modes, and follow-up duration rather than feasibility alone.", strings.TrimSpace(query)))
	return implications
}

func formatSynthesisBibliography(papers []search.Paper, limit int, registry *citationRegistry) []string {
	if limit <= 0 {
		limit = 8
	}
	papers = search.SortPapersByPreference(papers)
	lines := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, paper := range papers {
		if len(lines) >= limit {
			break
		}
		title := strings.TrimSpace(firstNonEmpty(paper.Title, paper.ID))
		if title == "" {
			continue
		}
		key := strings.ToLower(title)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		authors := paperSourceLabel(paper)
		segment := authors
		if paper.Year > 0 {
			segment = fmt.Sprintf("%s (%d)", authors, paper.Year)
		}
		segment += ". " + title
		if venue := strings.TrimSpace(paper.Venue); venue != "" {
			segment += ". " + venue
		}
		if paper.CitationCount > 0 {
			segment += fmt.Sprintf(". Citations: %d", paper.CitationCount)
		}
		prefix := ""
		if registry != nil {
			if marker := registry.marker(paper); marker != "" {
				prefix = marker + " "
			}
		}
		lines = append(lines, "- "+prefix+segment)
	}
	return lines
}
