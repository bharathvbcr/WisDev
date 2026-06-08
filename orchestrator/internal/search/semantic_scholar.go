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

var newRequestWithContext = http.NewRequestWithContext

const semanticScholarPaperFields = "title,abstract,url,externalIds,authors,year,citationCount,influentialCitationCount,referenceCount,venue,openAccessPdf"
const semanticScholarCitationFields = "citingPaper.title,citingPaper.abstract,citingPaper.url,citingPaper.externalIds,citingPaper.authors,citingPaper.year,citingPaper.citationCount,citingPaper.influentialCitationCount,citingPaper.referenceCount,citingPaper.venue,citingPaper.openAccessPdf"

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
	reqUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/author/%s/papers?limit=%d&fields=%s", url.PathEscape(authorID), limit, semanticScholarPaperFields)
	return s.fetchS2Papers(ctx, reqUrl)
}

func (s *SemanticScholarProvider) SearchByPaperID(ctx context.Context, paperID string) (*Paper, error) {
	// paperID can be S2 ID, DOI, arXiv ID, etc.
	id := strings.TrimPrefix(paperID, "s2:")
	reqUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s?fields=%s", url.PathEscape(id), semanticScholarPaperFields)

	req, err := newRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
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
		if providerHTTPErrorKind(resp) == "rate_limit" {
			return nil, fmt.Errorf("S2 lookup failed: rate limit exceeded (%d)", resp.StatusCode)
		}
		if providerHTTPErrorKind(resp) == "upstream_5xx" {
			return nil, fmt.Errorf("S2 lookup failed: upstream error (%d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("S2 lookup failed: %d", resp.StatusCode)
	}

	var p S2Paper
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("S2 lookup failed to parse response: %w", err)
	}

	paper := mapSemanticScholarPaper(p)
	return &paper, nil
}

func (s *SemanticScholarProvider) GetCitations(ctx context.Context, paperID string, limit int) ([]Paper, error) {
	if limit <= 0 {
		limit = 20
	}
	id := strings.TrimPrefix(paperID, "s2:")
	// We want the papers that CITED this paper (citingPaper fields)
	reqUrl := fmt.Sprintf("https://api.semanticscholar.org/graph/v1/paper/%s/citations?limit=%d&fields=%s", url.PathEscape(id), limit, semanticScholarCitationFields)

	req, err := newRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
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
		if providerHTTPErrorKind(resp) == "rate_limit" {
			return nil, fmt.Errorf("S2 citations failed: rate limit exceeded (%d)", resp.StatusCode)
		}
		if providerHTTPErrorKind(resp) == "upstream_5xx" {
			return nil, fmt.Errorf("S2 citations failed: upstream error (%d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("S2 citations failed: %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			CitingPaper S2Paper `json:"citingPaper"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("S2 citations failed to parse response: %w", err)
	}

	var papers []Paper
	for _, entry := range result.Data {
		papers = append(papers, mapSemanticScholarPaper(entry.CitingPaper))
	}
	return papers, nil
}

func (s *SemanticScholarProvider) fetchS2Papers(ctx context.Context, reqUrl string) ([]Paper, error) {
	req, err := newRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
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
		if providerHTTPErrorKind(resp) == "rate_limit" {
			return nil, fmt.Errorf("S2 request failed: rate limit exceeded (%d)", resp.StatusCode)
		}
		if providerHTTPErrorKind(resp) == "upstream_5xx" {
			return nil, fmt.Errorf("S2 request failed: upstream error (%d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("S2 request failed: %d", resp.StatusCode)
	}

	var s2Res S2Response
	if err := json.NewDecoder(resp.Body).Decode(&s2Res); err != nil {
		return nil, fmt.Errorf("S2 request failed to parse response: %w", err)
	}

	var papers []Paper
	for _, p := range s2Res.Data {
		papers = append(papers, mapSemanticScholarPaper(p))
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
	Year                     int    `json:"year"`
	CitationCount            int    `json:"citationCount"`
	InfluentialCitationCount int    `json:"influentialCitationCount"`
	ReferenceCount           int    `json:"referenceCount"`
	Venue                    string `json:"venue"`
	OpenAccessPdf            *struct {
		URL string `json:"url"`
	} `json:"openAccessPdf"`
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

	reqUrl := fmt.Sprintf("%s?query=%s&limit=%d&fields=%s", s.baseURL, url.QueryEscape(query), limit, semanticScholarPaperFields)

	if opts.YearFrom > 0 {
		if opts.YearTo > 0 {
			reqUrl += fmt.Sprintf("&year=%d-%d", opts.YearFrom, opts.YearTo)
		} else {
			reqUrl += fmt.Sprintf("&year=%d-", opts.YearFrom)
		}
	}

	req, err := newRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
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
		return nil, providerHTTPStatusError("semantic_scholar", resp)
	}

	var s2Res S2Response
	if err := json.NewDecoder(resp.Body).Decode(&s2Res); err != nil {
		s.RecordFailure()
		return nil, providerError("semantic_scholar", "failed to parse response: %v", err)
	}

	var papers []Paper
	for _, p := range s2Res.Data {
		papers = append(papers, mapSemanticScholarPaper(p))
	}

	s.RecordSuccess()
	return papers, nil
}

func mapSemanticScholarPaper(p S2Paper) Paper {
	authors := make([]string, 0, len(p.Authors))
	for _, a := range p.Authors {
		authors = append(authors, strings.TrimSpace(a.Name))
	}

	oaUrl := ""
	if p.OpenAccessPdf != nil {
		oaUrl = p.OpenAccessPdf.URL
	}

	return Paper{
		ID:                       "s2:" + p.PaperID,
		Title:                    p.Title,
		Abstract:                 p.Abstract,
		Link:                     p.URL,
		DOI:                      p.ExternalIds.DOI,
		Source:                   "semantic_scholar",
		SourceApis:               []string{"semantic_scholar"},
		Authors:                  authors,
		Year:                     p.Year,
		Venue:                    p.Venue,
		CitationCount:            p.CitationCount,
		InfluentialCitationCount: p.InfluentialCitationCount,
		ReferenceCount:           p.ReferenceCount,
		OpenAccessUrl:            oaUrl,
		PdfUrl:                   oaUrl,
	}
}
