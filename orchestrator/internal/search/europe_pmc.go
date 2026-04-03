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

// EuropePMCProvider searches Europe PubMed Central.
// No API key required. Covers life sciences, medicine, biomedical.
// Includes full-text open-access content and preprints.
type EuropePMCProvider struct {
	BaseProvider
	baseURL string
}

var _ SearchProvider = (*EuropePMCProvider)(nil)

func NewEuropePMCProvider() *EuropePMCProvider {
	return &EuropePMCProvider{baseURL: "https://www.ebi.ac.uk/europepmc/webservices/rest/search"}
}

func (e *EuropePMCProvider) Name() string { return "europe_pmc" }
func (e *EuropePMCProvider) Domains() []string {
	return []string{"medicine", "biology", "neuro", "climate"}
}

func (e *EuropePMCProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 15
	}

	params := url.Values{}
	params.Set("query", query)
	params.Set("format", "json")
	params.Set("pageSize", fmt.Sprintf("%d", limit))
	params.Set("resultType", "core") // includes abstracts
	params.Set("synonym", "true")

	reqURL := e.baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		e.RecordFailure()
		return nil, providerError("europe_pmc", "build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		e.RecordFailure()
		return nil, providerError("europe_pmc", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		e.RecordFailure()
		return nil, providerError("europe_pmc", "HTTP %d", resp.StatusCode)
	}

	var result struct {
		ResultList struct {
			Result []struct {
				ID           string `json:"id"`
				Source       string `json:"source"`
				Title        string `json:"title"`
				AbstractText string `json:"abstractText"`
				DOI          string `json:"doi"`
				PMID         string `json:"pmid"`
				PMCid        string `json:"pmcid"`
				PubYear      string `json:"pubYear"`
				CitedByCount int    `json:"citedByCount"`
				AuthorString string `json:"authorString"`
				IsOpenAccess string `json:"isOpenAccess"`
			} `json:"result"`
		} `json:"resultList"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		e.RecordFailure()
		return nil, providerError("europe_pmc", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(result.ResultList.Result))
	for _, item := range result.ResultList.Result {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}

		// Build canonical link
		link := ""
		if item.PMCid != "" {
			link = "https://europepmc.org/article/pmc/" + strings.TrimPrefix(item.PMCid, "PMC")
		} else if item.PMID != "" {
			link = "https://europepmc.org/article/med/" + item.PMID
		} else if item.DOI != "" {
			link = "https://doi.org/" + item.DOI
		}

		id := "epmc:" + item.ID
		if item.DOI != "" {
			id = "epmc:" + item.DOI
		}

		year := 0
		if item.PubYear != "" {
			fmt.Sscanf(item.PubYear, "%d", &year)
		}

		papers = append(papers, Paper{
			ID:            id,
			Title:         title,
			Abstract:      strings.TrimSpace(item.AbstractText),
			Link:          link,
			DOI:           item.DOI,
			Source:        "europe_pmc",
			Year:          year,
			CitationCount: item.CitedByCount,
		})
	}

	e.RecordSuccess()
	return papers, nil
}
