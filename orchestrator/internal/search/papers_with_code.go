package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PapersWithCodeProvider searches Papers With Code — a platform linking
// ML papers to their GitHub implementations and benchmark results.
// No API key required. Best for AI, ML, and deep learning research.
type PapersWithCodeProvider struct {
	BaseProvider
	baseURL string
}

const papersWithCodeRedirectError = "public api/v1 redirected away from paperswithcode.com"

func isAcceptedPapersWithCodeHost(host string) bool {
	host = strings.ToLower(host)
	return host == "paperswithcode.com" ||
		strings.HasPrefix(host, "paperswithcode.com:") ||
		host == "www.paperswithcode.com" ||
		strings.HasPrefix(host, "www.paperswithcode.com:") ||
		strings.HasPrefix(host, "127.0.0.1:") ||
		strings.HasPrefix(host, "localhost:") ||
		strings.HasPrefix(host, "[::1]:")
}

var _ SearchProvider = (*PapersWithCodeProvider)(nil)

func NewPapersWithCodeProvider() *PapersWithCodeProvider {
	return &PapersWithCodeProvider{baseURL: "https://paperswithcode.com/api/v1/papers/"}
}

func (p *PapersWithCodeProvider) Name() string { return "papers_with_code" }
func (p *PapersWithCodeProvider) Domains() []string {
	return []string{"cs", "ai", "engineering"}
}

func (pwc *PapersWithCodeProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 15
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("items_per_page", fmt.Sprintf("%d", limit))

	reqURL := pwc.baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		pwc.RecordFailure()
		return nil, providerError("papers_with_code", "build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "WisDev/1.0")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		pwc.RecordFailure()
		return nil, providerError("papers_with_code", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		pwc.RecordFailure()
		return nil, providerError("papers_with_code", "HTTP %d", resp.StatusCode)
	}

	if resp.Request != nil && resp.Request.URL != nil && !isAcceptedPapersWithCodeHost(resp.Request.URL.Host) {
		pwc.RecordFailure()
		return nil, providerError("papers_with_code", "%s (%s)", papersWithCodeRedirectError, resp.Request.URL.Host)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "application/json") {
		pwc.RecordFailure()
		return nil, providerError("papers_with_code", "expected JSON response, got %q", contentType)
	}

	var result struct {
		Results []struct {
			ID        string   `json:"id"`
			ArxivID   string   `json:"arxiv_id"`
			Title     string   `json:"title"`
			Abstract  string   `json:"abstract"`
			URL       string   `json:"url_abs"`
			PDFURL    string   `json:"url_pdf"`
			Published string   `json:"published"`
			Authors   []string `json:"authors"`
			Stars     int      `json:"stars"` // GitHub stars across linked repos
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		pwc.RecordFailure()
		return nil, providerError("papers_with_code", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(result.Results))
	for _, item := range result.Results {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}

		link := item.URL
		if link == "" && item.ArxivID != "" {
			link = "https://arxiv.org/abs/" + item.ArxivID
		}

		id := "pwc:" + item.ID
		if item.ArxivID != "" {
			id = "arxiv:" + item.ArxivID
		}

		year := 0
		if len(item.Published) >= 4 {
			fmt.Sscanf(item.Published[:4], "%d", &year)
		}

		papers = append(papers, Paper{
			ID:            id,
			Title:         title,
			Abstract:      strings.TrimSpace(item.Abstract),
			Link:          link,
			Source:        "papers_with_code",
			SourceApis:    []string{"papers_with_code"},
			Authors:       item.Authors,
			Year:          year,
			CitationCount: item.Stars, // stars as proxy for impact
			OpenAccessUrl: item.PDFURL,
			PdfUrl:        item.PDFURL,
		})
	}

	pwc.RecordSuccess()
	return papers, nil
}
