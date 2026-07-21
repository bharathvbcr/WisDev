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
	ID                    string           `json:"id"`
	Title                 string           `json:"title"`
	DOI                   string           `json:"doi"`
	AbstractInvertedIndex map[string][]int `json:"abstract_inverted_index"`
	PublicationYear       int              `json:"publication_year"`
	CitedByCount          int              `json:"cited_by_count"`
	ReferencedWorksCount  int              `json:"referenced_works_count"`
	PrimaryLocation       struct {
		LandingPageURL string `json:"landing_page_url"`
		PDFURL         string `json:"pdf_url"`
		Source         struct {
			DisplayName string `json:"display_name"`
		} `json:"source"`
	} `json:"primary_location"`
	OpenAccess struct {
		IsOA  bool   `json:"is_oa"`
		OAURL string `json:"oa_url"`
	} `json:"open_access"`
	Authorships []struct {
		Author struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"author"`
		RawAuthorName string `json:"raw_author_name"`
	} `json:"authorships"`
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

	// OpenAlex expects a single comma-separated filter= clause; accumulate then join.
	var filters []string
	if opts.YearFrom > 0 {
		if opts.YearTo > 0 {
			filters = append(filters, fmt.Sprintf("publication_year:%d-%d", opts.YearFrom, opts.YearTo))
		} else {
			filters = append(filters, fmt.Sprintf("publication_year:>%d", opts.YearFrom-1))
		}
	}
	if opts.OpenAccess {
		filters = append(filters, "is_oa:true")
	}
	if len(filters) > 0 {
		reqUrl += "&filter=" + strings.Join(filters, ",")
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
		return nil, providerHTTPStatusError("openalex", resp)
	}

	var oaRes OpenAlexResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaRes); err != nil {
		o.RecordFailure()
		return nil, providerError("openalex", "failed to parse response: %v", err)
	}

	papers := make([]Paper, 0, len(oaRes.Results))
	for _, w := range oaRes.Results {
		papers = append(papers, o.mapWork(w))
	}

	o.RecordSuccess()
	return papers, nil
}

// mapWork converts a single OpenAlex work into the unified Paper model. Shared by
// Search and GetByDOI so provider mapping stays DRY.
func (o *OpenAlexProvider) mapWork(w OpenAlexWork) Paper {
	sourceName := w.PrimaryLocation.Source.DisplayName
	if sourceName == "" {
		sourceName = "OpenAlex"
	}
	authors := make([]string, 0, len(w.Authorships))
	for _, authorship := range w.Authorships {
		name := strings.TrimSpace(authorship.Author.DisplayName)
		if name == "" {
			name = strings.TrimSpace(authorship.RawAuthorName)
		}
		if name == "" {
			continue
		}
		authors = append(authors, name)
	}
	pdfURL := strings.TrimSpace(w.PrimaryLocation.PDFURL)
	oaURL := ""
	if w.OpenAccess.IsOA && w.OpenAccess.OAURL != "" {
		oaURL = strings.TrimSpace(w.OpenAccess.OAURL)
	}
	link := strings.TrimSpace(w.PrimaryLocation.LandingPageURL)
	if link == "" {
		link = strings.TrimSpace(w.DOI)
	}
	if link == "" {
		link = strings.TrimSpace(w.ID)
	}
	if pdfURL == "" {
		pdfURL = oaURL
	}
	return Paper{
		ID:             "openalex:" + strings.TrimPrefix(w.ID, "https://openalex.org/"),
		Title:          w.Title,
		Abstract:       reconstructOpenAlexAbstract(w.AbstractInvertedIndex),
		Link:           link,
		DOI:            strings.TrimPrefix(w.DOI, "https://doi.org/"),
		Source:         "openalex",
		SourceApis:     []string{"openalex"},
		Authors:        authors,
		Venue:          sourceName,
		Year:           w.PublicationYear,
		CitationCount:  w.CitedByCount,
		ReferenceCount: w.ReferencedWorksCount,
		OpenAccessUrl:  oaURL,
		PdfUrl:         pdfURL,
	}
}

// GetByDOI resolves a single work by DOI via OpenAlex's /works/doi:<doi> endpoint.
// Canonical Go owner of provider DOI resolution — migrated from the frontend
// openAlexService.getOpenAlexByDoi during frontend thinning. Returns (nil, nil) when
// no work matches the DOI.
func (o *OpenAlexProvider) GetByDOI(ctx context.Context, doi string) (*Paper, error) {
	doi = strings.TrimSpace(doi)
	if doi == "" {
		return nil, providerError("openalex", "doi is required")
	}
	// Accept a bare DOI, a doi: prefix, or a full doi.org URL.
	doi = strings.TrimPrefix(doi, "https://doi.org/")
	doi = strings.TrimPrefix(doi, "http://doi.org/")
	doi = strings.TrimPrefix(doi, "doi:")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	reqUrl := fmt.Sprintf("%s/doi:%s", o.baseURL, url.PathEscape(doi))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		o.RecordFailure()
		return nil, providerError("openalex", "build request: %v", err)
	}
	if email := os.Getenv("OPENALEX_EMAIL"); email != "" {
		req.URL.RawQuery = "mailto=" + url.QueryEscape(email)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		o.RecordFailure()
		return nil, providerError("openalex", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		o.RecordSuccess()
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		o.RecordFailure()
		return nil, providerHTTPStatusError("openalex", resp)
	}

	var w OpenAlexWork
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		o.RecordFailure()
		return nil, providerError("openalex", "failed to parse response: %v", err)
	}
	paper := o.mapWork(w)
	o.RecordSuccess()
	return &paper, nil
}

func reconstructOpenAlexAbstract(index map[string][]int) string {
	if len(index) == 0 {
		return ""
	}

	maxPosition := -1
	for _, positions := range index {
		for _, position := range positions {
			if position > maxPosition {
				maxPosition = position
			}
		}
	}
	if maxPosition < 0 {
		return ""
	}

	words := make([]string, maxPosition+1)
	for word, positions := range index {
		normalizedWord := strings.TrimSpace(word)
		if normalizedWord == "" {
			continue
		}
		for _, position := range positions {
			if position < 0 || position >= len(words) || words[position] != "" {
				continue
			}
			words[position] = normalizedWord
		}
	}

	compact := words[:0]
	for _, word := range words {
		if word != "" {
			compact = append(compact, word)
		}
	}
	return strings.Join(compact, " ")
}
