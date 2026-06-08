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
	// arXiv supports date-range filtering via submittedDate Lucene field.
	// Format: submittedDate:[YYYYMMDDHHMMSS TO YYYYMMDDHHMMSS]
	// We pass it as part of the search_query parameter (will be URL-encoded).
	if opts.YearFrom > 0 {
		yearTo := opts.YearTo
		if yearTo <= 0 {
			yearTo = time.Now().Year()
		}
		searchQuery = fmt.Sprintf("(%s) AND submittedDate:[%d0101000000 TO %d1231235959]",
			searchQuery, opts.YearFrom, yearTo)
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
		return nil, providerHTTPStatusError("arxiv", resp)
	}

	var feed arxivFeed
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		a.RecordFailure()
		return nil, providerError("arxiv", "failed to parse response: %v", err)
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
		month := 0
		if len(entry.Published) >= 4 {
			fmt.Sscanf(entry.Published[:4], "%d", &year)
		}
		if len(entry.Published) >= 7 {
			fmt.Sscanf(entry.Published[5:7], "%d", &month)
		}

		authors := make([]string, 0, len(entry.Authors))
		for _, author := range entry.Authors {
			authors = append(authors, strings.TrimSpace(author))
		}

		arxivID := extractArXivID(entry.ID)

		papers = append(papers, Paper{
			ID:            "arxiv:" + arxivID,
			Title:         collapseWhitespace(entry.Title),
			Abstract:      collapseWhitespace(entry.Summary),
			Link:          pdfLink,
			Source:        "arxiv",
			SourceApis:    []string{"arxiv"},
			Authors:       authors,
			Year:          year,
			Month:         month,
			OpenAccessUrl: pdfLink,
			PdfUrl:        pdfLink,
		})
	}

	a.RecordSuccess()

	// Client-side post-filter: drop any papers outside the requested year range.
	// This catches cases where the API date filter is inexact or returns stale results.
	if opts.YearFrom > 0 || opts.YearTo > 0 {
		filtered := papers[:0]
		for _, p := range papers {
			if opts.YearFrom > 0 && p.Year > 0 && p.Year < opts.YearFrom {
				continue
			}
			if opts.YearTo > 0 && p.Year > 0 && p.Year > opts.YearTo {
				continue
			}
			filtered = append(filtered, p)
		}
		papers = filtered
	}

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
