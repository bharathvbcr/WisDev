package wisdev

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// On-demand full-text acquisition for evidence grounding. Reuses the
// Docling PDF-extraction plumbing (paper2skill.go): download the PDF from its
// open-access source, send it to the Python /ml/pdf worker, and attach the
// extracted text to the paper so span anchoring and hypothesis evaluation can
// quote paper bodies instead of abstracts.

const (
	maxFullTextFetchPerCall = 3
	fullTextFetchTimeout    = 25 * time.Second
)

// fullTextAcquisitionEnabled gates in-loop PDF fetching. Enabled by default;
// set WISDEV_FULLTEXT_ACQUISITION=false to disable (e.g. air-gapped runs).
func fullTextAcquisitionEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WISDEV_FULLTEXT_ACQUISITION"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// AcquireFullTextForPapers fetches extracted full text for up to maxFetch
// papers (in ranked order) that lack FullText but expose a fetchable PDF
// source (direct PDF URL, open-access URL, or arXiv ID). Papers are mutated in
// place. Best-effort: individual failures are logged at debug level and
// skipped. Returns the number of papers enriched.
func AcquireFullTextForPapers(ctx context.Context, papers []search.Paper, maxFetch int) int {
	if !fullTextAcquisitionEnabled() || len(papers) == 0 || ctx == nil {
		return 0
	}
	if maxFetch <= 0 || maxFetch > maxFullTextFetchPerCall {
		maxFetch = maxFullTextFetchPerCall
	}

	// SECURITY (SSRF): paper PdfUrl/OpenAccessUrl come from upstream providers and
	// model output; block redirects that land on private/loopback hosts.
	compiler := &Paper2SkillCompiler{HTTPClient: &http.Client{Timeout: fullTextFetchTimeout, CheckRedirect: secureRedirectPolicy}}
	fetched := 0
	for i := range papers {
		if fetched >= maxFetch {
			break
		}
		if err := ctx.Err(); err != nil {
			break
		}
		p := &papers[i]
		if strings.TrimSpace(p.FullText) != "" {
			continue
		}
		source := firstNonEmpty(
			strings.TrimSpace(p.PdfUrl),
			strings.TrimSpace(p.OpenAccessUrl),
			strings.TrimSpace(p.ArxivID),
		)
		if source == "" {
			continue
		}

		fetchCtx, cancel := context.WithTimeout(ctx, fullTextFetchTimeout)
		extraction, err := compiler.fetchPDFExtraction(fetchCtx, source)
		cancel()
		if err != nil {
			slog.Debug("Full-text acquisition skipped paper",
				"component", "wisdev.fulltext",
				"paperID", p.ID,
				"source", source,
				"error", err)
			continue
		}
		text := strings.TrimSpace(extraction.FullText())
		if text == "" {
			continue
		}
		p.FullText = text
		fetched++
	}

	if fetched > 0 {
		slog.Info("Acquired full text for evidence grounding",
			"component", "wisdev.fulltext",
			"papersEnriched", fetched)
	}
	return fetched
}
