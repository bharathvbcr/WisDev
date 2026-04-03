package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/resilience"
	"time"
)

type COREProvider struct {
	BaseProvider
	baseURL string
}

var _ SearchProvider = (*COREProvider)(nil)

func NewCOREProvider() *COREProvider {
	return &COREProvider{
		baseURL: "https://api.core.ac.uk/v3/search/works",
	}
}

func (c *COREProvider) Name() string { return "core" }

func (c *COREProvider) Domains() []string {
	return []string{} // General domain
}

type coreAPIWork struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	Abstract      string `json:"abstract"`
	DOI           string `json:"doi"`
	DownloadURL   string `json:"downloadUrl"`
	YearPublished int    `json:"yearPublished"`
}

type coreAPIResponse struct {
	TotalHits int           `json:"totalHits"`
	Results   []coreAPIWork `json:"results"`
}

func (c *COREProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	apiKey, _ := resilience.GetSecret(context.Background(), "CORE_API_KEY")
	if apiKey == "" {
		// No API key configured -- silently skip this source.
		return []Paper{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	searchQuery := query
	if opts.YearFrom > 0 {
		if opts.YearTo > 0 {
			searchQuery += fmt.Sprintf(" AND yearPublished>=%d AND yearPublished<=%d", opts.YearFrom, opts.YearTo)
		} else {
			searchQuery += fmt.Sprintf(" AND yearPublished>=%d", opts.YearFrom)
		}
	}

	reqURL := fmt.Sprintf("%s?q=%s&limit=%d", c.baseURL, url.QueryEscape(searchQuery), limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		c.RecordFailure()
		return nil, providerError("core", "build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		c.RecordFailure()
		return nil, providerError("core", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.RecordFailure()
		return nil, providerError("core", "HTTP %d", resp.StatusCode)
	}

	var coreRes coreAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&coreRes); err != nil {
		c.RecordFailure()
		return nil, providerError("core", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(coreRes.Results))
	for _, w := range coreRes.Results {
		link := w.DownloadURL
		if link == "" && w.DOI != "" {
			link = "https://doi.org/" + w.DOI
		}
		papers = append(papers, Paper{
			ID:       fmt.Sprintf("core:%d", w.ID),
			Title:    w.Title,
			Abstract: w.Abstract,
			Link:     link,
			DOI:      w.DOI,
			Source:   "core",
			Year:     w.YearPublished,
		})
	}

	c.RecordSuccess()
	return papers, nil
}
