package wisdev

import (
	"fmt"
	"sort"
	"strings"

	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

const (
	coverageLedgerStatusOpen     = "open"
	coverageLedgerStatusResolved = "resolved"

	questFollowUpQueriesScratchKey = "followUpQueries"
	maxQuestCritiqueRevisions      = 2
)

type evidenceClaimSeed struct {
	claim      string
	snippet    string
	section    string
	confidence float64
}

func buildEvidenceFindingsFromSource(source Source, limit int) []EvidenceFinding {
	seeds := extractEvidenceClaimSeeds(source, limit)
	if len(seeds) == 0 {
		return nil
	}

	sourceID := firstNonEmpty(
		strings.TrimSpace(source.DOI),
		strings.TrimSpace(source.ArxivID),
		strings.TrimSpace(source.ID),
		strings.TrimSpace(source.Link),
		strings.TrimSpace(source.Title),
	)
	baseConfidence := ClampFloat(firstNonEmptyScore(source.Score, 0.62), 0.45, 0.98)
	out := make([]EvidenceFinding, 0, len(seeds))
	for idx, seed := range seeds {
		confidence := ClampFloat((baseConfidence+seed.confidence)/2, 0.45, 0.99)
		out = append(out, EvidenceFinding{
			ID:         stableWisDevID("finding", sourceID, seed.claim, fmt.Sprintf("%d", idx)),
			Claim:      seed.claim,
			Keywords:   dedupeTrimmedStrings(append([]string(nil), source.Keywords...)),
			SourceID:   sourceID,
			PaperTitle: strings.TrimSpace(source.Title),
			Snippet:    seed.snippet,
			Year:       source.Year,
			Confidence: confidence,
			Status:     "accepted",
		})
	}
	return out
}

func buildEvidenceItemsFromPaper(paper internalsearch.Paper, limit int) []EvidenceItem {
	findings := buildEvidenceFindingsFromSource(mapPaperToSource(paper), limit)
	if len(findings) == 0 {
		return nil
	}
	items := make([]EvidenceItem, 0, len(findings))
	for _, finding := range findings {
		items = append(items, EvidenceItem{
			Claim:      finding.Claim,
			Snippet:    finding.Snippet,
			PaperTitle: finding.PaperTitle,
			PaperID:    firstNonEmpty(strings.TrimSpace(paper.ID), finding.SourceID),
			Confidence: finding.Confidence,
		})
	}
	return items
}

func collectEvidenceItemsFromPapers(papers []internalsearch.Paper, perPaperLimit int, totalLimit int) []EvidenceItem {
	if len(papers) == 0 || totalLimit == 0 {
		return nil
	}
	if perPaperLimit <= 0 {
		perPaperLimit = 2
	}
	items := make([]EvidenceItem, 0, MinInt(len(papers)*perPaperLimit, MaxInt(totalLimit, 1)))
	for _, paper := range papers {
		for _, item := range buildEvidenceItemsFromPaper(paper, perPaperLimit) {
			items = append(items, item)
			if totalLimit > 0 && len(items) >= totalLimit {
				return items
			}
		}
	}
	return items
}

func buildObservedSourceFamiliesFromSources(sources []Source) []string {
	families := make([]string, 0, len(sources)*2)
	seen := make(map[string]struct{}, len(sources)*2)
	add := func(value string) {
		trimmed := strings.ToLower(strings.TrimSpace(value))
		if trimmed == "" {
			return
		}
		if _, exists := seen[trimmed]; exists {
			return
		}
		seen[trimmed] = struct{}{}
		families = append(families, trimmed)
	}

	for _, source := range sources {
		add(source.Source)
		add(source.SiteName)
		for _, api := range source.SourceApis {
			add(api)
		}
	}
	sort.Strings(families)
	return families
}

func buildObservedSourceFamiliesFromPapers(papers []internalsearch.Paper) []string {
	if len(papers) == 0 {
		return nil
	}
	sources := make([]Source, 0, len(papers))
	for _, paper := range papers {
		sources = append(sources, mapPaperToSource(paper))
	}
	return buildObservedSourceFamiliesFromSources(sources)
}

func inferResearchCoverageFamilies(papers []internalsearch.Paper) []string {
	families := make([]string, 0, len(papers)*3)
	seen := make(map[string]struct{}, len(papers)*3)
	add := func(value string) {
		trimmed := strings.ToLower(strings.TrimSpace(value))
		if trimmed == "" {
			return
		}
		if _, exists := seen[trimmed]; exists {
			return
		}
		seen[trimmed] = struct{}{}
		families = append(families, trimmed)
	}
	for _, paper := range papers {
		sourceText := strings.ToLower(strings.Join([]string{
			paper.Source,
			strings.Join(paper.SourceApis, " "),
			paper.Venue,
			paper.EvidenceLevel,
			paper.Title,
			paper.Abstract,
		}, " "))
		if strings.TrimSpace(paper.Abstract) != "" || strings.TrimSpace(paper.FullText) != "" {
			add("primary_evidence")
		}
		if strings.TrimSpace(paper.DOI) != "" || strings.TrimSpace(paper.ArxivID) != "" || strings.TrimSpace(paper.ID) != "" {
			add("citation_metadata")
		}
		if containsAnyFold(sourceText, "systematic review", "meta-analysis", "meta analysis", "review") {
			add("review_or_meta_analysis")
		}
		if containsAnyFold(sourceText, "replication", "reproducibility", "reproduce", "benchmark", "ablation", "dataset") {
			add("replication_or_benchmark")
		}
		if containsAnyFold(sourceText, "limitation", "contradict", "conflict", "failed", "null result", "negative result") {
			add("counter_evidence")
		}
		if containsAnyFold(sourceText, "clinical trial", "randomized", "randomised", "cohort", "guideline", "pubmed", "clinical_trials") {
			add("clinical_primary")
		}
	}
	sort.Strings(families)
	return families
}

func containsAnyFold(text string, needles ...string) bool {
	lowered := strings.ToLower(strings.TrimSpace(text))
	if lowered == "" {
		return false
	}
	for _, needle := range needles {
		if strings.Contains(lowered, strings.ToLower(strings.TrimSpace(needle))) {
			return true
		}
	}
	return false
}

func sortedMapKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func buildLoopCoverageLedger(analysis *sufficiencyAnalysis, coverage LoopCoverageState, papers []internalsearch.Paper, plannedQueries []string) []CoverageLedgerEntry {
	sourceFamilies := buildObservedSourceFamiliesFromPapers(papers)
	entries := make([]CoverageLedgerEntry, 0, 10)
	appendEntry := func(category string, status string, title string, description string, queries []string, confidence float64, obligationType string, owner string, severity string) {
		entries = append(entries, CoverageLedgerEntry{
			ID:                stableWisDevID("coverage-ledger", category, title, fmt.Sprintf("%d", len(entries)+1)),
			Category:          strings.TrimSpace(category),
			Status:            strings.TrimSpace(firstNonEmpty(status, coverageLedgerStatusOpen)),
			Title:             strings.TrimSpace(title),
			Description:       strings.TrimSpace(description),
			SupportingQueries: dedupeTrimmedStrings(append([]string(nil), queries...)),
			SourceFamilies:    append([]string(nil), sourceFamilies...),
			Confidence:        ClampFloat(confidence, 0.25, 0.99),
			ObligationType:    strings.TrimSpace(obligationType),
			OwnerWorker:       strings.TrimSpace(owner),
			Severity:          strings.TrimSpace(severity),
		})
	}

	for _, query := range coverage.QueriesWithoutCoverage {
		appendEntry(
			"query_coverage",
			coverageLedgerStatusOpen,
			"No grounded evidence for query: "+strings.TrimSpace(query),
			fmt.Sprintf("The loop executed %q without attaching grounded evidence.", strings.TrimSpace(query)),
			[]string{strings.TrimSpace(query)},
			0.55,
			"missing_population",
			string(ResearchWorkerScout),
			"high",
		)
	}
	for _, query := range coverage.UnexecutedPlannedQueries {
		appendEntry(
			"planned_query",
			coverageLedgerStatusOpen,
			"Unexecuted planned branch: "+strings.TrimSpace(query),
			fmt.Sprintf("The loop stopped before executing planned query %q.", strings.TrimSpace(query)),
			[]string{strings.TrimSpace(query)},
			0.5,
			"missing_population",
			string(ResearchWorkerScout),
			"high",
		)
	}

	if analysis != nil {
		for _, aspect := range analysis.MissingAspects {
			obligationType, owner, severity := inferMissingAspectObligation(strings.TrimSpace(aspect))
			appendEntry("coverage", coverageLedgerStatusOpen, strings.TrimSpace(aspect), strings.TrimSpace(aspect), analysis.NextQueries, analysis.Confidence, obligationType, owner, severity)
		}
		for _, sourceType := range analysis.MissingSourceTypes {
			appendEntry(
				"source_diversity",
				coverageLedgerStatusOpen,
				"Missing source coverage: "+strings.TrimSpace(sourceType),
				fmt.Sprintf("The loop still needs stronger %s evidence.", strings.TrimSpace(sourceType)),
				analysis.NextQueries,
				analysis.Confidence,
				"missing_source_diversity",
				string(ResearchWorkerSourceDiversifier),
				"high",
			)
		}
		for _, contradiction := range analysis.Contradictions {
			appendEntry("contradiction", coverageLedgerStatusOpen, "Resolve contradiction", strings.TrimSpace(contradiction), analysis.NextQueries, analysis.Confidence, "missing_counter_evidence", string(ResearchWorkerContradictionCritic), "critical")
		}
		if analysis.Sufficient && len(entries) == 0 {
			appendEntry("coverage", coverageLedgerStatusResolved, "Coverage satisfied", strings.TrimSpace(analysis.Reasoning), nil, analysis.Confidence, "coverage_gap", string(ResearchWorkerScout), "low")
		}
	} else if len(entries) == 0 {
		status := coverageLedgerStatusOpen
		title := "Structured sufficiency checkpoint unavailable"
		description := "The loop completed without a structured sufficiency payload, so coverage remains open until a follow-up pass validates the evidence set."
		confidence := 0.42
		if coverage.UniquePaperCount == 0 {
			title = "No grounded evidence collected"
			description = "The loop did not collect grounded sources and must reopen retrieval before synthesis can be treated as comprehensive."
			confidence = 0.36
		}
		severity := "high"
		if status == coverageLedgerStatusResolved {
			severity = "low"
		}
		appendEntry("coverage", status, title, description, nil, confidence, "missing_population", string(ResearchWorkerScout), severity)
	}

	if len(sourceFamilies) > 0 {
		appendEntry(
			"source_inventory",
			coverageLedgerStatusResolved,
			"Observed source families",
			strings.Join(sourceFamilies, ", "),
			nil,
			0.8,
			"coverage_gap",
			string(ResearchWorkerScout),
			"low",
		)
	}
	if coverage.UniquePaperCount >= 3 {
		entries = mergeCoverageLedgerEntries(entries, buildCoverageRubricLedger(plannedQueries, papers, sourceFamilies))
	}
	return entries
}

func buildCoverageRubricLedger(plannedQueries []string, papers []internalsearch.Paper, providerFamilies []string) []CoverageLedgerEntry {
	baseQuery := firstNonEmpty(append([]string(nil), plannedQueries...)...)
	observed := make(map[string]struct{}, len(providerFamilies)+8)
	for _, family := range append(append([]string(nil), providerFamilies...), inferResearchCoverageFamilies(papers)...) {
		if trimmed := strings.ToLower(strings.TrimSpace(family)); trimmed != "" {
			observed[trimmed] = struct{}{}
		}
	}
	has := func(family string) bool {
		_, ok := observed[strings.ToLower(strings.TrimSpace(family))]
		return ok
	}
	requirements := []struct {
		family      string
		title       string
		description string
		querySuffix string
		priority    int
		confidence  float64
	}{
		{"primary_evidence", "Primary evidence required", "The research set needs at least one grounded primary or full-text evidence source.", "primary evidence study full text", 90, 0.62},
		{"review_or_meta_analysis", "Review or meta-analysis coverage required", "High-depth research should include review-level synthesis when available.", "systematic review meta analysis", 75, 0.58},
		{"replication_or_benchmark", "Replication or benchmark coverage required", "The loop must search for independent replication, reproducibility, benchmark, dataset, or ablation evidence.", "replication reproducibility benchmark dataset ablation", 80, 0.6},
		{"counter_evidence", "Counter-evidence coverage required", "The loop must actively search for limitations, null results, failed replication, or contradictory findings.", "limitations contradictory evidence failed replication null results", 85, 0.57},
		{"citation_metadata", "Citation metadata integrity required", "At least one source must expose stable citation identity such as DOI, arXiv, OpenAlex, Semantic Scholar, or an equivalent ID.", "DOI arXiv OpenAlex citation metadata", 88, 0.64},
	}
	entries := make([]CoverageLedgerEntry, 0, len(requirements)+1)
	for _, req := range requirements {
		if has(req.family) {
			continue
		}
		query := strings.TrimSpace(baseQuery + " " + req.querySuffix)
		entries = append(entries, CoverageLedgerEntry{
			ID:                stableWisDevID("coverage-rubric", req.family, baseQuery),
			Category:          "coverage_rubric",
			Status:            coverageLedgerStatusOpen,
			Title:             req.title,
			Description:       req.description,
			SupportingQueries: dedupeTrimmedStrings([]string{query}),
			SourceFamilies:    sortedMapKeys(observed),
			Confidence:        req.confidence,
			Required:          true,
			Priority:          req.priority,
			ObligationType:    inferCoverageRubricObligation(req.family),
			OwnerWorker:       inferCoverageRubricOwner(req.family),
			Severity:          inferCoverageRubricSeverity(req.family, req.priority),
		})
	}
	if len(entries) == 0 && len(papers) > 0 {
		entries = append(entries, CoverageLedgerEntry{
			ID:             stableWisDevID("coverage-rubric", "passed", baseQuery),
			Category:       "coverage_rubric",
			Status:         coverageLedgerStatusResolved,
			Title:          "Deterministic coverage rubric passed",
			Description:    "Primary evidence, review/synthesis, replication or benchmark, counter-evidence, and citation metadata families were observed.",
			SourceFamilies: sortedMapKeys(observed),
			Confidence:     0.84,
			Required:       true,
			Priority:       70,
			ObligationType: "coverage_gap",
			OwnerWorker:    string(ResearchWorkerScout),
			Severity:       "low",
		})
	}
	return entries
}

func inferMissingAspectObligation(aspect string) (obligationType string, ownerWorker string, severity string) {
	obligationType = inferCoverageObligationType(CoverageLedgerEntry{
		Category:    "coverage",
		Title:       strings.TrimSpace(aspect),
		Description: strings.TrimSpace(aspect),
	})
	switch obligationType {
	case "missing_counter_evidence":
		ownerWorker = string(ResearchWorkerContradictionCritic)
	case "missing_replication":
		ownerWorker = string(ResearchWorkerContradictionCritic)
	case "missing_citation_identity":
		ownerWorker = string(ResearchWorkerCitationGraph)
	case "missing_source_diversity":
		ownerWorker = string(ResearchWorkerSourceDiversifier)
	case "missing_full_text":
		ownerWorker = string(ResearchWorkerSourceDiversifier)
	case "missing_population":
		ownerWorker = string(ResearchWorkerScout)
	default:
		ownerWorker = string(ResearchWorkerScout)
	}
	if obligationType == "missing_counter_evidence" || obligationType == "missing_replication" {
		severity = "critical"
	} else if obligationType == "missing_full_text" || obligationType == "missing_source_diversity" || obligationType == "missing_citation_identity" {
		severity = "high"
	} else {
		severity = "medium"
	}
	return
}

func inferCoverageRubricObligation(family string) string {
	switch strings.ToLower(strings.TrimSpace(family)) {
	case "primary_evidence":
		return "missing_full_text"
	case "review_or_meta_analysis":
		return "missing_source_diversity"
	case "replication_or_benchmark":
		return "missing_replication"
	case "counter_evidence":
		return "missing_counter_evidence"
	case "citation_metadata":
		return "missing_citation_identity"
	default:
		return "coverage_gap"
	}
}

func inferCoverageRubricOwner(family string) string {
	switch inferCoverageRubricObligation(strings.ToLower(strings.TrimSpace(family))) {
	case "missing_counter_evidence", "missing_replication":
		return string(ResearchWorkerContradictionCritic)
	case "missing_citation_identity":
		return string(ResearchWorkerCitationGraph)
	case "missing_source_diversity", "missing_full_text":
		return string(ResearchWorkerSourceDiversifier)
	default:
		return string(ResearchWorkerScout)
	}
}

func inferCoverageRubricSeverity(family string, priority int) string {
	switch inferCoverageRubricObligation(strings.ToLower(strings.TrimSpace(family))) {
	case "missing_citation_identity":
		return "critical"
	case "missing_counter_evidence", "missing_replication", "missing_full_text":
		if priority >= 85 {
			return "critical"
		}
		return "high"
	case "missing_source_diversity":
		return "medium"
	default:
		return "medium"
	}
}

type sourceAcquisitionStats struct {
	Total             int
	FullText          int
	PDFCandidates     int
	OpenAccess        int
	LinkCandidates    int
	StableIdentities  int
	ProviderFamilies  map[string]struct{}
	SourceIdentities  []string
	DirectCandidateID []string
}

func mergeSourceAcquisitionLedger(gap *LoopGapState, query string, papers []internalsearch.Paper, planes ...ResearchExecutionPlane) *LoopGapState {
	if gap == nil {
		gap = &LoopGapState{}
	}
	if !isHighDepthResearchPlane(firstResearchExecutionPlane(planes...)) {
		return gap
	}
	acquisitionEntries := buildSourceAcquisitionLedger(query, papers)
	if len(acquisitionEntries) == 0 {
		return gap
	}
	gap.Ledger = mergeCoverageLedgerEntries(gap.Ledger, acquisitionEntries)
	openCount := 0
	for _, entry := range acquisitionEntries {
		if strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
			openCount++
		}
	}
	if openCount > 0 {
		gap.Sufficient = false
		gap.MissingAspects = dedupeTrimmedStrings(append(gap.MissingAspects, fmt.Sprintf("%d full-text source acquisition gap(s) remain open", openCount)))
		gap.NextQueries = dedupeTrimmedStrings(append(buildFollowUpQueriesFromLedger(query, acquisitionEntries, 3), gap.NextQueries...))
	}
	return gap
}

func buildSourceAcquisitionLedger(query string, papers []internalsearch.Paper) []CoverageLedgerEntry {
	stats := analyzeSourceAcquisition(papers)
	if stats.Total == 0 {
		return nil
	}
	requiredFullText := requiredFullTextSourceCount(stats.Total)
	status := coverageLedgerStatusOpen
	title := "Full-text source acquisition pending"
	confidence := 0.54
	priority := 94
	if stats.FullText >= requiredFullText {
		status = coverageLedgerStatusResolved
		title = "Full-text source acquisition satisfied"
		confidence = 0.84
		priority = 74
	} else if stats.FullText > 0 {
		title = "Full-text source acquisition is partial"
		confidence = 0.62
	} else if stats.PDFCandidates == 0 && stats.OpenAccess == 0 {
		title = "No direct full-text acquisition candidate found"
		confidence = 0.48
	}
	description := fmt.Sprintf(
		"Full text is available for %d/%d source(s); high-depth research requires %d. Direct acquisition candidates: pdf=%d, open_access=%d, links=%d, stable_identities=%d.",
		stats.FullText,
		stats.Total,
		requiredFullText,
		stats.PDFCandidates,
		stats.OpenAccess,
		stats.LinkCandidates,
		stats.StableIdentities,
	)
	var queries []string
	if status == coverageLedgerStatusOpen {
		queries = buildSourceAcquisitionFollowUpQueries(query, stats)
	}
	obligationType := "missing_full_text"
	ownerWorker := string(ResearchWorkerSourceDiversifier)
	severity := "high"
	if status == coverageLedgerStatusResolved {
		severity = "low"
	}
	if stats.FullText == 0 && stats.PDFCandidates == 0 && stats.OpenAccess == 0 {
		severity = "critical"
	}
	return []CoverageLedgerEntry{{
		ID:                stableWisDevID("source-acquisition", query, fmt.Sprintf("%d", stats.Total), fmt.Sprintf("%d", stats.FullText), fmt.Sprintf("%d", stats.PDFCandidates), fmt.Sprintf("%d", stats.OpenAccess)),
		Category:          "source_acquisition",
		Status:            status,
		Title:             title,
		Description:       description,
		SupportingQueries: queries,
		SourceFamilies:    buildSourceAcquisitionFamilies(stats),
		Confidence:        confidence,
		Required:          true,
		Priority:          priority,
		ObligationType:    obligationType,
		OwnerWorker:       ownerWorker,
		Severity:          severity,
	}}
}

func analyzeSourceAcquisition(papers []internalsearch.Paper) sourceAcquisitionStats {
	stats := sourceAcquisitionStats{
		Total:            len(papers),
		ProviderFamilies: make(map[string]struct{}, len(papers)+6),
		SourceIdentities: make([]string, 0, len(papers)),
	}
	for _, paper := range papers {
		if provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(paper.Source, strings.Join(paper.SourceApis, " ")))); provider != "" {
			stats.ProviderFamilies[provider] = struct{}{}
		}
		if strings.TrimSpace(paper.FullText) != "" {
			stats.FullText++
			stats.ProviderFamilies["full_text_available"] = struct{}{}
		}
		hasPDF := strings.TrimSpace(paper.PdfUrl) != ""
		hasOA := strings.TrimSpace(paper.OpenAccessUrl) != ""
		if hasPDF {
			stats.PDFCandidates++
			stats.ProviderFamilies["pdf_url_candidate"] = struct{}{}
		}
		if hasOA {
			stats.OpenAccess++
			stats.ProviderFamilies["open_access_candidate"] = struct{}{}
		}
		if strings.TrimSpace(paper.Link) != "" {
			stats.LinkCandidates++
			stats.ProviderFamilies["landing_page_candidate"] = struct{}{}
		}
		if identity := sourceAcquisitionPaperIdentity(paper); identity != "" {
			stats.StableIdentities++
			stats.SourceIdentities = append(stats.SourceIdentities, identity)
			if hasPDF || hasOA {
				stats.DirectCandidateID = append(stats.DirectCandidateID, identity)
			}
		}
	}
	stats.SourceIdentities = dedupeTrimmedStrings(stats.SourceIdentities)
	stats.DirectCandidateID = dedupeTrimmedStrings(stats.DirectCandidateID)
	return stats
}

func sourceAcquisitionPaperIdentity(paper internalsearch.Paper) string {
	return strings.TrimSpace(firstNonEmpty(
		paper.DOI,
		paper.ArxivID,
		paper.ID,
		paper.OpenAccessUrl,
		paper.PdfUrl,
		paper.Link,
		paper.Title,
	))
}

func requiredFullTextSourceCount(total int) int {
	switch {
	case total <= 0:
		return 0
	case total >= 8:
		return 3
	case total >= 3:
		return 2
	default:
		return 1
	}
}

func buildSourceAcquisitionFollowUpQueries(query string, stats sourceAcquisitionStats) []string {
	base := strings.TrimSpace(firstNonEmpty(query, "research question"))
	queries := []string{
		strings.TrimSpace(base + " open access PDF full text"),
	}
	if stats.PDFCandidates > 0 || stats.OpenAccess > 0 {
		queries = append(queries, strings.TrimSpace(base+" full text PDF extraction source acquisition"))
	}
	if stats.StableIdentities > 0 {
		queries = append(queries, strings.TrimSpace(base+" DOI arXiv PubMed OpenAlex full text"))
	}
	if stats.PDFCandidates == 0 && stats.OpenAccess == 0 {
		queries = append(queries, strings.TrimSpace(base+" Unpaywall open access full text PDF"))
	}
	return dedupeTrimmedStrings(queries)
}

func buildSourceAcquisitionFamilies(stats sourceAcquisitionStats) []string {
	families := make(map[string]struct{}, len(stats.ProviderFamilies)+len(stats.DirectCandidateID)+2)
	for family := range stats.ProviderFamilies {
		if trimmed := strings.ToLower(strings.TrimSpace(family)); trimmed != "" {
			families[trimmed] = struct{}{}
		}
	}
	if stats.FullText > 0 {
		families["full_text_present"] = struct{}{}
	}
	if stats.FullText < requiredFullTextSourceCount(stats.Total) {
		families["full_text_missing"] = struct{}{}
	}
	for _, identity := range stats.DirectCandidateID {
		if trimmed := strings.ToLower(strings.TrimSpace(identity)); trimmed != "" {
			families["candidate:"+trimmed] = struct{}{}
		}
	}
	return sortedMapKeys(families)
}

func mergeClaimEvidenceLedger(gap *LoopGapState, query string, findings []EvidenceFinding) *LoopGapState {
	if gap == nil {
		gap = &LoopGapState{}
	}
	claimEntries := buildClaimEvidenceCoverageLedger(query, findings)
	gap.Ledger = mergeCoverageLedgerEntries(gap.Ledger, claimEntries)
	if len(findings) > 0 {
		gap.ObservedEvidenceCount = MaxInt(gap.ObservedEvidenceCount, len(findings))
	}
	openClaimGaps := 0
	for _, entry := range claimEntries {
		if strings.EqualFold(entry.Status, coverageLedgerStatusOpen) {
			openClaimGaps++
		}
	}
	if openClaimGaps > 0 {
		gap.Sufficient = false
		gap.MissingAspects = dedupeTrimmedStrings(append(gap.MissingAspects, fmt.Sprintf("%d claim-level evidence gap(s) remain open", openClaimGaps)))
		gap.NextQueries = dedupeTrimmedStrings(append(buildFollowUpQueriesFromLedger(query, claimEntries, 3), gap.NextQueries...))
	}
	return gap
}

func mergeHypothesisBranchLedger(gap *LoopGapState, query string, hypotheses []Hypothesis) *LoopGapState {
	if gap == nil {
		gap = &LoopGapState{}
	}
	branchEntries := buildHypothesisBranchCoverageLedger(query, hypotheses)
	if len(branchEntries) == 0 {
		return gap
	}
	gap.Ledger = mergeCoverageLedgerEntries(gap.Ledger, branchEntries)
	openBranches := 0
	for _, entry := range branchEntries {
		if strings.EqualFold(entry.Status, coverageLedgerStatusOpen) {
			openBranches++
		}
	}
	if openBranches > 0 {
		gap.Sufficient = false
		gap.MissingAspects = dedupeTrimmedStrings(append(gap.MissingAspects, fmt.Sprintf("%d hypothesis branch coverage gap(s) remain open", openBranches)))
		gap.NextQueries = dedupeTrimmedStrings(append(buildFollowUpQueriesFromLedger(query, branchEntries, maxInt(3, minInt(6, openBranches+2))), gap.NextQueries...))
	}
	return gap
}

func buildHypothesisBranchCoverageLedger(query string, hypotheses []Hypothesis) []CoverageLedgerEntry {
	if len(hypotheses) == 0 {
		return nil
	}
	entries := make([]CoverageLedgerEntry, 0, len(hypotheses))
	for idx, hypothesis := range hypotheses {
		claim := strings.TrimSpace(firstNonEmpty(hypothesis.Claim, hypothesis.Text, hypothesis.Query))
		if claim == "" {
			continue
		}
		sourceIDs := hypothesisEvidenceSourceIDs(hypothesis)
		confidence := ClampFloat(firstNonEmptyFloat(hypothesis.ConfidenceScore, hypothesis.ConfidenceThreshold, averageLoopEvidenceConfidence(hypothesis.Evidence, 0.5)), 0.25, 0.99)
		status := coverageLedgerStatusResolved
		title := "Hypothesis branch grounded: " + trimEvidenceText(claim, 110)
		description := fmt.Sprintf("Branch has %d grounded evidence item(s), %d contradiction(s), and confidence %.2f.", len(hypothesis.Evidence), hypothesis.ContradictionCount, confidence)
		requiredQuery := buildHypothesisBranchFollowUpQuery(query, claim, hypothesis)
		if hypothesis.IsTerminated || len(hypothesis.Evidence) == 0 || hypothesis.ContradictionCount > 0 || confidence < 0.55 {
			status = coverageLedgerStatusOpen
			title = "Hypothesis branch needs resolution: " + trimEvidenceText(claim, 95)
			description = fmt.Sprintf("Branch requires follow-up because terminated=%v, evidence=%d, contradictions=%d, confidence=%.2f.", hypothesis.IsTerminated, len(hypothesis.Evidence), hypothesis.ContradictionCount, confidence)
		}
		entry := CoverageLedgerEntry{
			ID:                stableWisDevID("hypothesis-branch-ledger", query, claim, fmt.Sprintf("%d", idx)),
			Category:          "hypothesis_branch",
			Status:            status,
			Title:             title,
			Description:       description,
			SupportingQueries: dedupeTrimmedStrings([]string{requiredQuery}),
			SourceFamilies:    sourceIDs,
			Confidence:        confidence,
			Required:          true,
			Priority:          hypothesisBranchPriority(hypothesis, confidence),
		}
		entry.ObligationType = inferCoverageObligationType(entry)
		switch {
		case hypothesis.ContradictionCount > 0 && status == coverageLedgerStatusOpen:
			entry.ObligationType = "missing_counter_evidence"
		case len(hypothesis.Evidence) == 0 && status == coverageLedgerStatusOpen:
			entry.ObligationType = "missing_full_text"
		case hypothesis.IsTerminated && status == coverageLedgerStatusOpen:
			entry.ObligationType = "missing_replication"
		case strings.EqualFold(status, coverageLedgerStatusOpen) && entry.ObligationType == "coverage_gap":
			entry.ObligationType = "unverified_claim"
		}
		switch entry.ObligationType {
		case "missing_counter_evidence", "missing_replication":
			entry.OwnerWorker = string(ResearchWorkerContradictionCritic)
		case "missing_citation_identity":
			entry.OwnerWorker = string(ResearchWorkerCitationGraph)
		case "missing_source_diversity", "missing_full_text":
			entry.OwnerWorker = string(ResearchWorkerSourceDiversifier)
		case "unverified_claim":
			entry.OwnerWorker = string(ResearchWorkerIndependentVerifier)
		default:
			entry.OwnerWorker = string(ResearchWorkerScout)
		}
		entry.Severity = inferCoverageObligationSeverity(entry)
		if entry.Status == coverageLedgerStatusOpen && entry.Severity == "low" {
			entry.Severity = "medium"
		}
		entries = append(entries, entry)
	}
	return entries
}

func hypothesisEvidenceSourceIDs(hypothesis Hypothesis) []string {
	if len(hypothesis.Evidence) == 0 {
		return nil
	}
	ids := make([]string, 0, len(hypothesis.Evidence))
	for _, finding := range hypothesis.Evidence {
		if finding == nil {
			continue
		}
		ids = append(ids, strings.TrimSpace(firstNonEmpty(finding.SourceID, finding.PaperTitle)))
	}
	return dedupeTrimmedStrings(ids)
}

func buildHypothesisBranchFollowUpQuery(query string, claim string, hypothesis Hypothesis) string {
	terms := []string{strings.TrimSpace(query), strings.TrimSpace(claim)}
	if strings.TrimSpace(hypothesis.FalsifiabilityCondition) != "" {
		terms = append(terms, strings.TrimSpace(hypothesis.FalsifiabilityCondition))
	}
	if hypothesis.ContradictionCount > 0 {
		terms = append(terms, "contradictory evidence replication")
	} else if len(hypothesis.Evidence) == 0 {
		terms = append(terms, "primary evidence support")
	} else {
		terms = append(terms, "independent corroborating evidence")
	}
	return strings.Join(dedupeTrimmedStrings(terms), " ")
}

func hypothesisBranchPriority(hypothesis Hypothesis, confidence float64) int {
	priority := 70
	if hypothesis.ContradictionCount > 0 {
		priority += 20
	}
	if len(hypothesis.Evidence) == 0 {
		priority += 15
	}
	if confidence < 0.55 {
		priority += 10
	}
	if hypothesis.IsTerminated {
		priority += 8
	}
	return minInt(priority, 100)
}

func buildClaimEvidenceCoverageLedger(query string, findings []EvidenceFinding) []CoverageLedgerEntry {
	findings = dedupeEvidenceFindings(findings)
	if len(findings) == 0 {
		return []CoverageLedgerEntry{{
			ID:                stableWisDevID("claim-ledger-empty", query),
			Category:          "claim_evidence",
			Status:            coverageLedgerStatusOpen,
			Title:             "No claim-level evidence extracted",
			Description:       "The evidence pass did not extract grounded claims, so the research loop must reopen retrieval or evidence assembly before final synthesis is considered covered.",
			SupportingQueries: []string{strings.TrimSpace(firstNonEmpty(query, "claim evidence grounding"))},
			Confidence:        0.35,
			Required:          true,
			ObligationType:    "unverified_claim",
			OwnerWorker:       string(ResearchWorkerIndependentVerifier),
			Severity:          "critical",
		}}
	}
	entries := make([]CoverageLedgerEntry, 0, len(findings)+1)
	sourceSet := make(map[string]struct{}, len(findings))
	for idx, finding := range findings {
		claim := strings.TrimSpace(firstNonEmpty(finding.Claim, finding.Snippet))
		if claim == "" {
			continue
		}
		sourceID := strings.TrimSpace(firstNonEmpty(finding.SourceID, finding.PaperTitle))
		if sourceID != "" {
			sourceSet[strings.ToLower(sourceID)] = struct{}{}
		}
		status := coverageLedgerStatusResolved
		title := "Claim grounded: " + trimEvidenceText(claim, 120)
		confidence := ClampFloat(defaultPacketConfidence(finding.Confidence), 0.25, 0.99)
		description := strings.TrimSpace(firstNonEmpty(finding.Snippet, claim))
		if sourceID == "" || strings.TrimSpace(finding.Snippet) == "" || confidence < 0.55 {
			status = coverageLedgerStatusOpen
			title = "Claim needs stronger grounding: " + trimEvidenceText(claim, 100)
		}
		severity := "low"
		if status == coverageLedgerStatusOpen {
			severity = "high"
		}
		entries = append(entries, CoverageLedgerEntry{
			ID:                stableWisDevID("claim-ledger", query, sourceID, claim, fmt.Sprintf("%d", idx)),
			Category:          "claim_evidence",
			Status:            status,
			Title:             title,
			Description:       description,
			SupportingQueries: []string{strings.TrimSpace(firstNonEmpty(query, claim))},
			SourceFamilies:    dedupeTrimmedStrings([]string{sourceID}),
			Confidence:        confidence,
			ObligationType:    "unverified_claim",
			OwnerWorker:       string(ResearchWorkerIndependentVerifier),
			Severity:          severity,
			Required:          true,
		})
	}
	if len(sourceSet) < 2 && len(findings) > 0 {
		entries = append(entries, CoverageLedgerEntry{
			ID:                stableWisDevID("claim-ledger-source-diversity", query, fmt.Sprintf("%d", len(sourceSet))),
			Category:          "claim_source_diversity",
			Status:            coverageLedgerStatusOpen,
			Title:             "Claim set needs independent source diversity",
			Description:       "The accepted claim set is not yet triangulated across at least two independent source identities.",
			SupportingQueries: []string{strings.TrimSpace(query + " independent replication corroborating evidence")},
			Confidence:        0.58,
			Required:          true,
			ObligationType:    "missing_source_diversity",
			OwnerWorker:       string(ResearchWorkerSourceDiversifier),
			Severity:          "medium",
		})
	}
	return entries
}

func buildQuestCoverageLedger(quest *ResearchQuest, papers []Source, verdict CitationVerdict, critique string) []CoverageLedgerEntry {
	sourceFamilies := buildObservedSourceFamiliesFromSources(papers)
	entries := make([]CoverageLedgerEntry, 0, 8)
	appendEntry := func(category string, status string, title string, description string, queries []string, confidence float64, obligationType string, owner string, severity string) {
		entry := CoverageLedgerEntry{
			ID:                stableWisDevID("quest-ledger", quest.QuestID, category, title, fmt.Sprintf("%d", len(entries)+1)),
			Category:          strings.TrimSpace(category),
			Status:            strings.TrimSpace(firstNonEmpty(status, coverageLedgerStatusOpen)),
			Title:             strings.TrimSpace(title),
			Description:       strings.TrimSpace(description),
			SupportingQueries: dedupeTrimmedStrings(append([]string(nil), queries...)),
			SourceFamilies:    append([]string(nil), sourceFamilies...),
			Confidence:        ClampFloat(confidence, 0.25, 0.99),
			Required:          true,
		}
		entry.ObligationType = strings.TrimSpace(firstNonEmpty(obligationType, inferCoverageObligationType(entry)))
		switch {
		case strings.TrimSpace(owner) != "":
			entry.OwnerWorker = strings.TrimSpace(owner)
		case entry.OwnerWorker == "":
			entry.OwnerWorker = inferCoverageObligationOwner(entry)
		}
		entry.Severity = strings.TrimSpace(firstNonEmpty(severity, inferCoverageObligationSeverity(entry)))
		if strings.EqualFold(entry.Status, coverageLedgerStatusOpen) && entry.Severity == "low" {
			entry.Severity = "medium"
		}
		entries = append(entries, entry)
	}

	if len(quest.AcceptedClaims) == 0 {
		appendEntry(
			"coverage",
			coverageLedgerStatusOpen,
			"No accepted evidence claims",
			"Reasoning did not produce grounded claims that can support the final answer.",
			[]string{strings.TrimSpace(quest.Query)},
			0.55,
			"unverified_claim",
			string(ResearchWorkerIndependentVerifier),
			"high",
		)
	}
	if len(papers) < 2 {
		appendEntry(
			"coverage",
			coverageLedgerStatusOpen,
			"Evidence breadth is shallow",
			"Fewer than two grounded sources were available after retrieval.",
			[]string{strings.TrimSpace(quest.Query)},
			0.6,
			"missing_full_text",
			string(ResearchWorkerSourceDiversifier),
			"high",
		)
	}
	if len(sourceFamilies) < 2 && len(papers) > 0 {
		appendEntry(
			"source_diversity",
			coverageLedgerStatusOpen,
			"Source diversity is narrow",
			"Independent source families are still missing from the quest evidence set.",
			[]string{strings.TrimSpace(quest.Query)},
			0.62,
			"missing_source_diversity",
			string(ResearchWorkerSourceDiversifier),
			"medium",
		)
	}
	for _, issue := range verdict.BlockingIssues {
		appendEntry(
			"citation_gate",
			coverageLedgerStatusOpen,
			"Citation issue",
			strings.TrimSpace(issue),
			[]string{strings.TrimSpace(quest.Query)},
			0.7,
			"missing_citation_identity",
			string(ResearchWorkerCitationGraph),
			"high",
		)
	}
	if strings.TrimSpace(verdict.Status) != "" && !verdict.Promoted {
		appendEntry(
			"citation_gate",
			coverageLedgerStatusOpen,
			"Citation promotion blocked",
			firstNonEmpty(verdict.ConflictNote, "Citation metadata did not meet the multi-source promotion gate."),
			[]string{strings.TrimSpace(quest.Query + " DOI arXiv OpenAlex Semantic Scholar citation metadata")},
			0.74,
			"missing_citation_identity",
			string(ResearchWorkerCitationGraph),
			"critical",
		)
	}
	openCountBeforeCritique := len(entries)
	loweredCritique := strings.ToLower(strings.TrimSpace(critique))
	for _, trigger := range []string{"insufficient", "more evidence", "weak evidence", "unclear", "contradiction", "unresolved"} {
		if openCountBeforeCritique > 0 && strings.Contains(loweredCritique, trigger) {
			appendEntry(
				"critique",
				coverageLedgerStatusOpen,
				"Critique requested more evidence",
				strings.TrimSpace(critique),
				[]string{strings.TrimSpace(quest.Query)},
				0.68,
				"unverified_claim",
				string(ResearchWorkerIndependentVerifier),
				"high",
			)
			break
		}
	}

	if len(entries) == 0 {
		appendEntry(
			"coverage",
			coverageLedgerStatusResolved,
			"Quest evidence passed default hardening checks",
			firstNonEmpty(strings.TrimSpace(critique), "Coverage breadth, source diversity, and citation checks passed."),
			[]string{strings.TrimSpace(quest.Query)},
			0.85,
			"coverage_gap",
			string(ResearchWorkerScout),
			"low",
		)
	}
	return entries
}

func buildFollowUpQueriesFromLedger(query string, ledger []CoverageLedgerEntry, limit int) []string {
	if limit <= 0 {
		limit = 3
	}
	candidates := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, trimmed)
	}

	baseQuery := strings.TrimSpace(query)
	openEntries := make([]CoverageLedgerEntry, 0, len(ledger))
	for _, entry := range ledger {
		if entry.Status != coverageLedgerStatusOpen {
			continue
		}
		openEntries = append(openEntries, entry)
	}

	// Always emit ledger-authored follow-up queries first so explicit recovery
	// guidance is not displaced by derived fallback phrasing under tight limits.
	for _, entry := range openEntries {
		for _, supportingQuery := range entry.SupportingQueries {
			add(supportingQuery)
			if len(candidates) >= limit {
				return candidates[:limit]
			}
		}
	}

	sort.SliceStable(openEntries, func(i, j int) bool {
		return ledgerInformationGainScore(openEntries[i]) > ledgerInformationGainScore(openEntries[j])
	})
	for _, entry := range openEntries {
		switch entry.Category {
		case "coverage_rubric":
			if len(entry.SupportingQueries) == 0 && strings.TrimSpace(entry.Title) != "" {
				add(strings.TrimSpace(baseQuery + " " + summarizeLoopGapTerms(entry.Title)))
			}
		case "citation_integrity":
			add(strings.TrimSpace(baseQuery + " DOI arXiv OpenAlex Semantic Scholar citation metadata"))
		case "source_diversity":
			add(strings.TrimSpace(baseQuery + " independent replication systematic review"))
		case "source_acquisition":
			add(strings.TrimSpace(baseQuery + " open access PDF full text source acquisition"))
		case "citation_gate":
			add(strings.TrimSpace(baseQuery + " DOI citation metadata"))
		default:
			if coverageLedgerEntryIsGenericValidationCheckpoint(entry) {
				add(strings.TrimSpace(baseQuery + " independent corroborating evidence"))
				break
			}
			if gapTerms := summarizeLoopGapTerms(firstNonEmpty(entry.Description, entry.Title)); gapTerms != "" {
				add(strings.TrimSpace(baseQuery + " " + gapTerms))
			}
		}
		if len(candidates) >= limit {
			return candidates[:limit]
		}
	}
	return candidates
}

func ledgerInformationGainScore(entry CoverageLedgerEntry) float64 {
	score := ClampFloat(1-entry.Confidence, 0.05, 0.95)
	switch strings.ToLower(strings.TrimSpace(entry.Category)) {
	case "coverage_rubric":
		score += 0.50
	case "source_acquisition":
		score += 0.49
	case "citation_integrity":
		score += 0.46
	case "contradiction":
		score += 0.45
	case "hypothesis_branch":
		score += 0.42
	case "claim_evidence":
		score += 0.38
	case "claim_source_diversity", "source_diversity":
		score += 0.34
	case "citation_gate":
		score += 0.30
	case "query_coverage", "planned_query":
		score += 0.22
	default:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(entry.Category)), "worker_") {
			score += 0.18
		}
	}
	if len(entry.SupportingQueries) > 0 {
		score += 0.10
	}
	if len(entry.SourceFamilies) == 0 {
		score += 0.08
	}
	return score
}

func coverageLedgerEntryIsGenericValidationCheckpoint(entry CoverageLedgerEntry) bool {
	return strings.EqualFold(strings.TrimSpace(entry.Category), "coverage") &&
		strings.Contains(strings.ToLower(strings.TrimSpace(entry.Title)), "structured sufficiency checkpoint unavailable")
}

func coverageLedgerEntriesToAny(entries []CoverageLedgerEntry) []any {
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, map[string]any{
			"id":                entry.ID,
			"category":          entry.Category,
			"status":            entry.Status,
			"title":             entry.Title,
			"description":       entry.Description,
			"supportingQueries": stringSliceToAny(entry.SupportingQueries),
			"sourceFamilies":    stringSliceToAny(entry.SourceFamilies),
			"confidence":        entry.Confidence,
			"required":          entry.Required,
			"priority":          entry.Priority,
		})
	}
	return out
}

func encodeQuestFollowUpQueries(queries []string) string {
	return strings.Join(dedupeTrimmedStrings(append([]string(nil), queries...)), "\n")
}

func decodeQuestFollowUpQueries(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return dedupeTrimmedStrings(strings.Split(raw, "\n"))
}

func mergeQuestSources(existing []Source, incoming []Source) []Source {
	if len(incoming) == 0 {
		return append([]Source(nil), existing...)
	}
	merged := append([]Source(nil), existing...)
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	for _, source := range existing {
		if key := questSourceDedupKey(source); key != "" {
			seen[key] = struct{}{}
		}
	}
	for _, source := range incoming {
		key := questSourceDedupKey(source)
		if key != "" {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
		}
		merged = append(merged, source)
	}
	return merged
}

func questHasOpenCoverageLedger(ledger []CoverageLedgerEntry) bool {
	for _, entry := range ledger {
		if entry.Status == coverageLedgerStatusOpen {
			return true
		}
	}
	return false
}

func questCritiqueRequestedFollowUp(ledger []CoverageLedgerEntry) bool {
	for _, entry := range ledger {
		if entry.Status != coverageLedgerStatusOpen {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(entry.Category), "critique") {
			return true
		}
	}
	return false
}

func extractEvidenceClaimSeeds(source Source, limit int) []evidenceClaimSeed {
	if limit <= 0 {
		limit = 3
	}
	seeds := make([]evidenceClaimSeed, 0, limit)
	seen := make(map[string]struct{}, limit+2)
	add := func(text string, confidence float64, section string) {
		trimmed := trimEvidenceText(text, 320)
		if trimmed == "" {
			return
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		seeds = append(seeds, evidenceClaimSeed{
			claim:      trimmed,
			snippet:    trimmed,
			section:    section,
			confidence: ClampFloat(confidence, 0.35, 0.99),
		})
	}

	if summary := strings.TrimSpace(firstNonEmpty(source.Summary, source.Abstract)); summary != "" {
		add(firstEvidenceSentence(summary), 0.92, "summary")
	}
	for _, sentence := range splitEvidenceSentences(source.Abstract, 2) {
		add(sentence, 0.84, "abstract")
	}
	for _, sentence := range splitEvidenceSentences(source.FullText, 2) {
		add(sentence, 0.72, "full_text")
	}
	for _, snippet := range extractStructureMapSnippets(source.StructureMap, 2) {
		add(snippet, 0.76, "structure")
	}
	if len(seeds) == 0 && strings.TrimSpace(source.Title) != "" {
		add(source.Title, 0.45, "title")
	}
	if len(seeds) > limit {
		seeds = seeds[:limit]
	}
	return seeds
}

func extractStructureMapSnippets(items []any, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	out := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		text := trimEvidenceText(firstNonEmpty(
			optionalAnyString(item["summary"]),
			optionalAnyString(item["caption"]),
			optionalAnyString(item["text"]),
			optionalAnyString(item["title"]),
			optionalAnyString(item["label"]),
		), 320)
		if text == "" {
			continue
		}
		key := strings.ToLower(text)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, text)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func splitEvidenceSentences(text string, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	normalized := trimEvidenceText(text, 1200)
	if normalized == "" {
		return nil
	}
	normalized = strings.NewReplacer("!", ".", "?", ".", ";", ".", "\n", " ").Replace(normalized)
	rawParts := strings.Split(normalized, ".")
	out := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, rawPart := range rawParts {
		part := trimEvidenceText(rawPart, 320)
		if len(part) < 24 {
			continue
		}
		key := strings.ToLower(part)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, part)
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return []string{trimEvidenceText(normalized, 320)}
	}
	return out
}

func firstEvidenceSentence(text string) string {
	sentences := splitEvidenceSentences(text, 1)
	if len(sentences) == 0 {
		return trimEvidenceText(text, 320)
	}
	return sentences[0]
}

func trimEvidenceText(text string, limit int) string {
	trimmed := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if trimmed == "" {
		return ""
	}
	if limit > 0 && len(trimmed) > limit {
		return strings.TrimSpace(trimmed[:limit]) + "..."
	}
	return trimmed
}

func optionalAnyString(value any) string {
	trimmed := strings.TrimSpace(fmt.Sprintf("%v", value))
	if trimmed == "" || trimmed == "<nil>" {
		return ""
	}
	return trimmed
}

func questSourceDedupKey(source Source) string {
	for _, candidate := range []string{source.DOI, source.ArxivID, source.ID, source.Link, source.Title} {
		if trimmed := strings.ToLower(strings.TrimSpace(candidate)); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonEmptyScore(value float64, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}
