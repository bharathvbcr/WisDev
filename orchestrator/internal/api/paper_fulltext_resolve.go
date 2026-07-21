package api

// POST /paper/full-text/resolve — Go-owned full-text retrieval orchestration.
// Ports the OA fallback order, bounded concurrency, PDF extract coordination,
// section/page metadata, and retraction enrichment formerly in
// frontend/services/documentRetrievalService.ts. Manual PDF upload transport
// stays in the browser (POST /paper/extract-pdf).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

const (
	fullTextResolveMaxPapers     = 40
	fullTextResolveMaxConcurrent = 5
)

var fullTextResolvePerPaperTO = 25 * time.Second

var (
	fullTextLookupOpenAccess = search.LookupOpenAccess
	fullTextLookupCORE       = search.LookupCOREFullTextByDOI
	fullTextCheckRetractions = search.CheckRetractionsBatch
	fullTextValidateFetchURL = validatePublicHTTPFetchURL

	arxivIDFromURL = regexp.MustCompile(`(?i)arxiv\.org/(?:abs|pdf)/(\d{4}\.\d{4,5}(?:v\d+)?|[a-z.-]+/\d{7}(?:v\d+)?)(?:\.pdf)?`)
	arxivIDBare    = regexp.MustCompile(`(?i)^(?:arxiv:)?(\d{4}\.\d{4,5}(?:v\d+)?|[a-z.-]+/\d{7}(?:v\d+)?)$`)
	pageBreakRE    = regexp.MustCompile(`(?i)\f|\n\s*-\s*\d+\s*-\s*\n|\n\s*Page\s+\d+\s*\n`)
	sectionHeadingREs = []*regexp.Regexp{
		regexp.MustCompile(`(?im)^(?:\d+\.?\s+)?(?:abstract)\b`),
		regexp.MustCompile(`(?im)^(?:\d+\.?\s+)?(?:introduction)\b`),
		regexp.MustCompile(`(?im)^(?:\d+\.?\s+)?(?:background|related\s+work|literature\s+review)\b`),
		regexp.MustCompile(`(?im)^(?:\d+\.?\s+)?(?:methods?|methodology)\b`),
		regexp.MustCompile(`(?im)^(?:\d+\.?\s+)?(?:results?)\b`),
		regexp.MustCompile(`(?im)^(?:\d+\.?\s+)?(?:discussion)\b`),
		regexp.MustCompile(`(?im)^(?:\d+\.?\s+)?(?:conclusion|conclusions)\b`),
		regexp.MustCompile(`(?im)^(?:\d+\.?\s+)?(?:references|bibliography)\b`),
		regexp.MustCompile(`(?im)^(?:\d+\.?\s+)?(?:appendix|appendices)\b`),
	}
)

// FullTextPaperInput is one paper in a resolve batch.
type FullTextPaperInput struct {
	PaperID       string `json:"paperId,omitempty"`
	ID            string `json:"id,omitempty"`
	DOI           string `json:"doi,omitempty"`
	Title         string `json:"title,omitempty"`
	PDFUrl        string `json:"pdfUrl,omitempty"`
	OpenAccessUrl string `json:"openAccessUrl,omitempty"`
	OpenAccessPdf string `json:"openAccessPdf,omitempty"`
	ArxivID       string `json:"arxivId,omitempty"`
	Abstract      string `json:"abstract,omitempty"`
	Summary       string `json:"summary,omitempty"`
	Link          string `json:"link,omitempty"`
	SiteName      string `json:"siteName,omitempty"`
}

type fullTextResolveRequest struct {
	Papers []FullTextPaperInput `json:"papers"`
}

// FullTextDetectedSection mirrors FE DetectedSection.
type FullTextDetectedSection struct {
	Name      string `json:"name"`
	CharStart int    `json:"charStart"`
	CharEnd   int    `json:"charEnd"`
}

// FullTextResolveResult mirrors FE FullTextResult plus optional enrichment fields.
type FullTextResolveResult struct {
	PaperID          string                   `json:"paperId"`
	FullText         string                   `json:"fullText"`
	Source           string                   `json:"source"` // s2 | unpaywall | arxiv | core | abstract
	PDFUrl           string                   `json:"pdfUrl,omitempty"`
	PageBreaks       []int                    `json:"pageBreaks"`
	DetectedSections []FullTextDetectedSection `json:"detectedSections"`
	Sections         []FullTextDetectedSection `json:"sections,omitempty"`
	PageCount        int                      `json:"pageCount,omitempty"`
	AbstractOnly     bool                     `json:"abstractOnly,omitempty"`
	Retraction       *search.RetractionInfo   `json:"retraction,omitempty"`
	ExtractedAt      string                   `json:"extractedAt"`
}

// HandleResolveFullText serves POST /paper/full-text/resolve.
func (h *PaperHandler) HandleResolveFullText(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logAPIRouteLifecycle(r, "api.paper", "full_text_resolve", "request_received", "", "result", "accepted")

	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req fullTextResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "Failed to parse request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if len(req.Papers) == 0 {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "papers is required", nil)
		return
	}
	if len(req.Papers) > fullTextResolveMaxPapers {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters,
			fmt.Sprintf("too many papers (max %d)", fullTextResolveMaxPapers), nil)
		return
	}

	results := h.resolveFullTexts(r.Context(), req.Papers)

	logAPIRouteLifecycle(r, "api.paper", "full_text_resolve", "response", "",
		"result", "ok",
		"paper_count", len(req.Papers),
		"resolved_count", len(results),
		"latency_ms", time.Since(start).Milliseconds(),
	)

	writeEnvelope(w, "results", results)
}

func (h *PaperHandler) resolveFullTexts(ctx context.Context, papers []FullTextPaperInput) []FullTextResolveResult {
	out := make([]FullTextResolveResult, len(papers))
	sem := make(chan struct{}, fullTextResolveMaxConcurrent)
	var wg sync.WaitGroup

	for i := range papers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[idx] = h.abstractOnlyResult(papers[idx], nil)
				return
			}

			paperCtx, cancel := context.WithTimeout(ctx, fullTextResolvePerPaperTO)
			defer cancel()
			out[idx] = h.resolveOneFullText(paperCtx, papers[idx])
		}(i)
	}
	wg.Wait()
	return out
}

func (h *PaperHandler) resolveOneFullText(ctx context.Context, paper FullTextPaperInput) FullTextResolveResult {
	paperID := resolveFullTextPaperID(paper)
	retraction := h.lookupRetraction(ctx, paper.DOI)

	if err := ctx.Err(); err != nil {
		return h.abstractOnlyResult(paper, retraction)
	}

	// Strategy 1: Semantic Scholar / provided OA URL
	if pdfURL := s2OACandidateURL(paper); pdfURL != "" {
		if text, usedURL, ok := h.tryExtractPDF(ctx, pdfURL); ok {
			return buildFullTextResult(paperID, text, "s2", usedURL, retraction)
		}
	}

	// Strategy 2: Unpaywall
	if doi := strings.TrimSpace(paper.DOI); doi != "" {
		if info, err := fullTextLookupOpenAccess(ctx, doi); err != nil {
			slog.Debug("full-text Unpaywall lookup failed",
				"component", "api.paper.full_text_resolve",
				"doi", search.NormalizeDOI(doi),
				"error", err.Error(),
			)
		} else if info != nil && info.IsOA && info.BestOALocation != nil {
			pdfURL := firstNonEmptyTrimmed(
				info.BestOALocation.URLForPDF,
				info.BestOALocation.URL,
				info.BestOALocation.URLForLandingPage,
			)
			if pdfURL != "" {
				if text, usedURL, ok := h.tryExtractPDF(ctx, pdfURL); ok {
					return buildFullTextResult(paperID, text, "unpaywall", usedURL, retraction)
				}
			}
		}
	}

	// Strategy 3: arXiv
	if arxivID := resolvePaperArxivID(paper); arxivID != "" {
		pdfURL := "https://arxiv.org/pdf/" + arxivID + ".pdf"
		if text, usedURL, ok := h.tryExtractPDF(ctx, pdfURL); ok {
			return buildFullTextResult(paperID, text, "arxiv", usedURL, retraction)
		}
	}

	// Strategy 4: CORE
	if doi := strings.TrimSpace(paper.DOI); doi != "" {
		if coreText, err := fullTextLookupCORE(ctx, doi); err != nil {
			slog.Debug("full-text CORE lookup failed",
				"component", "api.paper.full_text_resolve",
				"doi", search.NormalizeDOI(doi),
				"error", err.Error(),
			)
		} else if strings.TrimSpace(coreText) != "" {
			return buildFullTextResult(paperID, strings.TrimSpace(coreText), "core", "", retraction)
		}
	}

	return h.abstractOnlyResult(paper, retraction)
}

func (h *PaperHandler) tryExtractPDF(ctx context.Context, rawURL string) (text string, usedURL string, ok bool) {
	if err := ctx.Err(); err != nil {
		return "", "", false
	}
	safeURL, err := fullTextValidateFetchURL(rawURL)
	if err != nil {
		slog.Debug("full-text skipped unsafe PDF URL",
			"component", "api.paper.full_text_resolve",
			"url_preview", firstNChars(rawURL, 120),
			"error", err.Error(),
		)
		return "", "", false
	}

	extracted, err := h.extractFullTextURL(ctx, safeURL)
	if err != nil {
		slog.Debug("full-text PDF extraction failed",
			"component", "api.paper.full_text_resolve",
			"url_preview", firstNChars(safeURL, 120),
			"error", err.Error(),
		)
		return "", "", false
	}
	extracted = strings.TrimSpace(extracted)
	if extracted == "" {
		return "", "", false
	}
	return extracted, safeURL, true
}

func (h *PaperHandler) extractFullTextURL(ctx context.Context, pdfURL string) (string, error) {
	if h.extractFullTextFromURL != nil {
		return h.extractFullTextFromURL(ctx, pdfURL)
	}
	return h.extractPDFTextFromURL(ctx, pdfURL)
}

func (h *PaperHandler) lookupRetraction(ctx context.Context, doi string) *search.RetractionInfo {
	clean := search.NormalizeDOI(doi)
	if clean == "" {
		return nil
	}
	results, err := fullTextCheckRetractions(ctx, []string{clean})
	if err != nil || len(results) == 0 {
		return nil
	}
	info := results[0]
	return &info
}

func (h *PaperHandler) abstractOnlyResult(paper FullTextPaperInput, retraction *search.RetractionInfo) FullTextResolveResult {
	paperID := resolveFullTextPaperID(paper)
	abstract := firstNonEmptyTrimmed(paper.Abstract, paper.Summary)
	result := FullTextResolveResult{
		PaperID:          paperID,
		FullText:         abstract,
		Source:           "abstract",
		PageBreaks:       []int{},
		DetectedSections: []FullTextDetectedSection{},
		AbstractOnly:     true,
		Retraction:       retraction,
		ExtractedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	if abstract != "" {
		sections := detectFullTextSections(abstract)
		result.DetectedSections = sections
		result.Sections = sections
	}
	return result
}

func buildFullTextResult(paperID, text, source, pdfURL string, retraction *search.RetractionInfo) FullTextResolveResult {
	pageBreaks := detectFullTextPageBreaks(text)
	sections := detectFullTextSections(text)
	pageCount := len(pageBreaks) + 1
	if strings.TrimSpace(text) == "" {
		pageCount = 0
	}
	return FullTextResolveResult{
		PaperID:          paperID,
		FullText:         text,
		Source:           source,
		PDFUrl:           pdfURL,
		PageBreaks:       pageBreaks,
		DetectedSections: sections,
		Sections:         sections,
		PageCount:        pageCount,
		Retraction:       retraction,
		ExtractedAt:      time.Now().UTC().Format(time.RFC3339),
	}
}

func resolveFullTextPaperID(paper FullTextPaperInput) string {
	return firstNonEmptyTrimmed(paper.PaperID, paper.ID, paper.DOI, paper.ArxivID, paper.Title)
}

func s2OACandidateURL(paper FullTextPaperInput) string {
	oa := firstNonEmptyTrimmed(paper.OpenAccessPdf, paper.OpenAccessUrl)
	if oa == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(oa), ".pdf") {
		return oa
	}
	if pdf := strings.TrimSpace(paper.PDFUrl); pdf != "" {
		return pdf
	}
	return oa
}

func resolvePaperArxivID(paper FullTextPaperInput) string {
	if id := strings.TrimSpace(paper.ArxivID); id != "" {
		if m := arxivIDBare.FindStringSubmatch(id); len(m) > 1 {
			return m[1]
		}
		return id
	}
	if id := search.ExtractArxivIDFromDOI(paper.DOI); id != "" {
		return id
	}
	for _, candidate := range []string{paper.Link, paper.PDFUrl, paper.OpenAccessUrl, paper.OpenAccessPdf} {
		if m := arxivIDFromURL.FindStringSubmatch(candidate); len(m) > 1 {
			return m[1]
		}
	}
	if id := strings.TrimSpace(paper.PaperID); id != "" {
		if m := arxivIDBare.FindStringSubmatch(id); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func detectFullTextPageBreaks(text string) []int {
	breaks := make([]int, 0)
	for _, match := range pageBreakRE.FindAllStringIndex(text, -1) {
		if len(match) == 2 {
			breaks = append(breaks, match[0])
		}
	}
	return breaks
}

func detectFullTextSections(text string) []FullTextDetectedSection {
	type hit struct {
		name  string
		index int
	}
	hits := make([]hit, 0, len(sectionHeadingREs))
	for _, re := range sectionHeadingREs {
		loc := re.FindStringIndex(text)
		if loc == nil {
			continue
		}
		name := strings.TrimSpace(re.FindString(text))
		name = regexp.MustCompile(`(?i)^\d+\.?\s+`).ReplaceAllString(name, "")
		name = strings.ToLower(strings.TrimSpace(name))
		hits = append(hits, hit{name: name, index: loc[0]})
	}
	if len(hits) == 0 {
		return []FullTextDetectedSection{}
	}
	// Sort by position (stable insertion for tiny n).
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].index < hits[j-1].index; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	sections := make([]FullTextDetectedSection, 0, len(hits))
	for i, h := range hits {
		end := len(text)
		if i+1 < len(hits) {
			end = hits[i+1].index
		}
		sections = append(sections, FullTextDetectedSection{
			Name:      h.name,
			CharStart: h.index,
			CharEnd:   end,
		})
	}
	return sections
}

// validatePublicHTTPFetchURL rejects non-http(s) schemes and private/loopback hosts (SSRF).
func validatePublicHTTPFetchURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid fetch url: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("disallowed fetch url scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("fetch url missing host")
	}
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("fetch url missing host")
	}
	// Block obvious private/loopback literals without DNS.
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return "", fmt.Errorf("fetch url resolves to a private or loopback address")
		}
	} else if isPrivateOrLoopback(host) {
		return "", fmt.Errorf("fetch url resolves to a private or loopback address")
	}
	return parsed.String(), nil
}
