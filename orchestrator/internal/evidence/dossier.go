package evidence

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

type extractedClaim struct {
	text         string
	support      string // a distinct adjacent sentence used as the evidence span
	claimType    string
	section      string
	locator      string
	materialKind string
	visualID     string
}

// BuildDossier remains the compatibility entrypoint for callers that only need
// the canonical evidence dossier. Full-paper flows should call the richer raw
// material builder through the WisDev manuscript pipeline.
func BuildDossier(jobID string, query string, papers []search.Paper) (Dossier, error) {
	_, dossier, err := BuildRawMaterialSet(jobID, query, papers)
	return dossier, err
}

func BuildRawMaterialSet(jobID string, query string, papers []search.Paper) (ManuscriptRawMaterialSet, Dossier, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return ManuscriptRawMaterialSet{}, Dossier{}, fmt.Errorf("jobID is required and cannot be empty")
	}
	if len(jobID) > 256 {
		return ManuscriptRawMaterialSet{}, Dossier{}, fmt.Errorf("jobID too long (max 256 chars)")
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return ManuscriptRawMaterialSet{}, Dossier{}, fmt.Errorf("query is required and cannot be empty")
	}
	if len(query) > 2048 {
		return ManuscriptRawMaterialSet{}, Dossier{}, fmt.Errorf("query too long (max 2048 chars)")
	}

	if len(papers) > 10000 {
		return ManuscriptRawMaterialSet{}, Dossier{}, fmt.Errorf("too many papers (max 10000): got %d", len(papers))
	}

	now := time.Now().UnixMilli()
	dossierID := fmt.Sprintf("dossier_%d_%s", now, hashID(jobID))
	rawMaterialSetID := fmt.Sprintf("raw_%d_%s", now, hashID(jobID))

	canonical := make([]CanonicalCitationRecord, 0, len(papers))
	claimPackets := make([]EvidencePacket, 0, len(papers)*3)
	sourceClusters := make([]ManuscriptSourceCluster, 0, len(papers))
	visualEvidence := make([]VisualEvidence, 0, len(papers))
	gaps := make([]string, 0)
	packetCounter := 0

	for idx, paper := range papers {
		if err := validatePaper(&paper, idx); err != nil {
			slog.Warn("skipping invalid paper while building raw materials", "job_id", jobID, "query", query, "index", idx, "error", err)
			continue
		}

		record := BuildCanonicalRecord(paper)
		canonical = append(canonical, record)

		clusterID := fmt.Sprintf("cluster_%d_%d", now, idx+1)
		claims, visuals := extractClaimsFromPaper(paper, record)
		if len(claims) == 0 {
			claims = append(claims, extractedClaim{
				text:         fallbackClaimText(paper.Title, paper.Abstract),
				claimType:    "finding",
				section:      "abstract",
				locator:      "abstract",
				materialKind: "abstract",
			})
		}

		clusterPacketIDs := make([]string, 0, len(claims))
		visualPacketIDs := map[string][]string{}
		for _, claim := range claims {
			packetCounter++
			packetID := fmt.Sprintf("evp_%d_%d", now, packetCounter)
			// Use a DISTINCT adjacent sentence as the evidence span when available, so
			// the span is corroborating text rather than a copy of the claim.
			snippet, supportLabel := claim.text, "asserts"
			if distinct := strings.TrimSpace(claim.support); distinct != "" && normalizeTitle(distinct) != normalizeTitle(claim.text) {
				snippet, supportLabel = distinct, "corroborates"
			}
			verifierStatus := derivePacketVerifierStatus(record)
			var verifierNotes []string
			// Entailment sanity check: if a distinct supporting sentence is on the
			// same subject but flips polarity vs the claim, the source's own text is
			// inconsistent — do not call the packet "verified".
			if verifierStatus == "verified" && !claimSnippetConsistent(claim.text, claim.support) {
				verifierStatus = "needs_review"
				verifierNotes = append(verifierNotes, "claim and its supporting sentence disagree in polarity")
			}
			packet := EvidencePacket{
				PacketID:         packetID,
				ClaimText:        sanitizeString(claim.text, 1024),
				ClaimType:        sanitizeString(claim.claimType, 64),
				SectionRelevance: inferSectionRelevance(claim.text, claim.claimType, claim.section),
				SourceClusterID:  clusterID,
				MaterialKinds:    uniqueStrings([]string{claim.materialKind}),
				EvidenceSpans: []EvidenceSpan{
					{
						SourceCanonicalID: record.CanonicalID,
						Section:           sanitizeString(claim.section, 120),
						Snippet:           sanitizeString(snippet, 1024),
						Locator:           sanitizeString(claim.locator, 240),
						Support:           supportLabel,
					},
				},
				VerifierStatus:     verifierStatus,
				VerifierNotes:      verifierNotes,
				Confidence:         record.ResolutionConfidence,
				QuantitativeClaims: extractQuantitativeClaims(claim.text),
				CreatedAt:          now,
			}
			if claim.visualID != "" {
				packet.VisualEvidenceIDs = []string{claim.visualID}
				visualPacketIDs[claim.visualID] = append(visualPacketIDs[claim.visualID], packetID)
			}
			claimPackets = append(claimPackets, packet)
			clusterPacketIDs = append(clusterPacketIDs, packetID)
		}

		for _, visual := range visuals {
			matched := visualPacketIDs[visual.VisualID]
			if len(matched) == 0 {
				matched = clusterPacketIDs
			}
			visual.SourcePacketIDs = append([]string{}, matched...)
			visualEvidence = append(visualEvidence, visual)
		}

		sourceClusters = append(sourceClusters, ManuscriptSourceCluster{
			ClusterID:          clusterID,
			Label:              sanitizeString(record.Title, 256),
			Theme:              inferClusterTheme(query, paper),
			SourceCanonicalIDs: []string{record.CanonicalID},
			PacketIDs:          clusterPacketIDs,
		})
	}

	if len(claimPackets) == 0 {
		packetCounter++
		fallbackPacketID := fmt.Sprintf("evp_%d_%d", now, packetCounter)
		claimPackets = append(claimPackets, EvidencePacket{
			PacketID:         fallbackPacketID,
			ClaimText:        fmt.Sprintf("Source evidence is not yet attached for the query: %s", query),
			ClaimType:        "research_gap",
			SectionRelevance: []string{"introduction", "literature_review", "discussion", "conclusion"},
			MaterialKinds:    []string{"query_seed"},
			VerifierStatus:   "needs_review",
			VerifierNotes:    []string{"no canonical papers were attached to seed the manuscript raw material set"},
			Confidence:       0.2,
			CreatedAt:        now,
		})
		gaps = append(gaps, "No source papers were supplied; manuscript sections are seeded from the query only.")
		sourceClusters = append(sourceClusters, ManuscriptSourceCluster{
			ClusterID:          fmt.Sprintf("cluster_%d_seed", now),
			Label:              sanitizeString(query, 256),
			Theme:              "query_seed",
			SourceCanonicalIDs: []string{},
			PacketIDs:          []string{fallbackPacketID},
		})
		visualEvidence = append(visualEvidence, VisualEvidence{
			VisualID:        fmt.Sprintf("visual_%d_seed", now),
			Kind:            "figure",
			Title:           "Manuscript Seed Map",
			Caption:         "Concept diagram showing the query seed and pending evidence expansion.",
			Locator:         "query_seed",
			SourcePacketIDs: []string{fallbackPacketID},
		})
	}

	annotateCorroboration(claimPackets)
	assignContradictions(claimPackets)
	linkVisualEvidence(claimPackets, visualEvidence)

	verified := make([]EvidencePacket, 0, len(claimPackets))
	tentative := make([]EvidencePacket, 0, len(claimPackets))
	for _, packet := range claimPackets {
		switch packet.VerifierStatus {
		case "verified":
			verified = append(verified, packet)
		default:
			tentative = append(tentative, packet)
		}
	}

	contradictions := buildContradictionPayloads(claimPackets)
	if len(contradictions) == 0 && len(gaps) > 0 {
		contradictions = append(contradictions, map[string]any{
			"type":    "coverage_gap",
			"summary": gaps[0],
		})
	}

	coverageMetrics := map[string]any{
		"sourceCount":         len(canonical),
		"claimPacketCount":    len(claimPackets),
		"verifiedClaimCount":  len(verified),
		"tentativeClaimCount": len(tentative),
		"resolvedSourceCount": countResolved(canonical),
		"visualEvidenceCount": len(visualEvidence),
		"sectionCoverage":     buildSectionCoverage(claimPackets),
	}

	rawMaterialSet := ManuscriptRawMaterialSet{
		RawMaterialSetID: rawMaterialSetID,
		JobID:            jobID,
		Query:            query,
		CanonicalSources: canonical,
		ClaimPackets:     claimPackets,
		SourceClusters:   sourceClusters,
		VisualEvidence:   visualEvidence,
		Gaps:             gaps,
		CoverageMetrics:  coverageMetrics,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	dossier := Dossier{
		DossierID:        dossierID,
		JobID:            jobID,
		Query:            query,
		CanonicalSources: canonical,
		VerifiedClaims:   verified,
		TentativeClaims:  tentative,
		Contradictions:   contradictions,
		Gaps:             gaps,
		CoverageMetrics:  coverageMetrics,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	slog.Info("built manuscript raw material set", "job_id", jobID, "query", query, "canonical_sources", len(canonical), "claim_packets", len(claimPackets), "visuals", len(visualEvidence))

	return rawMaterialSet, dossier, nil
}

func BuildCanonicalRecord(paper search.Paper) CanonicalCitationRecord {
	sourceIDs := CanonicalIDs{
		DOI:      sanitizeString(paper.DOI, 256),
		Arxiv:    sanitizeString(paper.ArxivID, 256),
		Crossref: sanitizeString(paper.DOI, 256),
	}

	paperID := strings.TrimSpace(strings.ToLower(paper.ID))
	if strings.HasPrefix(paperID, "openalex:") {
		sourceIDs.OpenAlex = sanitizeString(strings.TrimPrefix(paperID, "openalex:"), 256)
	}
	if strings.HasPrefix(paperID, "s2:") {
		sourceIDs.SemanticScholar = sanitizeString(strings.TrimPrefix(paperID, "s2:"), 256)
	}
	if strings.HasPrefix(paperID, "arxiv:") && sourceIDs.Arxiv == "" {
		sourceIDs.Arxiv = sanitizeString(strings.TrimPrefix(paperID, "arxiv:"), 256)
	}

	canonicalID := firstNonEmpty(
		formatID("doi", sourceIDs.DOI),
		formatID("arxiv", sourceIDs.Arxiv),
		formatID("openalex", sourceIDs.OpenAlex),
		formatID("s2", sourceIDs.SemanticScholar),
		formatID("title", normalizeTitle(paper.Title)),
	)

	title := sanitizeString(paper.Title, 512)
	authors := sanitizeAuthors(paper.Authors, 100, 256)

	return CanonicalCitationRecord{
		CanonicalID:          canonicalID,
		SourceIDs:            sourceIDs,
		Title:                title,
		Authors:              authors,
		Venue:                sanitizeString(paper.Venue, 256),
		Year:                 validateYear(paper.Year),
		Abstract:             sanitizeString(paper.Abstract, 4096),
		LandingURL:           sanitizeURL(paper.Link),
		CitationCount:        paper.CitationCount,
		Resolved:             canonicalID != "",
		ResolutionEngine:     "go-raw-material-assembler",
		ResolutionConfidence: confidenceFromRecord(sourceIDs, title),
	}
}

func extractClaimsFromPaper(paper search.Paper, record CanonicalCitationRecord) ([]extractedClaim, []VisualEvidence) {
	claims := make([]extractedClaim, 0, 6)
	visuals := make([]VisualEvidence, 0, 4)
	seen := map[string]struct{}{}
	visualCounter := 0

	addClaim := func(text, support, claimType, section, locator, materialKind, visualID string) {
		text = sanitizeString(text, 1024)
		if text == "" {
			return
		}
		key := normalizeTitle(text)
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		claims = append(claims, extractedClaim{
			text:         text,
			support:      sanitizeString(support, 1024),
			claimType:    classifyClaimType(text, claimType),
			section:      sanitizeString(section, 120),
			locator:      sanitizeString(locator, 240),
			materialKind: sanitizeString(materialKind, 64),
			visualID:     sanitizeString(visualID, 120),
		})
	}

	// Cover more of the abstract (most providers populate only the abstract) and
	// route each sentence to the section it actually fits, rather than dumping all
	// but the first into "introduction" — this gives thinner sources enough varied
	// packets and reduces off-section placement. The adjacent sentence is carried
	// as a distinct supporting span (so the evidence span is not a copy of the claim).
	abstractSentences := extractSentences(paper.Abstract, 6)
	for idx, sentence := range abstractSentences {
		section := "abstract"
		if idx > 0 {
			section = inferSectionHint(sentence, "abstract")
		}
		support := ""
		if idx+1 < len(abstractSentences) {
			support = abstractSentences[idx+1]
		}
		addClaim(sentence, support, "", section, "abstract", "abstract", "")
	}

	for idx, sentence := range extractSentences(paper.FullText, 2) {
		addClaim(sentence, "", "", inferSectionHint(sentence, "full_text"), fmt.Sprintf("full_text:%d", idx+1), "full_text", "")
	}

	for idx, item := range sliceAnyMap(paper.StructureMap) {
		itemType := strings.ToLower(firstNonEmpty(stringValue(item["type"]), stringValue(item["kind"])))
		title := sanitizeString(firstNonEmpty(stringValue(item["title"]), stringValue(item["label"])), 240)
		text := sanitizeString(firstNonEmpty(stringValue(item["summary"]), stringValue(item["caption"]), stringValue(item["text"]), title), 1024)
		section := sanitizeString(firstNonEmpty(stringValue(item["section"]), inferSectionHint(text, title)), 120)
		locator := fmt.Sprintf("structure:%d", idx+1)

		switch {
		case strings.Contains(itemType, "table"):
			visualCounter++
			visualID := fmt.Sprintf("visual_%s_table_%d", hashID(record.CanonicalID), visualCounter)
			headers := stringSliceValue(item["headers"])
			rows := stringMatrixValue(item["rows"])
			if len(rows) == 0 {
				rows = stringMatrixValue(item["cells"])
			}
			visuals = append(visuals, VisualEvidence{
				VisualID:          visualID,
				SourceCanonicalID: record.CanonicalID,
				Kind:              "table",
				Title:             firstNonEmpty(title, "Table Summary"),
				Caption:           text,
				Locator:           locator,
				Headers:           headers,
				Rows:              rows,
			})
			addClaim(text, title, "result", firstNonEmpty(section, "results"), locator, "table", visualID)
		case strings.Contains(itemType, "figure"), strings.Contains(itemType, "diagram"), strings.Contains(itemType, "plot"):
			visualCounter++
			visualID := fmt.Sprintf("visual_%s_figure_%d", hashID(record.CanonicalID), visualCounter)
			visuals = append(visuals, VisualEvidence{
				VisualID:          visualID,
				SourceCanonicalID: record.CanonicalID,
				Kind:              "figure",
				Title:             firstNonEmpty(title, "Figure"),
				Caption:           text,
				Locator:           locator,
			})
			addClaim(text, title, "result", firstNonEmpty(section, "results"), locator, "figure", visualID)
		default:
			if text != "" {
				addClaim(text, "", "", firstNonEmpty(section, "discussion"), locator, "section", "")
			}
		}
	}

	return claims, visuals
}

var quantitativePattern = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s?(%|(?:percent|patients|cases|studies|participants|cohorts?|-?fold|points?)\b)`)

// extractQuantitativeClaims pulls (value, unit) pairs from a claim so the writer
// can only cite numbers that were actually present in the source (anti-fabrication).
func extractQuantitativeClaims(text string) []QuantitativeClaim {
	matches := quantitativePattern.FindAllStringSubmatch(text, 6)
	if len(matches) == 0 {
		return nil
	}
	out := make([]QuantitativeClaim, 0, len(matches))
	seen := map[string]struct{}{}
	for _, m := range matches {
		value, unit := strings.TrimSpace(m[1]), strings.ToLower(strings.TrimSpace(m[2]))
		key := value + "|" + unit
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, QuantitativeClaim{Value: value, Unit: unit})
	}
	return out
}

// claimSnippetConsistent reports whether a claim and its distinct supporting
// sentence are consistent: true when there is no distinct snippet or they address
// different subjects (cannot judge), false only when they share a subject but flip
// polarity (one negates the other) — a real same-source inconsistency.
func claimSnippetConsistent(claim, snippet string) bool {
	snippet = strings.TrimSpace(snippet)
	if snippet == "" || normalizeTitle(snippet) == normalizeTitle(claim) {
		return true
	}
	if !sharesContradictionSubject(claim, snippet) {
		return true
	}
	return contradictionSignalPattern.MatchString(claim) == contradictionSignalPattern.MatchString(snippet)
}

func fallbackClaimText(title string, abstract string) string {
	if sentence := firstSentence(abstract); sentence != "" {
		return sentence
	}
	return sanitizeString(title, 1024)
}

// derivePacketVerifierStatus reports how strongly a claim packet's SOURCE is
// resolved — NOT whether the claim text was independently fact-checked. The
// pipeline runs no entailment check: the evidence span is the claim sentence
// itself, so "verified" must not imply content verification. It therefore means
// the citation resolves to a strong canonical identifier (DOI / arXiv / OpenAlex /
// Semantic Scholar, confidence >= 0.8); a title-only fuzzy match is "needs_review"
// (a real but weakly-identified source), and an unresolved/untitled source is
// "provisional".
func derivePacketVerifierStatus(record CanonicalCitationRecord) string {
	if record.Resolved && record.ResolutionConfidence >= 0.8 {
		return "verified"
	}
	if strings.TrimSpace(record.Title) != "" {
		return "needs_review"
	}
	return "provisional"
}

// annotateCorroboration sets CorroboratingSourceCount on each packet to the number
// of DISTINCT sources asserting a near-identical claim (so a finding reported by
// several independent papers is recognizably stronger), and nudges the confidence
// of multiply-corroborated packets up. It does not merge or remove packets, so all
// cluster/visual cross-references stay intact.
func annotateCorroboration(packets []EvidencePacket) {
	type corroborationGroup struct {
		tokens  map[string]struct{}
		indices []int
		sources map[string]struct{}
	}
	groups := make([]*corroborationGroup, 0)
	for i := range packets {
		tokens := contradictionSubjectTokens(packets[i].ClaimText)
		if len(tokens) == 0 {
			continue
		}
		var match *corroborationGroup
		for _, group := range groups {
			if jaccardTokenSets(tokens, group.tokens) >= 0.8 {
				match = group
				break
			}
		}
		if match == nil {
			match = &corroborationGroup{tokens: tokens, sources: map[string]struct{}{}}
			groups = append(groups, match)
		}
		match.indices = append(match.indices, i)
		for _, span := range packets[i].EvidenceSpans {
			if span.SourceCanonicalID != "" {
				match.sources[span.SourceCanonicalID] = struct{}{}
			}
		}
	}
	for _, group := range groups {
		count := len(group.sources)
		if count < 1 {
			count = 1
		}
		for _, idx := range group.indices {
			packets[idx].CorroboratingSourceCount = count
			if count >= 2 {
				if boosted := packets[idx].Confidence + 0.05*float64(count-1); boosted <= 0.99 {
					packets[idx].Confidence = boosted
				} else {
					packets[idx].Confidence = 0.99
				}
			}
		}
	}
}

func jaccardTokenSets(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for token := range a {
		if _, ok := b[token]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// assignContradictions links two claim packets as contradictory only when they
// (a) sit in the same primary manuscript section, (b) are about the same subject
// (shared content tokens), and (c) at least one carries a genuine opposition
// signal. The previous implementation linked every hedge-word claim to a single
// per-section (or global fallback) baseline regardless of subject, which
// manufactured dozens of spurious "contradictions"; that star topology and the
// cross-section fallback are removed here.
func assignContradictions(packets []EvidencePacket) {
	bySection := map[string][]int{}
	for idx := range packets {
		packet := &packets[idx]
		if len(packet.SectionRelevance) == 0 {
			continue
		}
		bySection[packet.SectionRelevance[0]] = append(bySection[packet.SectionRelevance[0]], idx)
	}
	for _, idxs := range bySection {
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				pa, pb := &packets[idxs[a]], &packets[idxs[b]]
				opposed := isPotentiallyContradictory(pa.ClaimText, pa.ClaimType) ||
					isPotentiallyContradictory(pb.ClaimText, pb.ClaimType)
				if !opposed || !sharesContradictionSubject(pa.ClaimText, pb.ClaimText) {
					continue
				}
				pa.ContradictionPacketIDs = uniqueStrings(append(pa.ContradictionPacketIDs, pb.PacketID))
				pb.ContradictionPacketIDs = uniqueStrings(append(pb.ContradictionPacketIDs, pa.PacketID))
			}
		}
	}
}

func linkVisualEvidence(packets []EvidencePacket, visuals []VisualEvidence) {
	visualIndex := make(map[string][]string, len(visuals))
	for _, visual := range visuals {
		for _, packetID := range visual.SourcePacketIDs {
			visualIndex[packetID] = append(visualIndex[packetID], visual.VisualID)
		}
	}
	for idx := range packets {
		packet := &packets[idx]
		packet.VisualEvidenceIDs = uniqueStrings(append(packet.VisualEvidenceIDs, visualIndex[packet.PacketID]...))
	}
}

func buildContradictionPayloads(packets []EvidencePacket) []map[string]any {
	type pair struct {
		left  string
		right string
	}
	seen := map[pair]struct{}{}
	out := make([]map[string]any, 0)
	packetIndex := make(map[string]EvidencePacket, len(packets))
	for _, packet := range packets {
		packetIndex[packet.PacketID] = packet
	}
	for _, packet := range packets {
		for _, otherID := range packet.ContradictionPacketIDs {
			if otherID == "" {
				continue
			}
			left := packet.PacketID
			right := otherID
			if left > right {
				left, right = right, left
			}
			key := pair{left: left, right: right}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			other := packetIndex[otherID]
			out = append(out, map[string]any{
				"packetIds": []string{left, right},
				"summary":   fmt.Sprintf("Potential contradiction between \"%s\" and \"%s\"", packet.ClaimText, other.ClaimText),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprintf("%v", out[i]["summary"]) < fmt.Sprintf("%v", out[j]["summary"])
	})
	return out
}

func buildSectionCoverage(packets []EvidencePacket) map[string]any {
	coverage := map[string]any{}
	counts := map[string]int{}
	for _, packet := range packets {
		for _, section := range packet.SectionRelevance {
			counts[section]++
		}
	}
	for section, count := range counts {
		coverage[section] = count
	}
	return coverage
}

func extractSentences(text string, limit int) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || limit <= 0 {
		return nil
	}
	parts := regexp.MustCompile(`[.!?]\s+`).Split(trimmed, -1)
	out := make([]string, 0, limit)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		part = strings.TrimSuffix(part, ".")
		part = strings.TrimSuffix(part, "!")
		part = strings.TrimSuffix(part, "?")
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, sanitizeString(part+".", 1024))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func classifyClaimType(text string, hint string) string {
	if normalized := strings.TrimSpace(strings.ToLower(hint)); normalized != "" {
		return normalized
	}
	value := strings.ToLower(text)
	switch {
	case strings.Contains(value, "however"), strings.Contains(value, "limitation"), strings.Contains(value, "challenge"), strings.Contains(value, "does not"), strings.Contains(value, "unclear"):
		return "limitation"
	case strings.Contains(value, "method"), strings.Contains(value, "framework"), strings.Contains(value, "pipeline"), strings.Contains(value, "approach"), strings.Contains(value, "dataset"):
		return "method"
	case strings.Contains(value, "improve"), strings.Contains(value, "outperform"), strings.Contains(value, "accuracy"), strings.Contains(value, "%"), containsNumericSignal(value):
		return "result"
	default:
		return "finding"
	}
}

func inferSectionRelevance(text string, claimType string, sectionHint string) []string {
	section := normalizeSectionName(sectionHint)
	relevance := make([]string, 0, 4)
	switch claimType {
	case "method":
		relevance = append(relevance, "methods", "discussion")
	case "result":
		relevance = append(relevance, "results", "discussion")
	case "limitation":
		relevance = append(relevance, "discussion", "conclusion")
	default:
		relevance = append(relevance, "literature_review", "introduction")
	}
	if section != "" {
		relevance = append([]string{section}, relevance...)
	}
	value := strings.ToLower(text)
	if strings.Contains(value, "survey") || strings.Contains(value, "review") {
		relevance = append(relevance, "literature_review")
	}
	if strings.Contains(value, "future work") || strings.Contains(value, "implication") {
		relevance = append(relevance, "conclusion")
	}
	return uniqueStrings(relevance)
}

func inferSectionHint(text string, fallback string) string {
	value := strings.ToLower(firstNonEmpty(text, fallback))
	switch {
	case strings.Contains(value, "method"), strings.Contains(value, "architecture"), strings.Contains(value, "dataset"), strings.Contains(value, "training"):
		return "methods"
	case strings.Contains(value, "result"), strings.Contains(value, "performance"), strings.Contains(value, "table"), strings.Contains(value, "figure"):
		return "results"
	case strings.Contains(value, "limitation"), strings.Contains(value, "however"), strings.Contains(value, "future work"):
		return "discussion"
	case strings.Contains(value, "conclusion"), strings.Contains(value, "implication"):
		return "conclusion"
	default:
		return "literature_review"
	}
}

func normalizeSectionName(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(normalized, "abstract"):
		return "abstract"
	case strings.Contains(normalized, "intro"):
		return "introduction"
	case strings.Contains(normalized, "review"), strings.Contains(normalized, "related"):
		return "literature_review"
	case strings.Contains(normalized, "method"), strings.Contains(normalized, "approach"):
		return "methods"
	case strings.Contains(normalized, "result"), strings.Contains(normalized, "experiment"):
		return "results"
	case strings.Contains(normalized, "discussion"), strings.Contains(normalized, "analysis"):
		return "discussion"
	case strings.Contains(normalized, "conclusion"):
		return "conclusion"
	default:
		return ""
	}
}

func inferClusterTheme(query string, paper search.Paper) string {
	combined := strings.ToLower(strings.TrimSpace(query + " " + paper.Title + " " + strings.Join(paper.Keywords, " ")))
	switch {
	case strings.Contains(combined, "survey"), strings.Contains(combined, "review"):
		return "survey"
	case strings.Contains(combined, "method"), strings.Contains(combined, "framework"), strings.Contains(combined, "architecture"):
		return "methodology"
	case strings.Contains(combined, "result"), strings.Contains(combined, "benchmark"), strings.Contains(combined, "evaluation"):
		return "results"
	default:
		return "evidence"
	}
}

// contradictionSignalPattern matches genuine opposition/negation language. It
// uses word boundaries so it cannot fire on innocuous substrings ("but" inside
// "contribution"/"attribute", "limited" inside "unlimited"), which previously
// flagged most review claims as contradictory.
var contradictionSignalPattern = regexp.MustCompile(`(?i)\b(however|whereas|contrary to|in contrast|contradict\w*|conflict\w*|disagree\w*|inconsistent|did not|does not|do not|failed to|fails to|worse than|less effective|no (significant )?(effect|difference|benefit|improvement)|negative (result|finding))\b`)

var contradictionTokenPattern = regexp.MustCompile(`[a-zA-Z0-9]+`)

// contradictionSubjectStopwords are generic + domain-ubiquitous tokens that must
// not, by themselves, make two claims look like they share a subject.
var contradictionSubjectStopwords = map[string]struct{}{
	"study": {}, "studies": {}, "research": {}, "results": {}, "result": {},
	"paper": {}, "model": {}, "models": {}, "method": {}, "methods": {},
	"approach": {}, "approaches": {}, "system": {}, "systems": {}, "data": {},
	"performance": {}, "clinical": {}, "patient": {}, "patients": {}, "medical": {},
	"using": {}, "based": {}, "between": {}, "across": {}, "within": {},
	"these": {}, "those": {}, "their": {}, "which": {}, "while": {}, "where": {},
	"there": {}, "applications": {}, "application": {}, "framework": {}, "frameworks": {},
}

// isPotentiallyContradictory reports whether a claim's text carries a genuine
// opposition/negation signal — a prerequisite (not proof) for a contradiction.
// Self-disclosed limitations (claimType "limitation") and incidental hedge words
// like "but"/"limited" are deliberately NOT treated as oppositions; they used to
// fire on most review claims and manufactured spurious contradiction links.
func isPotentiallyContradictory(text string, _ string) bool {
	return contradictionSignalPattern.MatchString(text)
}

// contradictionSubjectTokens returns the meaningful (>=5 char, non-stopword)
// content tokens of a claim, used to require that two claims are actually about
// the same subject before they can be called contradictory.
func contradictionSubjectTokens(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, tok := range contradictionTokenPattern.FindAllString(strings.ToLower(text), -1) {
		if len(tok) < 5 {
			continue
		}
		if _, blocked := contradictionSubjectStopwords[tok]; blocked {
			continue
		}
		out[tok] = struct{}{}
	}
	return out
}

// sharesContradictionSubject requires at least two shared content tokens so two
// claims must genuinely address the same subject before being linked.
func sharesContradictionSubject(a, b string) bool {
	ta, tb := contradictionSubjectTokens(a), contradictionSubjectTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	shared := 0
	for tok := range ta {
		if _, ok := tb[tok]; ok {
			shared++
		}
	}
	return shared >= 2
}

func containsNumericSignal(text string) bool {
	for _, field := range strings.Fields(text) {
		if _, err := strconv.ParseFloat(strings.Trim(field, "()%,"), 64); err == nil {
			return true
		}
	}
	return false
}

func sliceAnyMap(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return nil
	}
}

func validatePaper(paper *search.Paper, idx int) error {
	if paper == nil {
		return fmt.Errorf("paper is nil")
	}
	title := strings.TrimSpace(paper.Title)
	if title == "" {
		return fmt.Errorf("paper has empty title")
	}
	return nil
}

func sanitizeString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
}

func sanitizeURL(u string) string {
	u = strings.TrimSpace(u)
	if len(u) > 2048 {
		u = u[:2048]
	}
	if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return ""
	}
	return u
}

func sanitizeAuthors(authors []string, maxCount int, maxLen int) []string {
	if len(authors) == 0 {
		return nil
	}
	result := make([]string, 0, len(authors))
	for i, a := range authors {
		if i >= maxCount {
			break
		}
		if a = sanitizeString(a, maxLen); a != "" {
			result = append(result, a)
		}
	}
	return result
}

func validateYear(year int) int {
	currentYear := time.Now().Year()
	if year < 1900 || year > currentYear+1 {
		return 0
	}
	return year
}

func hashID(id string) string {
	if len(id) > 16 {
		return id[:16]
	}
	return id
}

func countResolved(records []CanonicalCitationRecord) int {
	count := 0
	for _, rec := range records {
		if rec.Resolved {
			count++
		}
	}
	return count
}

func confidenceFromRecord(ids CanonicalIDs, title string) float64 {
	switch {
	case strings.TrimSpace(ids.DOI) != "":
		return 0.95
	case strings.TrimSpace(ids.Arxiv) != "":
		return 0.9
	case strings.TrimSpace(ids.OpenAlex) != "":
		return 0.85
	case strings.TrimSpace(ids.SemanticScholar) != "":
		return 0.82
	case strings.TrimSpace(title) != "":
		return 0.7
	default:
		return 0.5
	}
}

func firstSentence(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	for _, sep := range []string{".", "!", "?"} {
		if idx := strings.Index(trimmed, sep); idx > 20 {
			return strings.TrimSpace(trimmed[:idx+1])
		}
	}
	if len(trimmed) > 240 {
		return strings.TrimSpace(trimmed[:240]) + "..."
	}
	return trimmed
}

func normalizeTitle(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	clean = strings.ReplaceAll(clean, ",", " ")
	clean = strings.ReplaceAll(clean, ".", " ")
	clean = strings.Join(strings.Fields(clean), " ")
	return clean
}

func formatID(prefix string, value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	return prefix + ":" + v
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

func stringSliceValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := sanitizeString(item, 512); text != "" {
				out = append(out, text)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := sanitizeString(stringValue(item), 512); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func stringMatrixValue(value any) [][]string {
	switch typed := value.(type) {
	case [][]string:
		out := make([][]string, 0, len(typed))
		for _, row := range typed {
			if parsed := stringSliceValue(row); len(parsed) > 0 {
				out = append(out, parsed)
			}
		}
		return out
	case []any:
		out := make([][]string, 0, len(typed))
		for _, row := range typed {
			if parsed := stringSliceValue(row); len(parsed) > 0 {
				out = append(out, parsed)
			}
		}
		return out
	default:
		return nil
	}
}
