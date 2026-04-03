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

type PubMedProvider struct {
	BaseProvider
	searchURL  string
	summaryURL string
}

var _ SearchProvider = (*PubMedProvider)(nil)

func NewPubMedProvider() *PubMedProvider {
	return &PubMedProvider{
		searchURL:  "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi",
		summaryURL: "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esummary.fcgi",
	}
}

func (p *PubMedProvider) Name() string { return "pubmed" }

func (p *PubMedProvider) Domains() []string {
	return []string{"medicine", "biology", "neuro"}
}

type PubMedSearchResponse struct {
	EsearchResult struct {
		IdList []string `json:"idlist"`
	} `json:"esearchresult"`
}

type PubMedSummaryResponse struct {
	Result map[string]struct {
		Title      string `json:"title"`
		ArticleIds []struct {
			IdType string `json:"idtype"`
			Value  string `json:"value"`
		} `json:"articleids"`
		PubDate string `json:"pubdate"`
	} `json:"result"`
}

func (p *PubMedProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	searchTerm := query
	if opts.YearFrom > 0 {
		if opts.YearTo > 0 {
			searchTerm += fmt.Sprintf(" AND %d:%d[pdat]", opts.YearFrom, opts.YearTo)
		} else {
			searchTerm += fmt.Sprintf(" AND %d:3000[pdat]", opts.YearFrom)
		}
	}

	searchUrl := fmt.Sprintf("%s?db=pubmed&term=%s&retmax=%d&retmode=json", p.searchURL, url.QueryEscape(searchTerm), limit)
	req1, err := http.NewRequestWithContext(ctx, http.MethodGet, searchUrl, nil)
	if err != nil {
		p.RecordFailure()
		return nil, providerError("pubmed", "build search request: %v", err)
	}

	resp1, err := SharedHTTPClient.Do(req1)
	if err != nil {
		p.RecordFailure()
		return nil, providerError("pubmed", "search request failed: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		p.RecordFailure()
		return nil, providerError("pubmed", "search HTTP %d", resp1.StatusCode)
	}

	var searchRes PubMedSearchResponse
	if err := json.NewDecoder(resp1.Body).Decode(&searchRes); err != nil {
		p.RecordFailure()
		return nil, providerError("pubmed", "decode search: %v", err)
	}

	ids := searchRes.EsearchResult.IdList
	if len(ids) == 0 {
		p.RecordSuccess()
		return []Paper{}, nil
	}

	idString := strings.Join(ids, ",")
	summaryUrl := fmt.Sprintf("%s?db=pubmed&id=%s&retmode=json", p.summaryURL, idString)
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, summaryUrl, nil)
	if err != nil {
		p.RecordFailure()
		return nil, providerError("pubmed", "build summary request: %v", err)
	}

	resp2, err := SharedHTTPClient.Do(req2)
	if err != nil {
		p.RecordFailure()
		return nil, providerError("pubmed", "summary request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		p.RecordFailure()
		return nil, providerError("pubmed", "summary HTTP %d", resp2.StatusCode)
	}

	var summaryRes PubMedSummaryResponse
	if err := json.NewDecoder(resp2.Body).Decode(&summaryRes); err != nil {
		p.RecordFailure()
		return nil, providerError("pubmed", "decode summary: %v", err)
	}

	var papers []Paper
	for _, id := range ids {
		if data, ok := summaryRes.Result[id]; ok {
			var doi string
			for _, aid := range data.ArticleIds {
				if aid.IdType == "doi" {
					doi = aid.Value
					break
				}
			}

			// PubDate parsing could be added if needed, kept simple for now

			papers = append(papers, Paper{
				ID:       "pubmed:" + id,
				Title:    data.Title,
				Abstract: "", // esummary doesn't return full abstract, need efetch
				Link:     fmt.Sprintf("https://pubmed.ncbi.nlm.nih.gov/%s/", id),
				DOI:      doi,
				Source:   "pubmed",
			})
		}
	}

	p.RecordSuccess()
	return papers, nil
}
