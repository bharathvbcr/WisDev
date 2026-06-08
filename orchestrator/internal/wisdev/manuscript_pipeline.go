package wisdev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
)

type ManuscriptBlueprint struct {
	BlueprintID     string         `json:"blueprintId"`
	JobID           string         `json:"jobId,omitempty"`
	Query           string         `json:"query"`
	SectionOrder    []string       `json:"sectionOrder"`
	Sections        []SectionBrief `json:"sections"`
	CoverageMetrics map[string]any `json:"coverageMetrics,omitempty"`
	CreatedAt       int64          `json:"createdAt"`
	UpdatedAt       int64          `json:"updatedAt"`
}

type SectionBrief struct {
	SectionID              string   `json:"sectionId"`
	Title                  string   `json:"title"`
	Goal                   string   `json:"goal"`
	WriterRole             string   `json:"writerRole"`
	RequiredClaimPacketIDs []string `json:"requiredClaimPacketIds"`
	SourceCanonicalIDs     []string `json:"sourceCanonicalIds"`
	SourceTitles           []string `json:"sourceTitles,omitempty"`
	PlannedVisualIDs       []string `json:"plannedVisualIds,omitempty"`
	UnresolvedIssues       []string `json:"unresolvedIssues,omitempty"`
	Status                 string   `json:"status"`
}

type SectionDraftParagraph struct {
	ParagraphID        string   `json:"paragraphId"`
	Text               string   `json:"text"`
	ClaimPacketIDs     []string `json:"claimPacketIds"`
	CitationIDs        []string `json:"citationIds"`
	VerificationStatus string   `json:"verificationStatus"`
	VerifierNotes      []string `json:"verifierNotes,omitempty"`
}

type BlindVerifierReport struct {
	Mode                string   `json:"mode"`
	Independent         bool     `json:"independent"`
	UsesWriterContent   bool     `json:"usesWriterContent"`
	VerifiedParagraphs  int      `json:"verifiedParagraphs"`
	FlaggedParagraphs   int      `json:"flaggedParagraphs"`
	RejectedParagraphs  int      `json:"rejectedParagraphs"`
	BlockingIssues      []string `json:"blockingIssues,omitempty"`
	VerificationSignals []string `json:"verificationSignals,omitempty"`
}

type ClaimProvenanceRecord struct {
	ParagraphID            string   `json:"paragraphId"`
	PacketID               string   `json:"packetId"`
	ClaimText              string   `json:"claimText"`
	VerifierStatus         string   `json:"verifierStatus"`
	SourceCanonicalIDs     []string `json:"sourceCanonicalIds,omitempty"`
	EvidenceLocators       []string `json:"evidenceLocators,omitempty"`
	EvidenceSnippets       []string `json:"evidenceSnippets,omitempty"`
	ContradictionPacketIDs []string `json:"contradictionPacketIds,omitempty"`
}

type ContradictionMapRecord struct {
	ParagraphID          string   `json:"paragraphId"`
	PacketID             string   `json:"packetId"`
	ConflictingPacketIDs []string `json:"conflictingPacketIds,omitempty"`
	Summary              string   `json:"summary"`
}

type SectionDraftArtifact struct {
	ArtifactID         string                   `json:"artifactId"`
	SectionID          string                   `json:"sectionId"`
	Title              string                   `json:"title"`
	WriterRole         string                   `json:"writerRole"`
	Content            string                   `json:"content"`
	Paragraphs         []SectionDraftParagraph  `json:"paragraphs"`
	ClaimPacketIDs     []string                 `json:"claimPacketIds"`
	SourceCanonicalIDs []string                 `json:"sourceCanonicalIds"`
	SourceTitles       []string                 `json:"sourceTitles,omitempty"`
	PlannedVisualIDs   []string                 `json:"plannedVisualIds,omitempty"`
	UnresolvedIssues   []string                 `json:"unresolvedIssues,omitempty"`
	ReviewStatus       string                   `json:"reviewStatus"`
	LastReviewDecision string                   `json:"lastReviewDecision,omitempty"`
	BlindVerifier      BlindVerifierReport      `json:"blindVerifier"`
	ClaimProvenance    []ClaimProvenanceRecord  `json:"claimProvenance,omitempty"`
	ContradictionMap   []ContradictionMapRecord `json:"contradictionMap,omitempty"`
	Version            int                      `json:"version"`
	CreatedAt          int64                    `json:"createdAt"`
	UpdatedAt          int64                    `json:"updatedAt"`
}

type VisualArtifact struct {
	ArtifactID         string   `json:"artifactId"`
	SectionID          string   `json:"sectionId,omitempty"`
	Title              string   `json:"title"`
	Kind               string   `json:"kind"`
	SpecType           string   `json:"specType"`
	Spec               any      `json:"spec"`
	Caption            string   `json:"caption"`
	SourcePacketIDs    []string `json:"sourcePacketIds"`
	SourceTitles       []string `json:"sourceTitles,omitempty"`
	ReviewStatus       string   `json:"reviewStatus"`
	LastReviewDecision string   `json:"lastReviewDecision,omitempty"`
	UnresolvedIssues   []string `json:"unresolvedIssues,omitempty"`
	Version            int      `json:"version"`
	CreatedAt          int64    `json:"createdAt"`
	UpdatedAt          int64    `json:"updatedAt"`
}

type ManuscriptPipelineResult struct {
	Dossier         evidence.Dossier                  `json:"dossier"`
	RawMaterials    evidence.ManuscriptRawMaterialSet `json:"rawMaterials"`
	Blueprint       ManuscriptBlueprint               `json:"blueprint"`
	SectionDrafts   []SectionDraftArtifact            `json:"sectionDrafts"`
	VisualArtifacts []VisualArtifact                  `json:"visualArtifacts"`
	CritiqueReport  map[string]any                    `json:"critiqueReport"`
	RevisionTasks   []map[string]any                  `json:"revisionTasks"`
	StageStates     []map[string]any                  `json:"stageStates"`
}

type ManuscriptPipeline struct {
	pythonBaseURL string
	httpClient    *http.Client
}

func NewManuscriptPipeline(pythonBaseURL string) *ManuscriptPipeline {
	baseURL := strings.TrimSpace(pythonBaseURL)
	if baseURL == "" {
		baseURL = ResolvePythonBase()
	}
	return &ManuscriptPipeline{
		pythonBaseURL: strings.TrimSuffix(baseURL, "/"),
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *ManuscriptPipeline) Run(ctx context.Context, jobID string, query string, papers []search.Paper) (ManuscriptPipelineResult, error) {
	rawMaterials, dossier, err := evidence.BuildRawMaterialSet(jobID, query, papers)
	if err != nil {
		return ManuscriptPipelineResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ManuscriptPipelineResult{}, err
	}

	blueprint := p.planSections(jobID, query, rawMaterials)
	if err := ctx.Err(); err != nil {
		return ManuscriptPipelineResult{}, err
	}

	visuals := p.composeVisuals(jobID, query, rawMaterials, blueprint)
	sections := p.writeSections(ctx, jobID, rawMaterials, blueprint)
	sections = p.verifySectionsBlind(sections)
	sections = p.refineSections(ctx, sections, rawMaterials)
	sections = p.verifySectionsBlind(sections)
	critique := p.peerReview(jobID, query, rawMaterials, blueprint, sections, visuals)
	revisionTasks := p.buildRevisionTasks(jobID, sections, visuals, critique)
	stageStates := p.buildStageStates(len(rawMaterials.ClaimPackets), len(sections), len(visuals), len(revisionTasks))

	return ManuscriptPipelineResult{
		Dossier:         dossier,
		RawMaterials:    rawMaterials,
		Blueprint:       blueprint,
		SectionDrafts:   sections,
		VisualArtifacts: visuals,
		CritiqueReport:  critique,
		RevisionTasks:   revisionTasks,
		StageStates:     stageStates,
	}, nil
}

func (p *ManuscriptPipeline) planSections(jobID string, query string, raw evidence.ManuscriptRawMaterialSet) ManuscriptBlueprint {
	now := time.Now().UnixMilli()
	sourceTitleByCanonicalID := sourceTitleIndex(raw.CanonicalSources)

	templates := []struct {
		id         string
		title      string
		goal       string
		writerRole string
	}{
		{id: "abstract", title: "Abstract", goal: "Summarize the reviewable manuscript state and the strongest grounded claims.", writerRole: "abstract_writer"},
		{id: "introduction", title: "Introduction", goal: "Frame the research problem, scope, and motivation from the current evidence base.", writerRole: "framing_writer"},
		{id: "literature_review", title: "Literature Review", goal: "Synthesize prior work and thematic clusters from the raw materials.", writerRole: "literature_reviewer"},
		{id: "methods", title: "Methods", goal: "Describe methodological patterns and extraction provenance grounded in the source corpus.", writerRole: "methods_writer"},
		{id: "results", title: "Results", goal: "Highlight the most grounded empirical findings and visual evidence.", writerRole: "results_writer"},
		{id: "discussion", title: "Discussion", goal: "Compare, reconcile, and critique the evidence, preserving unresolved conflicts.", writerRole: "discussion_writer"},
		{id: "conclusion", title: "Conclusion", goal: "Close with supported takeaways, limits, and open gaps for the next pass.", writerRole: "conclusion_writer"},
	}

	sectionOrder := make([]string, 0, len(templates))
	sections := make([]SectionBrief, 0, len(templates))
	for _, template := range templates {
		relevantPackets := selectRelevantPackets(raw.ClaimPackets, template.id, 4)
		sourceIDs := make([]string, 0, len(relevantPackets))
		sourceTitles := make([]string, 0, len(relevantPackets))
		claimPacketIDs := make([]string, 0, len(relevantPackets))
		unresolvedIssues := make([]string, 0)
		for _, packet := range relevantPackets {
			claimPacketIDs = append(claimPacketIDs, packet.PacketID)
			if packet.VerifierStatus != "verified" {
				unresolvedIssues = append(unresolvedIssues, fmt.Sprintf("Packet %s requires blind verification follow-up.", packet.PacketID))
			}
			if len(packet.ContradictionPacketIDs) > 0 {
				unresolvedIssues = append(unresolvedIssues, fmt.Sprintf("Packet %s has unresolved contradiction links.", packet.PacketID))
			}
			for _, span := range packet.EvidenceSpans {
				sourceIDs = append(sourceIDs, span.SourceCanonicalID)
				if title := sourceTitleByCanonicalID[span.SourceCanonicalID]; title != "" {
					sourceTitles = append(sourceTitles, title)
				}
			}
		}
		plannedVisuals := plannedVisualIDs(raw.VisualEvidence, claimPacketIDs)
		if len(claimPacketIDs) == 0 {
			unresolvedIssues = append(unresolvedIssues, "No grounded claim packets are assigned yet.")
		}
		if template.id == "results" && len(plannedVisuals) == 0 {
			unresolvedIssues = append(unresolvedIssues, "No grounded visual artifact is planned for the results section.")
		}
		sectionOrder = append(sectionOrder, template.id)
		sections = append(sections, SectionBrief{
			SectionID:              template.id,
			Title:                  template.title,
			Goal:                   template.goal,
			WriterRole:             template.writerRole,
			RequiredClaimPacketIDs: uniqueStrings(claimPacketIDs),
			SourceCanonicalIDs:     uniqueStrings(sourceIDs),
			SourceTitles:           uniqueStrings(sourceTitles),
			PlannedVisualIDs:       uniqueStrings(plannedVisuals),
			UnresolvedIssues:       uniqueStrings(unresolvedIssues),
			Status:                 sectionStatusFromClaims(relevantPackets),
		})
	}

	return ManuscriptBlueprint{
		BlueprintID:  fmt.Sprintf("blueprint_%d_%s", now, hashIDForPipeline(jobID)),
		JobID:        jobID,
		Query:        query,
		SectionOrder: sectionOrder,
		Sections:     sections,
		CoverageMetrics: map[string]any{
			"sectionCount":     len(sections),
			"claimPacketCount": len(raw.ClaimPackets),
			"visualCount":      len(raw.VisualEvidence),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (p *ManuscriptPipeline) writeSections(ctx context.Context, _ string, raw evidence.ManuscriptRawMaterialSet, blueprint ManuscriptBlueprint) []SectionDraftArtifact {
	now := time.Now().UnixMilli()
	packetIndex := packetIndexByID(raw.ClaimPackets)
	sections := make([]SectionDraftArtifact, len(blueprint.Sections))
	var wg sync.WaitGroup

	for idx, brief := range blueprint.Sections {
		wg.Add(1)
		go func(index int, sectionBrief SectionBrief) {
			defer wg.Done()

			paragraphs, scaffoldContent, claimPackets := buildSectionScaffold(sectionBrief, packetIndex, blueprint.Query)
			content := scaffoldContent
			if generated, err := p.generateSectionContent(ctx, sectionBrief, claimPackets, blueprint.Query); err == nil && strings.TrimSpace(generated) != "" {
				content = strings.TrimSpace(generated)
			}
			if rebuilt := buildContentParagraphs(sectionBrief.SectionID, content, claimPackets); len(rebuilt) > 0 {
				paragraphs = rebuilt
			}

			sections[index] = SectionDraftArtifact{
				ArtifactID:         fmt.Sprintf("section_%d_%d", now, index+1),
				SectionID:          sectionBrief.SectionID,
				Title:              sectionBrief.Title,
				WriterRole:         sectionBrief.WriterRole,
				Content:            content,
				Paragraphs:         paragraphs,
				ClaimPacketIDs:     uniqueStrings(sectionBrief.RequiredClaimPacketIDs),
				SourceCanonicalIDs: uniqueStrings(sectionBrief.SourceCanonicalIDs),
				SourceTitles:       uniqueStrings(sectionBrief.SourceTitles),
				PlannedVisualIDs:   uniqueStrings(sectionBrief.PlannedVisualIDs),
				UnresolvedIssues:   uniqueStrings(sectionBrief.UnresolvedIssues),
				ReviewStatus:       sectionBrief.Status,
				ClaimProvenance:    buildClaimProvenance(paragraphs, claimPackets),
				ContradictionMap:   buildContradictionMap(paragraphs, claimPackets),
				Version:            1,
				CreatedAt:          now,
				UpdatedAt:          now,
			}
		}(idx, brief)
	}

	wg.Wait()
	return sections
}

func (p *ManuscriptPipeline) refineSections(
	ctx context.Context,
	sections []SectionDraftArtifact,
	raw evidence.ManuscriptRawMaterialSet,
) []SectionDraftArtifact {
	var wg sync.WaitGroup
	out := make([]SectionDraftArtifact, len(sections))
	copy(out, sections)

	for i, section := range out {
		if section.ReviewStatus != "needs_revision" || len(section.UnresolvedIssues) == 0 {
			continue
		}
		wg.Add(1)
		go func(idx int, draft SectionDraftArtifact) {
			defer wg.Done()
			refined, err := p.refineSectionContent(ctx, draft, raw)
			if err == nil && strings.TrimSpace(refined) != "" {
				claimPackets := claimPacketsByIDs(raw.ClaimPackets, draft.ClaimPacketIDs)
				out[idx].Content = strings.TrimSpace(refined)
				if rebuilt := buildContentParagraphs(draft.SectionID, out[idx].Content, claimPackets); len(rebuilt) > 0 {
					out[idx].Paragraphs = rebuilt
				}
				out[idx].ClaimProvenance = buildClaimProvenance(out[idx].Paragraphs, claimPackets)
				out[idx].ContradictionMap = buildContradictionMap(out[idx].Paragraphs, claimPackets)
				out[idx].Version = 2
				out[idx].UpdatedAt = time.Now().UnixMilli()
				out[idx].ReviewStatus = "needs_review"
				out[idx].LastReviewDecision = "refined_pending_verification"
				out[idx].UnresolvedIssues = nil
			}
		}(i, section)
	}

	wg.Wait()
	return out
}

func (p *ManuscriptPipeline) generateSectionContent(
	ctx context.Context,
	brief SectionBrief,
	claimPackets []evidence.EvidencePacket,
	query string,
) (string, error) {
	payload := map[string]any{
		"section_id":    brief.SectionID,
		"writer_role":   brief.WriterRole,
		"section_goal":  brief.Goal,
		"claim_packets": claimPackets,
		"source_titles": uniqueStrings(brief.SourceTitles),
		"query":         query,
		"max_tokens":    800,
	}
	return p.postSectionContent(ctx, "/wisdev/manuscript/section/generate", payload)
}

func (p *ManuscriptPipeline) refineSectionContent(
	ctx context.Context,
	section SectionDraftArtifact,
	raw evidence.ManuscriptRawMaterialSet,
) (string, error) {
	payload := map[string]any{
		"section_id":        section.SectionID,
		"original_content":  section.Content,
		"unresolved_issues": section.UnresolvedIssues,
		"claim_packets":     claimPacketsByIDs(raw.ClaimPackets, section.ClaimPacketIDs),
		"max_tokens":        800,
	}
	return p.postSectionContent(ctx, "/wisdev/manuscript/section/refine", payload)
}

func (p *ManuscriptPipeline) postSectionContent(ctx context.Context, path string, payload map[string]any) (string, error) {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return "", fmt.Errorf("python sidecar base URL is not configured")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.pythonBaseURL+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Caller-Service", "go_orchestrator")
	if key := stackconfig.ResolveInternalServiceKey(); key != "" {
		req.Header.Set("X-Internal-Service-Key", key)
		req.Header.Set("Authorization", "Bearer "+key)
	}

	client := p.httpClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("python sidecar returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Content) == "" {
		return "", fmt.Errorf("python sidecar returned empty section content")
	}
	return result.Content, nil
}

func buildSectionScaffold(
	brief SectionBrief,
	packetIndex map[string]evidence.EvidencePacket,
	query string,
) ([]SectionDraftParagraph, string, []evidence.EvidencePacket) {
	paragraphs := make([]SectionDraftParagraph, 0, len(brief.RequiredClaimPacketIDs))
	contentBlocks := make([]string, 0, len(brief.RequiredClaimPacketIDs))
	claimPackets := make([]evidence.EvidencePacket, 0, len(brief.RequiredClaimPacketIDs))

	for paragraphIndex, packetID := range brief.RequiredClaimPacketIDs {
		packet, ok := packetIndex[packetID]
		if !ok {
			continue
		}
		claimPackets = append(claimPackets, packet)
		citationIDs := sourceIDsFromPacket(packet)
		text := fmt.Sprintf("%s [%s]", packet.ClaimText, packet.PacketID)
		if len(citationIDs) > 0 {
			text = fmt.Sprintf("%s Grounding sources: %s.", text, strings.Join(citationIDs, ", "))
		}
		paragraph := SectionDraftParagraph{
			ParagraphID:        fmt.Sprintf("paragraph_%s_%d", brief.SectionID, paragraphIndex+1),
			Text:               text,
			ClaimPacketIDs:     []string{packet.PacketID},
			CitationIDs:        citationIDs,
			VerificationStatus: packet.VerifierStatus,
			VerifierNotes:      append([]string{}, packet.VerifierNotes...),
		}
		paragraphs = append(paragraphs, paragraph)
		contentBlocks = append(contentBlocks, text)
	}

	if len(paragraphs) == 0 {
		paragraphs = append(paragraphs, SectionDraftParagraph{
			ParagraphID:        fmt.Sprintf("paragraph_%s_seed", brief.SectionID),
			Text:               fmt.Sprintf("%s remains a scaffold section pending grounded source packets for the query: %s.", brief.Title, query),
			ClaimPacketIDs:     []string{},
			CitationIDs:        []string{},
			VerificationStatus: "needs_revision",
			VerifierNotes:      []string{"section has no grounded claim packets yet"},
		})
		contentBlocks = append(contentBlocks, paragraphs[0].Text)
	}

	return paragraphs, strings.Join(contentBlocks, "\n\n"), claimPackets
}

func buildContentParagraphs(
	sectionID string,
	content string,
	claimPackets []evidence.EvidencePacket,
) []SectionDraftParagraph {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}

	paragraphTexts := splitContentParagraphs(trimmed)
	if len(paragraphTexts) == 0 {
		return nil
	}

	packetIndex := packetIndexByID(claimPackets)
	paragraphs := make([]SectionDraftParagraph, 0, len(paragraphTexts))
	for idx, paragraphText := range paragraphTexts {
		explicitPacketIDs := extractExplicitPacketIDs(paragraphText, claimPackets)
		matchedPacketIDs := explicitPacketIDs
		if len(matchedPacketIDs) == 0 {
			matchedPacketIDs = inferPacketIDsFromText(paragraphText, claimPackets)
		}

		citationIDs := sourceIDsFromPacketsByIDs(packetIndex, matchedPacketIDs)
		status := "verified"
		notes := make([]string, 0, 3)

		if len(explicitPacketIDs) == 0 {
			status = "needs_review"
			notes = append(notes, "paragraph is missing explicit packet citations")
		}
		if len(matchedPacketIDs) == 0 {
			status = "rejected"
			notes = append(notes, "paragraph could not be aligned to grounded claim packets")
		}
		if len(citationIDs) == 0 {
			if status != "rejected" {
				status = "needs_review"
			}
			notes = append(notes, "paragraph does not map to grounded source citations")
		}
		for _, packetID := range matchedPacketIDs {
			packet, ok := packetIndex[packetID]
			if !ok {
				continue
			}
			if packet.VerifierStatus != "verified" && status != "rejected" {
				status = "needs_review"
				notes = append(notes, fmt.Sprintf("packet %s is not blind-verified", packet.PacketID))
			}
		}

		paragraphs = append(paragraphs, SectionDraftParagraph{
			ParagraphID:        fmt.Sprintf("paragraph_%s_%d", sectionID, idx+1),
			Text:               paragraphText,
			ClaimPacketIDs:     matchedPacketIDs,
			CitationIDs:        citationIDs,
			VerificationStatus: status,
			VerifierNotes:      uniqueStrings(notes),
		})
	}

	return paragraphs
}

func splitContentParagraphs(content string) []string {
	rawBlocks := regexp.MustCompile(`\n\s*\n+`).Split(strings.TrimSpace(content), -1)
	paragraphs := make([]string, 0, len(rawBlocks))
	for _, block := range rawBlocks {
		trimmed := strings.TrimSpace(block)
		if trimmed == "" {
			continue
		}
		paragraphs = append(paragraphs, trimmed)
	}
	return paragraphs
}

func extractExplicitPacketIDs(text string, claimPackets []evidence.EvidencePacket) []string {
	if strings.TrimSpace(text) == "" || len(claimPackets) == 0 {
		return nil
	}

	allowed := make(map[string]struct{}, len(claimPackets))
	for _, packet := range claimPackets {
		allowed[packet.PacketID] = struct{}{}
	}

	matches := regexp.MustCompile(`\[(.*?)\]`).FindAllStringSubmatch(text, -1)
	found := make([]string, 0)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		for _, token := range strings.FieldsFunc(match[1], func(r rune) bool {
			return r == ',' || r == ';' || r == '|' || unicode.IsSpace(r)
		}) {
			token = strings.TrimSpace(token)
			if _, ok := allowed[token]; ok {
				found = append(found, token)
			}
		}
	}
	return uniqueStrings(found)
}

func inferPacketIDsFromText(text string, claimPackets []evidence.EvidencePacket) []string {
	paragraphTokens := keywordTokenSet(text)
	if len(paragraphTokens) == 0 {
		return nil
	}

	matched := make([]string, 0, len(claimPackets))
	for _, packet := range claimPackets {
		claimTokens := keywordTokenSet(packet.ClaimText)
		if len(claimTokens) == 0 {
			continue
		}
		overlapCount := 0
		for token := range paragraphTokens {
			if _, ok := claimTokens[token]; ok {
				overlapCount++
			}
		}
		overlapRatio := float64(overlapCount) / float64(len(claimTokens))
		if overlapCount >= 2 || overlapRatio >= 0.35 {
			matched = append(matched, packet.PacketID)
		}
	}
	return uniqueStrings(matched)
}

func keywordTokenSet(value string) map[string]struct{} {
	stopwords := map[string]struct{}{
		"that": {}, "this": {}, "with": {}, "from": {}, "were": {}, "have": {},
		"been": {}, "into": {}, "their": {}, "there": {}, "which": {}, "these": {},
		"using": {}, "used": {}, "than": {}, "when": {}, "where": {}, "while": {},
		"shows": {}, "showed": {}, "results": {}, "section": {}, "study": {},
	}
	tokens := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if len(token) < 4 {
			continue
		}
		if _, blocked := stopwords[token]; blocked {
			continue
		}
		out[token] = struct{}{}
	}
	return out
}

func sourceIDsFromPacketsByIDs(
	packetIndex map[string]evidence.EvidencePacket,
	packetIDs []string,
) []string {
	sourceIDs := make([]string, 0, len(packetIDs))
	for _, packetID := range packetIDs {
		packet, ok := packetIndex[packetID]
		if !ok {
			continue
		}
		sourceIDs = append(sourceIDs, sourceIDsFromPacket(packet)...)
	}
	return uniqueStrings(sourceIDs)
}

func claimPacketsByIDs(
	packets []evidence.EvidencePacket,
	packetIDs []string,
) []evidence.EvidencePacket {
	packetIndex := packetIndexByID(packets)
	selected := make([]evidence.EvidencePacket, 0, len(packetIDs))
	for _, packetID := range packetIDs {
		packet, ok := packetIndex[packetID]
		if !ok {
			continue
		}
		selected = append(selected, packet)
	}
	return selected
}

func (p *ManuscriptPipeline) verifySectionsBlind(sections []SectionDraftArtifact) []SectionDraftArtifact {
	verified := make([]SectionDraftArtifact, 0, len(sections))
	for _, section := range sections {
		next := section
		unresolved := append([]string{}, next.UnresolvedIssues...)
		blockingIssues := []string{}
		allGrounded := true
		report := BlindVerifierReport{
			Mode:              "lineage_only",
			Independent:       true,
			UsesWriterContent: false,
			VerificationSignals: []string{
				"claim_packet_lineage",
				"citation_lineage",
				"claim_packet_verification_status",
			},
		}
		for paragraphIndex, paragraph := range next.Paragraphs {
			if len(paragraph.ClaimPacketIDs) == 0 || len(paragraph.CitationIDs) == 0 {
				allGrounded = false
				paragraph.VerificationStatus = "rejected"
				paragraph.VerifierNotes = append(paragraph.VerifierNotes, "paragraph is missing claim packet lineage or citations")
				report.RejectedParagraphs++
				blockingIssues = append(blockingIssues, fmt.Sprintf("paragraph %s is missing claim packet lineage or citations", paragraph.ParagraphID))
			} else if paragraph.VerificationStatus != "verified" {
				allGrounded = false
				paragraph.VerificationStatus = "needs_review"
				paragraph.VerifierNotes = append(paragraph.VerifierNotes, "paragraph depends on non-verified claim packets")
				report.FlaggedParagraphs++
				blockingIssues = append(blockingIssues, fmt.Sprintf("paragraph %s depends on non-verified claim packets", paragraph.ParagraphID))
			} else {
				paragraph.VerificationStatus = "verified"
				report.VerifiedParagraphs++
			}
			next.Paragraphs[paragraphIndex] = paragraph
		}

		if allGrounded {
			next.ReviewStatus = "ready_for_review"
			next.LastReviewDecision = "blind_verified"
		} else {
			next.ReviewStatus = "needs_revision"
			next.LastReviewDecision = "blind_verifier_flagged"
			unresolved = append(unresolved, "Blind verifier found missing or weak paragraph grounding.")
		}
		report.BlockingIssues = uniqueStrings(blockingIssues)
		next.BlindVerifier = report
		next.UnresolvedIssues = uniqueStrings(unresolved)
		verified = append(verified, next)
	}
	return verified
}

func buildClaimProvenance(paragraphs []SectionDraftParagraph, claimPackets []evidence.EvidencePacket) []ClaimProvenanceRecord {
	packetIndex := packetIndexByID(claimPackets)
	out := make([]ClaimProvenanceRecord, 0)
	for _, paragraph := range paragraphs {
		for _, packetID := range paragraph.ClaimPacketIDs {
			packet, ok := packetIndex[packetID]
			if !ok {
				continue
			}
			locators := make([]string, 0, len(packet.EvidenceSpans))
			snippets := make([]string, 0, len(packet.EvidenceSpans))
			sourceCanonicalIDs := make([]string, 0, len(packet.EvidenceSpans))
			for _, span := range packet.EvidenceSpans {
				sourceCanonicalIDs = append(sourceCanonicalIDs, span.SourceCanonicalID)
				if locator := strings.TrimSpace(firstNonEmptyInPipeline(span.Locator, span.Section)); locator != "" {
					locators = append(locators, locator)
				}
				if snippet := strings.TrimSpace(span.Snippet); snippet != "" {
					snippets = append(snippets, snippet)
				}
			}
			out = append(out, ClaimProvenanceRecord{
				ParagraphID:            paragraph.ParagraphID,
				PacketID:               packet.PacketID,
				ClaimText:              packet.ClaimText,
				VerifierStatus:         firstNonEmptyInPipeline(packet.VerifierStatus, paragraph.VerificationStatus),
				SourceCanonicalIDs:     uniqueStrings(sourceCanonicalIDs),
				EvidenceLocators:       uniqueStrings(locators),
				EvidenceSnippets:       uniqueStrings(snippets),
				ContradictionPacketIDs: uniqueStrings(packet.ContradictionPacketIDs),
			})
		}
	}
	return out
}

func buildContradictionMap(paragraphs []SectionDraftParagraph, claimPackets []evidence.EvidencePacket) []ContradictionMapRecord {
	packetIndex := packetIndexByID(claimPackets)
	out := make([]ContradictionMapRecord, 0)
	for _, paragraph := range paragraphs {
		for _, packetID := range paragraph.ClaimPacketIDs {
			packet, ok := packetIndex[packetID]
			if !ok || len(packet.ContradictionPacketIDs) == 0 {
				continue
			}
			out = append(out, ContradictionMapRecord{
				ParagraphID:          paragraph.ParagraphID,
				PacketID:             packet.PacketID,
				ConflictingPacketIDs: uniqueStrings(packet.ContradictionPacketIDs),
				Summary:              fmt.Sprintf("Packet %s has unresolved contradiction links.", packet.PacketID),
			})
		}
	}
	return out
}

func (p *ManuscriptPipeline) composeVisuals(jobID string, query string, raw evidence.ManuscriptRawMaterialSet, blueprint ManuscriptBlueprint) []VisualArtifact {
	now := time.Now().UnixMilli()
	packetIndex := packetIndexByID(raw.ClaimPackets)
	sourceTitles := sourceTitleIndex(raw.CanonicalSources)
	out := make([]VisualArtifact, 0, len(raw.VisualEvidence)+1)
	for idx, visual := range raw.VisualEvidence {
		specType, spec := buildVisualSpec(visual, packetIndex)
		reviewStatus := "ready_for_review"
		unresolvedIssues := make([]string, 0)
		if len(visual.SourcePacketIDs) == 0 {
			reviewStatus = "needs_revision"
			unresolvedIssues = append(unresolvedIssues, "visual is not grounded to any claim packets")
		}
		sourceCanonicalIDs := uniqueStrings(sourceCanonicalIDsForVisual(visual, packetIndex))
		sourceLabelSet := make([]string, 0, len(sourceCanonicalIDs))
		for _, sourceID := range sourceCanonicalIDs {
			if title := sourceTitles[sourceID]; title != "" {
				sourceLabelSet = append(sourceLabelSet, title)
			}
		}
		out = append(out, VisualArtifact{
			ArtifactID:       fmt.Sprintf("visual_artifact_%d_%d", now, idx+1),
			SectionID:        inferVisualSection(visual, blueprint),
			Title:            firstNonEmptyInPipeline(visual.Title, strings.Title(strings.ReplaceAll(visual.Kind, "_", " "))),
			Kind:             visualKind(visual),
			SpecType:         specType,
			Spec:             spec,
			Caption:          firstNonEmptyInPipeline(visual.Caption, fmt.Sprintf("Grounded visual generated for query: %s", query)),
			SourcePacketIDs:  uniqueStrings(visual.SourcePacketIDs),
			SourceTitles:     uniqueStrings(sourceLabelSet),
			ReviewStatus:     reviewStatus,
			UnresolvedIssues: uniqueStrings(unresolvedIssues),
			Version:          1,
			CreatedAt:        now,
			UpdatedAt:        now,
		})
	}

	if len(out) == 0 {
		out = append(out, VisualArtifact{
			ArtifactID:      fmt.Sprintf("visual_artifact_%d_seed", now),
			SectionID:       "discussion",
			Title:           "Concept Diagram",
			Kind:            "concept_diagram",
			SpecType:        "mermaid",
			Spec:            fmt.Sprintf("flowchart TD\n    query[%q] --> blueprint[\"%d planned sections\"]\n    blueprint --> review[\"Peer review queue\"]", query, len(blueprint.Sections)),
			Caption:         "Concept diagram connecting the query seed to the current manuscript blueprint and review loop.",
			SourcePacketIDs: firstPacketIDs(raw.ClaimPackets, 1),
			SourceTitles:    []string{},
			ReviewStatus:    "needs_revision",
			UnresolvedIssues: []string{
				"visual is a scaffold concept diagram because no extracted table or figure evidence was available",
			},
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	return out
}

func (p *ManuscriptPipeline) peerReview(jobID string, query string, raw evidence.ManuscriptRawMaterialSet, blueprint ManuscriptBlueprint, sections []SectionDraftArtifact, visuals []VisualArtifact) map[string]any {
	now := time.Now().UnixMilli()
	strengths := make([]string, 0, 4)
	weaknesses := make([]string, 0, 6)
	risks := make([]string, 0, 6)
	recommendations := make([]string, 0, 6)

	if len(raw.ClaimPackets) > 0 {
		strengths = append(strengths, fmt.Sprintf("Raw material graph contains %d claim packets with packet-level lineage.", len(raw.ClaimPackets)))
	}
	if len(visuals) > 0 {
		strengths = append(strengths, fmt.Sprintf("%d visual artifacts are attached to the manuscript workspace.", len(visuals)))
	}
	if len(raw.SourceClusters) > 0 {
		strengths = append(strengths, fmt.Sprintf("Source materials are clustered into %d thematic groups.", len(raw.SourceClusters)))
	}

	for _, section := range sections {
		if section.ReviewStatus == "needs_revision" {
			weaknesses = append(weaknesses, fmt.Sprintf("%s has unresolved grounding issues.", section.Title))
			recommendations = append(recommendations, fmt.Sprintf("Rewrite %s with stronger packet-to-paragraph grounding.", strings.ToLower(section.Title)))
		}
		if len(section.UnresolvedIssues) > 0 {
			risks = append(risks, fmt.Sprintf("%s: %s", section.Title, section.UnresolvedIssues[0]))
		}
	}

	for _, visual := range visuals {
		if visual.ReviewStatus != "ready_for_review" {
			weaknesses = append(weaknesses, fmt.Sprintf("Visual %s is not fully grounded.", visual.Title))
			recommendations = append(recommendations, fmt.Sprintf("Regenerate %s to attach stronger packet lineage.", strings.ToLower(visual.Title)))
		}
		if len(visual.UnresolvedIssues) > 0 {
			risks = append(risks, fmt.Sprintf("%s: %s", visual.Title, visual.UnresolvedIssues[0]))
		}
	}

	if contradictionCount := contradictionCount(raw); contradictionCount > 0 {
		risks = append(risks, fmt.Sprintf("%d contradiction links remain unresolved in the raw material set.", contradictionCount))
	}
	if len(raw.Gaps) > 0 {
		weaknesses = append(weaknesses, raw.Gaps...)
		recommendations = append(recommendations, "Acquire additional papers before final export.")
	}

	score := 0.5
	score += minFloat(float64(len(raw.ClaimPackets))/12.0, 0.2)
	score += minFloat(float64(len(visuals))/4.0, 0.1)
	if len(weaknesses) == 0 {
		score += 0.15
	}
	if len(risks) == 0 {
		score += 0.05
	}
	score = minFloat(score, 0.95)

	return map[string]any{
		"critiqueId":      fmt.Sprintf("critique_%d_%s", now, hashIDForPipeline(jobID)),
		"jobId":           jobID,
		"query":           query,
		"createdAt":       now,
		"status":          "open",
		"overallScore":    score,
		"strengths":       uniqueStrings(strengths),
		"weaknesses":      uniqueStrings(weaknesses),
		"risks":           uniqueStrings(risks),
		"recommendations": uniqueStrings(recommendations),
		"blueprintId":     blueprint.BlueprintID,
	}
}

func (p *ManuscriptPipeline) buildRevisionTasks(jobID string, sections []SectionDraftArtifact, visuals []VisualArtifact, critique map[string]any) []map[string]any {
	now := time.Now().UnixMilli()
	tasks := make([]map[string]any, 0)
	for _, section := range sections {
		if section.ReviewStatus == "needs_revision" {
			tasks = append(tasks, map[string]any{
				"taskId":      fmt.Sprintf("revision_%d_%s", now, section.SectionID),
				"jobId":       jobID,
				"createdAt":   now,
				"status":      "pending",
				"priority":    revisionPriority(section.UnresolvedIssues),
				"title":       fmt.Sprintf("Rewrite %s", section.Title),
				"description": firstNonEmptyInPipeline(firstString(section.UnresolvedIssues), "Tighten packet grounding and citations."),
				"targetType":  "section",
				"targetId":    section.SectionID,
			})
		}
	}
	for _, visual := range visuals {
		if visual.ReviewStatus != "ready_for_review" {
			tasks = append(tasks, map[string]any{
				"taskId":      fmt.Sprintf("revision_%d_%s", now, visual.ArtifactID),
				"jobId":       jobID,
				"createdAt":   now,
				"status":      "pending",
				"priority":    "medium",
				"title":       fmt.Sprintf("Regenerate %s", visual.Title),
				"description": firstNonEmptyInPipeline(firstString(visual.UnresolvedIssues), "Attach grounded packet lineage to the visual."),
				"targetType":  "visual",
				"targetId":    visual.ArtifactID,
			})
		}
	}
	if len(tasks) == 0 {
		tasks = append(tasks, map[string]any{
			"taskId":      fmt.Sprintf("revision_%d_finalize", now),
			"jobId":       jobID,
			"createdAt":   now,
			"status":      "completed",
			"priority":    "low",
			"title":       "Finalize manuscript",
			"description": "The draft is grounded enough for approval or export.",
			"targetType":  "workspace",
			"targetId":    jobID,
		})
	}
	return tasks
}

func (p *ManuscriptPipeline) buildStageStates(claimCount int, sectionCount int, visualCount int, revisionCount int) []map[string]any {
	return []map[string]any{
		{"id": "scout", "label": "Scout", "status": "completed", "completion": 100},
		{"id": "raw_material_assembler", "label": "Raw Material Assembler", "status": "completed", "completion": 100, "claimPacketCount": claimCount},
		{"id": "section_planner", "label": "Section Planner", "status": "completed", "completion": 100, "sectionCount": sectionCount},
		{"id": "specialist_writer", "label": "Specialist Writer", "status": "completed", "completion": 100},
		{"id": "blind_verifier", "label": "Blind Verifier", "status": "completed", "completion": 100},
		{"id": "peer_reviewer", "label": "Peer Reviewer", "status": "awaiting_review", "completion": 100, "visualCount": visualCount},
		{"id": "revision_editor", "label": "Revision Editor", "status": stageStatusForRevisionCount(revisionCount), "completion": 0},
	}
}

func buildVisualSpec(visual evidence.VisualEvidence, packets map[string]evidence.EvidencePacket) (string, any) {
	if value := extractFirstNumericValue(visual.Caption); value != nil {
		return "vega_lite", map[string]any{
			"$schema": "https://vega.github.io/schema/vega-lite/v5.json",
			"data": map[string]any{
				"values": []map[string]any{
					{"label": firstNonEmptyInPipeline(visual.Title, visual.VisualID), "value": *value},
				},
			},
			"mark": "bar",
			"encoding": map[string]any{
				"x": map[string]any{"field": "label", "type": "nominal"},
				"y": map[string]any{"field": "value", "type": "quantitative"},
			},
		}
	}

	nodes := make([]string, 0, len(visual.SourcePacketIDs)+1)
	edges := make([]string, 0, len(visual.SourcePacketIDs)+1)
	root := sanitizeMermaidNode(visual.VisualID, firstNonEmptyInPipeline(visual.Title, "Visual"))
	nodes = append(nodes, root)
	for idx, packetID := range visual.SourcePacketIDs {
		packet, ok := packets[packetID]
		if !ok {
			continue
		}
		nodeID := fmt.Sprintf("p%d", idx+1)
		nodes = append(nodes, sanitizeMermaidNode(nodeID, truncateForLabel(packet.ClaimText, 42)))
		edges = append(edges, fmt.Sprintf("    %s --> %s", packetNodeRootID(visual.VisualID), nodeID))
	}
	specLines := []string{"flowchart TD"}
	specLines = append(specLines, "    "+root)
	specLines = append(specLines, edges...)
	return "mermaid", strings.Join(specLines, "\n")
}

func inferVisualSection(visual evidence.VisualEvidence, blueprint ManuscriptBlueprint) string {
	for _, section := range blueprint.Sections {
		for _, visualID := range section.PlannedVisualIDs {
			if visualID == visual.VisualID {
				return section.SectionID
			}
		}
	}
	switch strings.ToLower(visual.Kind) {
	case "table", "figure":
		return "results"
	default:
		return "discussion"
	}
}

func visualKind(visual evidence.VisualEvidence) string {
	switch strings.ToLower(visual.Kind) {
	case "table":
		return "table_summary"
	case "figure", "plot":
		return "chart"
	default:
		return "concept_diagram"
	}
}

func sourceCanonicalIDsForVisual(visual evidence.VisualEvidence, packets map[string]evidence.EvidencePacket) []string {
	out := make([]string, 0, len(visual.SourcePacketIDs))
	for _, packetID := range visual.SourcePacketIDs {
		packet, ok := packets[packetID]
		if !ok {
			continue
		}
		out = append(out, sourceIDsFromPacket(packet)...)
	}
	return uniqueStrings(out)
}

func packetIndexByID(packets []evidence.EvidencePacket) map[string]evidence.EvidencePacket {
	out := make(map[string]evidence.EvidencePacket, len(packets))
	for _, packet := range packets {
		out[packet.PacketID] = packet
	}
	return out
}

func sourceTitleIndex(sources []evidence.CanonicalCitationRecord) map[string]string {
	out := make(map[string]string, len(sources))
	for _, source := range sources {
		out[source.CanonicalID] = source.Title
	}
	return out
}

func selectRelevantPackets(packets []evidence.EvidencePacket, sectionID string, limit int) []evidence.EvidencePacket {
	selected := make([]evidence.EvidencePacket, 0, limit)
	for _, packet := range packets {
		if containsString(packet.SectionRelevance, sectionID) {
			selected = append(selected, packet)
			if len(selected) >= limit {
				return selected
			}
		}
	}
	for _, packet := range packets {
		if len(selected) >= limit {
			break
		}
		if containsString(uniquePacketIDs(selected), packet.PacketID) {
			continue
		}
		selected = append(selected, packet)
	}
	return selected
}

func uniquePacketIDs(packets []evidence.EvidencePacket) []string {
	out := make([]string, 0, len(packets))
	for _, packet := range packets {
		out = append(out, packet.PacketID)
	}
	return uniqueStrings(out)
}

func plannedVisualIDs(visuals []evidence.VisualEvidence, claimPacketIDs []string) []string {
	out := make([]string, 0, len(visuals))
	for _, visual := range visuals {
		for _, packetID := range visual.SourcePacketIDs {
			if containsString(claimPacketIDs, packetID) {
				out = append(out, visual.VisualID)
				break
			}
		}
	}
	return uniqueStrings(out)
}

func sourceIDsFromPacket(packet evidence.EvidencePacket) []string {
	out := make([]string, 0, len(packet.EvidenceSpans))
	for _, span := range packet.EvidenceSpans {
		out = append(out, span.SourceCanonicalID)
	}
	return uniqueStrings(out)
}

func stageStatusForRevisionCount(count int) string {
	if count > 0 {
		return "pending"
	}
	return "completed"
}

func sectionStatusFromClaims(packets []evidence.EvidencePacket) string {
	if len(packets) == 0 {
		return "needs_revision"
	}
	for _, packet := range packets {
		if packet.VerifierStatus != "verified" || len(packet.ContradictionPacketIDs) > 0 {
			return "needs_revision"
		}
	}
	return "ready_for_review"
}

func revisionPriority(issues []string) string {
	for _, issue := range issues {
		if strings.Contains(strings.ToLower(issue), "contradiction") || strings.Contains(strings.ToLower(issue), "no grounded") {
			return "high"
		}
	}
	if len(issues) > 0 {
		return "medium"
	}
	return "low"
}

func firstPacketIDs(packets []evidence.EvidencePacket, limit int) []string {
	out := make([]string, 0, limit)
	for _, packet := range packets {
		out = append(out, packet.PacketID)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func extractFirstNumericValue(text string) *float64 {
	for _, field := range strings.Fields(text) {
		value, err := parseNumericField(field)
		if err == nil {
			return &value
		}
	}
	return nil
}

func parseNumericField(field string) (float64, error) {
	clean := strings.TrimSpace(field)
	clean = strings.Trim(clean, "()%.,")
	return json.Number(clean).Float64()
}

func sanitizeMermaidNode(nodeID string, label string) string {
	return fmt.Sprintf("%s[%q]", packetNodeRootID(nodeID), label)
}

func packetNodeRootID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func truncateForLabel(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return strings.TrimSpace(text[:limit]) + "..."
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
	sort.Strings(out)
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func firstString(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptyInPipeline(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func minFloat(a float64, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func hashIDForPipeline(id string) string {
	if len(id) > 16 {
		return id[:16]
	}
	return id
}

func (set ManuscriptPipelineResult) ClaimPacketIDs() []string {
	out := make([]string, 0, len(set.RawMaterials.ClaimPackets))
	for _, packet := range set.RawMaterials.ClaimPackets {
		out = append(out, packet.PacketID)
	}
	return uniqueStrings(out)
}

func contradictionCount(raw evidence.ManuscriptRawMaterialSet) int {
	seen := make(map[string]struct{})
	count := 0
	for _, packet := range raw.ClaimPackets {
		for _, contradictionID := range packet.ContradictionPacketIDs {
			key := packet.PacketID + ":" + contradictionID
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			count++
		}
	}
	return count
}
