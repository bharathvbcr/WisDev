package search

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// arXiv is the authority for arXiv identifiers, and until now it could not
// resolve one. Only Semantic Scholar implemented PaperLookupProvider, so a
// registry configured `--provider openalex,arxiv` answered every arXiv ID with
// "no provider found for paper_lookup" -- a registry that holds the owning
// authority reporting the capability as absent.

// arxivEntryToPaper converts one Atom entry to a Paper. Extracted from Search
// so lookup and search cannot drift in how they read the same feed.
func arxivEntryToPaper(entry arxivEntry) Paper {
	title := collapseWhitespace(entry.Title)
	abstract := collapseWhitespace(entry.Summary)

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

	year, month := 0, 0
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
	return Paper{
		ID:            "arxiv:" + arxivID,
		ArxivID:       arxivID,
		Title:         title,
		Abstract:      abstract,
		Link:          pdfLink,
		Source:        "arxiv",
		SourceApis:    []string{"arxiv"},
		Authors:       authors,
		Year:          year,
		Month:         month,
		OpenAccessUrl: pdfLink,
		PdfUrl:        pdfLink,
	}
}

// Tools advertises the capabilities this provider actually implements.
// BaseProvider returns nil, so arXiv previously advertised none.
func (a *ArXivProvider) Tools() []string { return []string{"paper_lookup"} }

// SearchByPaperID resolves an arXiv identifier through arXiv's own id_list
// query, which is an exact-match lookup rather than a relevance search.
//
// A bare ID ("2401.07324", with or without a version suffix) and the legacy
// scheme ("cs.CL/012345") are both accepted; anything else is refused as
// not-our-identifier rather than sent upstream to fail.
func (a *ArXivProvider) SearchByPaperID(ctx context.Context, paperID string) (*Paper, error) {
	id := NormalizeArxivID(paperID)
	if id == "" {
		return nil, providerError("arxiv", "not an arXiv identifier: %q", paperID)
	}

	endpoint := fmt.Sprintf("%s?id_list=%s&max_results=1", a.baseURL, url.QueryEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
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
	if len(feed.Entries) == 0 {
		// A well-formed response holding nothing is a miss, not a failure.
		return nil, nil
	}
	// arXiv answers a withdrawn or unknown id_list with a placeholder entry
	// whose id is the query URL and whose title is "Error". Treating that as a
	// paper would report an error page as a resolved citation.
	if strings.EqualFold(strings.TrimSpace(feed.Entries[0].Title), "Error") {
		return nil, nil
	}

	paper := arxivEntryToPaper(feed.Entries[0])
	if strings.TrimSpace(paper.Title) == "" {
		return nil, nil
	}
	a.RecordSuccess()
	return &paper, nil
}
