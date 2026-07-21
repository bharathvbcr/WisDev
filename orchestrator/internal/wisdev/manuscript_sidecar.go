package wisdev

// manuscript_sidecar.go wires the manuscript pipeline to the Python sidecar so the
// scholarlm (go_orchestrator) DocGen produces real LLM prose, runs the agentic
// review→revise loop, and performs the coordinated whole-manuscript dedup — instead
// of degrading to grounded scaffolds. The sidecar's manuscript_llm() selector routes
// these calls to a configured local model (Ollama) or Gemini/Vertex. Every call is
// best-effort: when the sidecar is unreachable the caller falls back to its scaffold.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/stackconfig"
)

// sectionWordBudget splits the manuscript-wide TargetWords across the section count
// so each writer receives a per-section word target (0 = model default).
func (p *ManuscriptPipeline) sectionWordBudget(sectionCount int) int {
	if p.TargetWords <= 0 || sectionCount <= 0 {
		return 0
	}
	budget := p.TargetWords / sectionCount
	if budget < 1 {
		budget = 1
	}
	return budget
}

type sectionTemplate struct {
	id         string
	title      string
	goal       string
	writerRole string
}

// defaultSectionTemplates is the standard narrative-review section plan.
func defaultSectionTemplates() []sectionTemplate {
	return []sectionTemplate{
		{id: "abstract", title: "Abstract", goal: "Summarize, as a literature review, the strongest findings reported across the reviewed sources.", writerRole: "abstract_writer"},
		{id: "introduction", title: "Introduction", goal: "Frame the research problem, scope, and motivation drawn from the reviewed literature.", writerRole: "framing_writer"},
		{id: "literature_review", title: "Literature Review", goal: "Synthesize prior work and thematic clusters from the reviewed sources, attributing claims to their authors.", writerRole: "literature_reviewer"},
		{id: "methods", title: "Methodological Approaches in the Reviewed Literature", goal: "Describe and CONTRAST the methodological approaches the reviewed STUDIES used (how prior work designed, built, and evaluated their systems), each attributed to its source paper. This is a narrative review: do NOT describe a methodology of THIS review, do NOT imply a systematic search or PRISMA process, and do NOT explain technical architecture as if it were this paper's own method.", writerRole: "methods_writer"},
		{id: "results", title: "Synthesis of Findings", goal: "Synthesize and COMPARE the key findings reported across the reviewed studies, each EXPLICITLY attributed to its source (e.g. 'Busch et al. reviewed 89 studies across 29 specialties'). Do NOT present any reported finding as a result of THIS review.", writerRole: "results_writer"},
		{id: "discussion", title: "Discussion", goal: "Compare, reconcile, and critique the evidence, preserving unresolved conflicts.", writerRole: "discussion_writer"},
		{id: "conclusion", title: "Conclusion", goal: "Close with supported takeaways, limits, and open gaps for the next pass.", writerRole: "conclusion_writer"},
	}
}

// resolveSectionTemplates returns the section plan: the caller-supplied SectionFlow
// (mapped onto known templates, unknown ids becoming generic synthesis sections in
// the requested order) when set, otherwise the default plan.
func (p *ManuscriptPipeline) resolveSectionTemplates() []sectionTemplate {
	all := defaultSectionTemplates()
	if len(p.SectionFlow) == 0 {
		return all
	}
	byID := make(map[string]sectionTemplate, len(all))
	for _, t := range all {
		byID[t.id] = t
	}
	out := make([]sectionTemplate, 0, len(p.SectionFlow))
	seen := make(map[string]struct{}, len(p.SectionFlow))
	for _, raw := range p.SectionFlow {
		id := normalizeSectionID(raw)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if t, ok := byID[id]; ok {
			out = append(out, t)
			continue
		}
		out = append(out, sectionTemplate{
			id:         id,
			title:      humanizeSectionID(raw),
			goal:       fmt.Sprintf("Synthesize the reviewed literature relevant to '%s', attributing every claim to its source. This is a narrative review with no protocol of its own.", strings.TrimSpace(raw)),
			writerRole: "literature_reviewer",
		})
	}
	if len(out) == 0 {
		return all
	}
	return out
}

func normalizeSectionID(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

func humanizeSectionID(raw string) string {
	s := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(raw), "_", " "), "-", " ")
	if s == "" {
		return "Section"
	}
	return strings.Title(s) //nolint:staticcheck // ASCII section titles
}

func blueprintSectionTitles(blueprint ManuscriptBlueprint) []string {
	titles := make([]string, 0, len(blueprint.Sections))
	for _, brief := range blueprint.Sections {
		titles = append(titles, firstNonEmptyInPipeline(brief.Title, brief.SectionID))
	}
	return titles
}

// reviewGenre is the manuscript genre passed to the adversarial reviewer (defaults to
// a narrative literature review; overridable via the Genre field).
func (p *ManuscriptPipeline) reviewGenre() string {
	if g := strings.TrimSpace(p.Genre); g != "" {
		return g
	}
	return "narrative literature review"
}

func (p *ManuscriptPipeline) sidecarHTTPClient() *http.Client {
	if p.httpClient != nil {
		return p.httpClient
	}
	return &http.Client{Timeout: 120 * time.Second}
}

func (p *ManuscriptPipeline) setSidecarHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Caller-Service", "go_orchestrator")
	if key := stackconfig.ResolveInternalServiceKey(); key != "" {
		req.Header.Set("X-Internal-Service-Key", key)
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

// postSectionContent POSTs a section payload to a sidecar manuscript endpoint and
// returns the generated/refined “content“ string. Transport/timeout/5xx failures
// are retried by postManuscriptJSON (bounded, jittered backoff); 4xx are not.
// Ultimate failure still returns the error so the caller falls back to its
// grounded scaffold.
func (p *ManuscriptPipeline) postSectionContent(ctx context.Context, path string, payload map[string]any) (string, error) {
	sectionID, _ := payload["section_id"].(string)
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		// Expected on the offline/scaffold path; logged at debug so the fallback is traceable.
		slog.Debug("manuscript sidecar skipped (no base url) — section falls back to scaffold",
			"component", manuscriptLogComponent, "operation", "sidecar.section", "path", path, "section_id", sectionID)
		return "", fmt.Errorf("python sidecar base URL is not configured")
	}
	started := time.Now()
	var decoded struct {
		Content string `json:"content"`
	}
	if err := p.postManuscriptJSON(ctx, path, payload, &decoded); err != nil {
		slog.Warn("manuscript sidecar call failed",
			"component", manuscriptLogComponent, "operation", "sidecar.section",
			"path", path, "section_id", sectionID, "outcome", "error",
			"latency_ms", time.Since(started).Milliseconds(),
			"error", truncForLog(err.Error(), 200))
		return "", err
	}
	slog.Debug("manuscript sidecar call",
		"component", manuscriptLogComponent, "operation", "sidecar.section",
		"path", path, "section_id", sectionID, "outcome", "ok",
		"latency_ms", time.Since(started).Milliseconds(),
		"content_chars", len(strings.TrimSpace(decoded.Content)))
	return decoded.Content, nil
}

// ReviseSectionWithInstructions runs a single LLM-backed, instruction-guided rewrite
// of ONE already-drafted section via the sidecar /section/revise endpoint, so a user's
// free-text "rewrite this section like X" request is actually executed by ScholarLM
// (the interactive per-section counterpart to the pipeline's autonomous revise loop).
//
// claimPackets and priorSections are passed as already-JSON-shaped maps (the exact
// forms stored on the full-paper workspace), so the caller does not need the evidence
// types. The user instructions are forwarded BOTH as a review finding (what to fix) and
// as custom_instructions (author steering). Returns an error when no sidecar is
// configured or it returns empty, so the caller can fall back to a deterministic edit.
func (p *ManuscriptPipeline) ReviseSectionWithInstructions(
	ctx context.Context,
	sectionID string,
	content string,
	claimPackets []map[string]any,
	priorSections []map[string]any,
	instructions string,
) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return "", fmt.Errorf("python sidecar base URL is not configured")
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("section has no content to revise")
	}
	instructions = strings.TrimSpace(instructions)
	payload := map[string]any{
		"section_id":       sectionID,
		"original_content": content,
		"claim_packets":    claimPackets,
		"prior_sections":   priorSections,
		"max_tokens":       32768,
	}
	if instructions != "" {
		payload["review_findings"] = []string{instructions}
		payload["custom_instructions"] = instructions
	} else {
		payload["review_findings"] = []string{"Improve clarity, naturalness, and citation integration without adding unsupported claims."}
	}
	revised, err := p.postSectionContent(ctx, "/wisdev/manuscript/section/revise", payload)
	if err != nil {
		return "", err
	}
	revised = strings.TrimSpace(revised)
	if revised == "" {
		return "", fmt.Errorf("section revise returned empty content")
	}
	return minimizeEmDashes(revised), nil
}

// GroundedSection is one grounded, cited section produced by the canonical sidecar
// section generator (the same one the full-paper ManuscriptPipeline uses).
type GroundedSection struct {
	Content      string   `json:"content"`
	Citations    []string `json:"citations"`
	ClaimPackets int      `json:"claimPackets"`
}

// closingPassage returns the trailing paragraph(s) of a drafted section, capped at
// maxChars, so the next section's writer can open with a natural transition from it.
// Whole paragraphs are kept where possible (a mid-sentence cut reads worse than a
// shorter excerpt).
func closingPassage(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxChars <= 0 {
		return ""
	}
	if len(text) <= maxChars {
		return text
	}
	paragraphs := strings.Split(text, "\n\n")
	out := ""
	for i := len(paragraphs) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(paragraphs[i])
		if candidate == "" {
			continue
		}
		joined := candidate
		if out != "" {
			joined = candidate + "\n\n" + out
		}
		if len(joined) > maxChars {
			break
		}
		out = joined
	}
	if out == "" {
		// Single oversized paragraph: take its tail on a word boundary.
		tail := text[len(text)-maxChars:]
		if idx := strings.IndexAny(tail, " \n"); idx >= 0 && idx+1 < len(tail) {
			tail = tail[idx+1:]
		}
		out = strings.TrimSpace(tail)
	}
	return out
}

// GenerateGroundedSection drafts ONE section from a set of papers using the same
// evidence extraction + sidecar section generator (and the same natural-voice,
// broad-citation, custom-instruction prompt) the full-paper ManuscriptPipeline uses.
// This is how the legacy per-section drafting path converges its prose onto the
// canonical generator instead of emitting deterministic scaffold text. Returns an
// error when no sidecar is configured or generation yields nothing, so the caller
// can fall back to the deterministic scaffold and never hard-fail.
//
// priorSections carries the already-drafted sections ({title, text} maps, in
// document order) so the writer coheres with them instead of restarting cold: they
// are forwarded as prior_sections, and the closing passage of the LAST one becomes
// previous_section_ending — the sidecar prompt's cross-section flow context.
func (p *ManuscriptPipeline) GenerateGroundedSection(
	ctx context.Context,
	sectionID string,
	title string,
	goal string,
	papers []search.Paper,
	targetWords int,
	customInstructions string,
	priorSections []map[string]any,
) (GroundedSection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return GroundedSection{}, fmt.Errorf("python sidecar base URL is not configured")
	}
	if len(papers) == 0 {
		return GroundedSection{}, fmt.Errorf("no papers supplied to ground the section")
	}
	query := firstNonEmptyInPipeline(title, goal, "manuscript section")
	jobID := "draft_section_" + hashIDForPipeline(sectionID)
	raw, _, err := evidence.BuildRawMaterialSet(jobID, query, papers)
	if err != nil {
		return GroundedSection{}, err
	}
	if len(raw.ClaimPackets) == 0 {
		return GroundedSection{}, fmt.Errorf("no grounded claim packets extracted from papers")
	}

	sourceTitleByID := sourceTitleIndex(raw.CanonicalSources)
	sourceTitles := make([]string, 0, len(raw.CanonicalSources))
	for _, source := range raw.CanonicalSources {
		if t := strings.TrimSpace(source.Title); t != "" {
			sourceTitles = append(sourceTitles, t)
		}
	}

	payload := map[string]any{
		"section_id":    sectionID,
		"writer_role":   "literature_reviewer",
		"section_goal":  firstNonEmptyInPipeline(goal, "Synthesize the reviewed literature for this section, attributing every claim to its source."),
		"claim_packets": raw.ClaimPackets,
		"source_titles": uniqueStrings(sourceTitles),
		"query":         query,
		"max_tokens":    32768,
		"thesis":        manuscriptThesis(query, raw),
	}
	if targetWords > 0 {
		payload["target_words"] = targetWords
	}
	if p.MinCitations > 0 {
		payload["min_citations"] = p.MinCitations
	}
	if ci := firstNonEmptyInPipeline(strings.TrimSpace(customInstructions), p.trimmedCustomInstructions()); ci != "" {
		payload["custom_instructions"] = ci
	}
	if len(priorSections) > 0 {
		payload["prior_sections"] = priorSections
		if last, ok := priorSections[len(priorSections)-1]["text"].(string); ok {
			if ending := closingPassage(last, 1200); ending != "" {
				payload["previous_section_ending"] = ending
			}
		}
	}

	content, err := p.postSectionContent(ctx, "/wisdev/manuscript/section/generate", payload)
	if err != nil {
		return GroundedSection{}, err
	}
	content = minimizeEmDashes(strings.TrimSpace(content))
	if content == "" {
		return GroundedSection{}, fmt.Errorf("section generation returned empty content")
	}

	// Cited sources: the packets whose positional [n] marker appears in the prose,
	// mapped back to their source titles, so the caller can build the bibliography.
	citedTitles := make([]string, 0, len(raw.ClaimPackets))
	for _, packetID := range resolvePositionalPacketIDs(content, raw.ClaimPackets) {
		for _, pk := range raw.ClaimPackets {
			if pk.PacketID != packetID {
				continue
			}
			for _, span := range pk.EvidenceSpans {
				if t := strings.TrimSpace(sourceTitleByID[span.SourceCanonicalID]); t != "" {
					citedTitles = append(citedTitles, t)
				}
			}
		}
	}
	// Fall back to all source titles when the model attributed in prose ("Author et al.")
	// without positional markers, so the section is never left with an empty bibliography.
	citations := uniqueStrings(citedTitles)
	if len(citations) == 0 {
		citations = uniqueStrings(sourceTitles)
	}

	return GroundedSection{
		Content:      content,
		Citations:    citations,
		ClaimPackets: len(raw.ClaimPackets),
	}, nil
}

type manuscriptReviewResult struct {
	ContentScore      float64  `json:"content_score"`
	AttributionIssues []string `json:"attribution_issues"`
	FabricationRisks  []string `json:"fabrication_risks"`
	Redundancy        []string `json:"redundancy"`
	// ReadabilityIssues flags prose that reads unnaturally (mechanical citation
	// placement, list-like/robotic phrasing, no narrative flow). Fed into the
	// review→revise loop so "reads unnatural" becomes a correctable finding.
	ReadabilityIssues []string `json:"readability_issues"`
	Recommendations   []string `json:"recommendations"`
}

// fetchAdversarialReview runs the LLM peer review over the drafted sections, returning
// nil offline or on any error so callers degrade gracefully.
func (p *ManuscriptPipeline) fetchAdversarialReview(ctx context.Context, query string, blueprint ManuscriptBlueprint, raw evidence.ManuscriptRawMaterialSet, sections []SectionDraftArtifact) *manuscriptReviewResult {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return nil
	}
	body := make([]map[string]any, 0, len(sections))
	packetIDSet := make(map[string]struct{})
	for _, section := range sections {
		if content := strings.TrimSpace(section.Content); content != "" {
			body = append(body, map[string]any{"title": section.Title, "text": content})
		}
		for _, id := range section.ClaimPacketIDs {
			packetIDSet[id] = struct{}{}
		}
	}
	if len(body) == 0 {
		return nil
	}
	packetIDs := make([]string, 0, len(packetIDSet))
	for id := range packetIDSet {
		packetIDs = append(packetIDs, id)
	}
	review, err := p.postManuscriptReview(ctx, map[string]any{
		"query":         query,
		"thesis":        blueprint.Thesis,
		"genre":         p.reviewGenre(),
		"sections":      body,
		"claim_packets": claimPacketsByIDs(raw.ClaimPackets, packetIDs),
	})
	if err != nil {
		slog.Warn("manuscript adversarial review failed — degrading to lineage critique",
			"component", manuscriptLogComponent, "operation", "adversarial_review",
			"genre", p.reviewGenre(), "reviewed_sections", len(body), "error", err.Error())
		return nil
	}
	if review != nil {
		slog.Debug("manuscript adversarial review",
			"component", manuscriptLogComponent, "operation", "adversarial_review",
			"genre", p.reviewGenre(), "reviewed_sections", len(body),
			"content_score", review.ContentScore,
			"redundancy", len(review.Redundancy), "attribution_issues", len(review.AttributionIssues),
			"fabrication_risks", len(review.FabricationRisks), "recommendations", len(review.Recommendations))
	}
	return review
}

// postManuscriptReview runs the sidecar peer review; transient failures are
// retried by postManuscriptJSON, terminal ones surface to the caller (which
// degrades to the lineage critique).
func (p *ManuscriptPipeline) postManuscriptReview(ctx context.Context, payload map[string]any) (*manuscriptReviewResult, error) {
	var result manuscriptReviewResult
	if err := p.postManuscriptJSON(ctx, "/wisdev/manuscript/review", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// coordinateDedupeResult is the /manuscript/coordinate-dedupe response: each section's
// full revised text after whole-manuscript redundancy resolution.
type coordinateDedupeResult struct {
	Sections []struct {
		SectionID string `json:"section_id"`
		Text      string `json:"text"`
	} `json:"sections"`
}

// dedupePromptWindow mirrors the sidecar's per-section prompt truncation in
// /wisdev/manuscript/coordinate-dedupe (manuscript_router.py: `s.text[:6000]`).
// A section longer than this was only partially shown to the LLM, so its
// "revised" text cannot contain the unseen tail — replacing the full section
// with it would silently amputate the section.
const dedupePromptWindow = 6000

// dedupeMinKeepRatio rejects degenerate rewrites: dedupe should trim
// cross-section repetition, not gut a section. A rewrite shrinking a section
// below this fraction of its original length is skipped.
const dedupeMinKeepRatio = 0.4

// dedupeReplacementSafe reports whether a coordinate-dedupe rewrite may replace
// the original section content (see dedupePromptWindow / dedupeMinKeepRatio).
func dedupeReplacementSafe(original, revised string) bool {
	if len(original) > dedupePromptWindow {
		return false
	}
	return float64(len(revised)) >= float64(len(original))*dedupeMinKeepRatio
}

// coordinatedDedupeRevise (#9) runs a single whole-manuscript pass that sees ALL
// sections at once, resolving cross-section redundancy the per-section revise cannot.
// Best-effort: any failure leaves the per-section draft untouched.
func (p *ManuscriptPipeline) coordinatedDedupeRevise(ctx context.Context, query string, blueprint ManuscriptBlueprint, raw evidence.ManuscriptRawMaterialSet, sections []SectionDraftArtifact) []SectionDraftArtifact {
	if strings.TrimSpace(p.pythonBaseURL) == "" || len(sections) < 2 {
		return sections
	}
	review := p.fetchAdversarialReview(ctx, query, blueprint, raw, sections)
	if review == nil || len(review.Redundancy) == 0 {
		return sections
	}
	payloadSections := make([]map[string]any, 0, len(sections))
	for _, s := range sections {
		if strings.TrimSpace(s.Content) == "" {
			continue
		}
		payloadSections = append(payloadSections, map[string]any{
			"section_id": s.SectionID, "title": s.Title, "text": s.Content,
		})
	}
	if len(payloadSections) < 2 {
		return sections
	}
	slog.Debug("manuscript coordinated dedupe starting",
		"component", manuscriptLogComponent, "operation", "coordinated_dedupe",
		"redundancies", len(review.Redundancy), "sections", len(payloadSections))
	result, err := p.postCoordinateDedupe(ctx, map[string]any{
		"query":        query,
		"thesis":       blueprint.Thesis,
		"genre":        p.reviewGenre(),
		"sections":     payloadSections,
		"redundancies": review.Redundancy,
	})
	if err != nil || result == nil {
		if err != nil {
			slog.Warn("manuscript coordinated dedupe failed — keeping pre-dedupe sections",
				"component", manuscriptLogComponent, "operation", "coordinated_dedupe", "error", err.Error())
		}
		return sections
	}
	revisedByID := make(map[string]string, len(result.Sections))
	for _, rs := range result.Sections {
		if id := strings.TrimSpace(rs.SectionID); id != "" && strings.TrimSpace(rs.Text) != "" {
			revisedByID[id] = strings.TrimSpace(rs.Text)
		}
	}
	dedupedCount := 0
	for i := range sections {
		revised, ok := revisedByID[sections[i].SectionID]
		if !ok {
			continue
		}
		if !dedupeReplacementSafe(sections[i].Content, revised) {
			slog.Warn("manuscript coordinated dedupe skipped section — rewrite would truncate or gut it",
				"component", manuscriptLogComponent, "operation", "coordinated_dedupe",
				"section", sections[i].SectionID, "original_chars", len(sections[i].Content), "revised_chars", len(revised))
			continue
		}
		dedupedCount++
		revised = minimizeEmDashes(revised)
		claimPackets := claimPacketsByIDs(raw.ClaimPackets, sections[i].ClaimPacketIDs)
		sections[i].Content = revised
		applyCitationMarkerHygiene(&sections[i], claimPackets)
		if rebuilt := buildContentParagraphs(sections[i].SectionID, sections[i].Content, claimPackets); len(rebuilt) > 0 {
			sections[i].Paragraphs = rebuilt
		}
		sections[i].ClaimProvenance = buildClaimProvenance(sections[i].Paragraphs, claimPackets)
		sections[i].ContradictionMap = buildContradictionMap(sections[i].Paragraphs, claimPackets)
		sections[i].Version++
		sections[i].UpdatedAt = time.Now().UnixMilli()
	}
	slog.Debug("manuscript coordinated dedupe complete",
		"component", manuscriptLogComponent, "operation", "coordinated_dedupe", "sections_rewritten", dedupedCount)
	return sections
}

// postCoordinateDedupe runs the whole-manuscript dedupe rewrite; transient
// failures are retried by postManuscriptJSON, terminal ones surface to the
// caller (which keeps the pre-dedupe sections).
func (p *ManuscriptPipeline) postCoordinateDedupe(ctx context.Context, payload map[string]any) (*coordinateDedupeResult, error) {
	var result coordinateDedupeResult
	if err := p.postManuscriptJSON(ctx, "/wisdev/manuscript/coordinate-dedupe", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
