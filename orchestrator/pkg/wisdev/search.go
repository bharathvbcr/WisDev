package wisdev

import (
	"context"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

// Paper is the public paper model returned by custom search providers and
// surfaced in YOLO results.
type Paper struct {
	ID                       string   `json:"id"`
	Title                    string   `json:"title"`
	Abstract                 string   `json:"abstract,omitempty"`
	Link                     string   `json:"link,omitempty"`
	DOI                      string   `json:"doi,omitempty"`
	ArxivID                  string   `json:"arxivId,omitempty"`
	Source                   string   `json:"source,omitempty"`
	SourceAPIs               []string `json:"sourceApis,omitempty"`
	Authors                  []string `json:"authors,omitempty"`
	Year                     int      `json:"year,omitempty"`
	Month                    int      `json:"month,omitempty"`
	Venue                    string   `json:"venue,omitempty"`
	Keywords                 []string `json:"keywords,omitempty"`
	CitationCount            int      `json:"citationCount,omitempty"`
	ReferenceCount           int      `json:"referenceCount,omitempty"`
	InfluentialCitationCount int      `json:"influentialCitationCount,omitempty"`
	OpenAccessURL            string   `json:"openAccessUrl,omitempty"`
	PDFURL                   string   `json:"pdfUrl,omitempty"`
	Score                    float64  `json:"score,omitempty"`
	EvidenceLevel            string   `json:"evidenceLevel,omitempty"`
	FullText                 string   `json:"fullText,omitempty"`
}

// SearchOptions carries the public subset of search controls passed to custom
// providers.
type SearchOptions struct {
	UserID      string
	Limit       int
	Domain      string
	Sources     []string
	YearFrom    int
	YearTo      int
	SkipCache   bool
	QualitySort bool
	TraceID     string
}

// SearchProvider lets embedders supply retrieval without depending on
// orchestrator/internal/search.
type SearchProvider interface {
	Name() string
	Search(ctx context.Context, query string, opts SearchOptions) ([]Paper, error)
	Domains() []string
}

type searchProviderAdapter struct {
	provider SearchProvider
}

func (a searchProviderAdapter) Name() string {
	return a.provider.Name()
}

func (a searchProviderAdapter) Search(ctx context.Context, query string, opts search.SearchOpts) ([]search.Paper, error) {
	papers, err := a.provider.Search(ctx, query, SearchOptions{
		UserID:      opts.UserID,
		Limit:       opts.Limit,
		Domain:      opts.Domain,
		Sources:     append([]string(nil), opts.Sources...),
		YearFrom:    opts.YearFrom,
		YearTo:      opts.YearTo,
		SkipCache:   opts.SkipCache,
		QualitySort: opts.QualitySort,
		TraceID:     opts.TraceID,
	})
	if err != nil {
		return nil, err
	}
	return toInternalPapers(papers), nil
}

func (a searchProviderAdapter) Domains() []string {
	return append([]string(nil), a.provider.Domains()...)
}

func (a searchProviderAdapter) Healthy() bool {
	return true
}

func (a searchProviderAdapter) Tools() []string {
	return nil
}

func toInternalPapers(papers []Paper) []search.Paper {
	if len(papers) == 0 {
		return nil
	}
	converted := make([]search.Paper, 0, len(papers))
	for _, paper := range papers {
		converted = append(converted, search.Paper{
			ID:                       paper.ID,
			Title:                    paper.Title,
			Abstract:                 paper.Abstract,
			Link:                     paper.Link,
			DOI:                      paper.DOI,
			ArxivID:                  paper.ArxivID,
			Source:                   paper.Source,
			SourceApis:               append([]string(nil), paper.SourceAPIs...),
			Authors:                  append([]string(nil), paper.Authors...),
			Year:                     paper.Year,
			Month:                    paper.Month,
			Venue:                    paper.Venue,
			Keywords:                 append([]string(nil), paper.Keywords...),
			CitationCount:            paper.CitationCount,
			ReferenceCount:           paper.ReferenceCount,
			InfluentialCitationCount: paper.InfluentialCitationCount,
			OpenAccessUrl:            paper.OpenAccessURL,
			PdfUrl:                   paper.PDFURL,
			Score:                    paper.Score,
			EvidenceLevel:            paper.EvidenceLevel,
			FullText:                 paper.FullText,
		})
	}
	return converted
}

func fromInternalPapers(papers []search.Paper) []Paper {
	if len(papers) == 0 {
		return nil
	}
	converted := make([]Paper, 0, len(papers))
	for _, paper := range papers {
		converted = append(converted, Paper{
			ID:                       paper.ID,
			Title:                    paper.Title,
			Abstract:                 paper.Abstract,
			Link:                     paper.Link,
			DOI:                      paper.DOI,
			ArxivID:                  paper.ArxivID,
			Source:                   paper.Source,
			SourceAPIs:               append([]string(nil), paper.SourceApis...),
			Authors:                  append([]string(nil), paper.Authors...),
			Year:                     paper.Year,
			Month:                    paper.Month,
			Venue:                    paper.Venue,
			Keywords:                 append([]string(nil), paper.Keywords...),
			CitationCount:            paper.CitationCount,
			ReferenceCount:           paper.ReferenceCount,
			InfluentialCitationCount: paper.InfluentialCitationCount,
			OpenAccessURL:            paper.OpenAccessUrl,
			PDFURL:                   paper.PdfUrl,
			Score:                    paper.Score,
			EvidenceLevel:            paper.EvidenceLevel,
			FullText:                 paper.FullText,
		})
	}
	return converted
}
