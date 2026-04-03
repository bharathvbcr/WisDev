package search

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ArXivProvider struct {
	BaseProvider
	baseURL string
}

var _ SearchProvider = (*ArXivProvider)(nil)

func NewArXivProvider() *ArXivProvider {
	return &ArXivProvider{
		baseURL: "http://export.arxiv.org/api/query",
	}
}

func (a *ArXivProvider) Name() string { return "arxiv" }

func (a *ArXivProvider) Domains() []string {
	return []string{"cs", "physics", "math", "ai", "engineering"}
}

type arxivLink struct {
	Href  string `xml:"href,attr"`
	Rel   string `xml:"rel,attr"`
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr"`
}

type arxivEntry struct {
	ID        string      `xml:"id"`
	Updated   string      `xml:"updated"`
	Published string      `xml:"published"`
	Title     string      `xml:"title"`
	Summary   string      `xml:"summary"`
	Authors   []string    `xml:"author>name"`
	Links     []arxivLink `xml:"link"`
}

type arxivFeed struct {
	XMLName xml.Name     `xml:"feed"`
	Entries []arxivEntry `xml:"entry"`
}

func (a *ArXivProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Paper, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	searchQuery := query
	// arXiv doesn't have a strict year filter in its simple syntax, but we can do a hacky exact string or just skip filtering here and rely on the query text if needed. For now, we will add it to the search query if specified.
	if opts.YearFrom > 0 {
		// A rudimentary way to search for years in arXiv is to append the year to the query if it's a specific year,
		// but since it's a range, it's better handled client side or using advanced querying which is out of scope for a basic implementation.
	}

	reqURL := fmt.Sprintf("%s?search_query=all:%s&start=0&max_results=%d", a.baseURL, url.QueryEscape(searchQuery), limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		a.RecordFailure()
		return nil, providerError("arxiv", "build request: %v", err)
	}

	resp, err := SharedHTTPClient.Do(req)
	if err != nil {
		a.RecordFailure()
		return nil, providerError("arxiv", "request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		a.RecordFailure()
		return nil, providerError("arxiv", "HTTP %d", resp.StatusCode)
	}

	var feed arxivFeed
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		a.RecordFailure()
		return nil, providerError("arxiv", "decode: %v", err)
	}

	var papers []Paper
	for _, entry := range feed.Entries {
		pdfLink := ""
		for _, link := range entry.Links {
			if link.Type == "application/pdf" || link.Title == "pdf" {
				pdfLink = link.Href
				break
			}
		}
		if pdfLink == "" {
			pdfLink = entry.ID
		}

		year := 0
		if len(entry.Published) >= 4 {
			fmt.Sscanf(entry.Published[:4], "%d", &year)
		}

		authors := make([]string, 0, len(entry.Authors))
		for _, author := range entry.Authors {
			authors = append(authors, strings.TrimSpace(author))
		}

		arxivID := extractArXivID(entry.ID)

		papers = append(papers, Paper{
			ID:       "arxiv:" + arxivID,
			Title:    collapseWhitespace(entry.Title),
			Abstract: collapseWhitespace(entry.Summary),
			Link:     pdfLink,
			Source:   "arxiv",
			Authors:  authors,
			Year:     year,
		})
	}

	a.RecordSuccess()
	return papers, nil
}

func extractArXivID(rawURL string) string {
	id := rawURL
	if idx := strings.LastIndex(rawURL, "/abs/"); idx != -1 {
		id = rawURL[idx+5:]
	} else if idx := strings.LastIndex(rawURL, "/"); idx != -1 {
		id = rawURL[idx+1:]
	}
	if vIdx := strings.LastIndex(id, "v"); vIdx > 0 {
		candidate := id[vIdx+1:]
		allDigits := true
		for _, c := range candidate {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits && len(candidate) > 0 {
			id = id[:vIdx]
		}
	}
	return id
}

func collapseWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
