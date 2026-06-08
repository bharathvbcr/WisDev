package evidence

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

type extractedClaim struct {
	text         string
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

		record := buildCanonicalRecord(paper)
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
		for _, claim := range claims {
			packetCounter++
			packetID := fmt.Sprintf("evp_%d_%d", now, packetCounter)
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
						Snippet:           sanitizeString(claim.text, 1024),
						Locator:           sanitizeString(claim.locator, 240),
						Support:           "supports",
					},
				},
				VerifierStatus: derivePacketVerifierStatus(record),
				Confidence:     record.ResolutionConfidence,
				CreatedAt:      now,
			}
			if claim.visualID != "" {
				packet.VisualEvidenceIDs = []string{claim.visualID}
			}
			claimPackets = append(claimPackets, packet)
			clusterPacketIDs = append(clusterPacketIDs, packetID)
		}

		for _, visual := range visuals {
			visual.SourcePacketIDs = append([]string{}, clusterPacketIDs...)
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

func buildCanonicalRecord(paper search.Paper) CanonicalCitationRecord {
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

	addClaim := func(text string, claimType string, section string, locator string, materialKind string, visualID string) {
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
			claimType:    classifyClaimType(text, claimType),
			section:      sanitizeString(section, 120),
			locator:      sanitizeString(locator, 240),
			materialKind: sanitizeString(materialKind, 64),
			visualID:     sanitizeString(visualID, 120),
		})
	}

	for idx, sentence := range extractSentences(paper.Abstract, 3) {
		section := "abstract"
		if idx > 0 {
			section = "introduction"
		}
		addClaim(sentence, "", section, "abstract", "abstract", "")
	}

	for idx, sentence := range extractSentences(paper.FullText, 2) {
		addClaim(sentence, "", inferSectionHint(sentence, "full_text"), fmt.Sprintf("full_text:%d", idx+1), "full_text", "")
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
			visuals = append(visuals, VisualEvidence{
				VisualID:          visualID,
				SourceCanonicalID: record.CanonicalID,
				Kind:              "table",
				Title:             firstNonEmpty(title, "Table Summary"),
				Caption:           text,
				Locator:           locator,
			})
			addClaim(text, "result", firstNonEmpty(section, "results"), locator, "table", visualID)
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
			addClaim(text, "result", firstNonEmpty(section, "results"), locator, "figure", visualID)
		default:
			if text != "" {
				addClaim(text, "", firstNonEmpty(section, "discussion"), locator, "section", "")
			}
		}
	}

	return claims, visuals
}

func fallbackClaimText(title string, abstract string) string {
	if sentence := firstSentence(abstract); sentence != "" {
		return sentence
	}
	return sanitizeString(title, 1024)
}

func derivePacketVerifierStatus(record CanonicalCitationRecord) string {
	if record.Resolved {
		return "verified"
	}
	if strings.TrimSpace(record.Title) != "" {
		return "needs_review"
	}
	return "provisional"
}

func assignContradictions(packets []EvidencePacket) {
	baselineBySection := map[string]string{}
	fallbackBaselineID := ""
	byPacketID := map[string]*EvidencePacket{}
	for idx := range packets {
		packet := &packets[idx]
		byPacketID[packet.PacketID] = packet
		if fallbackBaselineID == "" && !isPotentiallyContradictory(packet.ClaimText, packet.ClaimType) {
			fallbackBaselineID = packet.PacketID
		}
		if len(packet.SectionRelevance) == 0 {
			continue
		}
		primarySection := packet.SectionRelevance[0]
		if isPotentiallyContradictory(packet.ClaimText, packet.ClaimType) {
			baselineID := baselineBySection[primarySection]
			if baselineID == "" {
				baselineID = fallbackBaselineID
			}
			if baselineID == "" || baselineID == packet.PacketID {
				continue
			}
			packet.ContradictionPacketIDs = uniqueStrings(append(packet.ContradictionPacketIDs, baselineID))
			if baseline := byPacketID[baselineID]; baseline != nil {
				baseline.ContradictionPacketIDs = uniqueStrings(append(baseline.ContradictionPacketIDs, packet.PacketID))
			}
			continue
		}
		if _, exists := baselineBySection[primarySection]; !exists {
			baselineBySection[primarySection] = packet.PacketID
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

func isPotentiallyContradictory(text string, claimType string) bool {
	if claimType == "limitation" {
		return true
	}
	value := strings.ToLower(text)
	return strings.Contains(value, "however") ||
		strings.Contains(value, "but") ||
		strings.Contains(value, "does not") ||
		strings.Contains(value, "limited") ||
		strings.Contains(value, "unclear")
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
