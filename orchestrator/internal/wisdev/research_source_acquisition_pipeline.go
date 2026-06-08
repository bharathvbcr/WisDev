package wisdev

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

const (
	sourceAcquisitionStatusPlanned   = "planned"
	sourceAcquisitionStatusSucceeded = "succeeded"
	sourceAcquisitionStatusFailed    = "failed"
	sourceAcquisitionStatusSkipped   = "skipped"
)

type ResearchSourceAcquisitionPlan struct {
	Query                     string                             `json:"query"`
	SourceCount               int                                `json:"sourceCount"`
	Attempts                  []ResearchSourceAcquisitionAttempt `json:"attempts,omitempty"`
	NormalizedSourceIDs       []string                           `json:"normalizedSourceIds,omitempty"`
	OpenFailureStates         []ResearchSourceAcquisitionFailure `json:"openFailureStates,omitempty"`
	RequiredPythonExtractions int                                `json:"requiredPythonExtractions,omitempty"`
	BrowserFetches            int                                `json:"browserFetches,omitempty"`
	FetchFailures             int                                `json:"fetchFailures,omitempty"`
	CoverageLedger            []CoverageLedgerEntry              `json:"coverageLedger,omitempty"`
}

type ResearchSourceAcquisitionAttempt struct {
	SourceID              string            `json:"sourceId,omitempty"`
	CanonicalID           string            `json:"canonicalId,omitempty"`
	Title                 string            `json:"title,omitempty"`
	SourceFamily          string            `json:"sourceFamily,omitempty"`
	SourceType            string            `json:"sourceType"`
	FetchURL              string            `json:"fetchUrl,omitempty"`
	Status                string            `json:"status"`
	ErrorCode             string            `json:"errorCode,omitempty"`
	FailureReason         string            `json:"failureReason,omitempty"`
	WorkerPlane           string            `json:"workerPlane"`
	FullTextAvailable     bool              `json:"fullTextAvailable,omitempty"`
	PDFCandidate          bool              `json:"pdfCandidate,omitempty"`
	NeedsPythonExtraction bool              `json:"needsPythonExtraction,omitempty"`
	IdentityFields        map[string]string `json:"identityFields,omitempty"`
}

type ResearchSourceAcquisitionFailure struct {
	SourceID    string `json:"sourceId,omitempty"`
	CanonicalID string `json:"canonicalId,omitempty"`
	Stage       string `json:"stage"`
	ErrorCode   string `json:"errorCode"`
	Reason      string `json:"reason"`
}

func buildResearchSourceAcquisitionPlan(query string, papers []search.Paper) *ResearchSourceAcquisitionPlan {
	plan := &ResearchSourceAcquisitionPlan{
		Query:       strings.TrimSpace(query),
		SourceCount: len(papers),
	}
	seenAttempts := map[string]struct{}{}
	seenIDs := map[string]struct{}{}
	persistentIDCount := 0
	for idx, paper := range papers {
		canonicalID, identityType := normalizedSourceIdentity(paper)
		sourceID := strings.TrimSpace(firstNonEmpty(paper.ID, paper.DOI, paper.ArxivID, paper.Link, paper.Title, fmt.Sprintf("source_%d", idx+1)))
		if canonicalID == "" {
			canonicalID = stableWisDevID("source", sourceID)
		}
		if _, ok := seenIDs[canonicalID]; !ok {
			seenIDs[canonicalID] = struct{}{}
			plan.NormalizedSourceIDs = append(plan.NormalizedSourceIDs, canonicalID)
		}
		if identityType == "doi" || identityType == "arxiv" || identityType == "pubmed" || identityType == "pmc" {
			persistentIDCount++
		}
		fullTextAvailable := strings.TrimSpace(paper.FullText) != ""
		base := ResearchSourceAcquisitionAttempt{
			SourceID:          sourceID,
			CanonicalID:       canonicalID,
			Title:             strings.TrimSpace(paper.Title),
			SourceFamily:      sourceFamilyForPaper(paper),
			Status:            sourceAcquisitionStatusPlanned,
			WorkerPlane:       "go_fetch",
			FullTextAvailable: fullTextAvailable,
			IdentityFields:    sourceIdentityFields(paper),
		}
		if fullTextAvailable {
			attempt := base
			attempt.SourceType = "full_text"
			attempt.Status = sourceAcquisitionStatusSucceeded
			appendSourceAcquisitionAttempt(plan, attempt, seenAttempts)
		}
		if doi := doiForPaper(paper); doi != "" {
			attempt := base
			attempt.SourceType = "doi"
			attempt.FetchURL = "https://doi.org/" + doi
			appendSourceAcquisitionAttempt(plan, attempt, seenAttempts)
		}
		if arxivID := normalizeArxivID(firstNonEmpty(paper.ArxivID, paper.ID, paper.Link)); arxivID != "" {
			attempt := base
			attempt.SourceType = "arxiv"
			attempt.FetchURL = "https://arxiv.org/abs/" + arxivID
			appendSourceAcquisitionAttempt(plan, attempt, seenAttempts)

			pdfAttempt := base
			pdfAttempt.SourceType = "pdf"
			pdfAttempt.FetchURL = "https://arxiv.org/pdf/" + arxivID + ".pdf"
			pdfAttempt.WorkerPlane = "python_docling"
			pdfAttempt.PDFCandidate = true
			pdfAttempt.NeedsPythonExtraction = !fullTextAvailable
			appendSourceAcquisitionAttempt(plan, pdfAttempt, seenAttempts)
		}
		if pmid := pubMedIDForPaper(paper); pmid != "" {
			attempt := base
			attempt.SourceType = "pubmed"
			attempt.FetchURL = "https://pubmed.ncbi.nlm.nih.gov/" + pmid + "/"
			appendSourceAcquisitionAttempt(plan, attempt, seenAttempts)
		}
		if pmcID := normalizePMCID(firstNonEmpty(paper.ID, paper.Link)); pmcID != "" {
			attempt := base
			attempt.SourceType = "pmc"
			attempt.FetchURL = "https://www.ncbi.nlm.nih.gov/pmc/articles/" + pmcID + "/"
			appendSourceAcquisitionAttempt(plan, attempt, seenAttempts)
		}
		if pdfURL := strings.TrimSpace(paper.PdfUrl); pdfURL != "" {
			attempt := base
			attempt.SourceType = "pdf"
			attempt.FetchURL = pdfURL
			attempt.WorkerPlane = "python_docling"
			attempt.PDFCandidate = true
			attempt.NeedsPythonExtraction = !fullTextAvailable
			appendSourceAcquisitionAttempt(plan, attempt, seenAttempts)
		}
		for _, webURL := range dedupeTrimmedStrings([]string{paper.OpenAccessUrl, paper.Link}) {
			if webURL == "" || strings.EqualFold(webURL, paper.PdfUrl) {
				continue
			}
			attempt := base
			attempt.SourceType = "web_fetch"
			attempt.FetchURL = webURL
			attempt.WorkerPlane = "go_browser_fetch"
			appendSourceAcquisitionAttempt(plan, attempt, seenAttempts)
		}
		if !paperHasFetchCandidate(paper) && !fullTextAvailable {
			plan.FetchFailures++
			failure := ResearchSourceAcquisitionFailure{
				SourceID:    sourceID,
				CanonicalID: canonicalID,
				Stage:       "source_fetch",
				ErrorCode:   "missing_fetch_candidate",
				Reason:      "source has no DOI, arXiv, PubMed, PMC, PDF, open-access URL, or landing page",
			}
			plan.OpenFailureStates = append(plan.OpenFailureStates, failure)
			appendSourceAcquisitionAttempt(plan, ResearchSourceAcquisitionAttempt{
				SourceID:       sourceID,
				CanonicalID:    canonicalID,
				Title:          strings.TrimSpace(paper.Title),
				SourceFamily:   sourceFamilyForPaper(paper),
				SourceType:     "source_fetch",
				Status:         sourceAcquisitionStatusFailed,
				ErrorCode:      failure.ErrorCode,
				FailureReason:  failure.Reason,
				WorkerPlane:    "go_fetch",
				IdentityFields: sourceIdentityFields(paper),
			}, seenAttempts)
		}
	}
	for _, attempt := range plan.Attempts {
		if attempt.NeedsPythonExtraction {
			plan.RequiredPythonExtractions++
		}
		if attempt.SourceType == "web_fetch" {
			plan.BrowserFetches++
		}
	}
	plan.CoverageLedger = buildSourceAcquisitionCoverageLedger(query, plan, persistentIDCount)
	return plan
}

func appendSourceAcquisitionAttempt(plan *ResearchSourceAcquisitionPlan, attempt ResearchSourceAcquisitionAttempt, seen map[string]struct{}) {
	if plan == nil {
		return
	}
	key := strings.ToLower(strings.Join([]string{
		strings.TrimSpace(attempt.CanonicalID),
		strings.TrimSpace(attempt.SourceType),
		strings.TrimSpace(attempt.FetchURL),
		strings.TrimSpace(attempt.ErrorCode),
	}, "|"))
	if key == "|||" {
		key = stableWisDevID("source_attempt", attempt.SourceID, attempt.Title, attempt.SourceType)
	}
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	if attempt.Status == "" {
		attempt.Status = sourceAcquisitionStatusPlanned
	}
	plan.Attempts = append(plan.Attempts, attempt)
}

func buildSourceAcquisitionCoverageLedger(query string, plan *ResearchSourceAcquisitionPlan, persistentIDCount int) []CoverageLedgerEntry {
	if plan == nil {
		return nil
	}
	ledger := []CoverageLedgerEntry{}
	if plan.SourceCount == 0 {
		ledger = append(ledger, CoverageLedgerEntry{
			ID:             stableWisDevID("source_fetch", query, "no_sources"),
			Category:       "source_fetch",
			Status:         coverageLedgerStatusOpen,
			Title:          "Acquire external sources",
			Description:    "No sources are available for fetch, full-text, or citation verification.",
			Required:       true,
			Priority:       3,
			Confidence:     0.82,
			ObligationType: "missing_full_text",
			OwnerWorker:    string(ResearchWorkerSourceDiversifier),
			Severity:       "critical",
		})
		return ledger
	}
	if persistentIDCount == 0 {
		ledger = append(ledger, CoverageLedgerEntry{
			ID:                stableWisDevID("source_identity", query, "persistent_ids"),
			Category:          "source_identity",
			Status:            coverageLedgerStatusOpen,
			Title:             "Normalize persistent source identities",
			Description:       "No DOI, arXiv, PubMed, or PMC identifiers were available for independent citation validation.",
			SupportingQueries: []string{strings.TrimSpace(query) + " DOI arXiv PubMed PMC"},
			Required:          true,
			Priority:          2,
			Confidence:        0.78,
			ObligationType:    "missing_citation_identity",
			OwnerWorker:       string(ResearchWorkerCitationGraph),
			Severity:          "high",
		})
	}
	if plan.RequiredPythonExtractions > 0 {
		ledger = append(ledger, CoverageLedgerEntry{
			ID:                stableWisDevID("full_text_fetch", query, "python_pdf", fmt.Sprintf("%d", plan.RequiredPythonExtractions)),
			Category:          "full_text_fetch",
			Status:            coverageLedgerStatusOpen,
			Title:             "Extract PDF full text through Python worker",
			Description:       "PDF candidates exist but must be extracted through the Python Docling worker before answer-level entailment can rely on full-text spans.",
			SupportingQueries: []string{strings.TrimSpace(query) + " full text PDF"},
			Required:          true,
			Priority:          3,
			Confidence:        0.84,
			ObligationType:    "missing_full_text",
			OwnerWorker:       string(ResearchWorkerSourceDiversifier),
			Severity:          "high",
		})
	}
	if plan.FetchFailures > 0 {
		ledger = append(ledger, CoverageLedgerEntry{
			ID:                stableWisDevID("source_fetch", query, "failures", fmt.Sprintf("%d", plan.FetchFailures)),
			Category:          "source_fetch",
			Status:            coverageLedgerStatusOpen,
			Title:             "Resolve source fetch failures",
			Description:       "At least one source lacks a resolvable fetch target or explicit full-text path.",
			SupportingQueries: []string{strings.TrimSpace(query) + " source identifiers full text"},
			Required:          true,
			Priority:          3,
			Confidence:        0.80,
			ObligationType:    "missing_full_text",
			OwnerWorker:       string(ResearchWorkerSourceDiversifier),
			Severity:          "high",
		})
	}
	if len(ledger) == 0 {
		ledger = append(ledger, CoverageLedgerEntry{
			ID:             stableWisDevID("source_fetch", query, "ready"),
			Category:       "source_fetch",
			Status:         coverageLedgerStatusResolved,
			Title:          "Source acquisition plan ready",
			Description:    "Sources have resolvable identities and fetch targets for citation validation.",
			Required:       false,
			Priority:       1,
			Confidence:     0.86,
			ObligationType: "coverage_gap",
			OwnerWorker:    string(ResearchWorkerScout),
			Severity:       "low",
		})
	}
	return ledger
}

func mergeSourceAcquisitionPlanIntoGap(query string, gap *LoopGapState, plan *ResearchSourceAcquisitionPlan) *LoopGapState {
	if plan == nil {
		return gap
	}
	if gap == nil {
		gap = &LoopGapState{Sufficient: true, Confidence: 0.7}
	}
	gap.Ledger = mergeCoverageLedgerEntries(gap.Ledger, plan.CoverageLedger)
	openCount := countOpenCoverageLedgerEntries(plan.CoverageLedger)
	if openCount > 0 {
		gap.Sufficient = false
		if strings.TrimSpace(gap.Reasoning) == "" {
			gap.Reasoning = "source acquisition obligations remain open"
		}
		gap.Confidence = ClampFloat(minFloatNonZero(gap.Confidence, 0.72), 0, 1)
	}
	if plan.RequiredPythonExtractions > 0 {
		gap.MissingSourceTypes = dedupeTrimmedStrings(append(gap.MissingSourceTypes, "python_pdf_full_text"))
		gap.NextQueries = append(gap.NextQueries, strings.TrimSpace(query)+" full text PDF")
	}
	if plan.FetchFailures > 0 {
		gap.MissingSourceTypes = dedupeTrimmedStrings(append(gap.MissingSourceTypes, "fetchable_source_identity"))
		gap.NextQueries = append(gap.NextQueries, strings.TrimSpace(query)+" DOI arXiv PubMed PMC")
	}
	gap.NextQueries = normalizeLoopQueries("", gap.NextQueries)
	return gap
}

func minFloatNonZero(current float64, fallback float64) float64 {
	if current <= 0 {
		return fallback
	}
	if current < fallback {
		return current
	}
	return fallback
}

func paperHasFetchCandidate(paper search.Paper) bool {
	return doiForPaper(paper) != "" ||
		strings.TrimSpace(paper.ArxivID) != "" ||
		strings.TrimSpace(paper.OpenAccessUrl) != "" ||
		strings.TrimSpace(paper.PdfUrl) != "" ||
		strings.TrimSpace(paper.Link) != "" ||
		pubMedIDForPaper(paper) != "" ||
		normalizePMCID(firstNonEmpty(paper.ID, paper.Link)) != ""
}

func normalizedSourceIdentity(paper search.Paper) (string, string) {
	if doi := doiForPaper(paper); doi != "" {
		return "doi:" + strings.ToLower(doi), "doi"
	}
	if arxivID := normalizeArxivID(firstNonEmpty(paper.ArxivID, paper.ID, paper.Link)); arxivID != "" {
		return "arxiv:" + strings.ToLower(arxivID), "arxiv"
	}
	if pmid := pubMedIDForPaper(paper); pmid != "" {
		return "pmid:" + pmid, "pubmed"
	}
	if pmcID := normalizePMCID(firstNonEmpty(paper.ID, paper.Link)); pmcID != "" {
		return strings.ToLower(pmcID), "pmc"
	}
	if link := normalizeURLIdentity(firstNonEmpty(paper.OpenAccessUrl, paper.PdfUrl, paper.Link)); link != "" {
		return "url:" + stableWisDevID("url", link), "url"
	}
	if title := strings.TrimSpace(paper.Title); title != "" {
		return stableWisDevID("title", strings.ToLower(title)), "title"
	}
	if id := strings.TrimSpace(paper.ID); id != "" {
		return stableWisDevID("source", id), "id"
	}
	return "", ""
}

func sourceIdentityFields(paper search.Paper) map[string]string {
	fields := map[string]string{}
	if value := strings.TrimSpace(paper.ID); value != "" {
		fields["id"] = value
	}
	if value := doiForPaper(paper); value != "" {
		fields["doi"] = value
	}
	if value := normalizeArxivID(firstNonEmpty(paper.ArxivID, paper.ID, paper.Link)); value != "" {
		fields["arxiv"] = value
	}
	if value := pubMedIDForPaper(paper); value != "" {
		fields["pmid"] = value
	}
	if value := normalizePMCID(firstNonEmpty(paper.ID, paper.Link)); value != "" {
		fields["pmcid"] = value
	}
	if value := strings.TrimSpace(paper.Link); value != "" {
		fields["link"] = value
	}
	if value := strings.TrimSpace(paper.PdfUrl); value != "" {
		fields["pdfUrl"] = value
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func sourceFamilyForPaper(paper search.Paper) string {
	if source := strings.TrimSpace(paper.Source); source != "" {
		return strings.ToLower(source)
	}
	if len(paper.SourceApis) > 0 {
		return strings.ToLower(strings.TrimSpace(paper.SourceApis[0]))
	}
	if _, identityType := normalizedSourceIdentity(paper); identityType != "" {
		return identityType
	}
	return "unknown"
}

var doiPattern = regexp.MustCompile(`(?i)(10\.[0-9]{4,9}/[-._;()/:A-Z0-9]+)`)

func normalizeDOI(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	matches := doiPattern.FindStringSubmatch(value)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(strings.ToLower(matches[1]))
}

func doiForPaper(paper search.Paper) string {
	return normalizeDOI(firstNonEmpty(paper.DOI, paper.ID, paper.Link, paper.OpenAccessUrl))
}

var arxivIDPattern = regexp.MustCompile(`(?i)(?:arxiv:|arxiv\.org/(?:abs|pdf)/)?([0-9]{4}\.[0-9]{4,5}(?:v[0-9]+)?|[a-z\-]+/[0-9]{7}(?:v[0-9]+)?)`)

func normalizeArxivID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	matches := arxivIDPattern.FindStringSubmatch(value)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(matches[1]), ".pdf")
}

var pubmedIDPattern = regexp.MustCompile(`(?i)(?:pmid[:\s]*|pubmed\.ncbi\.nlm\.nih\.gov/)([0-9]{5,10})`)

func normalizePubMedID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	matches := pubmedIDPattern.FindStringSubmatch(value)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

var barePubMedIDPattern = regexp.MustCompile(`^[0-9]{5,10}$`)

func pubMedIDForPaper(paper search.Paper) string {
	if pmid := normalizePubMedID(firstNonEmpty(paper.ID, paper.Link)); pmid != "" {
		return pmid
	}
	source := strings.ToLower(strings.Join(append([]string{paper.Source}, paper.SourceApis...), " "))
	if !strings.Contains(source, "pubmed") {
		return ""
	}
	id := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(paper.ID), "pmid:"))
	if barePubMedIDPattern.MatchString(id) {
		return id
	}
	return ""
}

var pmcIDPattern = regexp.MustCompile(`(?i)(PMC[0-9]+)`)

func normalizePMCID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	matches := pmcIDPattern.FindStringSubmatch(value)
	if len(matches) < 2 {
		return ""
	}
	return strings.ToUpper(matches[1])
}

func normalizeURLIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return value
	}
	parsed.Fragment = ""
	parsed.RawQuery = ""
	return strings.TrimRight(strings.ToLower(parsed.String()), "/")
}
