package docgen

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

// referenceEntry is a structured bibliography entry used while building the final list.
type referenceEntry struct {
	titleKey  string
	authors   []string
	year      int
	title     string
	venue     string
	link      string
	citations int
	preprint  bool
}

func normalizeRefTitle(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func collectReferences(research *agent.YOLOResult, result internalwisdev.ManuscriptPipelineResult) []referenceEntry {
	out := make([]referenceEntry, 0)
	if research != nil && len(research.Papers) > 0 {
		for _, paper := range research.Papers {
			if strings.TrimSpace(paper.Title) == "" {
				continue
			}
			link := firstNonEmpty(paper.Link, paper.OpenAccessURL, paper.DOI)
			out = append(out, referenceEntry{
				titleKey: normalizeRefTitle(paper.Title), authors: paper.Authors, year: paper.Year,
				title: paper.Title, venue: paper.Venue, link: link, citations: paper.CitationCount,
				preprint: isPreprintReference(paper.Venue, link),
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	for _, source := range result.RawMaterials.CanonicalSources {
		if strings.TrimSpace(source.Title) == "" {
			continue
		}
		out = append(out, referenceEntry{
			titleKey: normalizeRefTitle(source.Title), authors: source.Authors, year: source.Year,
			title: source.Title, venue: source.Venue, link: source.LandingURL,
			preprint: isPreprintReference(source.Venue, source.LandingURL),
		})
	}
	return out
}

func isPreprintReference(venue, link string) bool {
	hay := strings.ToLower(venue + " " + link)
	for _, marker := range []string{"preprint", "arxiv", "biorxiv", "medrxiv", "ssrn", "research square", "researchsquare", "preprints.org"} {
		if strings.Contains(hay, marker) {
			return true
		}
	}
	return false
}

func buildCitationRefMap(research *agent.YOLOResult, result internalwisdev.ManuscriptPipelineResult) map[string]int {
	refByCanonicalID := map[string]int{}
	if research != nil && len(research.Papers) > 0 {
		refByTitle := map[string]int{}
		ref := 0
		for _, paper := range research.Papers {
			if strings.TrimSpace(paper.Title) == "" {
				continue
			}
			ref++
			if key := normalizeRefTitle(paper.Title); key != "" {
				if _, exists := refByTitle[key]; !exists {
					refByTitle[key] = ref
				}
			}
		}
		for _, source := range result.RawMaterials.CanonicalSources {
			if n, ok := refByTitle[normalizeRefTitle(source.Title)]; ok {
				refByCanonicalID[source.CanonicalID] = n
			}
		}
	} else {
		ref := 0
		for _, source := range result.RawMaterials.CanonicalSources {
			if strings.TrimSpace(source.Title) == "" {
				continue
			}
			ref++
			refByCanonicalID[source.CanonicalID] = ref
		}
	}
	if len(refByCanonicalID) == 0 {
		return nil
	}
	packetRef := map[string]int{}
	for _, packet := range result.RawMaterials.ClaimPackets {
		for _, span := range packet.EvidenceSpans {
			if n, ok := refByCanonicalID[span.SourceCanonicalID]; ok {
				packetRef[packet.PacketID] = n
				break
			}
		}
	}
	if len(packetRef) == 0 {
		return nil
	}
	return packetRef
}

var citationMarkerPattern = regexp.MustCompile(`\[([0-9][0-9,;\s]*)\]`)

func remapSectionContentCitations(content string, sectionPacketIDs []string, packetRef map[string]int) string {
	if content == "" || len(sectionPacketIDs) == 0 || len(packetRef) == 0 {
		return content
	}
	return citationMarkerPattern.ReplaceAllStringFunc(content, func(marker string) string {
		inner := strings.Trim(marker, "[]")
		refs := make([]string, 0, 4)
		seen := map[int]struct{}{}
		for _, tok := range strings.FieldsFunc(inner, func(r rune) bool {
			return r == ',' || r == ';' || r == '|' || unicode.IsSpace(r)
		}) {
			k, err := strconv.Atoi(strings.TrimSpace(tok))
			if err != nil || k < 1 || k > len(sectionPacketIDs) {
				return marker
			}
			ref, ok := packetRef[sectionPacketIDs[k-1]]
			if !ok {
				return marker
			}
			if _, dup := seen[ref]; dup {
				continue
			}
			seen[ref] = struct{}{}
			refs = append(refs, strconv.Itoa(ref))
		}
		if len(refs) == 0 {
			return marker
		}
		return "[" + strings.Join(refs, ", ") + "]"
	})
}

var doiInTextPattern = regexp.MustCompile(`10\.\d{3,}/[^\s"'<>\])]+`)
var doiCitationBracketPattern = regexp.MustCompile(`\[\s*((?:10\.)?\d{3,}/[^\],;\s]+|s\d{4,}[^\],;\s]*)(?:\s*[,;]\s*(?:(?:10\.)?\d{3,}/[^\],;\s]+|s\d{4,}[^\],;\s]*))*\s*\]`)
var doiTokenSplitPattern = regexp.MustCompile(`\s*[,;]\s*`)
var multiSpacePattern = regexp.MustCompile(`[ \t]{2,}`)
var spaceBeforePunctPattern = regexp.MustCompile(`\s+([.,;:)])`)

func normalizeDOIKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if m := doiInTextPattern.FindString(s); m != "" {
		s = m
	}
	return strings.TrimRight(s, ".,;)")
}

func doiRefMap(refs []referenceEntry) map[string]int {
	out := make(map[string]int, len(refs))
	for i, ref := range refs {
		if key := normalizeDOIKey(ref.link); strings.HasPrefix(key, "10.") {
			if _, exists := out[key]; !exists {
				out[key] = i + 1
			}
		}
	}
	return out
}

func remapDOICitations(content string, doiRef map[string]int) string {
	if content == "" {
		return content
	}
	if !doiCitationBracketPattern.MatchString(content) {
		return content
	}
	out := doiCitationBracketPattern.ReplaceAllStringFunc(content, func(marker string) string {
		inner := strings.Trim(strings.TrimSpace(marker), "[]")
		nums := make([]string, 0, 2)
		seen := map[int]struct{}{}
		for _, tok := range doiTokenSplitPattern.Split(inner, -1) {
			key := normalizeDOIKey(tok)
			if !strings.HasPrefix(key, "10.") {
				key = "10." + strings.TrimPrefix(key, "10.")
			}
			if n, ok := doiRef[key]; ok {
				if _, dup := seen[n]; !dup {
					seen[n] = struct{}{}
					nums = append(nums, strconv.Itoa(n))
				}
			}
		}
		if len(nums) == 0 {
			return ""
		}
		return "[" + strings.Join(nums, ", ") + "]"
	})
	out = multiSpacePattern.ReplaceAllString(out, " ")
	out = spaceBeforePunctPattern.ReplaceAllString(out, "$1")
	return out
}

func buildReferenceModel(research *agent.YOLOResult, result internalwisdev.ManuscriptPipelineResult, includeUncited bool) ([]referenceEntry, map[string]int) {
	refs := collectReferences(research, result)
	baseRef := buildCitationRefMap(research, result)
	if len(refs) == 0 || len(baseRef) == 0 {
		return refs, baseRef
	}
	citedOrder := citedBaseRefOrder(result, baseRef)
	if len(citedOrder) == 0 {
		return refs, baseRef
	}
	final, baseToFinal := refineCitedReferences(refs, citedOrder)
	packetRef := make(map[string]int, len(baseRef))
	for packetID, base := range baseRef {
		if n, ok := baseToFinal[base]; ok {
			packetRef[packetID] = n
		}
	}
	if includeUncited {
		final = appendUncitedReferences(final, refs)
	}
	return final, packetRef
}

func appendUncitedReferences(final, allRefs []referenceEntry) []referenceEntry {
	present := make(map[string]struct{}, len(final))
	for _, ref := range final {
		if ref.titleKey != "" {
			present[ref.titleKey] = struct{}{}
		}
	}
	for _, ref := range allRefs {
		if ref.titleKey == "" {
			final = append(final, ref)
			continue
		}
		if _, ok := present[ref.titleKey]; ok {
			continue
		}
		present[ref.titleKey] = struct{}{}
		final = append(final, ref)
	}
	return final
}

func citedBaseRefOrder(result internalwisdev.ManuscriptPipelineResult, baseRef map[string]int) []int {
	draftBySection := make(map[string]internalwisdev.SectionDraftArtifact, len(result.SectionDrafts))
	for _, d := range result.SectionDrafts {
		draftBySection[d.SectionID] = d
	}
	order := result.Blueprint.SectionOrder
	if len(order) == 0 {
		for _, d := range result.SectionDrafts {
			order = append(order, d.SectionID)
		}
	}
	seen := map[int]struct{}{}
	out := make([]int, 0)
	for _, sectionID := range order {
		draft, ok := draftBySection[sectionID]
		if !ok {
			continue
		}
		for _, match := range citationMarkerPattern.FindAllStringSubmatch(draft.Content, -1) {
			for _, tok := range strings.FieldsFunc(match[1], func(r rune) bool {
				return r == ',' || r == ';' || r == '|' || unicode.IsSpace(r)
			}) {
				k, err := strconv.Atoi(strings.TrimSpace(tok))
				if err != nil || k < 1 || k > len(draft.ClaimPacketIDs) {
					continue
				}
				base, ok := baseRef[draft.ClaimPacketIDs[k-1]]
				if !ok {
					continue
				}
				if _, dup := seen[base]; dup {
					continue
				}
				seen[base] = struct{}{}
				out = append(out, base)
			}
		}
	}
	return out
}

func refineCitedReferences(refs []referenceEntry, citedOrder []int) ([]referenceEntry, map[int]int) {
	titleKeyFor := func(base int) string {
		key := refs[base-1].titleKey
		if key == "" {
			return "ref:" + strconv.Itoa(base)
		}
		return key
	}
	finalByTitle := map[string]int{}
	repByTitle := map[string]int{}
	final := make([]referenceEntry, 0, len(citedOrder))
	for _, base := range citedOrder {
		if base < 1 || base > len(refs) {
			continue
		}
		key := titleKeyFor(base)
		if pos, exists := finalByTitle[key]; exists {
			if shouldPreferReference(refs[base-1], refs[repByTitle[key]-1]) {
				final[pos-1] = refs[base-1]
				repByTitle[key] = base
			}
			continue
		}
		final = append(final, refs[base-1])
		finalByTitle[key] = len(final)
		repByTitle[key] = base
	}
	baseToFinal := map[int]int{}
	for i := range refs {
		base := i + 1
		if n, ok := finalByTitle[titleKeyFor(base)]; ok {
			baseToFinal[base] = n
		}
	}
	return final, baseToFinal
}

func shouldPreferReference(candidate, current referenceEntry) bool {
	if current.preprint && !candidate.preprint {
		return true
	}
	if current.preprint == candidate.preprint {
		candDOI := strings.Contains(strings.ToLower(candidate.link), "doi.org")
		curDOI := strings.Contains(strings.ToLower(current.link), "doi.org")
		return candDOI && !curDOI
	}
	return false
}

func referencesFromEntries(entries []referenceEntry) []Reference {
	out := make([]Reference, 0, len(entries))
	for i, ref := range entries {
		out = append(out, Reference{
			Authors: ref.authors, Year: ref.year, Title: ref.title, Venue: ref.venue,
			Link: ref.link, Citations: ref.citations, Preprint: ref.preprint, Number: i + 1,
		})
	}
	return out
}

func referencesFromPapers(papers []agent.Paper) []Reference {
	out := make([]Reference, 0, len(papers))
	for i, paper := range papers {
		if strings.TrimSpace(paper.Title) == "" {
			continue
		}
		link := firstNonEmpty(paper.Link, paper.OpenAccessURL, paper.DOI)
		out = append(out, Reference{
			ID: paper.ID, Authors: paper.Authors, Year: paper.Year, Title: paper.Title,
			Venue: paper.Venue, Link: link, Citations: paper.CitationCount, Number: i + 1,
		})
	}
	return out
}

func referencesFromCanonical(sources []evidence.CanonicalCitationRecord) []Reference {
	out := make([]Reference, 0, len(sources))
	for i, source := range sources {
		if strings.TrimSpace(source.Title) == "" {
			continue
		}
		out = append(out, Reference{
			ID: source.CanonicalID, Authors: source.Authors, Year: source.Year,
			Title: source.Title, Venue: source.Venue, Link: source.LandingURL, Number: i + 1,
		})
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
