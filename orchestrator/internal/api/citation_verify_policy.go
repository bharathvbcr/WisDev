package api

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

const (
	citationVerifyMinConfidenceDefault = verifyCiteMinConfidenceDefault
	citationVerifyMarkerMinConfidence  = 0.6
)

// structuredCitationInput is the preferred citation shape for full-paper jobs.
type structuredCitationInput struct {
	CitationID string `json:"citationId"`
	PaperID    string `json:"paperId"`
	Marker     string `json:"marker"`
	SectionID  string `json:"sectionId"`
	Claim      string `json:"claim,omitempty"`
}

// citationVerifyOutcome captures bibliographic verification for one marker occurrence.
type citationVerifyOutcome struct {
	CitationID       string
	SectionID        string
	Marker           string
	PaperID          string
	Verified         bool
	Confidence       float64
	Reason           string
	OccurrenceIndex  int
	VerificationMethod string
}

// IsCitationVerified is the canonical Review-card rule: a citation counts as verified
// when it resolves to a source bibliographically. Positional [n] markers that resolve
// via source index are always verified (same as section mode). Other markers require
// confidence >= minConfidence. Do not conflate with VerifyCitationRecordsSecurely.
func IsCitationVerified(resolved *verifyCitationSource, byMarker bool, confidence float64, minConfidence float64) bool {
	if resolved == nil {
		return false
	}
	if byMarker {
		return true
	}
	if minConfidence <= 0 {
		minConfidence = citationVerifyMinConfidenceDefault
	}
	return confidence >= minConfidence
}

func applyMarkerGroundingVerification(traced *FETracedCitation, byMarker bool) {
	if traced == nil || !byMarker || traced.Verified {
		return
	}
	traced.Verified = true
	if traced.Confidence < citationVerifyMarkerMinConfidence {
		traced.Confidence = citationVerifyMarkerMinConfidence
	}
	traced.VerificationMethod = "grounding"
}

func verifyExtractedCitationOutcome(
	cit extractedCitation,
	sources []verifyCitationSource,
	sectionID string,
	occurrenceIndex int,
) citationVerifyOutcome {
	marker := strings.TrimSpace(cit.Text)
	citationID := structuredCitationID(sectionID, marker, occurrenceIndex, cit.PaperID)

	resolved, byMarker := resolveCitationSource(cit, sources)
	if resolved == nil {
		reason := "citation could not be matched to a source"
		if cit.PaperID != "" {
			reason = "positional marker did not resolve to a source"
		}
		return citationVerifyOutcome{
			CitationID:      citationID,
			SectionID:       sectionID,
			Marker:          marker,
			Verified:        false,
			Reason:          reason,
			OccurrenceIndex: occurrenceIndex,
		}
	}

	match := matchCitationToSource(cit, sources)
	confidence := match.confidence
	if confidence <= 0 && byMarker {
		confidence = citationVerifyMarkerMinConfidence
	}
	verified := IsCitationVerified(resolved, byMarker, confidence, citationVerifyMinConfidenceDefault)
	method := "manual"
	if verified {
		method = "grounding"
	}

	return citationVerifyOutcome{
		CitationID:         citationID,
		SectionID:          sectionID,
		Marker:             marker,
		PaperID:            resolved.PaperID,
		Verified:           verified,
		Confidence:         confidence,
		Reason:             citationUnverifiedReason(verified, byMarker),
		OccurrenceIndex:    occurrenceIndex,
		VerificationMethod: method,
	}
}

func verifyStructuredCitationOutcome(
	input structuredCitationInput,
	sources []verifyCitationSource,
) citationVerifyOutcome {
	marker := strings.TrimSpace(input.Marker)
	if marker == "" {
		marker = "[?]"
	}
	citationID := strings.TrimSpace(input.CitationID)
	if citationID == "" {
		citationID = structuredCitationID(input.SectionID, marker, 0, input.PaperID)
	}

	cit := extractedCitation{Text: marker, PaperID: strings.TrimSpace(input.PaperID)}
	if cit.PaperID == "" && marker != "" {
		if m := verifyCiteNumberedRE.FindStringSubmatch(marker); len(m) > 1 {
			cit.PaperID = m[1]
		}
	}

	outcome := verifyExtractedCitationOutcome(cit, sources, strings.TrimSpace(input.SectionID), 0)
	outcome.CitationID = citationID
	if paperID := strings.TrimSpace(input.PaperID); paperID != "" && outcome.PaperID == "" {
		outcome.PaperID = paperID
	}
	return outcome
}

// VerifyStructuredCitations applies the canonical IsCitationVerified policy to explicit
// structured inputs (preferred for full-paper jobs).
func VerifyStructuredCitations(inputs []structuredCitationInput, sources []verifyCitationSource) []citationVerifyOutcome {
	out := make([]citationVerifyOutcome, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, verifyStructuredCitationOutcome(input, sources))
	}
	return out
}

func verifySectionTextCitations(sectionID, text string, sources []verifyCitationSource) []citationVerifyOutcome {
	extracted := extractCitationsFromText(text)
	out := make([]citationVerifyOutcome, 0, len(extracted))
	occurrenceByMarker := make(map[string]int)
	for _, cit := range extracted {
		if cit.Text == "" {
			continue
		}
		if cit.PaperID == "" && cit.Authors == "" && cit.Year == "" && cit.Title == "" {
			continue
		}
		idx := occurrenceByMarker[cit.Text]
		occurrenceByMarker[cit.Text] = idx + 1
		out = append(out, verifyExtractedCitationOutcome(cit, sources, sectionID, idx))
	}
	return out
}

func structuredCitationID(sectionID, marker string, occurrenceIndex int, paperID string) string {
	base := strings.TrimSpace(sectionID)
	if base == "" {
		base = "section"
	}
	normalizedMarker := regexp.MustCompile(`\s+`).ReplaceAllString(strings.TrimSpace(marker), "_")
	if normalizedMarker == "" {
		normalizedMarker = "cite"
	}
	if occurrenceIndex > 0 {
		return fmt.Sprintf("cite_%s_%s_%d", base, normalizedMarker, occurrenceIndex)
	}
	if paperID := strings.TrimSpace(paperID); paperID != "" {
		return fmt.Sprintf("cite_%s_%s", base, paperID)
	}
	return fmt.Sprintf("cite_%s_%s", base, normalizedMarker)
}

func citationUnverifiedReason(verified bool, byMarker bool) string {
	if verified {
		return ""
	}
	if byMarker {
		return "positional marker did not resolve to a source"
	}
	return "citation could not be matched to a source"
}

func summarizeCitationVerifyOutcomes(outcomes []citationVerifyOutcome) map[string]any {
	total := len(outcomes)
	verified := 0
	for _, outcome := range outcomes {
		if outcome.Verified {
			verified++
		}
	}
	return map[string]any{
		"total":      total,
		"verified":   verified,
		"ungrounded": total - verified,
	}
}

func ungroundedFromOutcomes(outcomes []citationVerifyOutcome) []map[string]any {
	out := make([]map[string]any, 0)
	for _, outcome := range outcomes {
		if outcome.Verified {
			continue
		}
		entry := map[string]any{
			"sectionId":  outcome.SectionID,
			"citationId": outcome.CitationID,
			"reason":     outcome.Reason,
		}
		if marker := strings.TrimSpace(outcome.Marker); marker != "" {
			entry["marker"] = marker
		}
		if outcome.OccurrenceIndex > 0 {
			entry["occurrenceIndex"] = outcome.OccurrenceIndex
		}
		out = append(out, entry)
	}
	return out
}

func verifyFullPaperWorkspaceCitations(workspace map[string]any) ([]citationVerifyOutcome, []verifyCitationSource) {
	sources := verifySourcesFromFullPaperWorkspace(workspace)
	sectionDrafts := sliceAnyMap(workspace["sectionDraftArtifacts"])
	outcomes := make([]citationVerifyOutcome, 0)
	for _, section := range sectionDrafts {
		sectionID := wisdev.AsOptionalString(section["sectionId"])
		text := firstNonEmptyString(
			wisdev.AsOptionalString(section["content"]),
			wisdev.AsOptionalString(section["text"]),
		)
		text = stripHTMLForCitationScan(text)
		if strings.TrimSpace(text) == "" {
			continue
		}
		outcomes = append(outcomes, verifySectionTextCitations(sectionID, text, sources)...)
	}
	return outcomes, sources
}

func verifySourcesFromFullPaperWorkspace(workspace map[string]any) []verifyCitationSource {
	rawSources := fullPaperWorkspaceSourceRecords(workspace)
	out := make([]verifyCitationSource, 0, len(rawSources))
	for _, raw := range rawSources {
		src := verifyCitationSource{
			PaperID:  firstNonEmptyString(wisdev.AsOptionalString(raw["paperId"]), wisdev.AsOptionalString(raw["canonicalId"])),
			Title:    wisdev.AsOptionalString(raw["title"]),
			Link:     wisdev.AsOptionalString(raw["link"]),
			Abstract: firstNonEmptyString(wisdev.AsOptionalString(raw["summary"]), wisdev.AsOptionalString(raw["abstract"])),
			Year:     raw["year"],
		}
		if authors := sliceAnyMap(raw["authors"]); len(authors) > 0 {
			for _, author := range authors {
				name := wisdev.AsOptionalString(author["name"])
				if name == "" {
					continue
				}
				src.Authors = append(src.Authors, struct {
					Name string `json:"name"`
				}{Name: name})
			}
		}
		out = append(out, src)
	}
	return out
}

func fullPaperWorkspaceSourceRecords(workspace map[string]any) []map[string]any {
	for _, artifact := range sliceAnyMap(workspace["artifacts"]) {
		if wisdev.AsOptionalString(artifact["type"]) != "source_bundle" {
			continue
		}
		content := mapAny(artifact["content"])
		if sources := sliceAnyMap(content["sources"]); len(sources) > 0 {
			return sources
		}
	}
	if bundle := mapAny(workspace["sourceBundle"]); len(bundle) > 0 {
		if sources := sliceAnyMap(bundle["sources"]); len(sources) > 0 {
			return sources
		}
	}
	return nil
}

func stripHTMLForCitationScan(html string) string {
	html = regexp.MustCompile(`(?is)<script.*?</script>`).ReplaceAllString(html, " ")
	html = regexp.MustCompile(`(?is)<style.*?</style>`).ReplaceAllString(html, " ")
	html = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(html, " ")
	html = regexp.MustCompile(`\s+`).ReplaceAllString(html, " ")
	return strings.TrimSpace(html)
}

func citationIntegrityFingerprint(outcomes []citationVerifyOutcome) string {
	if len(outcomes) == 0 {
		return "clean"
	}
	parts := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		if outcome.Verified {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%s", outcome.SectionID, outcome.CitationID))
	}
	return strings.Join(parts, "|")
}

func failingSectionIDs(outcomes []citationVerifyOutcome) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, outcome := range outcomes {
		if outcome.Verified || strings.TrimSpace(outcome.SectionID) == "" {
			continue
		}
		if _, ok := seen[outcome.SectionID]; ok {
			continue
		}
		seen[outcome.SectionID] = struct{}{}
		out = append(out, outcome.SectionID)
	}
	return out
}

func sectionUserEdited(workspace map[string]any, sectionID string) bool {
	for _, section := range sliceAnyMap(workspace["sectionDraftArtifacts"]) {
		if wisdev.AsOptionalString(section["sectionId"]) != sectionID {
			continue
		}
		if edited, ok := section["userEdited"].(bool); ok && edited {
			return true
		}
	}
	return false
}

func defaultCitationRegroundInstructions() string {
	return "Re-ground all citations in this section. Ensure every inline [n] marker resolves to the correct source from the bibliography and remove or fix ungrounded references."
}

// verifyCiteStructured is the synthesis adapter for explicit structured citations.
func (h *SynthesisHandler) verifyCiteStructured(_ context.Context, req verifyCitationsRequest) []FETracedCitation {
	inputs := make([]structuredCitationInput, 0, len(req.StructuredCitations))
	for _, raw := range req.StructuredCitations {
		inputs = append(inputs, structuredCitationInput{
			CitationID: strings.TrimSpace(raw.CitationID),
			PaperID:    strings.TrimSpace(raw.PaperID),
			Marker:     strings.TrimSpace(raw.Marker),
			SectionID:  strings.TrimSpace(raw.SectionID),
			Claim:      strings.TrimSpace(raw.Claim),
		})
	}
	outcomes := VerifyStructuredCitations(inputs, req.Sources)
	traced := make([]FETracedCitation, 0, len(outcomes))
	for _, outcome := range outcomes {
		item := FETracedCitation{
			CitationID:         outcome.CitationID,
			CitationText:       outcome.Marker,
			PaperID:            outcome.PaperID,
			Verified:           outcome.Verified,
			Confidence:         outcome.Confidence,
			SourceChunks:       []FEChunkReference{},
			SupportingQuotes:   []FESupportingQuote{},
			VerificationMethod: outcome.VerificationMethod,
		}
		traced = append(traced, item)
	}
	return traced
}
