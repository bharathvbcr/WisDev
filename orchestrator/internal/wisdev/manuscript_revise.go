package wisdev

// manuscript_revise.go ports the WisDev ARC pipeline's aggressive review-guided
// rewrite, self-methodology backstop, sentence-level dedup, and uncited-specific
// citation attachment so the ScholarLM (go_orchestrator) DocGen pipeline runs the
// same stages. Kept structurally identical to the wisdev-arc implementation so the
// two trees stay in parity.

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
)

var ownSearchStrategyPattern = regexp.MustCompile(`(?i)\bsearch strateg(?:y|ies)\b\s+(?:\w+\s+){0,3}(focused|focuses|utilized|utilizes|included|includes|comprised|comprises|involved|involves|targeted|targets|prioritized|prioritizes|encompassed|encompasses|consisted|spanned|covered|combined|drew)\b`)
var selfAttributedProtocolPattern = regexp.MustCompile(`(?i)\b(this|the present|our)\s+(?:narrative\s+|systematic\s+|scoping\s+|comprehensive\s+|present\s+)?(review|study|paper|analysis|survey)\b[^.]{0,18}\b(employ\w*|use[sd]?|using|conduct\w*|perform\w*|appl(?:y|ies|ied)|follow\w*|adopt\w*|utiliz\w+|implement\w*|synthesiz\w*|quer(?:y|ies|ied|ying))\b[^.]{0,70}\b(prisma|systematic search|systematic review|search strateg\w+|database search|literature search|inclusion criteria|selection criteria|search protocol|screened|meta-analysis|pubmed|scopus|embase|medline|peer-reviewed (?:studies|literature|articles))\b`)
var selfConductPassivePattern = regexp.MustCompile(`(?i)\b(this|the|our)\s+(?:narrative\s+|systematic\s+|scoping\s+|comprehensive\s+|present\s+)?(review|study|analysis|survey)\b[^.]{0,20}\b(was|were|is|has been|have been)\s+(conduct\w*|perform\w*|undertak\w*|carried out|completed|executed)\b`)
var selfProtocolGerundPattern = regexp.MustCompile(`(?i)\b(utilizing|employing|using|adopting|following|conducting|performing|leveraging|querying|searching|queried|searched)\b[^.]{0,25}\b(prisma-compliant|systematic search strateg\w+|systematic literature search|prisma (?:2020|guidelines?|framework|methodolog\w+)|systematic review methodolog\w+|pubmed|scopus|embase|medline)\b`)
var criteriaMethodologyPattern = regexp.MustCompile(`(?i)\b(selection|inclusion|exclusion|eligibility)\s+criteria\b[^.]{0,40}\b(focused|focuses|mandated|required|prioriti[sz]ed|included|comprised|specified|restricted|limited|encompassed|stipulated|consisted|emphasi[sz]ed)\b[^.]{0,40}\b(stud(?:y|ies)|literature|paper|article|publication|peer-reviewed|empirical|evidence base|research articles)\b`)
var methodsSubsectionHeadingPattern = regexp.MustCompile(`(?im)^[ \t]*(?:#{1,6}[ \t]*|\*\*[ \t]*)(search strateg(?:y|ies)|selection criteria|inclusion(?: and exclusion)? criteria|exclusion criteria|eligibility criteria|study selection|data extraction(?: and synthesis)?|search and selection|literature search(?: strategy)?|methodology|methods)\b[^\n]*$`)
var strongProtocolTermPattern = regexp.MustCompile(`(?i)\bPRISMA\b|\bsystematic (?:literature )?(?:review|search)\b|\bSLR\b|\binclusion criteria\b|\bexclusion criteria\b|\bscreened \d+\b|\b(?:pubmed|embase|scopus|medline|cochrane|ieee xplore)\b`)
var citationOrAttributionPattern = regexp.MustCompile(`\[\s*(?:\d|evp_)|(?i)\bet al\b|\bby [A-Z][a-z]+`)
var distinctiveSpecificPattern = regexp.MustCompile(`\b[A-Z][a-z]+[A-Z][A-Za-z0-9-]*\b|\b\d{1,3},\d{3}\b|\b\d{3}\b`)
var anyCitationMarkerPattern = regexp.MustCompile(`\[\s*(?:\d|evp_)`)
var numberUnitPattern = regexp.MustCompile(`(?i)\b(\d{2,4})\s+(?:[a-z][a-z-]*\s+){0,2}(stud(?:y|ies)|papers?|cases?|patients?|questions?|documents?|guidelines?|trials?|participants?|specialties|records?|vignettes?|reviews?|articles?|publications?)\b`)

func (p *ManuscriptPipeline) reviseSectionsWithReview(ctx context.Context, query string, blueprint ManuscriptBlueprint, raw evidence.ManuscriptRawMaterialSet, sections []SectionDraftArtifact) []SectionDraftArtifact {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return sections
	}
	findings := reviewFindings(p.fetchAdversarialReview(ctx, query, blueprint, raw, sections))
	// Run the rewrite when the review found issues OR there is a canonical ownership
	// plan to enforce OR any section carries entailment flags — otherwise the
	// ownership/entailment directives would be silently skipped by the old
	// findings-only early return.
	if len(findings) == 0 && len(blueprint.OwnershipConcepts) == 0 && !anySectionHasEntailmentFlags(sections) {
		slog.Debug("manuscript revise skipped — no review findings, ownership plan, or entailment flags",
			"component", manuscriptLogComponent, "operation", "review_revise")
		return sections
	}
	slog.Debug("manuscript revise pass starting",
		"component", manuscriptLogComponent, "operation", "review_revise",
		"review_findings", len(findings), "ownership_concepts", len(blueprint.OwnershipConcepts),
		"entailment_flags", anySectionHasEntailmentFlags(sections))
	revisedCount := 0
	for i := range sections {
		// A manual edit is authoritative — never regenerate a user-edited section.
		if sectionIsUserEdited(sections[i]) {
			slog.Debug("manuscript revise skipped user-edited section",
				"component", manuscriptLogComponent, "operation", "review_revise",
				"section_id", sections[i].SectionID)
			continue
		}
		content := strings.TrimSpace(sections[i].Content)
		if content == "" {
			continue
		}
		claimPackets := claimPacketsByIDs(raw.ClaimPackets, sections[i].ClaimPacketIDs)
		payload := map[string]any{
			"section_id":       sections[i].SectionID,
			"original_content": content,
			"claim_packets":    claimPackets,
			"thesis":           blueprint.Thesis,
			"prior_sections":   otherSectionsContext(sections, i),
			"review_findings":  mergeReviseFindings(findings, sections[i], blueprint.OwnershipConcepts),
			"max_tokens":       32768,
		}
		if ci := p.trimmedCustomInstructions(); ci != "" {
			payload["custom_instructions"] = ci
		}
		revised, err := p.postSectionContent(ctx, "/wisdev/manuscript/section/revise", payload)
		if err != nil || strings.TrimSpace(revised) == "" {
			continue
		}
		revisedCount++
		sections[i].Content = minimizeEmDashes(strings.TrimSpace(revised))
		applyCitationMarkerHygiene(&sections[i], claimPackets)
		if rebuilt := buildContentParagraphs(sections[i].SectionID, sections[i].Content, claimPackets); len(rebuilt) > 0 {
			sections[i].Paragraphs = rebuilt
		}
		sections[i].ClaimProvenance = buildClaimProvenance(sections[i].Paragraphs, claimPackets)
		sections[i].ContradictionMap = buildContradictionMap(sections[i].Paragraphs, claimPackets)
		sections[i].Version++
		sections[i].UpdatedAt = time.Now().UnixMilli()
	}
	slog.Debug("manuscript revise pass complete",
		"component", manuscriptLogComponent, "operation", "review_revise",
		"sections_revised", revisedCount, "sections_total", len(sections))
	return sections
}

func reviewFindings(review *manuscriptReviewResult) []string {
	if review == nil {
		return nil
	}
	findings := make([]string, 0, len(review.Redundancy)+len(review.AttributionIssues)+len(review.FabricationRisks)+len(review.ReadabilityIssues)+len(review.Recommendations))
	findings = append(findings, review.Redundancy...)
	findings = append(findings, review.AttributionIssues...)
	findings = append(findings, review.FabricationRisks...)
	findings = append(findings, review.ReadabilityIssues...)
	findings = append(findings, review.Recommendations...)
	return findings
}

func otherSectionsContext(sections []SectionDraftArtifact, skip int) []map[string]any {
	out := make([]map[string]any, 0, len(sections))
	for i, section := range sections {
		if i == skip {
			continue
		}
		if content := strings.TrimSpace(section.Content); content != "" {
			out = append(out, map[string]any{"title": section.Title, "text": content})
		}
	}
	return out
}

func (p *ManuscriptPipeline) stripSelfMethodology(sections []SectionDraftArtifact, raw evidence.ManuscriptRawMaterialSet) []SectionDraftArtifact {
	for i := range sections {
		stripped := stripSelfMethodologySentences(sections[i].Content)
		if stripped == sections[i].Content {
			continue
		}
		claimPackets := claimPacketsByIDs(raw.ClaimPackets, sections[i].ClaimPacketIDs)
		sections[i].Content = stripped
		if rebuilt := buildContentParagraphs(sections[i].SectionID, stripped, claimPackets); len(rebuilt) > 0 {
			sections[i].Paragraphs = rebuilt
		}
		sections[i].ClaimProvenance = buildClaimProvenance(sections[i].Paragraphs, claimPackets)
		sections[i].ContradictionMap = buildContradictionMap(sections[i].Paragraphs, claimPackets)
		sections[i].Version++
		sections[i].UpdatedAt = time.Now().UnixMilli()
	}
	return sections
}

func stripSelfMethodologySentences(content string) string {
	if strings.TrimSpace(content) == "" {
		return content
	}
	cleaned := methodsSubsectionHeadingPattern.ReplaceAllString(content, "")
	if cleaned == content && !contentClaimsOwnMethodology(content) {
		return content
	}
	paragraphs := strings.Split(cleaned, "\n\n")
	out := make([]string, 0, len(paragraphs))
	for _, para := range paragraphs {
		p := strings.TrimSpace(para)
		if p == "" {
			continue
		}
		if !contentClaimsOwnMethodology(p) {
			out = append(out, p)
			continue
		}
		kept := make([]string, 0)
		for _, s := range citationSafeProseSentences(p, 0) {
			if claimsOwnSystematicMethodology(s) || isUncitedProtocolSentence(s) {
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) > 0 {
			out = append(out, strings.Join(kept, " "))
		}
	}
	if len(out) == 0 {
		return content // never empty a section
	}
	result := strings.Join(out, "\n\n")
	if strings.TrimSpace(result) == strings.TrimSpace(content) {
		return content
	}
	return result
}

func isUncitedProtocolSentence(sentence string) bool {
	return strongProtocolTermPattern.MatchString(sentence) &&
		!citationOrAttributionPattern.MatchString(sentence)
}

func contentClaimsOwnMethodology(content string) bool {
	if claimsOwnSystematicMethodology(content) {
		return true
	}
	if !strongProtocolTermPattern.MatchString(content) {
		return false
	}
	for _, s := range citationSafeProseSentences(content, 0) {
		if isUncitedProtocolSentence(s) {
			return true
		}
	}
	return false
}

func claimsOwnSystematicMethodology(content string) bool {
	return selfAttributedProtocolPattern.MatchString(content) ||
		selfConductPassivePattern.MatchString(content) ||
		selfProtocolGerundPattern.MatchString(content) ||
		ownSearchStrategyPattern.MatchString(content) ||
		criteriaMethodologyPattern.MatchString(content)
}

func containsSpecificWord(haystackLower, specificLower string) bool {
	if specificLower == "" {
		return false
	}
	for idx := strings.Index(haystackLower, specificLower); idx >= 0; {
		before := idx == 0 || !isASCIIWordByte(haystackLower[idx-1])
		end := idx + len(specificLower)
		after := end >= len(haystackLower) || !isASCIIWordByte(haystackLower[end])
		if before && after {
			return true
		}
		next := strings.Index(haystackLower[idx+1:], specificLower)
		if next < 0 {
			break
		}
		idx += 1 + next
	}
	return false
}

func (p *ManuscriptPipeline) dedupeCrossSectionSentences(sections []SectionDraftArtifact, raw evidence.ManuscriptRawMaterialSet) []SectionDraftArtifact {
	kept := make([]map[string]struct{}, 0)
	for si := range sections {
		if sections[si].SectionID == "abstract" || strings.TrimSpace(sections[si].Content) == "" {
			continue
		}
		paragraphs := strings.Split(sections[si].Content, "\n\n")
		outParas := make([]string, 0, len(paragraphs))
		thisSection := make([]map[string]struct{}, 0)
		changed := false
		for _, para := range paragraphs {
			sentences := citationSafeProseSentences(para, 0)
			if len(sentences) == 0 {
				continue
			}
			keptSentences := make([]string, 0, len(sentences))
			for _, s := range sentences {
				tokens := keywordTokenSet(s)
				if len(tokens) < 6 { // too short/generic to judge as a restatement
					keptSentences = append(keptSentences, s)
					continue
				}
				isDup := false
				for _, prior := range kept {
					if jaccardTokens(tokens, prior) >= 0.80 {
						isDup = true
						break
					}
				}
				if isDup {
					changed = true
					continue
				}
				keptSentences = append(keptSentences, s)
				thisSection = append(thisSection, tokens)
			}
			if len(keptSentences) > 0 {
				outParas = append(outParas, strings.Join(keptSentences, " "))
			}
		}
		if len(outParas) == 0 { // never empty a section: leave it (and seed nothing new)
			continue
		}
		kept = append(kept, thisSection...)
		if !changed {
			continue
		}
		newContent := strings.Join(outParas, "\n\n")
		ordered := claimPacketsByIDs(raw.ClaimPackets, sections[si].ClaimPacketIDs)
		sections[si].Content = newContent
		applyCitationMarkerHygiene(&sections[si], ordered)
		if rebuilt := buildContentParagraphs(sections[si].SectionID, sections[si].Content, ordered); len(rebuilt) > 0 {
			sections[si].Paragraphs = rebuilt
		}
		sections[si].ClaimProvenance = buildClaimProvenance(sections[si].Paragraphs, ordered)
		sections[si].ContradictionMap = buildContradictionMap(sections[si].Paragraphs, ordered)
		sections[si].Version++
		sections[si].UpdatedAt = time.Now().UnixMilli()
	}
	return sections
}

func (p *ManuscriptPipeline) attachUncitedSpecifics(sections []SectionDraftArtifact, raw evidence.ManuscriptRawMaterialSet) []SectionDraftArtifact {
	for i := range sections {
		ordered := claimPacketsByIDs(raw.ClaimPackets, sections[i].ClaimPacketIDs)
		if len(ordered) == 0 {
			continue
		}
		updated := attachUncitedSpecificCitations(sections[i].Content, ordered)
		if updated == sections[i].Content {
			continue
		}
		sections[i].Content = updated
		if rebuilt := buildContentParagraphs(sections[i].SectionID, updated, ordered); len(rebuilt) > 0 {
			sections[i].Paragraphs = rebuilt
		}
		sections[i].ClaimProvenance = buildClaimProvenance(sections[i].Paragraphs, ordered)
		sections[i].ContradictionMap = buildContradictionMap(sections[i].Paragraphs, ordered)
		sections[i].Version++
		sections[i].UpdatedAt = time.Now().UnixMilli()
	}
	return sections
}

func attachUncitedSpecificCitations(content string, ordered []evidence.EvidencePacket) string {
	if content == "" || len(ordered) == 0 {
		return content
	}
	paragraphs := strings.Split(content, "\n\n")
	changed := false
	for pi := range paragraphs {
		sentences := citationSafeProseSentences(paragraphs[pi], 0)
		if len(sentences) == 0 {
			continue
		}
		edited := false
		for si := range sentences {
			if anyCitationMarkerPattern.MatchString(sentences[si]) {
				continue
			}
			k := uniquePacketForSpecifics(distinctiveSpecificPattern.FindAllString(sentences[si], -1), ordered)
			if k == 0 {
				k = uniquePacketForNumberUnit(sentences[si], ordered)
			}
			if k > 0 {
				sentences[si] = appendCitationMarker(sentences[si], k)
				edited = true
			}
		}
		if edited {
			paragraphs[pi] = strings.Join(sentences, " ")
			changed = true
		}
	}
	if !changed {
		return content
	}
	return strings.Join(paragraphs, "\n\n")
}

func uniquePacketForSpecifics(specifics []string, ordered []evidence.EvidencePacket) int {
	for _, spec := range specifics {
		specLower := strings.ToLower(spec)
		matchK, count := 0, 0
		for k := 1; k <= len(ordered); k++ {
			if packetMatchesSpecific(ordered[k-1], specLower) {
				count++
				matchK = k
			}
		}
		if count == 1 {
			return matchK
		}
	}
	return 0
}

func uniquePacketForNumberUnit(sentence string, ordered []evidence.EvidencePacket) int {
	for _, m := range numberUnitPattern.FindAllStringSubmatch(sentence, -1) {
		num := strings.ToLower(m[1])
		unit := strings.ToLower(m[2])
		stem := unit
		if len(stem) > 4 {
			stem = stem[:4]
		}
		matchK, count := 0, 0
		for k := 1; k <= len(ordered); k++ {
			text := strings.ToLower(ordered[k-1].ClaimText)
			for _, span := range ordered[k-1].EvidenceSpans {
				text += " " + strings.ToLower(span.Snippet)
			}
			if containsSpecificWord(text, num) && strings.Contains(text, stem) {
				count++
				matchK = k
			}
		}
		if count == 1 {
			return matchK
		}
	}
	return 0
}

func packetMatchesSpecific(packet evidence.EvidencePacket, specificLower string) bool {
	if containsSpecificWord(strings.ToLower(packet.ClaimText), specificLower) {
		return true
	}
	for _, span := range packet.EvidenceSpans {
		if containsSpecificWord(strings.ToLower(span.Snippet), specificLower) {
			return true
		}
	}
	return false
}

func appendCitationMarker(sentence string, k int) string {
	s := strings.TrimRight(sentence, " ")
	marker := " [" + strconv.Itoa(k) + "]"
	if n := len(s); n > 0 {
		if last := s[n-1]; last == '.' || last == '!' || last == '?' {
			return s[:n-1] + marker + string(last)
		}
	}
	return s + marker
}

func appendCritiqueList(critique map[string]any, key string, extra []string) {
	if len(extra) == 0 {
		return
	}
	existing, _ := critique[key].([]string)
	critique[key] = uniqueStrings(append(append([]string{}, existing...), extra...))
}

const maxSectionPacketBudget = 80

func sectionPacketLimit(sectionID string, minCitations int) int {
	if isBroadSynthesisSection(sectionID) {
		limit := 18
		if minCitations > limit {
			limit = minCitations
		}
		if limit > maxSectionPacketBudget {
			return maxSectionPacketBudget
		}
		return limit
	}
	limit := 14
	if half := minCitations / 2; half > limit {
		limit = half
	}
	if limit > maxSectionPacketBudget {
		return maxSectionPacketBudget
	}
	return limit
}

func isASCIIWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
