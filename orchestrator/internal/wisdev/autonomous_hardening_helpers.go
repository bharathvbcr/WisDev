package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/evidence"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type LoopDraftCritique struct {
	NeedsRevision           bool     `json:"needsRevision"`
	RetrievalReopened       bool     `json:"retrievalReopened,omitempty"`
	AdditionalEvidenceFound bool     `json:"additionalEvidenceFound,omitempty"`
	Reasoning               string   `json:"reasoning"`
	NextQueries             []string `json:"nextQueries,omitempty"`
	MissingAspects          []string `json:"missingAspects,omitempty"`
	MissingSourceTypes      []string `json:"missingSourceTypes,omitempty"`
	Contradictions          []string `json:"contradictions,omitempty"`
	Confidence              float64  `json:"confidence,omitempty"`
}

func buildEvidenceItemsFromRawMaterial(query string, papers []search.Paper, limit int) []EvidenceItem {
	findings := buildEvidenceFindingsFromRawMaterial(query, papers, limit)
	if len(findings) == 0 {
		return nil
	}
	items := make([]EvidenceItem, 0, len(findings))
	for _, finding := range findings {
		items = append(items, EvidenceItem{
			Claim:      finding.Claim,
			Snippet:    finding.Snippet,
			PaperTitle: finding.PaperTitle,
			PaperID:    finding.SourceID,
			Status:     finding.Status,
			Confidence: finding.Confidence,
		})
	}
	return items
}

func buildEvidenceFindingsFromRawMaterial(query string, papers []search.Paper, limit int) []EvidenceFinding {
	if limit <= 0 {
		limit = 20
	}
	if len(papers) == 0 {
		return nil
	}

	jobID := stableWisDevID("raw-material", query, fmt.Sprintf("%d", len(papers)))
	_, dossier, err := evidence.BuildRawMaterialSet(jobID, query, papers)
	if err != nil {
		slog.Warn("raw-material evidence extraction failed",
			"component", "wisdev.evidence",
			"operation", "buildEvidenceFindingsFromRawMaterial",
			"query", strings.TrimSpace(query),
			"error", err.Error(),
		)
		return nil
	}

	sourceIndex := make(map[string]evidence.CanonicalCitationRecord, len(dossier.CanonicalSources))
	for _, source := range dossier.CanonicalSources {
		if canonicalID := strings.TrimSpace(source.CanonicalID); canonicalID != "" {
			sourceIndex[canonicalID] = source
		}
	}

	packets := make([]evidence.EvidencePacket, 0, len(dossier.VerifiedClaims)+len(dossier.TentativeClaims))
	packets = append(packets, dossier.VerifiedClaims...)
	packets = append(packets, dossier.TentativeClaims...)

	out := make([]EvidenceFinding, 0, minInt(limit, len(packets)))
	seen := make(map[string]struct{}, limit)
	for _, packet := range packets {
		if len(out) >= limit {
			break
		}
		claim := strings.TrimSpace(packet.ClaimText)
		if claim == "" {
			continue
		}

		span := firstMeaningfulEvidenceSpan(packet)
		sourceID := strings.TrimSpace(packet.PacketID)
		snippet := claim
		if span != nil {
			sourceID = firstNonEmpty(strings.TrimSpace(span.SourceCanonicalID), sourceID)
			snippet = firstNonEmpty(strings.TrimSpace(span.Snippet), snippet)
		}

		title := ""
		year := 0
		if record, ok := sourceIndex[sourceID]; ok {
			title = strings.TrimSpace(record.Title)
			year = record.Year
		} else if span != nil {
			if record, ok := sourceIndex[strings.TrimSpace(span.SourceCanonicalID)]; ok {
				title = strings.TrimSpace(record.Title)
				year = record.Year
			}
		}

		finding := EvidenceFinding{
			ID:         stableWisDevID("evidence-packet", packet.PacketID, sourceID, claim),
			Claim:      claim,
			Keywords:   dedupeTrimmedStrings(append([]string(nil), packet.SectionRelevance...)),
			SourceID:   firstNonEmpty(sourceID, strings.TrimSpace(packet.PacketID)),
			PaperTitle: title,
			Snippet:    firstNonEmpty(snippet, claim),
			Year:       year,
			Confidence: ClampFloat(defaultPacketConfidence(packet.Confidence), 0.25, 0.99),
			Status:     firstNonEmpty(strings.TrimSpace(packet.VerifierStatus), "needs_review"),
		}
		if _, exists := seen[finding.ID]; exists {
			continue
		}
		seen[finding.ID] = struct{}{}
		out = append(out, finding)
	}
	return out
}

func firstMeaningfulEvidenceSpan(packet evidence.EvidencePacket) *evidence.EvidenceSpan {
	for idx := range packet.EvidenceSpans {
		span := &packet.EvidenceSpans[idx]
		if strings.TrimSpace(span.SourceCanonicalID) != "" || strings.TrimSpace(span.Snippet) != "" {
			return span
		}
	}
	return nil
}

func defaultPacketConfidence(value float64) float64 {
	if value <= 0 {
		return 0.55
	}
	return value
}

func countOpenCoverageLedgerEntries(ledger []CoverageLedgerEntry) int {
	count := 0
	for _, entry := range ledger {
		if strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
			count++
		}
	}
	return count
}

func computeCitationIntegrityFromFindings(findings []EvidenceFinding) float64 {
	if len(findings) == 0 {
		return 0
	}
	grounded := 0
	for _, finding := range findings {
		if strings.TrimSpace(finding.SourceID) != "" && strings.TrimSpace(finding.Snippet) != "" {
			grounded++
		}
	}
	return ClampFloat(float64(grounded)/float64(len(findings)), 0, 1)
}

func (l *AutonomousLoop) critiqueDraft(ctx context.Context, query string, draft string, papers []search.Paper, evidenceItems []EvidenceItem, gap *LoopGapState) *LoopDraftCritique {
	papers = SanitizeRetrievedPapersForLLM(papers, "critiqueDraft")
	evidenceItems = SanitizeEvidenceItemsForLLM(evidenceItems, "critiqueDraft")
	fallback := heuristicDraftCritique(query, draft, papers, evidenceItems, gap)
	if l == nil || l.llmClient == nil {
		return fallback
	}
	if remaining := autonomousLLMCooldownRemaining(l); remaining > 0 {
		slog.Warn("draft critique using cooldown fallback",
			"component", "wisdev.autonomous",
			"operation", "critiqueDraft",
			"stage", "cooldown_fallback",
			"query", strings.TrimSpace(query),
			"retry_after_ms", remaining.Milliseconds(),
		)
		return fallback
	}

	evidenceLines := make([]string, 0, minInt(len(evidenceItems), 8))
	for idx, item := range evidenceItems {
		if idx >= 8 {
			break
		}
		evidenceLines = append(evidenceLines, fmt.Sprintf("- [%s] %s", strings.TrimSpace(firstNonEmpty(item.PaperTitle, item.PaperID)), strings.TrimSpace(firstNonEmpty(item.Snippet, item.Claim))))
	}
	if len(evidenceLines) == 0 {
		evidenceLines = append(evidenceLines, "- No grounded evidence items were assembled.")
	}

	prompt := appendWisdevStructuredOutputInstruction(fmt.Sprintf(`Critique the following research draft and determine whether retrieval must be reopened before the answer is finalized.
Query: %s
Observed Source Families: %s
Open Coverage Ledger Items: %d
Coverage Ledger:
%s

Grounded Evidence:
%s

Draft:
%s

Return:
- needsRevision: whether the system must reopen retrieval
- reasoning: concise explanation
- nextQueries: up to 3 targeted follow-up queries
- missingAspects: up to 5 unresolved coverage gaps
- missingSourceTypes: up to 4 missing source families or evidence types
- contradictions: up to 4 unresolved contradictions
- confidence: confidence between 0 and 1

Set needsRevision to true when the draft overreaches the grounded evidence, unresolved contradictions remain, or the coverage ledger is still materially open.
`, strings.TrimSpace(query), strings.Join(gapObservedSourceFamilies(gap), ", "), countOpenCoverageLedgerEntries(gapLedger(gap)), formatCoverageLedgerForPrompt(gapLedger(gap), 6), strings.Join(evidenceLines, "\n"), clipPromptText(draft, 5000)))

	reqCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	resp, err := l.llmClient.StructuredOutput(reqCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveStandardModel(),
		JsonSchema: `{"type":"object","required":["needsRevision","reasoning"],"properties":{"needsRevision":{"type":"boolean"},"reasoning":{"type":"string"},"nextQueries":{"type":"array","items":{"type":"string"},"maxItems":3},"missingAspects":{"type":"array","items":{"type":"string"},"maxItems":5},"missingSourceTypes":{"type":"array","items":{"type":"string"},"maxItems":4},"contradictions":{"type":"array","items":{"type":"string"},"maxItems":4},"confidence":{"type":"number"}}}`,
	}))
	cancel()
	if err != nil {
		slog.Warn("draft critique fell back to heuristic path",
			"component", "wisdev.autonomous",
			"operation", "critiqueDraft",
			"query", strings.TrimSpace(query),
			"error", err.Error(),
		)
		return fallback
	}

	var critique LoopDraftCritique
	if err := unmarshalLLMJSON(resp.JsonResult, &critique); err != nil {
		slog.Warn("draft critique JSON parse failed",
			"component", "wisdev.autonomous",
			"operation", "critiqueDraft",
			"query", strings.TrimSpace(query),
			"error", err.Error(),
		)
		return fallback
	}
	return normalizeDraftCritique(query, &critique, papers, gap)
}

func heuristicDraftCritique(query string, draft string, papers []search.Paper, evidenceItems []EvidenceItem, gap *LoopGapState) *LoopDraftCritique {
	critique := &LoopDraftCritique{
		Reasoning:  "Coverage and grounding passed the default autonomous critique.",
		Confidence: 0.74,
	}
	needsRevision := strings.TrimSpace(draft) == "" || len(evidenceItems) < 3 || hasMaterialOpenCoverageGaps(gap, evidenceItems)
	if len(papers) > 0 && len(gapObservedSourceFamilies(gap)) < 2 {
		needsRevision = true
	}
	if gap != nil && len(gap.Contradictions) > 0 {
		needsRevision = true
	}

	if needsRevision {
		critique.NeedsRevision = true
		critique.Reasoning = "The draft still has open coverage gaps, weak evidence grounding, or unresolved contradictions."
		critique.MissingAspects = dedupeTrimmedStrings(append([]string(nil), gapMissingAspects(gap)...))
		critique.MissingSourceTypes = dedupeTrimmedStrings(append([]string(nil), gapMissingSourceTypes(gap)...))
		critique.Contradictions = dedupeTrimmedStrings(append([]string(nil), gapContradictions(gap)...))
		critique.Confidence = 0.58
	}
	return normalizeDraftCritique(query, critique, papers, gap)
}

func hasMaterialOpenCoverageGaps(gap *LoopGapState, evidenceItems []EvidenceItem) bool {
	if gap == nil {
		return false
	}
	if len(gap.MissingAspects) > 0 || len(gap.MissingSourceTypes) > 0 || len(gap.Contradictions) > 0 {
		return true
	}
	for _, entry := range gap.Ledger {
		if !strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
			continue
		}
		if coverageLedgerEntryIsGenericValidationCheckpoint(entry) &&
			gap.Sufficient &&
			len(evidenceItems) >= 3 {
			continue
		}
		return true
	}
	return false
}

func normalizeDraftCritique(query string, critique *LoopDraftCritique, papers []search.Paper, gap *LoopGapState) *LoopDraftCritique {
	if critique == nil {
		critique = &LoopDraftCritique{}
	}
	critique.Reasoning = strings.TrimSpace(critique.Reasoning)
	critique.MissingAspects = dedupeTrimmedStrings(append([]string(nil), critique.MissingAspects...))
	critique.MissingSourceTypes = dedupeTrimmedStrings(append([]string(nil), critique.MissingSourceTypes...))
	critique.Contradictions = dedupeTrimmedStrings(append([]string(nil), critique.Contradictions...))
	critique.NextQueries = buildCritiqueFollowUpQueries(query, critique, gap, papers)
	if critique.Confidence <= 0 {
		critique.Confidence = map[bool]float64{true: 0.58, false: 0.74}[critique.NeedsRevision]
	}
	critique.Confidence = ClampFloat(critique.Confidence, 0, 1)
	return critique
}

func buildCritiqueFollowUpQueries(query string, critique *LoopDraftCritique, gap *LoopGapState, papers []search.Paper) []string {
	queries := normalizeLoopQueries("", critique.NextQueries)
	if len(queries) == 0 {
		queries = append(queries, buildFollowUpQueriesFromLedger(query, gapLedger(gap), 4)...)
	}
	if len(queries) == 0 {
		analysis := &sufficiencyAnalysis{
			MissingAspects:     append([]string(nil), critique.MissingAspects...),
			MissingSourceTypes: append([]string(nil), critique.MissingSourceTypes...),
			Contradictions:     append([]string(nil), critique.Contradictions...),
		}
		queries = deriveLoopFollowUpQueries(query, analysis, papers)
	}
	if len(queries) > 4 {
		queries = queries[:4]
	}
	return queries
}

func mergeDraftCritiqueIntoGapState(gap *LoopGapState, critique *LoopDraftCritique, query string) *LoopGapState {
	if critique == nil {
		return gap
	}
	if gap == nil {
		gap = &LoopGapState{}
	}
	gapStillOpen := loopGapNeedsFollowUp(gap)
	if critique.NeedsRevision && !critique.RetrievalReopened {
		gap.Sufficient = false
	}
	if strings.TrimSpace(gap.Reasoning) == "" {
		gap.Reasoning = strings.TrimSpace(critique.Reasoning)
	} else if strings.TrimSpace(critique.Reasoning) != "" && !strings.Contains(strings.ToLower(gap.Reasoning), strings.ToLower(critique.Reasoning)) {
		gap.Reasoning = strings.TrimSpace(gap.Reasoning + " Critique: " + critique.Reasoning)
	}
	if !critique.RetrievalReopened || gapStillOpen {
		gap.MissingAspects = dedupeTrimmedStrings(append(gap.MissingAspects, critique.MissingAspects...))
		gap.MissingSourceTypes = dedupeTrimmedStrings(append(gap.MissingSourceTypes, critique.MissingSourceTypes...))
		gap.Contradictions = dedupeTrimmedStrings(append(gap.Contradictions, critique.Contradictions...))
		gap.NextQueries = dedupeTrimmedStrings(append(append([]string(nil), critique.NextQueries...), gap.NextQueries...))
	}
	if critique.Confidence > 0 {
		if gap.Confidence <= 0 || critique.Confidence < gap.Confidence {
			gap.Confidence = ClampFloat(critique.Confidence, 0, 1)
		}
	}
	status := coverageLedgerStatusResolved
	title := "Draft critique passed"
	switch {
	case critique.NeedsRevision && !critique.RetrievalReopened:
		status = coverageLedgerStatusOpen
		title = "Draft critique requested follow-up retrieval"
	case critique.NeedsRevision && critique.RetrievalReopened && !critique.AdditionalEvidenceFound:
		status = coverageLedgerStatusOpen
		title = "Draft critique reopened retrieval without new evidence"
	case critique.NeedsRevision && gapStillOpen:
		status = coverageLedgerStatusOpen
		title = "Draft critique reopened retrieval but gaps remain"
	case critique.NeedsRevision && critique.RetrievalReopened && critique.AdditionalEvidenceFound:
		title = "Draft critique reopened retrieval and resolved"
	}
	entry := CoverageLedgerEntry{
		ID:                stableWisDevID("draft-critique", query, critique.Reasoning, fmt.Sprintf("%t", critique.NeedsRevision)),
		Category:          "draft_critique",
		Status:            status,
		Title:             title,
		Description:       strings.TrimSpace(critique.Reasoning),
		SupportingQueries: append([]string(nil), critique.NextQueries...),
		SourceFamilies:    append([]string(nil), gapObservedSourceFamilies(gap)...),
		Confidence:        ClampFloat(defaultPacketConfidence(critique.Confidence), 0.25, 0.99),
	}
	entry.ObligationType = inferCoverageObligationType(entry)
	entry.OwnerWorker = string(ResearchWorkerScout)
	switch entry.ObligationType {
	case "missing_counter_evidence", "missing_replication":
		entry.OwnerWorker = string(ResearchWorkerContradictionCritic)
	case "missing_citation_identity":
		entry.OwnerWorker = string(ResearchWorkerCitationGraph)
	case "missing_source_diversity", "missing_full_text":
		entry.OwnerWorker = string(ResearchWorkerSourceDiversifier)
	case "unverified_claim":
		entry.OwnerWorker = string(ResearchWorkerIndependentVerifier)
	}
	entry.Severity = inferCoverageObligationSeverity(entry)
	if entry.ObligationType == "coverage_gap" && strings.EqualFold(status, coverageLedgerStatusOpen) {
		entry.ObligationType = "missing_population"
		entry.OwnerWorker = string(ResearchWorkerScout)
		entry.Severity = "high"
	}
	gap.Ledger = append(gap.Ledger, entry)
	return gap
}

func loopGapNeedsFollowUp(gap *LoopGapState) bool {
	if gap == nil {
		return false
	}
	if !gap.Sufficient {
		return true
	}
	if len(gap.MissingAspects) > 0 || len(gap.MissingSourceTypes) > 0 || len(gap.Contradictions) > 0 {
		return true
	}
	return hasOpenActionableCoverageGaps(gap)
}

func formatCoverageLedgerForPrompt(ledger []CoverageLedgerEntry, limit int) string {
	if len(ledger) == 0 {
		return "- No coverage ledger entries were recorded."
	}
	lines := make([]string, 0, minInt(limit, len(ledger)))
	for idx, entry := range ledger {
		if idx >= limit {
			break
		}
		lines = append(lines, fmt.Sprintf("- [%s/%s] %s :: %s", strings.TrimSpace(firstNonEmpty(entry.Category, "coverage")), strings.TrimSpace(firstNonEmpty(entry.Status, coverageLedgerStatusOpen)), strings.TrimSpace(firstNonEmpty(entry.Title, "unnamed")), strings.TrimSpace(firstNonEmpty(entry.Description, entry.Title))))
	}
	return strings.Join(lines, "\n")
}

func clipPromptText(text string, maxLen int) string {
	trimmed := strings.TrimSpace(text)
	if maxLen > 0 && len(trimmed) > maxLen {
		return strings.TrimSpace(trimmed[:maxLen]) + "..."
	}
	return trimmed
}

func gapLedger(gap *LoopGapState) []CoverageLedgerEntry {
	if gap == nil {
		return nil
	}
	return gap.Ledger
}

func gapObservedSourceFamilies(gap *LoopGapState) []string {
	if gap == nil {
		return nil
	}
	return gap.ObservedSourceFamilies
}

func gapMissingAspects(gap *LoopGapState) []string {
	if gap == nil {
		return nil
	}
	return gap.MissingAspects
}

func gapMissingSourceTypes(gap *LoopGapState) []string {
	if gap == nil {
		return nil
	}
	return gap.MissingSourceTypes
}

func gapContradictions(gap *LoopGapState) []string {
	if gap == nil {
		return nil
	}
	return gap.Contradictions
}

// BuildResearchCoverageObligations converts open coverage-ledger entries from a
// ResearchSessionState into a serialisable slice for API payload embedding.
// Open entries represent gaps that still require evidence collection; fulfilled
// entries are omitted to keep the payload lean.
func BuildResearchCoverageObligations(state *ResearchSessionState) []any {
	if state == nil || len(state.CoverageLedger) == 0 {
		return nil
	}
	obligations := make([]any, 0, len(state.CoverageLedger))
	for _, entry := range state.CoverageLedger {
		if !strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
			continue
		}
		obligations = append(obligations, map[string]any{
			"id":             entry.ID,
			"title":          entry.Title,
			"status":         entry.Status,
			"obligationType": inferCoverageObligationType(entry),
			"ownerWorker":    inferCoverageObligationOwner(entry),
		})
	}
	return obligations
}

// ResearchVerifierVerdict returns a concise verdict string from a
// ResearchVerifierDecision, suitable for payload metadata embedding.
func ResearchVerifierVerdict(decision *ResearchVerifierDecision) string {
	if decision == nil {
		return ""
	}
	verdict := strings.TrimSpace(decision.Verdict)
	if verdict == "" {
		verdict = "unknown"
	}
	return verdict
}
