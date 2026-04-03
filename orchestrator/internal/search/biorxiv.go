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

// BioRxivProvider searches bioRxiv and medRxiv preprint servers.
// No API key required. Covers biology, medicine, neuroscience, bioinformatics.
type BioRxivProvider struct {
	BaseProvider
	baseURL string
	server  string // "biorxiv" or "medrxiv"
}

var _ SearchProvider = (*BioRxivProvider)(nil)

// NewBioRxivProvider returns a provider for bioRxiv preprints.
func NewBioRxivProvider() *BioRxivProvider {
	return &BioRxivProvider{
		baseURL: "https://api.biorxiv.org/details",
		server:  "biorxiv",
	}
}

// NewMedRxivProvider returns a provider for medRxiv preprints.
func NewMedRxivProvider() *BioRxivProvider {
	return &BioRxivProvider{
		baseURL: "https://api.biorxiv.org/details",
		server:  "medrxiv",
	}
}

func (b *BioRxivProvider) Name() string { return b.server }
func (b *BioRxivProvider) Domains() []string {
	if b.server == "medrxiv" {
		return []string{"medicine", "biology"}
	}
	return []string{"biology", "neuro"}
}

// BioRxiv API supports date-range queries, not keyword search.
// We use the search endpoint from the JATS API via Content API.
func (b *BioRxivProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 15
	}

	// BioRxiv provides a search endpoint via their content search API
	searchURL := fmt.Sprintf(
		"https://api.biorxiv.org/publisher/10.1101/na/0/%d/%s",
		limit,
		url.QueryEscape(query),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		b.RecordFailure()
		return nil, providerError(b.server, "build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		// Fall back to date-range approach
		return b.searchByDateRange(ctx, query, opts)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return b.searchByDateRange(ctx, query, opts)
	}

	var result struct {
		Collection []struct {
			DOI      string `json:"doi"`
			Title    string `json:"title"`
			Abstract string `json:"abstract"`
			Date     string `json:"date"`
			Authors  string `json:"authors"`
			Server   string `json:"server"`
		} `json:"collection"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		b.RecordFailure()
		return nil, providerError(b.server, "decode: %v", err)
	}

	papers := make([]Paper, 0, len(result.Collection))
	for _, item := range result.Collection {
		if item.DOI == "" && item.Title == "" {
			continue
		}
		link := "https://www.biorxiv.org/content/" + item.DOI
		if item.Server == "medrxiv" {
			link = "https://www.medrxiv.org/content/" + item.DOI
		}
		papers = append(papers, Paper{
			ID:       b.server + ":" + item.DOI,
			Title:    strings.TrimSpace(item.Title),
			Abstract: strings.TrimSpace(item.Abstract),
			Link:     link,
			DOI:      item.DOI,
			Source:   b.server,
		})
	}

	b.RecordSuccess()
	return papers, nil
}

// searchByDateRange fetches recent papers from the last 6 months and
// filters locally by query keyword match. Used as fallback.
func (b *BioRxivProvider) searchByDateRange(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	now := time.Now()
	from := now.AddDate(0, -6, 0)
	fromStr := from.Format("2006-01-02")
	toStr := now.Format("2006-01-02")

	reqURL := fmt.Sprintf("%s/%s/%s/%s/0/json", b.baseURL, b.server, fromStr, toStr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		b.RecordFailure()
		return nil, providerError(b.server, "build fallback request: %v", err)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		b.RecordFailure()
		return nil, providerError(b.server, "fallback request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b.RecordFailure()
		return nil, providerError(b.server, "HTTP %d", resp.StatusCode)
	}

	var result struct {
		Collection []struct {
			DOI      string `json:"doi"`
			Title    string `json:"title"`
			Abstract string `json:"abstract"`
			Date     string `json:"date"`
		} `json:"collection"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		b.RecordFailure()
		return nil, providerError(b.server, "decode: %v", err)
	}

	queryLower := strings.ToLower(query)
	keywords := strings.Fields(queryLower)

	limit := opts.Limit
	if limit <= 0 {
		limit = 15
	}

	papers := make([]Paper, 0, limit)
	for _, item := range result.Collection {
		if len(papers) >= limit {
			break
		}
		titleLower := strings.ToLower(item.Title)
		absLower := strings.ToLower(item.Abstract)
		matched := 0
		for _, kw := range keywords {
			if len(kw) < 4 {
				continue
			}
			if strings.Contains(titleLower, kw) || strings.Contains(absLower, kw) {
				matched++
			}
		}
		if matched == 0 && len(keywords) > 0 {
			continue
		}
		link := fmt.Sprintf("https://www.%s.org/content/%s", b.server, item.DOI)
		papers = append(papers, Paper{
			ID:       b.server + ":" + item.DOI,
			Title:    strings.TrimSpace(item.Title),
			Abstract: strings.TrimSpace(item.Abstract),
			Link:     link,
			DOI:      item.DOI,
			Source:   b.server,
		})
	}

	b.RecordSuccess()
	return papers, nil
}
