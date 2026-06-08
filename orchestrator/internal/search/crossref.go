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

// CrossrefProvider searches the Crossref metadata API.
// Free to use with optional polite-pool email. Best for humanities,
// social sciences, and interdisciplinary work. Covers all disciplines
// via DOI metadata.
type CrossrefProvider struct {
	BaseProvider
	baseURL    string
	politePool string // "mailto:contact@example.org" for polite pool
}

var _ SearchProvider = (*CrossrefProvider)(nil)

func NewCrossrefProvider() *CrossrefProvider {
	email := os.Getenv("CROSSREF_POLITE_EMAIL")
	if email == "" {
		email = "api@wisdev.local"
	}
	return &CrossrefProvider{
		baseURL:    "https://api.crossref.org/works",
		politePool: email,
	}
}

func (c *CrossrefProvider) Name() string { return "crossref" }
func (c *CrossrefProvider) Domains() []string {
	return []string{"social", "humanities", "climate", "engineering"}
}

func (c *CrossrefProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 15
	}

	params := url.Values{}
	params.Set("query", query)
	params.Set("rows", fmt.Sprintf("%d", limit))
	params.Set("select", "DOI,title,abstract,author,published,is-referenced-by-count,references-count,container-title,URL,type")
	// Use polite pool for higher rate limits
	params.Set("mailto", c.politePool)

	if opts.YearFrom > 0 {
		params.Set("filter", fmt.Sprintf("from-pub-date:%d", opts.YearFrom))
	}

	reqURL := c.baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		c.RecordFailure()
		return nil, providerError("crossref", "build request: %v", err)
	}
	req.Header.Set("User-Agent", "WisDev/1.0 (mailto:"+c.politePool+")")
	req.Header.Set("Accept", "application/json")

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		c.RecordFailure()
		return nil, providerError("crossref", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.RecordFailure()
		return nil, providerError("crossref", "HTTP %d", resp.StatusCode)
	}

	var result struct {
		Message struct {
			Items []struct {
				DOI      string   `json:"DOI"`
				Title    []string `json:"title"`
				Abstract string   `json:"abstract"`
				URL      string   `json:"URL"`
				Author   []struct {
					Given  string `json:"given"`
					Family string `json:"family"`
				} `json:"author"`
				Published struct {
					DateParts [][]int `json:"date-parts"`
				} `json:"published"`
				ContainerTitle      []string `json:"container-title"`
				IsReferencedByCount int      `json:"is-referenced-by-count"`
				ReferencesCount     int      `json:"references-count"`
				Type                string   `json:"type"`
			} `json:"items"`
		} `json:"message"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.RecordFailure()
		return nil, providerError("crossref", "decode: %v", err)
	}

	papers := make([]Paper, 0, len(result.Message.Items))
	for _, item := range result.Message.Items {
		if len(item.Title) == 0 || item.Title[0] == "" {
			continue
		}
		title := strings.TrimSpace(item.Title[0])

		link := item.URL
		if link == "" && item.DOI != "" {
			link = "https://doi.org/" + item.DOI
		}

		authors := make([]string, 0, len(item.Author))
		for _, a := range item.Author {
			name := strings.TrimSpace(a.Given + " " + a.Family)
			if name != " " {
				authors = append(authors, name)
			}
		}

		year := 0
		month := 0
		if len(item.Published.DateParts) > 0 && len(item.Published.DateParts[0]) > 0 {
			year = item.Published.DateParts[0][0]
			if len(item.Published.DateParts[0]) > 1 {
				month = item.Published.DateParts[0][1]
			}
		}

		// Strip XML tags from abstract (Crossref sometimes returns JATS XML)
		abstract := stripJATSTags(item.Abstract)

		venue := ""
		if len(item.ContainerTitle) > 0 {
			venue = item.ContainerTitle[0]
		}

		papers = append(papers, Paper{
			ID:             "crossref:" + item.DOI,
			Title:          title,
			Abstract:       abstract,
			Link:           link,
			DOI:            item.DOI,
			Source:         "crossref",
			SourceApis:     []string{"crossref"},
			Venue:          venue,
			Authors:        authors,
			Year:           year,
			Month:          month,
			CitationCount:  item.IsReferencedByCount,
			ReferenceCount: item.ReferencesCount,
		})
	}

	c.RecordSuccess()
	return papers, nil
}

// stripJATSTags removes JATS XML markup from Crossref abstracts.
func stripJATSTags(s string) string {
	if !strings.ContainsRune(s, '<') {
		return s
	}
	var out strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(out.String())
}
