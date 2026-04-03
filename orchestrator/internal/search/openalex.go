package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type OpenAlexProvider struct {
	BaseProvider
	baseURL string
}

var _ SearchProvider = (*OpenAlexProvider)(nil)

func NewOpenAlexProvider() *OpenAlexProvider {
	return &OpenAlexProvider{
		baseURL: "https://api.openalex.org/works",
	}
}

func (o *OpenAlexProvider) Name() string { return "openalex" }

func (o *OpenAlexProvider) Domains() []string {
	return []string{} // Default across many domains
}

type OpenAlexWork struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	DOI   string `json:"doi"`
	// Note: OpenAlex abstract is inverted, we skip reconstructing it here for speed
	PublicationYear int `json:"publication_year"`
	CitedByCount    int `json:"cited_by_count"`
	PrimaryLocation struct {
		Source struct {
			DisplayName string `json:"display_name"`
		} `json:"source"`
	} `json:"primary_location"`
}

type OpenAlexResponse struct {
	Results []OpenAlexWork `json:"results"`
}

func (o *OpenAlexProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	reqUrl := fmt.Sprintf("%s?search=%s&per_page=%d", o.baseURL, url.QueryEscape(query), limit)

	if opts.YearFrom > 0 {
		if opts.YearTo > 0 {
			reqUrl += fmt.Sprintf("&filter=publication_year:%d-%d", opts.YearFrom, opts.YearTo)
		} else {
			reqUrl += fmt.Sprintf("&filter=publication_year:>%d", opts.YearFrom-1)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		o.RecordFailure()
		return nil, providerError("openalex", "build request: %v", err)
	}

	email := os.Getenv("OPENALEX_EMAIL")
	if email != "" {
		req.URL.RawQuery += fmt.Sprintf("&mailto=%s", url.QueryEscape(email))
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		o.RecordFailure()
		return nil, providerError("openalex", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		o.RecordFailure()
		return nil, providerError("openalex", "HTTP %d", resp.StatusCode)
	}

	var oaRes OpenAlexResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaRes); err != nil {
		o.RecordFailure()
		return nil, providerError("openalex", "decode: %v", err)
	}

	var papers []Paper
	for _, w := range oaRes.Results {
		sourceName := w.PrimaryLocation.Source.DisplayName
		if sourceName == "" {
			sourceName = "OpenAlex"
		}
		papers = append(papers, Paper{
			ID:            "openalex:" + strings.TrimPrefix(w.ID, "https://openalex.org/"),
			Title:         w.Title,
			Abstract:      "", // Abstract reconstruction omitted for performance
			Link:          w.ID,
			DOI:           strings.TrimPrefix(w.DOI, "https://doi.org/"),
			Source:        sourceName,
			Year:          w.PublicationYear,
			CitationCount: w.CitedByCount,
		})
	}

	o.RecordSuccess()
	return papers, nil
}
