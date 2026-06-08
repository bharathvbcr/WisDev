package search

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type PubMedProvider struct {
	BaseProvider
	searchURL  string
	summaryURL string
	fetchURL   string
}

var _ SearchProvider = (*PubMedProvider)(nil)

func NewPubMedProvider() *PubMedProvider {
	return &PubMedProvider{
		searchURL:  "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi",
		summaryURL: "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esummary.fcgi",
		fetchURL:   "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/efetch.fcgi",
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
		Title           string `json:"title"`
		Source          string `json:"source"`
		FullJournalName string `json:"fulljournalname"`
		Authors         []struct {
			Name string `json:"name"`
		} `json:"authors"`
		ArticleIds []struct {
			IdType string `json:"idtype"`
			Value  string `json:"value"`
		} `json:"articleids"`
		PubDate string `json:"pubdate"`
	} `json:"result"`
}

type pubMedFetchResponse struct {
	Articles []struct {
		MedlineCitation struct {
			PMID struct {
				Text string `xml:",chardata"`
			} `xml:"PMID"`
			Article struct {
				Abstract struct {
					Texts []struct {
						Label string `xml:"Label,attr"`
						Text  string `xml:",chardata"`
					} `xml:"AbstractText"`
				} `xml:"Abstract"`
			} `xml:"Article"`
		} `xml:"MedlineCitation"`
	} `xml:"PubmedArticle"`
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

	abstracts, err := p.fetchAbstracts(ctx, ids)
	if err != nil {
		slog.Warn("pubmed abstract fetch failed; continuing with summary metadata",
			"service", "go_orchestrator",
			"runtime", "go",
			"component", "search_pubmed",
			"operation", "fetch_abstracts",
			"stage", "pubmed_efetch_failed",
			"provider", "pubmed",
			"result", "degraded",
			"error_code", "PUBMED_ABSTRACT_FETCH_FAILED",
			"error", err,
		)
		abstracts = map[string]string{}
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

			authors := make([]string, 0, len(data.Authors))
			seenAuthors := make(map[string]struct{}, len(data.Authors))
			for _, author := range data.Authors {
				authors = appendUniqueAuthor(authors, seenAuthors, author.Name)
			}

			year := 0
			if data.PubDate != "" {
				fmt.Sscanf(data.PubDate, "%d", &year)
			}

			venue := strings.TrimSpace(data.FullJournalName)
			if venue == "" {
				venue = strings.TrimSpace(data.Source)
			}

			papers = append(papers, Paper{
				ID:         "pubmed:" + id,
				Title:      data.Title,
				Abstract:   abstracts[id],
				Link:       fmt.Sprintf("https://pubmed.ncbi.nlm.nih.gov/%s/", id),
				DOI:        doi,
				Source:     "pubmed",
				SourceApis: []string{"pubmed"},
				Authors:    authors,
				Year:       year,
				Venue:      venue,
			})
		}
	}

	p.RecordSuccess()
	return papers, nil
}

func (p *PubMedProvider) fetchAbstracts(ctx context.Context, ids []string) (map[string]string, error) {
	abstracts := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return abstracts, nil
	}

	idString := strings.Join(ids, ",")
	fetchURL := fmt.Sprintf("%s?db=pubmed&id=%s&retmode=xml", p.fetchURL, url.QueryEscape(idString))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return abstracts, providerError("pubmed", "build abstract fetch request: %v", err)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return abstracts, providerError("pubmed", "abstract fetch request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return abstracts, providerError("pubmed", "abstract fetch HTTP %d", resp.StatusCode)
	}

	var fetchRes pubMedFetchResponse
	if err := xml.NewDecoder(resp.Body).Decode(&fetchRes); err != nil {
		return abstracts, providerError("pubmed", "decode abstract fetch: %v", err)
	}

	for _, article := range fetchRes.Articles {
		id := strings.TrimSpace(article.MedlineCitation.PMID.Text)
		if id == "" {
			continue
		}
		parts := make([]string, 0, len(article.MedlineCitation.Article.Abstract.Texts))
		for _, abstractText := range article.MedlineCitation.Article.Abstract.Texts {
			text := strings.Join(strings.Fields(abstractText.Text), " ")
			if text == "" {
				continue
			}
			label := strings.TrimSpace(abstractText.Label)
			if label != "" {
				text = label + ": " + text
			}
			parts = append(parts, text)
		}
		if len(parts) > 0 {
			abstracts[id] = strings.Join(parts, " ")
		}
	}

	return abstracts, nil
}
