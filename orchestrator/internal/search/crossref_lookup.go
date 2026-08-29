package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Crossref is the authority for DOIs and could not resolve one. A DOI lookup
// fell through to Semantic Scholar or, without it, to "no provider found".
// /works/{doi} is an exact-match route, so this is a direct read rather than a
// relevance search over the DOI string.

type crossrefWorkResponse struct {
	Message crossrefWork `json:"message"`
}

// crossrefWorkToPaper converts one work record to a Paper.
func crossrefWorkToPaper(item crossrefWork) Paper {
	title := ""
	if len(item.Title) > 0 {
		title = strings.TrimSpace(item.Title[0])
	}
	link := item.URL
	if link == "" && item.DOI != "" {
		link = "https://doi.org/" + item.DOI
	}
	authors := make([]string, 0, len(item.Author))
	for _, a := range item.Author {
		name := strings.TrimSpace(a.Given + " " + a.Family)
		if name != "" {
			authors = append(authors, name)
		}
	}
	year, month := 0, 0
	if len(item.Published.DateParts) > 0 && len(item.Published.DateParts[0]) > 0 {
		year = item.Published.DateParts[0][0]
		if len(item.Published.DateParts[0]) > 1 {
			month = item.Published.DateParts[0][1]
		}
	}
	venue := ""
	if len(item.ContainerTitle) > 0 {
		venue = item.ContainerTitle[0]
	}
	return Paper{
		ID:            "crossref:" + item.DOI,
		Title:         title,
		Abstract:      stripJATSTags(item.Abstract),
		Link:          link,
		DOI:           item.DOI,
		Source:        "crossref",
		SourceApis:    []string{"crossref"},
		Authors:       authors,
		Year:          year,
		Month:         month,
		Venue:         venue,
		CitationCount: item.IsReferencedByCount,
	}
}

// Tools advertises what this provider implements. BaseProvider returns nil, so
// Crossref previously advertised nothing and was never offered a DOI.
func (c *CrossrefProvider) Tools() []string { return []string{"paper_lookup"} }

// SearchByPaperID resolves a DOI through Crossref's exact /works/{doi} route.
// Anything that is not a DOI is refused here rather than sent upstream.
func (c *CrossrefProvider) SearchByPaperID(ctx context.Context, paperID string) (*Paper, error) {
	doi := ValidDOI(paperID)
	if doi == "" {
		return nil, providerError("crossref", "not a DOI: %q", paperID)
	}

	reqURL := fmt.Sprintf("%s/%s?mailto=%s", c.baseURL, url.PathEscape(doi), url.QueryEscape(c.politePool))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, providerError("crossref", "build request: %v", err)
	}
	req.Header.Set("User-Agent", "ScholarLM/1.0 (mailto:"+c.politePool+")")
	req.Header.Set("Accept", "application/json")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		c.RecordFailure()
		return nil, providerError("crossref", "request failed: %v", err)
	}
	defer resp.Body.Close()

	// 404 is Crossref stating the DOI is not registered. That is a clean
	// answer, not an outage, and must not be reported as a provider failure.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		c.RecordFailure()
		return nil, providerHTTPStatusError("crossref", resp)
	}

	var result crossrefWorkResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.RecordFailure()
		return nil, providerError("crossref", "decode: %v", err)
	}
	if strings.TrimSpace(result.Message.DOI) == "" && len(result.Message.Title) == 0 {
		return nil, nil
	}
	paper := crossrefWorkToPaper(result.Message)
	if strings.TrimSpace(paper.Title) == "" {
		return nil, nil
	}
	c.RecordSuccess()
	return &paper, nil
}
