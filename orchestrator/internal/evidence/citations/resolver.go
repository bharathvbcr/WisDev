package citations

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Retry and rate-limiting configuration
const (
	maxRetries       = 3
	baseBackoffMs    = 100
	maxBackoffMs     = 3000
	maxResponseBytes = 1 << 20 // 1 MB limit per response
	rateLimitRetryMs = 500     // Wait 500ms before retrying after rate limit
)

type ResolveInput struct {
	Title             string
	DOI               string
	ArxivID           string
	SemanticScholarID string
	OpenAlexID        string
	Year              int
	Authors           []string
}

// ValidateResolveInput checks if the input has at least minimum required fields
func ValidateResolveInput(in ResolveInput) error {
	// Sanitize all inputs
	in.Title = strings.TrimSpace(in.Title)
	in.DOI = strings.TrimSpace(in.DOI)
	in.ArxivID = strings.TrimSpace(in.ArxivID)
	in.OpenAlexID = strings.TrimSpace(in.OpenAlexID)
	in.SemanticScholarID = strings.TrimSpace(in.SemanticScholarID)

	// Need at least one identifier
	hasIdentifier := in.DOI != "" || in.ArxivID != "" || in.OpenAlexID != "" || in.SemanticScholarID != "" || in.Title != ""
	if !hasIdentifier {
		return fmt.Errorf("input must contain at least one of: DOI, ArxivID, OpenAlexID, SemanticScholarID, or Title")
	}

	// Validate title length if present
	if len(in.Title) > 512 {
		return fmt.Errorf("title too long (max 512 chars): got %d", len(in.Title))
	}

	// Validate year if set
	currentYear := time.Now().Year()
	if in.Year < 1900 || in.Year > currentYear+1 {
		return fmt.Errorf("invalid year: %d (must be 1900-%d)", in.Year, currentYear+1)
	}

	// Validate authors array size
	if len(in.Authors) > 100 {
		return fmt.Errorf("too many authors (max 100): got %d", len(in.Authors))
	}
	for i, author := range in.Authors {
		if len(author) > 256 {
			return fmt.Errorf("author %d name too long (max 256 chars)", i)
		}
	}

	return nil
}

type ResolvedCitation struct {
	CanonicalID          string   `json:"canonicalId"`
	Title                string   `json:"title"`
	DOI                  string   `json:"doi,omitempty"`
	ArxivID              string   `json:"arxivId,omitempty"`
	OpenAlexID           string   `json:"openAlexId,omitempty"`
	SemanticScholarID    string   `json:"semanticScholarId,omitempty"`
	LandingURL           string   `json:"landingUrl,omitempty"`
	Year                 int      `json:"year,omitempty"`
	Authors              []string `json:"authors,omitempty"`
	Resolved             bool     `json:"resolved"`
	ResolutionEngine     string   `json:"resolutionEngine"`
	ResolutionConfidence float64  `json:"resolutionConfidence"`
}

type PromotionVerdict struct {
	Canonical        ResolvedCitation `json:"canonical"`
	AgreementSources []string         `json:"agreementSources,omitempty"`
	Promoted         bool             `json:"promoted"`
	ConflictNote     string           `json:"conflictNote,omitempty"`
}

type Resolver struct {
	httpClient    *http.Client
	openAlexEmail string
	logger        *log.Logger // For structured logging of API calls and errors
}

func NewResolver(openAlexEmail string) *Resolver {
	return &Resolver{
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		openAlexEmail: strings.TrimSpace(openAlexEmail),
		logger:        log.New(io.Discard, "[resolver] ", log.LstdFlags), // Default: silent
	}
}

// WithLogger sets a logger for debugging API calls and errors
func (r *Resolver) WithLogger(logger *log.Logger) *Resolver {
	if logger != nil {
		r.logger = logger
	}
	return r
}

func (r *Resolver) Resolve(ctx context.Context, in ResolveInput) (ResolvedCitation, error) {
	// Validate input early to fail fast
	if err := ValidateResolveInput(in); err != nil {
		r.logger.Printf("invalid input: %v", err)
		return ResolvedCitation{
			Title:                in.Title,
			ResolutionEngine:     "go-canonical-resolver",
			ResolutionConfidence: 0,
		}, err
	}

	// Check context before starting work
	if err := ctx.Err(); err != nil {
		r.logger.Printf("context error before resolution: %v", err)
		return ResolvedCitation{
			Title:                in.Title,
			ResolutionEngine:     "go-canonical-resolver",
			ResolutionConfidence: 0,
		}, err
	}

	res := ResolvedCitation{
		Title:                strings.TrimSpace(in.Title),
		DOI:                  normalizeDOI(in.DOI),
		ArxivID:              normalizeArxivID(in.ArxivID),
		OpenAlexID:           strings.TrimSpace(in.OpenAlexID),
		SemanticScholarID:    strings.TrimSpace(in.SemanticScholarID),
		Year:                 in.Year,
		Authors:              append([]string(nil), in.Authors...),
		ResolutionEngine:     "go-canonical-resolver",
		ResolutionConfidence: 0.55,
	}

	// Try to resolve from multiple sources in priority order
	if res.DOI != "" {
		r.enrichFromCrossref(ctx, &res)
		if ctx.Err() != nil {
			return res, nil // Return partial result on context cancel
		}
		r.enrichFromOpenAlexByDOI(ctx, &res)
		if ctx.Err() != nil {
			return res, nil
		}
		r.enrichFromSemanticScholar(ctx, &res, "DOI:"+res.DOI)
		if ctx.Err() != nil {
			return res, nil
		}
	}

	if res.ArxivID != "" && ctx.Err() == nil {
		r.enrichFromArxiv(ctx, &res)
		if ctx.Err() != nil {
			return res, nil
		}
		r.enrichFromOpenAlexByArxiv(ctx, &res)
		if ctx.Err() != nil {
			return res, nil
		}
		r.enrichFromSemanticScholar(ctx, &res, "ARXIV:"+res.ArxivID)
		if ctx.Err() != nil {
			return res, nil
		}
	}

	if res.SemanticScholarID != "" && ctx.Err() == nil {
		r.enrichFromSemanticScholar(ctx, &res, strings.TrimPrefix(res.SemanticScholarID, "s2:"))
		if ctx.Err() != nil {
			return res, nil
		}
	}

	if res.OpenAlexID != "" && ctx.Err() == nil {
		r.enrichFromOpenAlexByID(ctx, &res)
		if ctx.Err() != nil {
			return res, nil
		}
	}

	if res.OpenAlexID == "" && res.Title != "" && ctx.Err() == nil {
		r.enrichFromOpenAlexByTitle(ctx, &res)
		if ctx.Err() != nil {
			return res, nil
		}
	}

	// Compute canonical ID from best available source
	res.CanonicalID = firstNonEmpty(
		formatID("doi", res.DOI),
		formatID("arxiv", res.ArxivID),
		formatID("openalex", res.OpenAlexID),
		formatID("s2", res.SemanticScholarID),
		formatID("title", normalizeTitle(res.Title)),
	)
	res.Resolved = res.CanonicalID != ""
	res.ResolutionConfidence = confidenceFor(&res)

	return res, nil
}

func (r *Resolver) enrichFromCrossref(ctx context.Context, out *ResolvedCitation) {
	if out.DOI == "" {
		return
	}
	endpoint := "https://api.crossref.org/works/" + url.PathEscape(out.DOI)
	var payload struct {
		Message struct {
			Title  []string `json:"title"`
			URL    string   `json:"URL"`
			Issued struct {
				DateParts [][]int `json:"date-parts"`
			} `json:"issued"`
			Author []struct {
				Given  string `json:"given"`
				Family string `json:"family"`
			} `json:"author"`
		} `json:"message"`
	}
	if err := r.getJSON(ctx, endpoint, &payload, map[string]string{"Accept": "application/json"}); err != nil {
		return
	}
	if out.Title == "" && len(payload.Message.Title) > 0 {
		out.Title = strings.TrimSpace(payload.Message.Title[0])
	}
	if out.LandingURL == "" {
		out.LandingURL = strings.TrimSpace(payload.Message.URL)
	}
	if out.Year == 0 && len(payload.Message.Issued.DateParts) > 0 && len(payload.Message.Issued.DateParts[0]) > 0 {
		out.Year = payload.Message.Issued.DateParts[0][0]
	}
	if len(out.Authors) == 0 {
		for _, author := range payload.Message.Author {
			name := strings.TrimSpace(strings.TrimSpace(author.Given) + " " + strings.TrimSpace(author.Family))
			if name != "" {
				out.Authors = append(out.Authors, name)
			}
		}
	}
}

func (r *Resolver) enrichFromOpenAlexByDOI(ctx context.Context, out *ResolvedCitation) {
	if out.DOI == "" {
		return
	}
	endpoint := "https://api.openalex.org/works?filter=doi:https://doi.org/" + url.QueryEscape(out.DOI)
	if r.openAlexEmail != "" {
		endpoint += "&mailto=" + url.QueryEscape(r.openAlexEmail)
	}
	r.enrichFromOpenAlexSearch(ctx, endpoint, out)
}

func (r *Resolver) enrichFromOpenAlexByArxiv(ctx context.Context, out *ResolvedCitation) {
	if out.ArxivID == "" {
		return
	}
	endpoint := "https://api.openalex.org/works?filter=locations.landing_page_url:https://arxiv.org/abs/" + url.QueryEscape(out.ArxivID)
	if r.openAlexEmail != "" {
		endpoint += "&mailto=" + url.QueryEscape(r.openAlexEmail)
	}
	r.enrichFromOpenAlexSearch(ctx, endpoint, out)
}

func (r *Resolver) enrichFromOpenAlexByTitle(ctx context.Context, out *ResolvedCitation) {
	endpoint := "https://api.openalex.org/works?search=" + url.QueryEscape(out.Title)
	if r.openAlexEmail != "" {
		endpoint += "&mailto=" + url.QueryEscape(r.openAlexEmail)
	}
	r.enrichFromOpenAlexSearch(ctx, endpoint, out)
}

func (r *Resolver) enrichFromOpenAlexByID(ctx context.Context, out *ResolvedCitation) {
	id := strings.TrimSpace(strings.TrimPrefix(out.OpenAlexID, "openalex:"))
	if id == "" {
		return
	}
	endpoint := "https://api.openalex.org/works/" + url.PathEscape(id)
	if r.openAlexEmail != "" {
		endpoint += "?mailto=" + url.QueryEscape(r.openAlexEmail)
	}
	var item struct {
		ID              string `json:"id"`
		Title           string `json:"title"`
		DOI             string `json:"doi"`
		PublicationYear int    `json:"publication_year"`
		PrimaryLocation struct {
			LandingPageURL string `json:"landing_page_url"`
		} `json:"primary_location"`
	}
	if err := r.getJSON(ctx, endpoint, &item, nil); err != nil {
		return
	}
	r.mergeOpenAlexItem(out, item.ID, item.Title, item.DOI, item.PublicationYear, item.PrimaryLocation.LandingPageURL)
}

func (r *Resolver) enrichFromOpenAlexSearch(ctx context.Context, endpoint string, out *ResolvedCitation) {
	var payload struct {
		Results []struct {
			ID              string `json:"id"`
			Title           string `json:"title"`
			DOI             string `json:"doi"`
			PublicationYear int    `json:"publication_year"`
			PrimaryLocation struct {
				LandingPageURL string `json:"landing_page_url"`
			} `json:"primary_location"`
		} `json:"results"`
	}
	if err := r.getJSON(ctx, endpoint, &payload, nil); err != nil {
		return
	}
	if len(payload.Results) == 0 {
		return
	}
	item := payload.Results[0]
	r.mergeOpenAlexItem(out, item.ID, item.Title, item.DOI, item.PublicationYear, item.PrimaryLocation.LandingPageURL)
}

func (r *Resolver) mergeOpenAlexItem(out *ResolvedCitation, id string, title string, doi string, year int, urlValue string) {
	if out.OpenAlexID == "" {
		out.OpenAlexID = strings.TrimSpace(strings.TrimPrefix(id, "https://openalex.org/"))
	}
	if out.Title == "" {
		out.Title = strings.TrimSpace(title)
	}
	if out.DOI == "" {
		out.DOI = normalizeDOI(doi)
	}
	if out.Year == 0 {
		out.Year = year
	}
	if out.LandingURL == "" {
		out.LandingURL = strings.TrimSpace(urlValue)
	}
}

func (r *Resolver) enrichFromSemanticScholar(ctx context.Context, out *ResolvedCitation, id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	endpoint := "https://api.semanticscholar.org/graph/v1/paper/" + url.PathEscape(id) +
		"?fields=paperId,title,year,url,externalIds,authors"
	var payload struct {
		PaperID     string `json:"paperId"`
		Title       string `json:"title"`
		Year        int    `json:"year"`
		URL         string `json:"url"`
		ExternalIDs struct {
			DOI   string `json:"DOI"`
			ArXiv string `json:"ArXiv"`
		} `json:"externalIds"`
		Authors []struct {
			Name string `json:"name"`
		} `json:"authors"`
	}
	if err := r.getJSON(ctx, endpoint, &payload, nil); err != nil {
		return
	}
	if out.SemanticScholarID == "" {
		out.SemanticScholarID = strings.TrimSpace(payload.PaperID)
	}
	if out.Title == "" {
		out.Title = strings.TrimSpace(payload.Title)
	}
	if out.DOI == "" {
		out.DOI = normalizeDOI(payload.ExternalIDs.DOI)
	}
	if out.ArxivID == "" {
		out.ArxivID = normalizeArxivID(payload.ExternalIDs.ArXiv)
	}
	if out.Year == 0 {
		out.Year = payload.Year
	}
	if out.LandingURL == "" {
		out.LandingURL = strings.TrimSpace(payload.URL)
	}
	if len(out.Authors) == 0 {
		for _, author := range payload.Authors {
			name := strings.TrimSpace(author.Name)
			if name != "" {
				out.Authors = append(out.Authors, name)
			}
		}
	}
}

func (r *Resolver) enrichFromArxiv(ctx context.Context, out *ResolvedCitation) {
	if out.ArxivID == "" {
		return
	}
	endpoint := "http://export.arxiv.org/api/query?id_list=" + url.QueryEscape(out.ArxivID)
	var feed struct {
		Entries []struct {
			Title     string   `xml:"title"`
			Published string   `xml:"published"`
			Summary   string   `xml:"summary"`
			Authors   []string `xml:"author>name"`
			ID        string   `xml:"id"`
		} `xml:"entry"`
	}
	if err := r.getXML(ctx, endpoint, &feed); err != nil {
		return
	}
	if len(feed.Entries) == 0 {
		return
	}
	entry := feed.Entries[0]
	if out.Title == "" {
		out.Title = strings.TrimSpace(strings.Join(strings.Fields(entry.Title), " "))
	}
	if out.Year == 0 && len(entry.Published) >= 4 {
		if year, err := strconv.Atoi(entry.Published[:4]); err == nil {
			out.Year = year
		}
	}
	if out.LandingURL == "" {
		out.LandingURL = strings.TrimSpace(entry.ID)
	}
	if len(out.Authors) == 0 {
		for _, author := range entry.Authors {
			author = strings.TrimSpace(author)
			if author != "" {
				out.Authors = append(out.Authors, author)
			}
		}
	}
}

// backoffDuration calculates exponential backoff with jitter
func backoffDuration(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	if attempt > 10 {
		attempt = 10 // Cap at 2^9 to avoid overflow and limit max backoff
	}
	// Exponential backoff: 100ms, 200ms, 400ms, 800ms, etc.
	ms := baseBackoffMs * int(math.Pow(2, float64(attempt-1)))
	if ms > maxBackoffMs {
		ms = maxBackoffMs
	}
	// Add small jitter (±10%)
	// Note: In a real app we might use rand.Intn, but for simplicity we use a deterministic jitter for now
	// to avoid extra complexity in this helper.
	jitter := int(float64(ms) * 0.1)
	return time.Duration(ms+jitter) * time.Millisecond
}

func endpointPreview(endpoint string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(endpoint) <= maxLen {
		return endpoint
	}
	return endpoint[:maxLen]
}

func (r *Resolver) getJSON(ctx context.Context, endpoint string, target any, headers map[string]string) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Check context on each retry
		if err := ctx.Err(); err != nil {
			r.logger.Printf("context cancelled on attempt %d: %v", attempt, err)
			return err
		}

		if attempt > 0 {
			// Back off exponentially before retry
			backoff := backoffDuration(attempt)
			r.logger.Printf("retry attempt %d after %v for endpoint %q", attempt, backoff, endpointPreview(endpoint, 50))
			time.Sleep(backoff)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			r.logger.Printf("failed to create request: %v", err)
			return err // Non-retryable error
		}

		// Set reasonable headers
		req.Header.Set("User-Agent", "wisdev-resolver/1.0")
		for key, value := range headers {
			req.Header.Set(key, value)
		}

		resp, err := r.httpClient.Do(req)
		if err != nil {
			lastErr = err
			r.logger.Printf("request failed on attempt %d: %v", attempt, err)
			if attempt < maxRetries {
				continue // Retry on network errors
			}
			return err
		}
		defer resp.Body.Close()

		// Handle rate limiting with long backoff
		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("rate limited (HTTP 429)")
			r.logger.Printf("rate limited on attempt %d, backing off %dms", attempt, rateLimitRetryMs)
			if attempt < maxRetries {
				time.Sleep(time.Duration(rateLimitRetryMs) * time.Millisecond)
				continue
			}
			return lastErr
		}

		// Retry on recoverable server errors
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			lastErr = fmt.Errorf("server error (HTTP %d)", resp.StatusCode)
			r.logger.Printf("server error on attempt %d: HTTP %d", attempt, resp.StatusCode)
			if attempt < maxRetries {
				continue
			}
			return lastErr
		}

		// Don't retry on client errors (4xx except 429)
		if resp.StatusCode != http.StatusOK {
			r.logger.Printf("unexpected status on attempt %d: HTTP %d", attempt, resp.StatusCode)
			return fmt.Errorf("unexpected status %d", resp.StatusCode)
		}

		// Limit response body size
		limitedBody := io.LimitReader(resp.Body, maxResponseBytes)
		if err := json.NewDecoder(limitedBody).Decode(target); err != nil {
			lastErr = err
			r.logger.Printf("failed to decode JSON on attempt %d: %v", attempt, err)
			if attempt < maxRetries && isTransientDecodingError(err) {
				continue
			}
			return err
		}

		return nil // Success
	}

	if lastErr != nil {
		return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
	}
	return fmt.Errorf("exhausted retries for JSON endpoint")
}

func (r *Resolver) getXML(ctx context.Context, endpoint string, target any) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Check context on each retry
		if err := ctx.Err(); err != nil {
			r.logger.Printf("context cancelled on attempt %d: %v", attempt, err)
			return err
		}

		if attempt > 0 {
			backoff := backoffDuration(attempt)
			r.logger.Printf("retry attempt %d after %v for endpoint %q", attempt, backoff, endpointPreview(endpoint, 50))
			time.Sleep(backoff)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			r.logger.Printf("failed to create request: %v", err)
			return err
		}

		req.Header.Set("User-Agent", "wisdev-resolver/1.0")

		resp, err := r.httpClient.Do(req)
		if err != nil {
			lastErr = err
			r.logger.Printf("request failed on attempt %d: %v", attempt, err)
			if attempt < maxRetries {
				continue
			}
			return err
		}
		defer resp.Body.Close()

		// Handle rate limiting
		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("rate limited (HTTP 429)")
			r.logger.Printf("rate limited on attempt %d", attempt)
			if attempt < maxRetries {
				time.Sleep(time.Duration(rateLimitRetryMs) * time.Millisecond)
				continue
			}
			return lastErr
		}

		// Retry on recoverable server errors
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			lastErr = fmt.Errorf("server error (HTTP %d)", resp.StatusCode)
			r.logger.Printf("server error on attempt %d: HTTP %d", attempt, resp.StatusCode)
			if attempt < maxRetries {
				continue
			}
			return lastErr
		}

		// Don't retry on client errors
		if resp.StatusCode != http.StatusOK {
			r.logger.Printf("unexpected status on attempt %d: HTTP %d", attempt, resp.StatusCode)
			return fmt.Errorf("unexpected status %d", resp.StatusCode)
		}

		// Limit response body size
		limitedBody := io.LimitReader(resp.Body, maxResponseBytes)
		if err := xml.NewDecoder(limitedBody).Decode(target); err != nil {
			lastErr = err
			r.logger.Printf("failed to decode XML on attempt %d: %v", attempt, err)
			if attempt < maxRetries && isTransientDecodingError(err) {
				continue
			}
			return err
		}

		return nil // Success
	}

	if lastErr != nil {
		return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
	}
	return fmt.Errorf("exhausted retries for XML endpoint")
}

// isTransientDecodingError checks if a decoding error is worth retrying
func isTransientDecodingError(err error) bool {
	// Only retry syntax/unicode errors, not logical errors
	if err == nil {
		return false
	}
	msg := err.Error()
	// These are worth retrying (might be transient upstream issues)
	return strings.Contains(msg, "EOF") || strings.Contains(msg, "eof")
}

func normalizeDOI(doi string) string {
	// Remove spaces and validate format
	doi = strings.TrimSpace(doi)
	if doi == "" {
		return ""
	}

	// Strip common prefixes
	doi = strings.TrimPrefix(doi, "https://doi.org/")
	doi = strings.TrimPrefix(doi, "http://doi.org/")
	doi = strings.TrimSpace(doi)

	// DOI must start with 10. and contain /
	if !strings.HasPrefix(doi, "10.") || !strings.Contains(doi, "/") {
		return "" // Invalid DOI format
	}

	// Limit length (DOIs are typically short)
	if len(doi) > 256 {
		return ""
	}

	return doi
}

func normalizeArxivID(arxivID string) string {
	arxivID = strings.TrimSpace(arxivID)
	if arxivID == "" {
		return ""
	}

	// Strip common prefixes (case-insensitive)
	arxivID = strings.TrimPrefix(strings.ToLower(arxivID), "arxiv:")
	arxivID = strings.TrimSpace(arxivID)

	// ArXiv IDs must match pattern: digits or vN format (e.g., 2301.12345 or 0704.0123)
	// Old format: hep-th/0703216, New format: 0704.0123 or 2301.12345
	if len(arxivID) == 0 || len(arxivID) > 20 {
		return ""
	}

	// Quick validation: must be alphanumeric with dots, slashes, dashes
	for _, ch := range arxivID {
		if !((ch >= '0' && ch <= '9') || ch == '.' || ch == '/' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '-') {
			return "" // Invalid character
		}
	}

	return arxivID
}

func normalizeTitle(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	// Limit title length for canonical ID purposes
	if len(value) > 512 {
		value = value[:512]
	}

	// Normalize: lowercase, remove punctuation, collapse whitespace
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, ",", " ")
	value = strings.ReplaceAll(value, ".", " ")
	value = strings.ReplaceAll(value, "?", " ")
	value = strings.ReplaceAll(value, "!", " ")
	value = strings.ReplaceAll(value, ":", " ")
	value = strings.ReplaceAll(value, ";", " ")
	value = strings.Join(strings.Fields(value), " ")

	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func formatID(prefix string, value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	return prefix + ":" + v
}

func confidenceFor(result *ResolvedCitation) float64 {
	switch {
	case strings.TrimSpace(result.DOI) != "":
		return 0.98
	case strings.TrimSpace(result.ArxivID) != "":
		return 0.94
	case strings.TrimSpace(result.OpenAlexID) != "":
		return 0.9
	case strings.TrimSpace(result.SemanticScholarID) != "":
		return 0.86
	case strings.TrimSpace(result.Title) != "":
		return 0.72
	default:
		return 0.5
	}
}

func citationFingerprint(citation ResolvedCitation) string {
	title := normalizeTitle(citation.Title)
	year := citation.Year
	id := firstNonEmpty(
		formatID("doi", normalizeDOI(citation.DOI)),
		formatID("arxiv", normalizeArxivID(citation.ArxivID)),
		formatID("openalex", strings.TrimSpace(citation.OpenAlexID)),
		formatID("s2", strings.TrimSpace(citation.SemanticScholarID)),
	)
	if title == "" && id == "" {
		return ""
	}
	return fmt.Sprintf("%s|%d|%s", title, year, id)
}

// EvaluatePromotion applies writer-promotion gating.
// A citation is promoted only when at least minSources agree on title, year,
// and one persistent identifier (doi/arxiv/openalex/s2).
func EvaluatePromotion(results []ResolvedCitation, minSources int) PromotionVerdict {
	if minSources <= 0 {
		minSources = 2
	}
	if len(results) == 0 {
		return PromotionVerdict{Promoted: false, ConflictNote: "no resolution results"}
	}

	votes := make(map[string]int, len(results))
	canonicalByFingerprint := make(map[string]ResolvedCitation, len(results))
	sourcesByFingerprint := make(map[string][]string, len(results))

	for idx, result := range results {
		fingerprint := citationFingerprint(result)
		if fingerprint == "" {
			continue
		}
		votes[fingerprint]++
		if _, exists := canonicalByFingerprint[fingerprint]; !exists {
			canonicalByFingerprint[fingerprint] = result
		}
		sourceName := firstNonEmpty(strings.TrimSpace(result.ResolutionEngine), fmt.Sprintf("source_%d", idx+1))
		sourcesByFingerprint[fingerprint] = append(sourcesByFingerprint[fingerprint], sourceName)
	}

	if len(votes) == 0 {
		return PromotionVerdict{Promoted: false, ConflictNote: "no canonical fingerprints produced"}
	}

	bestFingerprint := ""
	bestVotes := -1
	for fingerprint, count := range votes {
		if count > bestVotes {
			bestVotes = count
			bestFingerprint = fingerprint
		}
	}

	canonical := canonicalByFingerprint[bestFingerprint]
	agreementSources := sourcesByFingerprint[bestFingerprint]
	promoted := bestVotes >= minSources
	verdict := PromotionVerdict{
		Canonical:        canonical,
		AgreementSources: agreementSources,
		Promoted:         promoted,
	}

	if promoted {
		return verdict
	}

	if len(votes) == 1 {
		verdict.ConflictNote = fmt.Sprintf("insufficient agreement: only %d source(s), requires %d", bestVotes, minSources)
		return verdict
	}

	conflicts := make([]string, 0, len(votes))
	for fingerprint, count := range votes {
		if fingerprint == bestFingerprint {
			continue
		}
		conflicts = append(conflicts, fmt.Sprintf("%s (%d)", fingerprint, count))
	}
	sort.Strings(conflicts)
	verdict.ConflictNote = fmt.Sprintf("source disagreement: best=%s (%d), conflicts=%s", bestFingerprint, bestVotes, strings.Join(conflicts, "; "))
	return verdict
}
