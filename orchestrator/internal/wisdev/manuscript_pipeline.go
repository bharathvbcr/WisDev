package wisdev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/stackconfig"
)

type ManuscriptBlueprint struct {
	BlueprintID       string             `json:"blueprintId"`
	JobID             string             `json:"jobId,omitempty"`
	Query             string             `json:"query"`
	Thesis            string             `json:"thesis,omitempty"`
	SectionOrder      []string           `json:"sectionOrder"`
	Sections          []SectionBrief     `json:"sections"`
	OwnershipConcepts []OwnershipConcept `json:"ownershipConcepts,omitempty"`
	CoverageMetrics   map[string]any     `json:"coverageMetrics,omitempty"`
	CreatedAt         int64              `json:"createdAt"`
	UpdatedAt         int64              `json:"updatedAt"`
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
	// EntailmentChecked is the tri-state guard: only when true does EntailmentScore
	// carry meaning. A bare float64 zero-value must NEVER trigger a verifier downgrade
	// (that would cascade-flag every un-checked paragraph, incl. the offline path).
	EntailmentChecked bool    `json:"entailmentChecked,omitempty"`
	EntailmentScore   float64 `json:"entailmentScore,omitempty"`
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
	// TargetWords, when > 0, asks the section writers to aim for roughly this many
	// words across the whole manuscript. It is forwarded to the sidecar as a
	// per-section budget hint; 0 leaves section length to the model's defaults.
	TargetWords int
	// MinCitations, when > 0, is the minimum number of distinct sources the
	// manuscript should cite. It is forwarded to the sidecar as a breadth directive;
	// callers should also ensure at least this many papers are retrieved.
	MinCitations int
	// SectionFlow, when non-empty, overrides the default section plan with this
	// ordered list of section ids/titles (e.g. ["introduction","methods","results",
	// "discussion"]). Unknown ids become generic synthesis sections. Empty = default.
	SectionFlow []string
	// ReviewRounds bounds the agentic review→revise loop: each round re-reviews the
	// current draft and rewrites flagged sections, stopping early when the review
	// converges (no changes). 0 = pipeline default (defaultReviewRounds).
	ReviewRounds int
	// Genre is the manuscript genre passed to the adversarial reviewer (e.g.
	// "narrative literature review" or "research paper"). It controls whether
	// first-person voice is penalized. Empty = narrative literature review.
	Genre string
}

// defaultReviewRounds is the review→revise loop budget when the caller does not
// set one. Each round early-exits once the review stops finding changes, so this
// is an upper bound, not a fixed cost.
const defaultReviewRounds = 2

// reviewRounds returns the effective review→revise loop budget (clamped to a sane
// ceiling so a misconfigured caller cannot run an unbounded number of LLM passes).
func (p *ManuscriptPipeline) reviewRounds() int {
	rounds := p.ReviewRounds
	if rounds <= 0 {
		rounds = defaultReviewRounds
	}
	if rounds > 5 {
		rounds = 5
	}
	return rounds
}

func NewManuscriptPipeline(pythonBaseURL string) *ManuscriptPipeline {
	baseURL := strings.TrimSpace(pythonBaseURL)
	if baseURL == "" {
		baseURL = ResolvePythonBase()
	}
	return &ManuscriptPipeline{
		pythonBaseURL: strings.TrimSuffix(baseURL, "/"),
		httpClient:    &http.Client{Timeout: 120 * time.Second},
	}
}

// NewManuscriptPipelineOffline returns a pipeline that never contacts the Python
// sidecar: postSectionContent short-circuits on the empty base URL, so every
// section falls back to its grounded scaffold with zero network I/O. Used by the
// CLI's offline document-generation path so `--offline` makes no network calls.
func NewManuscriptPipelineOffline() *ManuscriptPipeline {
	return &ManuscriptPipeline{
		pythonBaseURL: "",
		httpClient:    &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *ManuscriptPipeline) Run(ctx context.Context, jobID string, query string, papers []search.Paper) (ManuscriptPipelineResult, error) {
	log := newStageLogger(jobID)
	slog.Info("manuscript pipeline start",
		"component", manuscriptLogComponent, "operation", manuscriptLogOp, "job_id", jobID,
		"query", truncForLog(query, 160), "papers", len(papers),
		"mode", p.pipelineMode(), "python_base", p.pythonBaseURL,
		"target_words", p.TargetWords, "min_citations", p.MinCitations,
		"genre", p.reviewGenre(), "section_flow", p.SectionFlow, "review_rounds", p.reviewRounds(),
	)

	rawMaterials, dossier, err := evidence.BuildRawMaterialSet(jobID, query, papers)
	if err != nil {
		log.warn("build_raw_materials", "BuildRawMaterialSet failed", "error", err.Error())
		return ManuscriptPipelineResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ManuscriptPipelineResult{}, err
	}
	log.stage("build_raw_materials", "claim_packets", len(rawMaterials.ClaimPackets), "papers", len(papers))
	if len(rawMaterials.ClaimPackets) == 0 {
		log.warn("build_raw_materials", "no claim packets extracted — sections will have no grounded evidence")
	}

	blueprint := p.planSections(jobID, query, rawMaterials)
	log.stage("plan_sections", "section_order", blueprint.SectionOrder,
		"section_count", len(blueprint.SectionOrder), "flow_override", len(p.SectionFlow) > 0)
	if err := ctx.Err(); err != nil {
		return ManuscriptPipelineResult{}, err
	}

	// Concept-level cross-section coordination: an LLM editor assigns each salient
	// named stat/study/taxonomy/example to ONE owning section so it is developed in
	// full in one place only. No-op offline (OwnershipConcepts stays nil -> writers
	// fall back to today's behavior).
	blueprint.OwnershipConcepts = p.fetchCoordinationPlan(ctx, blueprint, rawMaterials)
	log.stage("coordination_plan", "ownership_concepts", len(blueprint.OwnershipConcepts))
	if err := ctx.Err(); err != nil {
		return ManuscriptPipelineResult{}, err
	}

	visuals := p.composeVisuals(jobID, query, rawMaterials, blueprint)
	log.stage("compose_visuals", "visuals", len(visuals))

	sections := p.writeSections(ctx, jobID, rawMaterials, blueprint)
	log.stage("write_sections", sectionsMetrics(sections)...)
	for i := range sections {
		if strings.TrimSpace(sections[i].Content) == "" {
			log.warn("write_sections", "section produced empty content", "section_id", sections[i].SectionID)
		}
	}

	sections = p.verifySectionsBlind(sections)
	log.stage("verify_blind.post_write", groundingMetrics(sections)...)

	sections = p.refineSections(ctx, sections, rawMaterials)
	log.stage("refine_sections", sectionsMetrics(sections)...)

	sections = p.verifySectionsBlind(sections)
	log.stage("verify_blind.post_refine", groundingMetrics(sections)...)

	// Redraft the abstract LAST, summarizing the now-finalized body (so it does not
	// pre-introduce or duplicate the sections it should summarize).
	sections = p.regenerateAbstractFromBody(ctx, sections, rawMaterials, blueprint)
	log.stage("regenerate_abstract", sectionsMetrics(sections)...)

	// Drop paragraphs that recycle content already stated in an earlier section,
	// then re-verify so the peer-review grounding ratio reflects the trimmed draft.
	sections = dedupeCrossSectionParagraphs(sections)
	log.stage("dedupe_paragraphs", sectionsMetrics(sections)...)

	sections = p.verifySectionsBlind(sections)
	log.stage("verify_blind.post_dedupe", groundingMetrics(sections)...)

	// FEED pass: prose-vs-source entailment fact-check whose flags become
	// UnresolvedIssues (section-level, so they survive the paragraph rebuild the next
	// rewrite performs) and drive the aggressive revise below. Not score-bearing.
	sections = p.factCheckSections(ctx, sections, rawMaterials, false)
	log.stage("fact_check.feed", sectionsMetrics(sections)...)
	// Agentic generate → review → revise LOOP. Each round runs an LLM peer review
	// (+ the ownership plan and entailment flags) that drives a per-section rewrite
	// cutting cross-section repetition, raising density, strengthening
	// synthesis/attribution, and re-grounding flagged sentences; it then re-verifies
	// and re-runs the entailment fact-check so the next round targets the freshly
	// rewritten prose. The loop stops as soon as a round makes no changes (the review
	// converged) or the round budget (reviewRounds) is exhausted.
	budget := p.reviewRounds()
	roundsRun, converged := 0, false
	for round := 0; round < budget; round++ {
		if err := ctx.Err(); err != nil {
			return ManuscriptPipelineResult{}, err
		}
		roundsRun++
		before := sectionsContentFingerprint(sections)
		sections = p.reviseSectionsWithReview(ctx, query, blueprint, rawMaterials, sections)
		sections = p.verifySectionsBlind(sections)
		changed := sectionsContentFingerprint(sections) != before
		log.stage("review_revise.round",
			append([]any{"round", round + 1, "of", budget, "changed", changed}, groundingMetrics(sections)...)...)
		if !changed {
			converged = true
			break // review→revise converged: no further changes this round
		}
		// Refresh entailment flags so the next round's revise targets the new prose.
		sections = p.factCheckSections(ctx, sections, rawMaterials, false)
	}
	log.stage("review_revise.done", "rounds_run", roundsRun, "budget", budget, "converged", converged)
	if !converged && roundsRun >= budget {
		log.warn("review_revise.done", "review loop exhausted its round budget without converging — draft may still have flagged issues", "budget", budget)
	}

	// Coordinated whole-manuscript dedup (#9): one pass over ALL sections resolves the
	// cross-section redundancy the per-section revise can't (it only sees one section
	// at a time), keeping each repeated point in a single owning section.
	sections = p.coordinatedDedupeRevise(ctx, query, blueprint, rawMaterials, sections)
	log.stage("coordinated_dedupe", sectionsMetrics(sections)...)

	// Deterministic genre backstop: delete any sentence that (re)introduced a claim of
	// THIS review's own systematic-search/PRISMA methodology — the model keeps
	// resurfacing these across rewrites despite the prompt + first-draft detector.
	sections = p.stripSelfMethodology(sections, rawMaterials)
	log.stage("strip_self_methodology", sectionsMetrics(sections)...)

	// Drop sentences that near-verbatim restate one already made in an earlier section
	// (the sentence-level counterpart to the paragraph dedup above), on the final post-
	// rewrite content.
	sections = p.dedupeCrossSectionSentences(sections, rawMaterials)
	log.stage("dedupe_sentences", sectionsMetrics(sections)...)

	// Attach the correct citation to a real-but-uncited specific (a named model, a
	// "2,400 cases" count) that uniquely matches one of the section's packets, so source
	// facts the writer left uncited read as attributed rather than fabricated.
	sections = p.attachUncitedSpecifics(sections, rawMaterials)
	log.stage("attach_uncited_specifics", sectionsMetrics(sections)...)
	// SCORE pass: re-run the entailment fact-check AFTER the last paragraph rebuild so
	// EntailmentChecked/Score persist onto the final paragraphs; the verify below then
	// honestly downgrades any prose still not entailed by its cited sources.
	// INVARIANT: this must remain the LAST paragraph-affecting stage before the scoring
	// verify — any buildContentParagraphs after it (refine/revise/abstract) would wipe
	// EntailmentChecked and silently un-downgrade the flagged prose.
	sections = p.factCheckSections(ctx, sections, rawMaterials, true)
	log.stage("fact_check.score", sectionsMetrics(sections)...)

	sections = p.verifySectionsBlind(sections)
	log.stage("verify_blind.final", groundingMetrics(sections)...)

	critique := p.peerReview(jobID, query, rawMaterials, blueprint, sections, visuals)
	log.stage("peer_review", "critique_keys", len(critique))

	critique = p.applyAdversarialReview(ctx, query, blueprint, rawMaterials, sections, critique)
	log.stage("adversarial_review", "critique_keys", len(critique))

	revisionTasks := p.buildRevisionTasks(jobID, sections, visuals, critique)
	stageStates := p.buildStageStates(len(rawMaterials.ClaimPackets), len(sections), len(visuals), len(revisionTasks))
	log.stage("build_revision_tasks", "revision_tasks", len(revisionTasks), "stage_states", len(stageStates))

	finalGrounding := groundingMetrics(sections)
	slog.Info("manuscript pipeline complete",
		append([]any{
			"component", manuscriptLogComponent, "operation", manuscriptLogOp, "job_id", jobID,
			"total_ms", time.Since(log.started).Milliseconds(), "mode", p.pipelineMode(),
			"sections", len(sections), "visuals", len(visuals), "revision_tasks", len(revisionTasks),
		}, finalGrounding...)...)

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
// (mapped onto the known templates, with unknown ids becoming generic synthesis
// sections in the requested order) when set, otherwise the default plan.
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

// normalizeSectionID lower-cases a section id/title and slugs spaces to underscores
// so "Literature Review" and "literature_review" map to the same template.
func normalizeSectionID(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

// humanizeSectionID renders a section id/title for display (Title Case).
func humanizeSectionID(raw string) string {
	s := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(raw), "_", " "), "-", " ")
	if s == "" {
		return "Section"
	}
	return strings.Title(s) //nolint:staticcheck // ASCII section titles; strings.Title is sufficient here.
}

// minimizeEmDashes rewrites em-dashes to lighter punctuation so the manuscript
// avoids the "—" connector the docGen style guide discourages. It is a
// deterministic safety net on top of the sidecar prompt directive: a spaced
// connector ("word — word") becomes a comma clause, and any stray em-dash becomes
// a comma, with whitespace/comma artifacts tidied.
func minimizeEmDashes(s string) string {
	if !strings.Contains(s, "—") {
		return s
	}
	s = strings.ReplaceAll(s, " — ", ", ")
	s = strings.ReplaceAll(s, "—", ", ")
	s = strings.ReplaceAll(s, " ,", ",")
	s = strings.ReplaceAll(s, ",,", ",")
	s = strings.ReplaceAll(s, "  ", " ")
	return s
}

// sectionsContentFingerprint returns a deterministic signature of the section
// bodies so the agentic review→revise loop can detect convergence (a round that
// changed nothing and can therefore stop early). Section id + body is sufficient.
func sectionsContentFingerprint(sections []SectionDraftArtifact) string {
	var b strings.Builder
	for i := range sections {
		b.WriteString(sections[i].SectionID)
		b.WriteByte(0)
		b.WriteString(sections[i].Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func (p *ManuscriptPipeline) planSections(jobID string, query string, raw evidence.ManuscriptRawMaterialSet) ManuscriptBlueprint {
	now := time.Now().UnixMilli()
	sourceTitleByCanonicalID := sourceTitleIndex(raw.CanonicalSources)

	templates := p.resolveSectionTemplates()

	sectionOrder := make([]string, 0, len(templates))
	sections := make([]SectionBrief, 0, len(templates))
	// assigned tracks packets already claimed by an earlier SPECIFIC section so the
	// next specific section prefers distinct evidence (reduces cross-section
	// redundancy). Synthesis sections neither consume nor are constrained by it.
	assigned := map[string]int{}
	for _, template := range templates {
		relevantPackets := selectRelevantPackets(raw.ClaimPackets, template.id, sectionPacketLimit(template.id), assigned)
		if !isBroadSynthesisSection(template.id) {
			for _, packet := range relevantPackets {
				assigned[packet.PacketID]++
			}
		}
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
		// (The Results section's visual is synthesized later as an evidence-summary
		// table in composeVisuals, so it is not flagged as missing here.)
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
		Thesis:       manuscriptThesis(query, raw),
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

// primaryResearchVoicePattern matches first-person primary-research phrasing that
// is never appropriate in a narrative review of others' work.
var primaryResearchVoicePattern = regexp.MustCompile(`(?i)\b(we (conducted|performed|evaluated|tested|developed|propose|proposed|present|presented|designed|implemented|built|introduce|introduced|hypothesize|recruited|measured|identif\w+|examin\w+|analy[sz]\w+|select\w+|search\w+|review\w+|extract\w+|screen\w+|assess\w+|categori[sz]\w+|compil\w+|retriev\w+|gather\w+|quer\w+|synthesi[sz]\w+|includ\w+|exclud\w+)|our (study|method\w*|methodolog\w*|framework|approach|results|model|experiment\w*|analysis|dataset|cohort|finding\w*|search|review|synthesis|inclusion|selection|design\w*|protocol\w*|evaluation\w*|criteria|investigation\w*|procedure\w*|sample|cohorts)|this (study|paper|work) (proposes|proposed|presents|presented|develops|developed|introduces|introduced|conducts|conducted)|the present (study|work)|in this (study|paper|review), we)\b`)

func containsPrimaryResearchVoice(content string) bool {
	return primaryResearchVoicePattern.MatchString(content)
}

// selfAttributedProtocolPattern matches a NARRATIVE review claiming it performed a
// systematic-review protocol of its own ("This review employs a PRISMA-compliant
// search strategy", "the present study conducted a database search", "This review
// synthesizes 30 peer-reviewed studies identified through a systematic search"). The
// subject is restricted to self-references (this/our/the present + [narrative/...] +
// review/study/...) with the method verb close behind, so it does NOT match a protocol
// correctly attributed to a cited source ("the systematic review by [3] applied PRISMA").
var selfAttributedProtocolPattern = regexp.MustCompile(`(?i)\b(this|the present|our)\s+(?:narrative\s+|systematic\s+|scoping\s+|comprehensive\s+|present\s+)?(review|study|paper|analysis|survey)\b[^.]{0,18}\b(employ\w*|use[sd]?|using|conduct\w*|perform\w*|appl(?:y|ies|ied)|follow\w*|adopt\w*|utiliz\w+|implement\w*|synthesiz\w*|quer(?:y|ies|ied|ying))\b[^.]{0,70}\b(prisma|systematic search|systematic review|search strateg\w+|database search|literature search|inclusion criteria|selection criteria|search protocol|screened|meta-analysis|pubmed|scopus|embase|medline|peer-reviewed (?:studies|literature|articles))\b`)

// selfConductPassivePattern matches the passive/active self-conduct claim ("This
// narrative review was conducted", "the study was performed"). Narrative reviews
// synthesize/examine/discuss — they are not "conducted/performed" like a study.
var selfConductPassivePattern = regexp.MustCompile(`(?i)\b(this|the|our)\s+(?:narrative\s+|systematic\s+|scoping\s+|comprehensive\s+|present\s+)?(review|study|analysis|survey)\b[^.]{0,20}\b(was|were|is|has been|have been)\s+(conduct\w*|perform\w*|undertak\w*|carried out|completed|executed)\b`)

// selfProtocolGerundPattern matches the same self-attribution in a gerund clause that
// drops the explicit subject ("Utilizing a PRISMA-compliant search strategy to ...",
// "...conducted by querying PubMed and Scopus").
var selfProtocolGerundPattern = regexp.MustCompile(`(?i)\b(utilizing|employing|using|adopting|following|conducting|performing|leveraging|querying|searching|queried|searched)\b[^.]{0,25}\b(prisma-compliant|systematic search strateg\w+|systematic literature search|prisma (?:2020|guidelines?|framework|methodolog\w+)|systematic review methodolog\w+|pubmed|scopus|embase|medline)\b`)

// ownSearchStrategyPattern matches the review describing its OWN literature-gathering
// process ("The search strategy focused on peer-reviewed databases", "Search
// strategies utilized ..."). The literature-gathering verbs (focused/utilized/
// comprised/...) distinguish it from the TOPICAL sense on this retrieval-heavy subject
// ("RAG's search strategy retrieves documents from a vector database").
var ownSearchStrategyPattern = regexp.MustCompile(`(?i)\bsearch strateg(?:y|ies)\b\s+(?:\w+\s+){0,3}(focused|focuses|utilized|utilizes|included|includes|comprised|comprises|involved|involves|targeted|targets|prioritized|prioritizes|encompassed|encompasses|consisted|spanned|covered|combined|drew)\b`)

// criteriaMethodologyPattern matches the review describing its own study-selection
// criteria ("The selection criteria focused on peer-reviewed literature", "Inclusion
// criteria mandated empirical validation"). A literature/study object is required so it
// does NOT match the topical sense ("model selection criteria prioritize latency").
var criteriaMethodologyPattern = regexp.MustCompile(`(?i)\b(selection|inclusion|exclusion|eligibility)\s+criteria\b[^.]{0,40}\b(focused|focuses|mandated|required|prioriti[sz]ed|included|comprised|specified|restricted|limited|encompassed|stipulated|consisted|emphasi[sz]ed)\b[^.]{0,40}\b(stud(?:y|ies)|literature|paper|article|publication|peer-reviewed|empirical|evidence base|research articles)\b`)

// methodsSubsectionHeadingPattern matches a heading line for a systematic-review methods
// subsection a narrative review must not have. Requires a markdown/bold heading marker so
// it never matches ordinary prose that merely starts with one of these words.
var methodsSubsectionHeadingPattern = regexp.MustCompile(`(?im)^[ \t]*(?:#{1,6}[ \t]*|\*\*[ \t]*)(search strateg(?:y|ies)|selection criteria|inclusion(?: and exclusion)? criteria|exclusion criteria|eligibility criteria|study selection|data extraction(?: and synthesis)?|search and selection|literature search(?: strategy)?|methodology|methods)\b[^\n]*$`)

// claimsOwnSystematicMethodology reports whether the prose claims THIS review ran a
// systematic-search/PRISMA protocol or describes its own literature-search/selection
// process — a genre violation for a narrative review.
func claimsOwnSystematicMethodology(content string) bool {
	return selfAttributedProtocolPattern.MatchString(content) ||
		selfConductPassivePattern.MatchString(content) ||
		selfProtocolGerundPattern.MatchString(content) ||
		ownSearchStrategyPattern.MatchString(content) ||
		criteriaMethodologyPattern.MatchString(content)
}

// strongProtocolTermPattern matches a systematic-review-protocol term that a NARRATIVE
// review can only legitimately mention when attributing it to a cited source — PRISMA, a
// systematic review/search, inclusion/exclusion criteria, a screened record count, or a
// LITERATURE database (PubMed/Embase/...; never the topical "vector database" of RAG).
var strongProtocolTermPattern = regexp.MustCompile(`(?i)\bPRISMA\b|\bsystematic (?:literature )?(?:review|search)\b|\bSLR\b|\binclusion criteria\b|\bexclusion criteria\b|\bscreened \d+\b|\b(?:pubmed|embase|scopus|medline|cochrane|ieee xplore)\b`)

// citationOrAttributionPattern reports whether a sentence attributes its claim to a
// source — a bracketed citation, "et al.", or "by <Author>".
var citationOrAttributionPattern = regexp.MustCompile(`\[\s*(?:\d|evp_)|(?i)\bet al\b|\bby [A-Z][a-z]+`)

// isUncitedProtocolSentence is the ROBUST genre check: a sentence stating a strong
// systematic-protocol term with NO citation/attribution is the review claiming the
// protocol as its own — regardless of how the subject/verb is phrased. An attributed
// mention ("the systematic review by Smith et al. [3] applied PRISMA") is spared.
func isUncitedProtocolSentence(sentence string) bool {
	return strongProtocolTermPattern.MatchString(sentence) &&
		!citationOrAttributionPattern.MatchString(sentence)
}

// contentClaimsOwnMethodology combines the subject/verb patterns with the robust
// uncited-protocol-sentence check across the content's sentences.
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

// stripSelfMethodologySentences deterministically removes the methods-subsection headings
// the model inserts AND every sentence claiming THIS review's own systematic-search/
// selection methodology. The model reintroduces these across rewrites despite the prompt
// + first-draft detector, so this is the final backstop. Paragraph-aware (preserves the
// section's paragraph structure), drops a wholly-methodological paragraph, and never
// empties a section. Safe because the detector has no false positives on attributed-source
// or topical prose.
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

// stripSelfMethodology applies stripSelfMethodologySentences to every section AFTER the
// last rewrite stage and rebuilds the paragraph lineage for any section it changed.
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

// distinctiveSpecificPattern matches a distinctive, checkable specific — a CamelCase
// product/model name (LiVersa, FactScore, RadGraph-F1), a thousands-separated number
// (2,400), or a bare 3-digit count (314). 4+ digit bare numbers (years, DOIs) are
// deliberately excluded to avoid spurious matches.
var distinctiveSpecificPattern = regexp.MustCompile(`\b[A-Z][a-z]+[A-Z][A-Za-z0-9-]*\b|\b\d{1,3},\d{3}\b|\b\d{3}\b`)

// anyCitationMarkerPattern reports whether a sentence already carries a citation.
var anyCitationMarkerPattern = regexp.MustCompile(`\[\s*(?:\d|evp_)`)

func isASCIIWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// containsSpecificWord reports a word-boundary (not mere substring) occurrence of
// specificLower in haystackLower.
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

// uniquePacketForSpecifics returns the 1-based positional index of the section packet
// that UNIQUELY contains one of the sentence's distinctive specifics, or 0 when none is
// unambiguous (so a citation is only added when it is clearly correct).
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

// numberUnitPattern matches a count + study-unit noun ("30 studies", "30 guidance
// documents", "89 papers"). A unit NOUN is required, so "30%", "30 ms", "30 minutes" do
// not match.
var numberUnitPattern = regexp.MustCompile(`(?i)\b(\d{2,4})\s+(?:[a-z][a-z-]*\s+){0,2}(stud(?:y|ies)|papers?|cases?|patients?|questions?|documents?|guidelines?|trials?|participants?|specialties|records?|vignettes?|reviews?|articles?|publications?)\b`)

// uniquePacketForNumberUnit cites the packet that uniquely contains BOTH a sentence's
// count and its study-unit (e.g. a "review of 30 studies" -> the survey packet), or 0.
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

// attachUncitedSpecificCitations adds the correct positional [k] marker to a sentence
// that states a distinctive specific (a named model, a 2,400-style count) but carries NO
// citation, when exactly one of the section's packets contains that specific. Real facts
// from the sources that the writer left uncited then read as attributed, not fabricated.
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

// attachUncitedSpecifics applies attachUncitedSpecificCitations to every section and
// rebuilds the paragraph lineage so the added citations count toward grounding.
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
			source := "scaffold"
			generated, genErr := p.generateSectionContent(ctx, sectionBrief, claimPackets, blueprint)
			if genErr == nil && strings.TrimSpace(generated) != "" {
				content = strings.TrimSpace(generated)
				source = "sidecar"
			} else if genErr != nil {
				slog.Warn("manuscript section fell back to scaffold",
					"component", manuscriptLogComponent, "operation", "write_sections",
					"section_id", sectionBrief.SectionID, "error", genErr.Error())
			}
			slog.Debug("manuscript section drafted",
				"component", manuscriptLogComponent, "operation", "write_sections",
				"section_id", sectionBrief.SectionID, "source", source,
				"claim_packets", len(claimPackets), "content_chars", len(content))
			content = minimizeEmDashes(content)
			if rebuilt := buildContentParagraphs(sectionBrief.SectionID, content, claimPackets); len(rebuilt) > 0 {
				paragraphs = rebuilt
			}

			// A narrative review must never use first-person primary-research voice
			// ("we conducted", "this study proposes"). The model occasionally slips
			// despite the prompt; flag it so the refine pass rewrites it.
			unresolved := uniqueStrings(sectionBrief.UnresolvedIssues)
			reviewStatus := sectionBrief.Status
			if containsPrimaryResearchVoice(content) {
				unresolved = uniqueStrings(append(unresolved, "Section uses first-person primary-research voice; rewrite in attributed third-person review voice."))
				reviewStatus = "needs_revision"
			}
			if contentClaimsOwnMethodology(content) {
				unresolved = uniqueStrings(append(unresolved, "Section claims THIS review performed a systematic/PRISMA search or screening (or mentions PRISMA/a literature database without attributing it to a cited source); this is a narrative review with NO protocol of its own — remove the self-attributed methodology, or reattribute the protocol to the specific cited source [n] that used it."))
				reviewStatus = "needs_revision"
			}

			sections[index] = SectionDraftArtifact{
				ArtifactID: fmt.Sprintf("section_%d_%d", now, index+1),
				SectionID:  sectionBrief.SectionID,
				Title:      sectionBrief.Title,
				WriterRole: sectionBrief.WriterRole,
				Content:    content,
				Paragraphs: paragraphs,
				// Use the FILTERED, ordered packets the sidecar numbered against, so a
				// rendered [n] maps to the same packet the writer cited.
				ClaimPacketIDs:     uniquePacketIDs(claimPackets),
				SourceCanonicalIDs: uniqueStrings(sectionBrief.SourceCanonicalIDs),
				SourceTitles:       uniqueStrings(sectionBrief.SourceTitles),
				PlannedVisualIDs:   uniqueStrings(sectionBrief.PlannedVisualIDs),
				UnresolvedIssues:   unresolved,
				ReviewStatus:       reviewStatus,
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
				out[idx].Content = minimizeEmDashes(strings.TrimSpace(refined))
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
	blueprint ManuscriptBlueprint,
) (string, error) {
	payload := map[string]any{
		"section_id":      brief.SectionID,
		"writer_role":     brief.WriterRole,
		"section_goal":    brief.Goal,
		"claim_packets":   claimPackets,
		"source_titles":   uniqueStrings(brief.SourceTitles),
		"query":           blueprint.Query,
		"max_tokens":      32768,
		"thesis":          blueprint.Thesis,
		"section_outline": blueprintSectionTitles(blueprint),
	}
	if directive := renderOwnershipForSection(brief.SectionID, blueprint.OwnershipConcepts); directive != "" {
		payload["ownership_directive"] = directive
	}
	if budget := p.sectionWordBudget(len(blueprint.Sections)); budget > 0 {
		payload["target_words"] = budget
	}
	if p.MinCitations > 0 {
		payload["min_citations"] = p.MinCitations
	}
	return p.postSectionContent(ctx, "/wisdev/manuscript/section/generate", payload)
}

// sectionWordBudget splits the manuscript-wide TargetWords evenly across the
// section count so each section writer receives a per-section word target. It
// returns 0 when no target is set (or no sections), leaving length to the model.
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

// manuscriptThesis builds a one-line thesis (query + leading thematic clusters)
// passed to every section writer so the sections argue toward a shared claim.
func manuscriptThesis(query string, raw evidence.ManuscriptRawMaterialSet) string {
	themes := make([]string, 0, 3)
	for _, cluster := range raw.SourceClusters {
		if label := strings.TrimSpace(cluster.Label); label != "" {
			themes = append(themes, label)
		}
		if len(themes) >= 3 {
			break
		}
	}
	thesis := fmt.Sprintf("This review synthesizes the published literature on %s", strings.TrimSpace(query))
	if len(themes) > 0 {
		thesis += ", focusing on " + strings.Join(themes, ", ")
	}
	return thesis + "."
}

func blueprintSectionTitles(blueprint ManuscriptBlueprint) []string {
	titles := make([]string, 0, len(blueprint.Sections))
	for _, brief := range blueprint.Sections {
		titles = append(titles, firstNonEmptyInPipeline(brief.Title, brief.SectionID))
	}
	return titles
}

// regenerateAbstractFromBody redrafts the Abstract LAST, summarizing the verified
// body sections (passed as prior_sections), so it no longer runs as a parallel
// section over raw packets and pre-introduces/duplicates the body. No-op offline
// or when the sidecar/body is unavailable.
func (p *ManuscriptPipeline) regenerateAbstractFromBody(ctx context.Context, sections []SectionDraftArtifact, raw evidence.ManuscriptRawMaterialSet, blueprint ManuscriptBlueprint) []SectionDraftArtifact {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return sections
	}
	abstractIdx := -1
	prior := make([]map[string]any, 0, len(sections))
	for i := range sections {
		if sections[i].SectionID == "abstract" {
			abstractIdx = i
			continue
		}
		if content := strings.TrimSpace(sections[i].Content); content != "" {
			prior = append(prior, map[string]any{"title": sections[i].Title, "text": content})
		}
	}
	if abstractIdx < 0 || len(prior) == 0 {
		return sections
	}
	claimPackets := claimPacketsByIDs(raw.ClaimPackets, sections[abstractIdx].ClaimPacketIDs)
	payload := map[string]any{
		"section_id":      "abstract",
		"writer_role":     sections[abstractIdx].WriterRole,
		"section_goal":    "Summarize the drafted manuscript into a faithful abstract.",
		"claim_packets":   claimPackets,
		"query":           blueprint.Query,
		"max_tokens":      16384,
		"thesis":          blueprint.Thesis,
		"section_outline": blueprintSectionTitles(blueprint),
		"prior_sections":  prior,
	}
	content, err := p.postSectionContent(ctx, "/wisdev/manuscript/section/generate", payload)
	if err != nil || strings.TrimSpace(content) == "" {
		return sections
	}
	sections[abstractIdx].Content = minimizeEmDashes(strings.TrimSpace(content))
	if rebuilt := buildContentParagraphs("abstract", sections[abstractIdx].Content, claimPackets); len(rebuilt) > 0 {
		sections[abstractIdx].Paragraphs = rebuilt
	}
	sections[abstractIdx].ClaimProvenance = buildClaimProvenance(sections[abstractIdx].Paragraphs, claimPackets)
	sections[abstractIdx].Version++
	sections[abstractIdx].UpdatedAt = time.Now().UnixMilli()
	return p.verifySectionsBlind(sections)
}

// dedupeCrossSectionParagraphs removes paragraphs that near-duplicate (Jaccard
// >= 0.85) a paragraph already kept in an EARLIER section, so recycled content
// (the same idea restated section after section) is dropped. It never empties a
// section and never dedupes within a section (that is the writer's structure).
func dedupeCrossSectionParagraphs(sections []SectionDraftArtifact) []SectionDraftArtifact {
	kept := make([]map[string]struct{}, 0)
	for si := range sections {
		// The abstract is a SUMMARY of the body (regenerated last), so it both
		// legitimately echoes the body and must not seed the dedup baseline — else
		// it would shadow and delete the very body paragraphs it summarizes. Leave
		// it untouched and out of `kept`.
		if sections[si].SectionID == "abstract" {
			continue
		}
		paras := sections[si].Paragraphs
		out := make([]SectionDraftParagraph, 0, len(paras))
		removed := false
		for _, paragraph := range paras {
			tokens := keywordTokenSet(paragraph.Text)
			isDup := false
			for _, prior := range kept {
				if jaccardTokens(tokens, prior) >= 0.85 {
					isDup = true
					break
				}
			}
			if isDup {
				removed = true
				continue
			}
			out = append(out, paragraph)
		}
		if len(out) == 0 { // never empty a section
			out = paras
			removed = false
		}
		for _, paragraph := range out {
			kept = append(kept, keywordTokenSet(paragraph.Text))
		}
		if removed {
			sections[si].Paragraphs = out
			texts := make([]string, 0, len(out))
			for _, paragraph := range out {
				texts = append(texts, paragraph.Text)
			}
			sections[si].Content = strings.Join(texts, "\n\n")
		}
	}
	return sections
}

// dedupeCrossSectionSentences drops a sentence that NEAR-VERBATIM restates one already
// kept in an EARLIER section (keyword-token Jaccard >= 0.80) — the sentence-level
// counterpart to dedupeCrossSectionParagraphs, which only catches whole-paragraph dups.
// 0.80 on content keywords is high enough that distinct on-topic claims (which share few
// keyword tokens) are never dropped, only verbatim/near-verbatim repeats. Skips the
// abstract (it legitimately summarizes the body), preserves paragraph structure, and
// never empties a section. Runs on the FINAL post-rewrite content.
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
		if rebuilt := buildContentParagraphs(sections[si].SectionID, newContent, ordered); len(rebuilt) > 0 {
			sections[si].Paragraphs = rebuilt
		}
		sections[si].ClaimProvenance = buildClaimProvenance(sections[si].Paragraphs, ordered)
		sections[si].ContradictionMap = buildContradictionMap(sections[si].Paragraphs, ordered)
		sections[si].Version++
		sections[si].UpdatedAt = time.Now().UnixMilli()
	}
	return sections
}

func jaccardTokens(a, b map[string]struct{}) float64 {
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

type manuscriptReviewResult struct {
	ContentScore      float64  `json:"content_score"`
	AttributionIssues []string `json:"attribution_issues"`
	FabricationRisks  []string `json:"fabrication_risks"`
	Redundancy        []string `json:"redundancy"`
	Recommendations   []string `json:"recommendations"`
}

// fetchAdversarialReview runs the LLM peer review over the drafted sections,
// returning nil offline or on any error so callers degrade gracefully.
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
	// Send the resolved sources behind the [n] markers so the reviewer can tell a
	// grounded, cited claim from an actual fabrication (citation-blind reviewers were
	// false-flagging valid prose). De-duplicated across all reviewed sections.
	packetIDs := make([]string, 0, len(packetIDSet))
	for id := range packetIDSet {
		packetIDs = append(packetIDs, id)
	}
	claimPackets := claimPacketsByIDs(raw.ClaimPackets, packetIDs)
	review, err := p.postManuscriptReview(ctx, map[string]any{
		"query":         query,
		"thesis":        blueprint.Thesis,
		"genre":         p.reviewGenre(),
		"sections":      body,
		"claim_packets": claimPackets,
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

// reviewGenre is the manuscript genre passed to the adversarial reviewer so it
// applies the right voice rules. Defaults to a narrative literature review (the
// pipeline's section briefs), overridable via the Genre field.
func (p *ManuscriptPipeline) reviewGenre() string {
	if g := strings.TrimSpace(p.Genre); g != "" {
		return g
	}
	return "narrative literature review"
}

// reviewFindings flattens a review's issue lists into revision instructions.
func reviewFindings(review *manuscriptReviewResult) []string {
	if review == nil {
		return nil
	}
	findings := make([]string, 0, len(review.Redundancy)+len(review.AttributionIssues)+len(review.FabricationRisks)+len(review.Recommendations))
	findings = append(findings, review.Redundancy...)
	findings = append(findings, review.AttributionIssues...)
	findings = append(findings, review.FabricationRisks...)
	findings = append(findings, review.Recommendations...)
	return findings
}

// otherSectionsContext returns the OTHER drafted sections (title + content) so a
// revision can see — and avoid repeating — what they already cover.
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

// reviseSectionsWithReview runs an AGGRESSIVE, review-guided rewrite of every
// section: it fetches the adversarial review, then asks the sidecar to rewrite each
// section to cut cross-section repetition, raise information density, strengthen
// synthesis, and fix attribution — using the review findings + the other sections
// as context. No-op offline / when the review is empty / on any per-section error.
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
		revised, err := p.postSectionContent(ctx, "/wisdev/manuscript/section/revise", payload)
		if err != nil || strings.TrimSpace(revised) == "" {
			continue
		}
		revisedCount++
		sections[i].Content = minimizeEmDashes(strings.TrimSpace(revised))
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

// applyAdversarialReview augments the lineage-based critique with an LLM peer
// review (misattribution / fabrication / redundancy + a content score) and blends
// the content score into the overall score. No-op offline or on any sidecar error,
// so the lineage critique always stands.
func (p *ManuscriptPipeline) applyAdversarialReview(ctx context.Context, query string, blueprint ManuscriptBlueprint, raw evidence.ManuscriptRawMaterialSet, sections []SectionDraftArtifact, critique map[string]any) map[string]any {
	if critique == nil {
		return critique
	}
	review := p.fetchAdversarialReview(ctx, query, blueprint, raw, sections)
	if review == nil {
		return critique
	}
	appendCritiqueList(critique, "weaknesses", review.AttributionIssues)
	appendCritiqueList(critique, "weaknesses", review.FabricationRisks)
	appendCritiqueList(critique, "weaknesses", review.Redundancy)
	appendCritiqueList(critique, "recommendations", review.Recommendations)
	critique["contentReviewScore"] = review.ContentScore
	critique["verificationMode"] = "citation-lineage + adversarial LLM content review"
	// A real content review ran, so blend it with the lineage score and lift the
	// "not content-reviewed" ceiling.
	if lineage, ok := critique["overallScore"].(float64); ok {
		critique["overallScore"] = minFloat(0.6*lineage+0.4*review.ContentScore, 0.95)
	}
	return critique
}

func appendCritiqueList(critique map[string]any, key string, extra []string) {
	if len(extra) == 0 {
		return
	}
	existing, _ := critique[key].([]string)
	critique[key] = uniqueStrings(append(append([]string{}, existing...), extra...))
}

func (p *ManuscriptPipeline) postManuscriptReview(ctx context.Context, payload map[string]any) (*manuscriptReviewResult, error) {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return nil, fmt.Errorf("python sidecar base URL is not configured")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.pythonBaseURL+"/wisdev/manuscript/review", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Caller-Service", "go_orchestrator")
	if key := stackconfig.ResolveInternalServiceKey(); key != "" {
		req.Header.Set("X-Internal-Service-Key", key)
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := p.httpClient
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("manuscript review returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var result manuscriptReviewResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// coordinateDedupeResult is the /manuscript/coordinate-dedupe response: each
// section's full revised text after whole-manuscript redundancy resolution.
type coordinateDedupeResult struct {
	Sections []struct {
		SectionID string `json:"section_id"`
		Text      string `json:"text"`
	} `json:"sections"`
}

// coordinatedDedupeRevise (#9) runs a single whole-manuscript pass that sees ALL
// sections at once, so it can resolve the cross-section redundancy the per-section
// revise cannot — keeping each repeated point in one owning section and removing it
// from the others. It is gated on the reviewer actually flagging redundancies and is
// best-effort: any failure leaves the per-section draft untouched.
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
		dedupedCount++
		revised = minimizeEmDashes(revised)
		claimPackets := claimPacketsByIDs(raw.ClaimPackets, sections[i].ClaimPacketIDs)
		sections[i].Content = revised
		if rebuilt := buildContentParagraphs(sections[i].SectionID, revised, claimPackets); len(rebuilt) > 0 {
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

func (p *ManuscriptPipeline) postCoordinateDedupe(ctx context.Context, payload map[string]any) (*coordinateDedupeResult, error) {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return nil, fmt.Errorf("python sidecar base URL is not configured")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.pythonBaseURL+"/wisdev/manuscript/coordinate-dedupe", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Caller-Service", "go_orchestrator")
	if key := stackconfig.ResolveInternalServiceKey(); key != "" {
		req.Header.Set("X-Internal-Service-Key", key)
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := p.httpClient
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("manuscript coordinate-dedupe returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var result coordinateDedupeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
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
		"max_tokens":        32768,
	}
	return p.postSectionContent(ctx, "/wisdev/manuscript/section/refine", payload)
}

func (p *ManuscriptPipeline) postSectionContent(ctx context.Context, path string, payload map[string]any) (string, error) {
	sectionID, _ := payload["section_id"].(string)
	logSidecarCall := func(outcome string, started time.Time, status int, chars int, errMsg string) {
		attrs := []any{
			"component", manuscriptLogComponent, "operation", "sidecar.section",
			"path", path, "section_id", sectionID, "outcome", outcome,
			"latency_ms", time.Since(started).Milliseconds(),
		}
		if status != 0 {
			attrs = append(attrs, "http_status", status)
		}
		if chars > 0 {
			attrs = append(attrs, "content_chars", chars)
		}
		if errMsg != "" {
			attrs = append(attrs, "error", errMsg)
			slog.Warn("manuscript sidecar call failed", attrs...)
			return
		}
		slog.Debug("manuscript sidecar call", attrs...)
	}

	if strings.TrimSpace(p.pythonBaseURL) == "" {
		// Not an error worth a stack: the offline pipeline expects this and falls back
		// to grounded scaffolds. Logged at debug so the fallback is still traceable.
		slog.Debug("manuscript sidecar skipped (no base url) — section falls back to scaffold",
			"component", manuscriptLogComponent, "operation", "sidecar.section", "path", path, "section_id", sectionID)
		return "", fmt.Errorf("python sidecar base URL is not configured")
	}

	started := time.Now()
	body, err := json.Marshal(payload)
	if err != nil {
		logSidecarCall("marshal_error", started, 0, 0, err.Error())
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.pythonBaseURL+path, bytes.NewReader(body))
	if err != nil {
		logSidecarCall("request_error", started, 0, 0, err.Error())
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
		client = &http.Client{Timeout: 120 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		// Transport failure (sidecar down, timeout) — the caller falls back to scaffold.
		logSidecarCall("transport_error", started, 0, 0, err.Error())
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logSidecarCall("http_error", started, resp.StatusCode, 0, truncForLog(string(respBody), 200))
		return "", fmt.Errorf("python sidecar returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logSidecarCall("decode_error", started, resp.StatusCode, 0, err.Error())
		return "", err
	}
	if strings.TrimSpace(result.Content) == "" {
		logSidecarCall("empty_content", started, resp.StatusCode, 0, "sidecar returned empty content")
		return "", fmt.Errorf("python sidecar returned empty section content")
	}
	logSidecarCall("ok", started, resp.StatusCode, len(result.Content), "")
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
		// A paragraph is "cited" if it carries either a literal packet-ID token
		// ([evp_...]) or a positional numeric citation ([1][2]) — the latter being
		// the form the sidecar prompt actually instructs the model to emit. Both
		// are real grounding; only fall back to fuzzy lexical inference when neither
		// is present.
		explicitPacketIDs := extractExplicitPacketIDs(paragraphText, claimPackets)
		positionalPacketIDs := resolvePositionalPacketIDs(paragraphText, claimPackets)
		citedPacketIDs := uniqueStrings(append(append([]string{}, explicitPacketIDs...), positionalPacketIDs...))
		matchedPacketIDs := citedPacketIDs
		if len(matchedPacketIDs) == 0 {
			matchedPacketIDs = inferPacketIDsFromText(paragraphText, claimPackets)
		}

		citationIDs := sourceIDsFromPacketsByIDs(packetIndex, matchedPacketIDs)
		status := "verified"
		notes := make([]string, 0, 3)

		if len(citedPacketIDs) == 0 {
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

// resolvePositionalPacketIDs maps positional numeric citations like [1] or [2,3]
// — the form the sidecar prompt instructs the model to emit ("Reference each
// supporting claim by its bracketed number") — to packet IDs by indexing the
// section's ordered claim packets (the exact slice handed to the sidecar, so
// [n] -> orderedPackets[n-1]). Out-of-range and non-integer tokens are ignored;
// literal packet-ID tokens are handled separately by extractExplicitPacketIDs.
func resolvePositionalPacketIDs(text string, orderedPackets []evidence.EvidencePacket) []string {
	if strings.TrimSpace(text) == "" || len(orderedPackets) == 0 {
		return nil
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
			n, err := strconv.Atoi(strings.TrimSpace(token))
			if err != nil || n < 1 || n > len(orderedPackets) {
				continue
			}
			found = append(found, orderedPackets[n-1].PacketID)
		}
	}
	return uniqueStrings(found)
}

func inferPacketIDsFromText(text string, claimPackets []evidence.EvidencePacket) []string {
	paragraphTokens := keywordTokenSet(text)
	if len(paragraphTokens) == 0 {
		return nil
	}

	type scoredPacket struct {
		id    string
		score float64
	}
	candidates := make([]scoredPacket, 0, len(claimPackets))
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
		// Score overlap against the SHORTER token set: a short claim fully present
		// in the paragraph scores high, while a couple of incidental shared domain
		// words against a long claim do not. The old absolute overlapCount>=2 floor
		// matched most packets in a single-domain corpus and fabricated provenance.
		shorter := len(claimTokens)
		if len(paragraphTokens) < shorter {
			shorter = len(paragraphTokens)
		}
		ratio := float64(overlapCount) / float64(shorter)
		if overlapCount >= 2 && ratio >= 0.5 {
			candidates = append(candidates, scoredPacket{id: packet.PacketID, score: ratio})
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	// Cap to the two strongest matches: a paragraph rarely grounds in more than a
	// couple of distinct claims, and capping prevents fan-out to loosely-related
	// packets whose sources/contradiction links would then render as provenance.
	if len(candidates) > 2 {
		candidates = candidates[:2]
	}
	matched := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		matched = append(matched, candidate.id)
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
			switch {
			case len(paragraph.ClaimPacketIDs) == 0 || len(paragraph.CitationIDs) == 0:
				// Genuinely uncited prose (scaffold filler) — hard block.
				allGrounded = false
				paragraph.VerificationStatus = "rejected"
				paragraph.VerifierNotes = append(paragraph.VerifierNotes, "paragraph is missing claim packet lineage or citations")
				report.RejectedParagraphs++
				blockingIssues = append(blockingIssues, fmt.Sprintf("paragraph %s is missing claim packet lineage or citations", paragraph.ParagraphID))
			case paragraph.EntailmentChecked && paragraph.EntailmentScore >= 0 && paragraph.EntailmentScore <= entailmentBlockThreshold:
				// The score-bearing fact-check found prose whose concrete specifics are
				// NOT entailed by the cited sources. This is a BLOCKING downgrade (the
				// section loses grounding ratio) but NOT a rejection — lineage exists,
				// it is the prose that overreaches. Gated on EntailmentChecked so an
				// un-checked paragraph's zero-value score never trips this.
				allGrounded = false
				paragraph.VerificationStatus = "needs_review"
				paragraph.VerifierNotes = append(paragraph.VerifierNotes, "paragraph prose is not entailed by its cited sources")
				report.FlaggedParagraphs++
				blockingIssues = append(blockingIssues, fmt.Sprintf("paragraph %s prose is not entailed by its cited sources", paragraph.ParagraphID))
			case paragraph.VerificationStatus == "verified":
				report.VerifiedParagraphs++
			case paragraph.VerificationStatus == "needs_review":
				// Cited and source-mapped, but backed by a packet that is real yet
				// not independently re-verified. That is legitimate grounding for a
				// review draft: surface it as flagged but do NOT block the section.
				// This stops the all-sections-flagged cascade for manuscripts that
				// draw on unresolved-but-real sources, while empty-lineage prose
				// (above) and provisional/tentative packets (below) still block.
				report.FlaggedParagraphs++
			default:
				// "provisional"/"tentative"/other — genuinely weak grounding.
				allGrounded = false
				paragraph.VerificationStatus = "needs_review"
				paragraph.VerifierNotes = append(paragraph.VerifierNotes, "paragraph depends on non-verified claim packets")
				report.FlaggedParagraphs++
				blockingIssues = append(blockingIssues, fmt.Sprintf("paragraph %s depends on non-verified claim packets", paragraph.ParagraphID))
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
		spec, drawn := buildConceptDiagramSpec(query, raw, blueprint)
		reviewStatus := "needs_revision"
		unresolved := []string{"visual is a scaffold concept diagram because no real claim packets were available to ground it"}
		caption := "Concept diagram connecting the query to the planned manuscript sections."
		// Only call the diagram grounded when it actually depicts real evidence
		// (drew on >=1 non-placeholder packet) AND the corpus has no open gaps.
		if len(drawn) > 0 && len(raw.Gaps) == 0 {
			reviewStatus = "ready_for_review"
			unresolved = nil
			caption = fmt.Sprintf("Concept map of %d manuscript sections grounded in %d claim packets.", len(blueprint.Sections), len(drawn))
		}
		sourcePackets := drawn
		if len(sourcePackets) == 0 {
			sourcePackets = firstPacketIDs(raw.ClaimPackets, 1)
		}
		out = append(out, VisualArtifact{
			ArtifactID:       fmt.Sprintf("visual_artifact_%d_seed", now),
			SectionID:        "introduction",
			Title:            "Concept Diagram",
			Kind:             "concept_diagram",
			SpecType:         "mermaid",
			Spec:             spec,
			Caption:          caption,
			SourcePacketIDs:  uniqueStrings(sourcePackets),
			SourceTitles:     []string{},
			ReviewStatus:     reviewStatus,
			UnresolvedIssues: unresolved,
			Version:          1,
			CreatedAt:        now,
			UpdatedAt:        now,
		})
	}

	// Synthesize a thematic evidence-summary table from the source clusters so the
	// Results section has a real data artifact even for a text-only corpus.
	if table, tableDrawn := buildEvidenceSummaryTable(raw); len(table.Rows) >= 2 {
		out = append(out, VisualArtifact{
			ArtifactID:      fmt.Sprintf("visual_artifact_%d_table", now),
			SectionID:       "results",
			Title:           "Evidence Summary",
			Kind:            "table_summary",
			SpecType:        "table",
			Spec:            table,
			Caption:         fmt.Sprintf("Key findings synthesized across %d thematic source groups.", len(table.Rows)),
			SourcePacketIDs: tableDrawn,
			SourceTitles:    []string{},
			ReviewStatus:    "ready_for_review",
			Version:         1,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
	}

	return out
}

// buildConceptDiagramSpec renders a content-grounded Mermaid concept map of the
// manuscript: the query root fans out to each planned section, and each section
// links to its strongest (highest-confidence, non-placeholder) claim packet. It
// returns the spec plus the real packet IDs actually drawn, so the caller marks
// the visual grounded only when it depicts genuine evidence.
func buildConceptDiagramSpec(query string, raw evidence.ManuscriptRawMaterialSet, blueprint ManuscriptBlueprint) (string, []string) {
	packetIndex := packetIndexByID(raw.ClaimPackets)
	root := "q_root"
	lines := []string{
		"flowchart TD",
		"    " + sanitizeMermaidNode(root, truncateForLabel(firstNonEmptyInPipeline(query, "Research query"), 60)),
	}
	// Each distinct claim is drawn ONCE (keyed by packet) and shared by every
	// section that lands on it, so the diagram never repeats the same label across
	// sections. A section prefers its strongest claim that is not yet on the
	// diagram, falling back to sharing an existing node.
	claimNodeByPacket := map[string]string{}
	drawn := make([]string, 0, len(blueprint.Sections))
	for i, brief := range blueprint.Sections {
		sectionNode := fmt.Sprintf("s%d", i+1)
		lines = append(lines, "    "+sanitizeMermaidNode(sectionNode, truncateForLabel(firstNonEmptyInPipeline(brief.Title, brief.SectionID), 40)))
		lines = append(lines, fmt.Sprintf("    %s --> %s", packetNodeRootID(root), packetNodeRootID(sectionNode)))
		best := strongestUndrawnSectionPacket(brief.RequiredClaimPacketIDs, packetIndex, claimNodeByPacket)
		if best == nil {
			best = strongestSectionPacket(brief.RequiredClaimPacketIDs, packetIndex)
		}
		if best == nil {
			continue
		}
		claimID, exists := claimNodeByPacket[best.PacketID]
		if !exists {
			claimID = fmt.Sprintf("c%d", len(claimNodeByPacket)+1)
			lines = append(lines, "    "+sanitizeMermaidNode(claimID, truncateForLabel(best.ClaimText, 48)))
			claimNodeByPacket[best.PacketID] = claimID
			drawn = append(drawn, best.PacketID)
		}
		lines = append(lines, fmt.Sprintf("    %s --> %s", packetNodeRootID(sectionNode), packetNodeRootID(claimID)))
	}
	return strings.Join(lines, "\n"), uniqueStrings(drawn)
}

// strongestSectionPacket returns the highest-confidence real (non query-seed,
// non research-gap) packet among a section's required packets, or nil.
func strongestSectionPacket(packetIDs []string, packetIndex map[string]evidence.EvidencePacket) *evidence.EvidencePacket {
	var best *evidence.EvidencePacket
	for _, id := range packetIDs {
		packet, ok := packetIndex[id]
		if !ok || isPlaceholderPacket(packet) {
			continue
		}
		if best == nil || packet.Confidence > best.Confidence {
			selected := packet
			best = &selected
		}
	}
	return best
}

// strongestUndrawnSectionPacket is strongestSectionPacket restricted to packets
// not already on the diagram, so each section contributes a distinct claim node.
func strongestUndrawnSectionPacket(packetIDs []string, packetIndex map[string]evidence.EvidencePacket, drawn map[string]string) *evidence.EvidencePacket {
	var best *evidence.EvidencePacket
	for _, id := range packetIDs {
		if _, already := drawn[id]; already {
			continue
		}
		packet, ok := packetIndex[id]
		if !ok || isPlaceholderPacket(packet) {
			continue
		}
		if best == nil || packet.Confidence > best.Confidence {
			selected := packet
			best = &selected
		}
	}
	return best
}

// ManuscriptTableSpec is a rendered-table visual (markdown table / LaTeX tabular).
type ManuscriptTableSpec struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

// buildEvidenceSummaryTable synthesizes a thematic evidence table (theme / key
// finding / source) from the strongest packet of each source cluster, so a
// text-only corpus (which yields no extracted figures/tables) still produces a
// real data artifact. Returns the spec and the packet IDs it drew on.
func buildEvidenceSummaryTable(raw evidence.ManuscriptRawMaterialSet) (ManuscriptTableSpec, []string) {
	packetIndex := packetIndexByID(raw.ClaimPackets)
	sourceTitles := sourceTitleIndex(raw.CanonicalSources)
	spec := ManuscriptTableSpec{Headers: []string{"Theme", "Key finding", "Source"}}
	drawn := make([]string, 0)
	seen := map[string]struct{}{}
	for _, cluster := range raw.SourceClusters {
		best := strongestSectionPacket(cluster.PacketIDs, packetIndex)
		if best == nil {
			continue
		}
		if _, dup := seen[best.PacketID]; dup {
			continue
		}
		seen[best.PacketID] = struct{}{}
		source := ""
		for _, span := range best.EvidenceSpans {
			if title := sourceTitles[span.SourceCanonicalID]; title != "" {
				source = title
				break
			}
		}
		spec.Rows = append(spec.Rows, []string{
			truncateForLabel(firstNonEmptyInPipeline(cluster.Label, cluster.Theme, "General"), 48),
			truncateForLabel(best.ClaimText, 160),
			truncateForLabel(source, 70),
		})
		drawn = append(drawn, best.PacketID)
		if len(spec.Rows) >= 12 {
			break
		}
	}
	return spec, uniqueStrings(drawn)
}

// isPlaceholderPacket reports whether a packet is a synthetic stand-in (the
// injected query-seed / research-gap packet) rather than real extracted evidence.
func isPlaceholderPacket(packet evidence.EvidencePacket) bool {
	if strings.EqualFold(packet.ClaimType, "research_gap") {
		return true
	}
	for _, kind := range packet.MaterialKinds {
		if strings.Contains(strings.ToLower(kind), "query_seed") {
			return true
		}
	}
	return false
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
	corroborated := 0
	for _, packet := range raw.ClaimPackets {
		if packet.CorroboratingSourceCount >= 2 {
			corroborated++
		}
	}
	if corroborated > 0 {
		strengths = append(strengths, fmt.Sprintf("%d findings are corroborated by multiple independent sources.", corroborated))
	}

	verifiedParas, flaggedParas, totalParas, flaggedSections := 0, 0, 0, 0
	for _, section := range sections {
		report := section.BlindVerifier
		verifiedParas += report.VerifiedParagraphs
		flaggedParas += report.FlaggedParagraphs
		totalParas += report.VerifiedParagraphs + report.FlaggedParagraphs + report.RejectedParagraphs
		if section.ReviewStatus == "needs_revision" {
			flaggedSections++
			weaknesses = append(weaknesses, fmt.Sprintf("%s has unresolved grounding issues.", section.Title))
			recommendations = append(recommendations, fmt.Sprintf("Rewrite %s with stronger packet-to-paragraph grounding.", strings.ToLower(section.Title)))
		}
		// Surface only NON-contradiction unresolved issues here; contradictions are
		// reported once, consolidated with the count below, to avoid double-counting.
		for _, issue := range section.UnresolvedIssues {
			if strings.Contains(strings.ToLower(issue), "contradiction") {
				continue
			}
			risks = append(risks, fmt.Sprintf("%s: %s", section.Title, issue))
			break
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

	// Consolidated contradiction reporting: ONE risk carrying the (undirected) count
	// and the distinct sections involved, instead of one note per packet-per-section
	// (which made a single link read as several inconsistent risks).
	contradictions := contradictionCount(raw)
	if contradictions > 0 {
		detail := ""
		if involved := contradictionSectionTitles(sections); len(involved) > 0 {
			detail = " (" + strings.Join(involved, ", ") + ")"
		}
		risks = append(risks, fmt.Sprintf("%d unresolved contradiction %s in the evidence set%s.", contradictions, pluralizeWord(contradictions, "link", "links"), detail))
		recommendations = append(recommendations, "Reconcile or explicitly discuss the conflicting findings before publication.")
	}

	if len(raw.Gaps) > 0 {
		weaknesses = append(weaknesses, raw.Gaps...)
		recommendations = append(recommendations, "Acquire additional papers before final export.")
	}

	// Honest verification caveat — surfaced ALWAYS (not only when a section fails):
	// verification is citation-lineage only, with no independent claim<->evidence
	// entailment check, so a reviewer must not read "verified" as "fact-checked".
	weaknesses = append(weaknesses, "Verification is citation-lineage only: prose was not independently fact-checked against source evidence.")
	recommendations = append(recommendations, "Independently verify quantitative claims and attributions against the cited sources before publication.")
	if flaggedParas > 0 {
		weaknesses = append(weaknesses, fmt.Sprintf("%d paragraph(s) rely on weakly-resolved sources (no strong DOI/identifier).", flaggedParas))
	}

	groundingRatio := 1.0
	if totalParas > 0 {
		// Verified paragraphs (strongly-resolved sources) count fully; flagged ones
		// (cited to a real but weakly-resolved source) count at half; rejected (no
		// lineage) count zero.
		groundingRatio = (float64(verifiedParas) + 0.5*float64(flaggedParas)) / float64(totalParas)
	}
	score := 0.25 + 0.55*groundingRatio                          // grounding dominates (0.25..0.80)
	score += minFloat(float64(len(raw.ClaimPackets))/40.0, 0.10) // small coverage bonus
	score += minFloat(float64(len(visuals))/4.0, 0.05)
	score -= minFloat(float64(flaggedSections)*0.05, 0.20)
	score -= minFloat(float64(contradictions)*0.03, 0.15)
	if score < 0.05 {
		score = 0.05
	}
	// Ceiling: no independent content verification runs, so the draft cannot earn a
	// "fully verified" score regardless of lineage coverage. >0.85 is reserved for a
	// future entailment-checked pipeline.
	score = minFloat(score, 0.85)

	return map[string]any{
		"critiqueId":       fmt.Sprintf("critique_%d_%s", now, hashIDForPipeline(jobID)),
		"jobId":            jobID,
		"query":            query,
		"createdAt":        now,
		"status":           "open",
		"overallScore":     score,
		"verificationMode": "citation-lineage only (no independent content fact-check)",
		"strengths":        uniqueStrings(strengths),
		"weaknesses":       uniqueStrings(weaknesses),
		"risks":            uniqueStrings(risks),
		"recommendations":  uniqueStrings(recommendations),
		"blueprintId":      blueprint.BlueprintID,
	}
}

// contradictionSectionTitles returns the distinct section titles whose unresolved
// issues mention a contradiction link, for the consolidated peer-review risk.
func contradictionSectionTitles(sections []SectionDraftArtifact) []string {
	out := make([]string, 0)
	seen := map[string]struct{}{}
	for _, section := range sections {
		for _, issue := range section.UnresolvedIssues {
			if !strings.Contains(strings.ToLower(issue), "contradiction") {
				continue
			}
			if _, dup := seen[section.Title]; !dup {
				seen[section.Title] = struct{}{}
				out = append(out, section.Title)
			}
			break
		}
	}
	return out
}

func pluralizeWord(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
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
	// Emit every labeled node declaration (root + children) BEFORE the edges, so
	// Mermaid renders the labels. Previously only the root + bare-id edges were
	// joined, so child labels were silently dropped and nodes showed as "p1 p2".
	specLines := []string{"flowchart TD"}
	for _, node := range nodes {
		specLines = append(specLines, "    "+node)
	}
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

func selectRelevantPackets(packets []evidence.EvidencePacket, sectionID string, limit int, assigned map[string]int) []evidence.EvidencePacket {
	selected := make([]evidence.EvidencePacket, 0, limit)
	seen := make(map[string]struct{}, limit)
	isAssigned := func(p evidence.EvidencePacket) bool { return assigned != nil && assigned[p.PacketID] > 0 }
	// add appends packet if new; returns true once the limit is reached.
	add := func(p evidence.EvidencePacket) bool {
		if _, dup := seen[p.PacketID]; dup {
			return len(selected) >= limit
		}
		seen[p.PacketID] = struct{}{}
		selected = append(selected, p)
		return len(selected) >= limit
	}

	// Pass 1: section-relevant packets, UNASSIGNED first so each section
	// contributes distinct evidence and specific sections don't all draw the same
	// packets; assigned ones are still available as a fallback so nothing starves.
	var matchUnassigned, matchAssigned []evidence.EvidencePacket
	for _, packet := range packets {
		if !containsString(packet.SectionRelevance, sectionID) {
			continue
		}
		if isAssigned(packet) {
			matchAssigned = append(matchAssigned, packet)
		} else {
			matchUnassigned = append(matchUnassigned, packet)
		}
	}
	for _, packet := range append(matchUnassigned, matchAssigned...) {
		if add(packet) {
			return selected
		}
	}

	// Back-fill. Synthesis sections (abstract/introduction/literature_review/
	// discussion/conclusion) legitimately summarize the whole corpus, so they may
	// draw on any packet. Empirically-specific sections (methods/results) must NOT
	// be padded with packets that belong to a different section — that fabricated
	// "Methods grounded on results packets" provenance — so they back-fill only
	// with section-agnostic packets and otherwise stay short (a real coverage gap).
	// Unassigned packets are preferred here too.
	broad := isBroadSynthesisSection(sectionID)
	var fillUnassigned, fillAssigned []evidence.EvidencePacket
	for _, packet := range packets {
		if _, dup := seen[packet.PacketID]; dup {
			continue
		}
		if !broad && len(packet.SectionRelevance) > 0 {
			continue
		}
		if isAssigned(packet) {
			fillAssigned = append(fillAssigned, packet)
		} else {
			fillUnassigned = append(fillUnassigned, packet)
		}
	}
	for _, packet := range append(fillUnassigned, fillAssigned...) {
		if add(packet) {
			return selected
		}
	}
	return selected
}

// sectionPacketLimit is how many claim packets a section may draw on. Broad
// synthesis sections get a larger budget (they cite across the whole corpus);
// specific sections get a smaller but still wide one. Larger limits let the prose
// engage many more distinct sources, so the cited bibliography is not tiny.
func sectionPacketLimit(sectionID string) int {
	if isBroadSynthesisSection(sectionID) {
		return 18
	}
	return 14
}

// isBroadSynthesisSection reports whether a section legitimately synthesizes the
// whole corpus (and may therefore draw on packets relevant to any section).
func isBroadSynthesisSection(sectionID string) bool {
	switch sectionID {
	case "abstract", "introduction", "literature_review", "discussion", "conclusion":
		return true
	default:
		return false
	}
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
	return fmt.Sprintf("%s[\"%s\"]", packetNodeRootID(nodeID), mermaidEscapeLabel(label))
}

// mermaidEscapeLabel makes free text safe inside a Mermaid ["..."] label:
// interior double quotes become the #quot; entity (Go's %q would emit \" which
// Mermaid does not understand), and characters that break label parsing are
// normalized to spaces/parentheses.
var mermaidLabelReplacer = strings.NewReplacer(
	"\"", "#quot;",
	"\n", " ",
	"\r", " ",
	"[", "(",
	"]", ")",
	"{", "(",
	"}", ")",
)

func mermaidEscapeLabel(label string) string {
	return mermaidLabelReplacer.Replace(strings.TrimSpace(label))
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
	// Count UNDIRECTED unique pairs: assignContradictions stores each link in both
	// directions (A->B and B->A), so keying on the ordered endpoint pair avoids the
	// 2x double count the directed key produced.
	seen := make(map[string]struct{})
	for _, packet := range raw.ClaimPackets {
		for _, contradictionID := range packet.ContradictionPacketIDs {
			lo, hi := packet.PacketID, contradictionID
			if lo > hi {
				lo, hi = hi, lo
			}
			seen[lo+"|"+hi] = struct{}{}
		}
	}
	return len(seen)
}
