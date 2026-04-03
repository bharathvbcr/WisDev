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

type SemanticScholarProvider struct {
	BaseProvider
	baseURL string
}

var _ SearchProvider = (*SemanticScholarProvider)(nil)

func NewSemanticScholarProvider() *SemanticScholarProvider {
	return &SemanticScholarProvider{
		baseURL: "https://api.semanticscholar.org/graph/v1/paper/search",
	}
}

func (s *SemanticScholarProvider) Name() string { return "semantic_scholar" }

func (s *SemanticScholarProvider) Tools() []string {
	return []string{"author_lookup", "paper_lookup"}
}

func (s *SemanticScholarProvider) Domains() []string {
	return []string{"medicine", "cs", "ai", "social", "physics", "engineering", "humanities", "biology", "neuro"}
}

// ... (existing S2Paper, S2Response, and Search method)

func (s *SemanticScholarProvider) SearchByAuthor(ctx context.Context, authorID string, limit int) ([]Paper, error) {
	if limit <= 0 {
		limit = 20
	}
	// authorID can be S2 Author ID or name (though ID is better)
	reqUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/author/%s/papers?limit=%d&fields=title,abstract,url,externalIds,authors,year,citationCount", url.PathEscape(authorID), limit)
	return s.fetchS2Papers(ctx, reqUrl)
}

func (s *SemanticScholarProvider) SearchByPaperID(ctx context.Context, paperID string) (*Paper, error) {
	// paperID can be S2 ID, DOI, arXiv ID, etc.
	id := strings.TrimPrefix(paperID, "s2:")
	reqUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s?fields=title,abstract,url,externalIds,authors,year,citationCount", url.PathEscape(id))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		return nil, err
	}

	apiKey := os.Getenv("SEMANTIC_SCHOLAR_API_KEY")
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("S2 lookup failed: %d", resp.StatusCode)
	}

	var p S2Paper
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}

	authors := make([]string, 0, len(p.Authors))
	for _, a := range p.Authors {
		authors = append(authors, strings.TrimSpace(a.Name))
	}

	return &Paper{
		ID:            "s2:" + p.PaperID,
		Title:         p.Title,
		Abstract:      p.Abstract,
		Link:          p.URL,
		DOI:           p.ExternalIds.DOI,
		Source:        "semantic_scholar",
		Authors:       authors,
		Year:          p.Year,
		CitationCount: p.CitationCount,
	}, nil
}

func (s *SemanticScholarProvider) GetCitations(ctx context.Context, paperID string, limit int) ([]Paper, error) {
	if limit <= 0 {
		limit = 20
	}
	id := strings.TrimPrefix(paperID, "s2:")
	// We want the papers that CITED this paper (citingPaper fields)
	reqUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s/citations?limit=%d&fields=citingPaper.title,citingPaper.abstract,citingPaper.url,citingPaper.externalIds,citingPaper.authors,citingPaper.year,citingPaper.citationCount", url.PathEscape(id), limit)
	
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		return nil, err
	}

	apiKey := os.Getenv("SEMANTIC_SCHOLAR_API_KEY")
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("S2 citations failed: %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			CitingPaper S2Paper `json:"citingPaper"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var papers []Paper
	for _, entry := range result.Data {
		p := entry.CitingPaper
		authors := make([]string, 0, len(p.Authors))
		for _, a := range p.Authors {
			authors = append(authors, strings.TrimSpace(a.Name))
		}

		papers = append(papers, Paper{
			ID:            "s2:" + p.PaperID,
			Title:         p.Title,
			Abstract:      p.Abstract,
			Link:          p.URL,
			DOI:           p.ExternalIds.DOI,
			Source:        "semantic_scholar",
			Authors:       authors,
			Year:          p.Year,
			CitationCount: p.CitationCount,
		})
	}
	return papers, nil
}

func (s *SemanticScholarProvider) fetchS2Papers(ctx context.Context, reqUrl string) ([]Paper, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		return nil, err
	}

	apiKey := os.Getenv("SEMANTIC_SCHOLAR_API_KEY")
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("S2 request failed: %d", resp.StatusCode)
	}

	var s2Res S2Response
	if err := json.NewDecoder(resp.Body).Decode(&s2Res); err != nil {
		return nil, err
	}

	var papers []Paper
	for _, p := range s2Res.Data {
		authors := make([]string, 0, len(p.Authors))
		for _, a := range p.Authors {
			authors = append(authors, strings.TrimSpace(a.Name))
		}

		papers = append(papers, Paper{
			ID:            "s2:" + p.PaperID,
			Title:         p.Title,
			Abstract:      p.Abstract,
			Link:          p.URL,
			DOI:           p.ExternalIds.DOI,
			Source:        "semantic_scholar",
			Authors:       authors,
			Year:          p.Year,
			CitationCount: p.CitationCount,
		})
	}
	return papers, nil
}

type S2Paper struct {
	PaperID     string `json:"paperId"`
	Title       string `json:"title"`
	Abstract    string `json:"abstract"`
	URL         string `json:"url"`
	ExternalIds struct {
		DOI string `json:"DOI"`
	} `json:"externalIds"`
	Authors []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Year          int `json:"year"`
	CitationCount int `json:"citationCount"`
}

type S2Response struct {
	Data []S2Paper `json:"data"`
}

func (s *SemanticScholarProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	reqUrl := fmt.Sprintf("%s?query=%s&limit=%d&fields=title,abstract,url,externalIds,authors,year,citationCount", s.baseURL, url.QueryEscape(query), limit)

	if opts.YearFrom > 0 {
		if opts.YearTo > 0 {
			reqUrl += fmt.Sprintf("&year=%d-%d", opts.YearFrom, opts.YearTo)
		} else {
			reqUrl += fmt.Sprintf("&year=%d-", opts.YearFrom)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		s.RecordFailure()
		return nil, providerError("semantic_scholar", "build request: %v", err)
	}

	apiKey := os.Getenv("SEMANTIC_SCHOLAR_API_KEY")
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		s.RecordFailure()
		return nil, providerError("semantic_scholar", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.RecordFailure()
		return nil, providerError("semantic_scholar", "HTTP %d", resp.StatusCode)
	}

	var s2Res S2Response
	if err := json.NewDecoder(resp.Body).Decode(&s2Res); err != nil {
		s.RecordFailure()
		return nil, providerError("semantic_scholar", "decode: %v", err)
	}

	var papers []Paper
	for _, p := range s2Res.Data {
		authors := make([]string, 0, len(p.Authors))
		for _, a := range p.Authors {
			authors = append(authors, strings.TrimSpace(a.Name))
		}

		papers = append(papers, Paper{
			ID:            "s2:" + p.PaperID,
			Title:         p.Title,
			Abstract:      p.Abstract,
			Link:          p.URL,
			DOI:           p.ExternalIds.DOI,
			Source:        "semantic_scholar",
			Authors:       authors,
			Year:          p.Year,
			CitationCount: p.CitationCount,
		})
	}

	s.RecordSuccess()
	return papers, nil
}
