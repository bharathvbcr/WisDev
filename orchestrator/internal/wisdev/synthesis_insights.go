package wisdev

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

type synthesisTopicSection struct {
	Label  string
	Papers []search.Paper
}

type citationRegistry struct {
	byKey  map[string]int
	papers []search.Paper
}

func buildCitationRegistry(papers []search.Paper) *citationRegistry {
	papers = search.SortPapersByPreference(papers)
	registry := &citationRegistry{
		byKey:  make(map[string]int, len(papers)),
		papers: papers,
	}
	for idx, paper := range papers {
		number := idx + 1
		for _, key := range paperCitationKeys(paper) {
			if key != "" {
				registry.byKey[key] = number
			}
		}
	}
	return registry
}

func (r *citationRegistry) marker(paper search.Paper) string {
	if r == nil {
		return ""
	}
	for _, key := range paperCitationKeys(paper) {
		if number, ok := r.byKey[key]; ok && number > 0 {
			return fmt.Sprintf("[%d]", number)
		}
	}
	return ""
}

func formatNumberedInlineCitation(registry *citationRegistry, paper search.Paper) string {
	inline := formatInlineNarrativeCitation(paper)
	if registry == nil {
		return inline
	}
	if marker := registry.marker(paper); marker != "" {
		return marker + " " + inline
	}
	return inline
}

func synthesisOrderedTopicSections(query string, papers []search.Paper) []synthesisTopicSection {
	clauses := queryTopicClauses(query)
	if len(clauses) < 2 || len(papers) == 0 {
		return nil
	}
	sections := make([]synthesisTopicSection, 0, len(clauses))
	for _, clause := range clauses {
		label := synthesisSectionLabel(clause)
		sectionPapers := make([]search.Paper, 0)
		for _, paper := range papers {
			if paperMatchesSingleTopicRelevance(clause, paper) {
				sectionPapers = append(sectionPapers, paper)
			}
		}
		if len(sectionPapers) == 0 {
			continue
		}
		sections = append(sections, synthesisTopicSection{
			Label:  label,
			Papers: search.SortPapersByPreferenceWithQuery(sectionPapers, query),
		})
	}
	if len(sections) < 2 {
		return nil
	}
	return sections
}

var studyDesignSignals = []struct {
	Label    string
	Keywords []string
}{
	{Label: "systematic review", Keywords: []string{"systematic review", "scoping review"}},
	{Label: "meta-analysis", Keywords: []string{"meta-analysis", "meta analysis"}},
	{Label: "randomized trial", Keywords: []string{"randomized controlled", "randomised controlled", "rct", "clinical trial"}},
	{Label: "cohort study", Keywords: []string{"cohort study", "prospective study", "retrospective study"}},
	{Label: "in vitro", Keywords: []string{"in vitro", "cell culture"}},
	{Label: "animal model", Keywords: []string{"in vivo", "rat model", "mouse model", "animal model"}},
	{Label: "review", Keywords: []string{"narrative review", "literature review", "review article"}},
}

func detectStudyDesignLabel(paper search.Paper) string {
	body := strings.ToLower(strings.TrimSpace(paper.Title + " " + paper.Abstract + " " + paper.Venue))
	if body == "" {
		return ""
	}
	for _, signal := range studyDesignSignals {
		for _, keyword := range signal.Keywords {
			if strings.Contains(body, keyword) {
				return signal.Label
			}
		}
	}
	return ""
}

func buildSynthesisEvidenceMix(papers []search.Paper) string {
	if len(papers) == 0 {
		return ""
	}
	counts := make(map[string]int)
	unlabeled := 0
	for _, paper := range papers {
		if label := detectStudyDesignLabel(paper); label != "" {
			counts[label]++
		} else {
			unlabeled++
		}
	}
	if len(counts) == 0 {
		return fmt.Sprintf("%d source(s) lacked explicit study-design keywords in title/abstract; treat design claims as provisional until full-text review.", len(papers))
	}
	type labelCount struct {
		label string
		count int
	}
	ranked := make([]labelCount, 0, len(counts))
	for label, count := range counts {
		ranked = append(ranked, labelCount{label: label, count: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count == ranked[j].count {
			return ranked[i].label < ranked[j].label
		}
		return ranked[i].count > ranked[j].count
	})
	parts := make([]string, 0, len(ranked)+1)
	for _, item := range ranked {
		parts = append(parts, fmt.Sprintf("%d %s", item.count, item.label))
	}
	if unlabeled > 0 {
		parts = append(parts, fmt.Sprintf("%d unlabeled", unlabeled))
	}
	return "Evidence mix from title/abstract signals: " + strings.Join(parts, ", ") + ". Design labels are heuristic — confirm in methods sections."
}

func buildSynthesisCorroboration(query string, papers []search.Paper, registry *citationRegistry) string {
	papers = search.SortPapersByPreferenceWithQuery(papers, query)
	if len(papers) < 2 {
		return ""
	}
	secondary := papers[1]
	sentence := firstEvidenceSentence(firstNonEmpty(secondary.Abstract, secondary.FullText, secondary.Title))
	if sentence == "" {
		return ""
	}
	if !strings.HasSuffix(sentence, ".") {
		sentence += "."
	}
	title := strings.TrimSpace(firstNonEmpty(secondary.Title, secondary.ID))
	return fmt.Sprintf("A corroborating line of evidence comes from %s regarding *%s*.",
		formatNumberedInlineCitation(registry, secondary), title) + " " + sentence
}

func buildSynthesisTensions(query string, papers []search.Paper) []string {
	if len(papers) < 2 {
		return nil
	}
	papers = search.SortPapersByPreferenceWithQuery(papers, query)
	tensions := make([]string, 0, 3)

	var newest, anchor *search.Paper
	for idx := range papers {
		paper := papers[idx]
		if paper.Year <= 0 {
			continue
		}
		if newest == nil || paper.Year > newest.Year {
			newest = &papers[idx]
		}
		if anchor == nil || paper.CitationCount > anchor.CitationCount {
			anchor = &papers[idx]
		}
	}
	if newest != nil && anchor != nil && newest.ID != anchor.ID && anchor.CitationCount >= 50 && newest.Year >= anchor.Year+3 {
		tensions = append(tensions, fmt.Sprintf("Recency tension: %s (%d) is more recent than the high-citation anchor %s (%d citations, %d) — check whether newer methods overturn or refine the canonical view.",
			formatInlineNarrativeCitation(*newest), newest.Year,
			formatInlineNarrativeCitation(*anchor), anchor.CitationCount, anchor.Year))
	}

	designs := make(map[string]int)
	for _, paper := range papers {
		if label := detectStudyDesignLabel(paper); label != "" {
			designs[label]++
		}
	}
	if len(designs) >= 2 {
		labels := make([]string, 0, len(designs))
		for label, count := range designs {
			labels = append(labels, fmt.Sprintf("%d %s", count, label))
		}
		sort.Strings(labels)
		tensions = append(tensions, "Design heterogeneity ("+strings.Join(labels, ", ")+") limits direct comparison — separate mechanistic, preclinical, and clinical claims before synthesis.")
	}

	if len(tensions) == 0 {
		tensions = append(tensions, fmt.Sprintf("For %q, no strong contradiction was detected heuristically; still read methods and outcome measures before citing.", strings.TrimSpace(query)))
	}
	return tensions
}

func scoreEvidenceStrength(paper search.Paper, grounded bool) string {
	design := detectStudyDesignLabel(paper)
	switch {
	case design == "systematic review" || design == "meta-analysis" || paper.CitationCount >= 50:
		return "strong"
	case grounded || paper.CitationCount >= 15 || (paper.Year >= 2020 && strings.TrimSpace(paper.Abstract) != ""):
		return "moderate"
	default:
		return "exploratory"
	}
}

func formatEvidenceStrengthTag(paper search.Paper, grounded bool) string {
	switch scoreEvidenceStrength(paper, grounded) {
	case "strong":
		return " [strong evidence]"
	case "moderate":
		return " [moderate evidence]"
	default:
		return " [exploratory evidence]"
	}
}

func paperHasGroundedEvidence(paper search.Paper, evidence []EvidenceItem) bool {
	title := strings.ToLower(strings.TrimSpace(firstNonEmpty(paper.Title, paper.ID)))
	if title == "" {
		return false
	}
	for _, item := range evidence {
		itemTitle := strings.ToLower(strings.TrimSpace(firstNonEmpty(item.PaperTitle, item.PaperID)))
		if itemTitle == title {
			return strings.TrimSpace(firstNonEmpty(item.Claim, item.Snippet)) != ""
		}
	}
	return false
}

var synthesisThemeStopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "from": {}, "that": {}, "this": {}, "were": {},
	"was": {}, "are": {}, "has": {}, "have": {}, "had": {}, "into": {}, "using": {}, "used": {},
	"study": {}, "studies": {}, "results": {}, "methods": {}, "method": {}, "background": {},
	"conclusion": {}, "conclusions": {}, "objective": {}, "objectives": {}, "purpose": {},
	"analysis": {}, "review": {}, "data": {}, "based": {}, "between": {}, "among": {}, "after": {},
}

func extractSynthesisThemes(query string, papers []search.Paper, limit int) []string {
	if limit <= 0 {
		limit = 5
	}
	anchors := queryAnchorTerms(query)
	anchorSet := make(map[string]struct{}, len(anchors))
	for _, anchor := range anchors {
		anchorSet[anchor] = struct{}{}
	}
	counts := make(map[string]int)
	for _, paper := range papers {
		for _, token := range loopEvidenceTokens(firstNonEmpty(paper.Title, paper.Abstract)) {
			if len(token) < 4 {
				continue
			}
			if _, stop := synthesisThemeStopwords[token]; stop {
				continue
			}
			if _, anchor := anchorSet[token]; anchor {
				counts[token]++
				continue
			}
			body := strings.ToLower(strings.TrimSpace(paper.Title + " " + paper.Abstract))
			for anchor := range anchorSet {
				if strings.Contains(body, anchor) && strings.Contains(body, token) {
					counts[token]++
				}
			}
		}
	}
	type themeCount struct {
		theme string
		count int
	}
	ranked := make([]themeCount, 0, len(counts))
	for theme, count := range counts {
		if count < 2 {
			continue
		}
		ranked = append(ranked, themeCount{theme: theme, count: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count == ranked[j].count {
			return ranked[i].theme < ranked[j].theme
		}
		return ranked[i].count > ranked[j].count
	})
	themes := make([]string, 0, limit)
	for _, item := range ranked {
		themes = append(themes, titleCasePhrase(item.theme))
		if len(themes) >= limit {
			break
		}
	}
	return themes
}

func buildSynthesisExecutiveTakeaway(query string, papers []search.Paper, evidence []EvidenceItem, registry *citationRegistry) []string {
	if len(papers) == 0 {
		return []string{fmt.Sprintf("No on-topic sources were retrieved for %q — treat this run as a query/retrieval tuning exercise.", strings.TrimSpace(query))}
	}
	papers = search.SortPapersByPreferenceWithQuery(papers, query)
	takeaways := make([]string, 0, 3)
	lead := papers[0]
	leadSentence := firstEvidenceSentence(firstNonEmpty(lead.Abstract, lead.FullText, lead.Title))
	if leadSentence != "" {
		takeaways = append(takeaways, fmt.Sprintf("%s %s that %s",
			formatNumberedInlineCitation(registry, lead), inlineReportVerb(lead), leadSentence))
	}
	if themes := extractSynthesisThemes(query, papers, 3); len(themes) > 0 {
		takeaways = append(takeaways, "Recurring thematic anchors include "+strings.Join(themes, ", ")+".")
	}
	if len(evidence) > 0 {
		takeaways = append(takeaways, fmt.Sprintf("%d grounded snippet(s) support specific claims — prioritize these over abstract-only summaries when writing or citing.", len(evidence)))
	} else if len(papers) >= 3 {
		takeaways = append(takeaways, "No grounded snippets were extracted; read full texts before treating abstract overlap as consensus.")
	}
	if len(takeaways) > 3 {
		takeaways = takeaways[:3]
	}
	return takeaways
}

func buildSynthesisCrossThemeBridge(clauses []string, sections []synthesisTopicSection) string {
	if len(clauses) < 2 || len(sections) < 2 {
		return ""
	}
	return fmt.Sprintf("Bridging %s and %s: compare whether the top sources in each thread share outcome measures, patient populations, or only topical vocabulary — interaction claims require more than co-retrieval.",
		titleCasePhrase(clauses[0]), titleCasePhrase(clauses[1]))
}

func buildSynthesisRetrievalGaps(query string, relevant, all []search.Paper, evidence []EvidenceItem) []string {
	gaps := make([]string, 0, 4)
	if len(relevant) == 0 {
		gaps = append(gaps, "No on-topic papers passed relevance filtering — widen providers, relax query anchors, or increase iterations.")
		return gaps
	}
	if len(relevant) < 3 {
		gaps = append(gaps, fmt.Sprintf("Only %d on-topic source(s) — statistical or systematic conclusions are premature.", len(relevant)))
	}
	if len(all) > len(relevant) {
		gaps = append(gaps, fmt.Sprintf("%d retrieved source(s) were filtered as off-topic; check whether the query anchors are too narrow.", len(all)-len(relevant)))
	}
	hasClinical := false
	hasPreclinical := false
	for _, paper := range relevant {
		switch detectStudyDesignLabel(paper) {
		case "randomized trial", "cohort study", "systematic review", "meta-analysis":
			hasClinical = true
		case "in vitro", "animal model":
			hasPreclinical = true
		}
	}
	if hasPreclinical && !hasClinical {
		gaps = append(gaps, "Retrieved set is preclinical-heavy — clinical translation claims need separate retrieval.")
	}
	if hasClinical && !hasPreclinical {
		gaps = append(gaps, "Clinical sources dominate; mechanistic or materials detail may be underrepresented.")
	}
	recent := 0
	for _, paper := range relevant {
		if paper.Year >= 2022 {
			recent++
		}
	}
	if recent == 0 {
		gaps = append(gaps, "No sources from 2022 onward — recent methods and debates may be missing.")
	}
	if len(evidence) == 0 {
		gaps = append(gaps, "No grounded evidence snippets were extracted — quotations and effect sizes still need manual verification.")
	}
	if len(gaps) == 0 {
		gaps = append(gaps, fmt.Sprintf("For %q, heuristic gap scan found no obvious retrieval holes; next step is full-text reading of top-ranked sources.", strings.TrimSpace(query)))
	}
	if len(gaps) > 4 {
		gaps = gaps[:4]
	}
	return gaps
}

func formatProviderDiversity(papers []search.Paper) string {
	if len(papers) == 0 {
		return ""
	}
	counts := make(map[string]int)
	for _, paper := range papers {
		source := strings.TrimSpace(paper.Source)
		if source == "" {
			source = "unknown"
		}
		counts[strings.ToLower(source)]++
	}
	type sourceCount struct {
		source string
		count  int
	}
	ranked := make([]sourceCount, 0, len(counts))
	for source, count := range counts {
		ranked = append(ranked, sourceCount{source: source, count: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count == ranked[j].count {
			return ranked[i].source < ranked[j].source
		}
		return ranked[i].count > ranked[j].count
	})
	parts := make([]string, 0, len(ranked))
	for _, item := range ranked {
		parts = append(parts, fmt.Sprintf("%s (%d)", item.source, item.count))
	}
	return "Provider mix: " + strings.Join(parts, ", ") + "."
}

func polishSynthesisText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "     .", ".")
	text = whitespaceBeforePeriodRe.ReplaceAllString(text, ".")
	replacer := strings.NewReplacer(
		"..", ".",
		". .", ".",
		"  ", " ",
		" \n", "\n",
	)
	text = replacer.Replace(text)
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(text)
}

func buildSynthesisConsensusNote(papers []search.Paper, evidence []EvidenceItem) string {
	if len(papers) == 0 {
		return ""
	}
	if len(evidence) >= 3 {
		return fmt.Sprintf("%d grounded evidence snippet(s) across %d source(s) support convergent claims; treat convergence as provisional until effect sizes and cohorts are compared.", len(evidence), len(papers))
	}
	if len(evidence) > 0 {
		return fmt.Sprintf("Only %d grounded snippet(s) were extracted — convergence is suggestive, not systematic.", len(evidence))
	}
	return "No extracted evidence snippets — consensus assessment is based on abstract-level similarity only."
}
