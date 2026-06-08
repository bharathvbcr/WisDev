package wisdev

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

// ──────────────────────────────────────────────
// ADK ↔ MCP bridge — Function-tool wrappers
//
// Uses the canonical ADK Go API:
//   functiontool.New(functiontool.Config{Name, Description}, func(adktool.Context, In) (Out, error))
// ──────────────────────────────────────────────

// MCPADKBridge holds ADK function-tool wrappers backed by the MCPServer.
type MCPADKBridge struct {
	server *MCPServer
}

// NewMCPADKBridge constructs a bridge backed by the given search registry.
func NewMCPADKBridge(registry *search.ProviderRegistry) *MCPADKBridge {
	return &MCPADKBridge{server: NewMCPServer(registry)}
}

// BuildADKTools returns all MCP-backed tools as ADK-compatible adktool.Tool values.
func (b *MCPADKBridge) BuildADKTools() []adktool.Tool {
	return []adktool.Tool{
		b.searchPapersTool(),
		b.paperLookupTool(),
		b.evidenceSearchTool(),
		b.authorSearchTool(),
	}
}

// ──────────────────────────────────────────────
// Shared I/O types
// ──────────────────────────────────────────────

type adkPaperSummary struct {
	Title         string   `json:"title"`
	Authors       []string `json:"authors,omitempty"`
	Year          int      `json:"year,omitempty"`
	Abstract      string   `json:"abstract,omitempty"`
	DOI           string   `json:"doi,omitempty"`
	ArxivID       string   `json:"arxivId,omitempty"`
	Link          string   `json:"link,omitempty"`
	CitationCount int      `json:"citationCount,omitempty"`
	Source        string   `json:"source,omitempty"`
	Score         float64  `json:"score,omitempty"`
}

type adkPaperDetail struct {
	Title          string   `json:"title"`
	Authors        []string `json:"authors,omitempty"`
	Year           int      `json:"year,omitempty"`
	Venue          string   `json:"venue,omitempty"`
	Abstract       string   `json:"abstract,omitempty"`
	DOI            string   `json:"doi,omitempty"`
	ArxivID        string   `json:"arxivId,omitempty"`
	Link           string   `json:"link,omitempty"`
	PdfURL         string   `json:"pdfUrl,omitempty"`
	OpenAccessURL  string   `json:"openAccessUrl,omitempty"`
	CitationCount  int      `json:"citationCount,omitempty"`
	ReferenceCount int      `json:"referenceCount,omitempty"`
}

type adkEvidence struct {
	PaperTitle    string   `json:"paperTitle"`
	Authors       []string `json:"authors,omitempty"`
	Year          int      `json:"year,omitempty"`
	Snippet       string   `json:"snippet"`
	Link          string   `json:"link,omitempty"`
	CitationCount int      `json:"citationCount,omitempty"`
}

// ──────────────────────────────────────────────
// wisdevSearchPapers
// ──────────────────────────────────────────────

type adkSearchPapersInput struct {
	Query       string   `json:"query"`
	Limit       int      `json:"limit,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	Domain      string   `json:"domain,omitempty"`
	YearFrom    int      `json:"yearFrom,omitempty"`
	YearTo      int      `json:"yearTo,omitempty"`
	QualitySort bool     `json:"qualitySort,omitempty"`
}

type adkSearchPapersOutput struct {
	TotalFound int               `json:"totalFound"`
	Papers     []adkPaperSummary `json:"papers"`
	Providers  map[string]int    `json:"providers,omitempty"`
	LatencyMs  int64             `json:"latencyMs"`
	MCPTool    string            `json:"mcpTool"`
}

func (b *MCPADKBridge) searchPapersTool() adktool.Tool {
	tool, _ := functiontool.New(functiontool.Config{
		Name:        MCPToolSearchPapers,
		Description: "Search academic papers across 15+ providers (OpenAlex, arXiv, Semantic Scholar, PubMed, Europe PMC, Crossref, DBLP). Returns ranked papers with metadata.",
	}, func(toolCtx adktool.Context, in adkSearchPapersInput) (adkSearchPapersOutput, error) {
		ctx := adkCtxToContext(toolCtx)
		if strings.TrimSpace(in.Query) == "" {
			return adkSearchPapersOutput{}, fmt.Errorf("query is required")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}
		if limit > 50 {
			limit = 50
		}
		start := time.Now()
		sr := search.ParallelSearch(ctx, b.server.SearchRegistry, in.Query, search.SearchOpts{
			Limit:       limit,
			Domain:      in.Domain,
			Sources:     in.Sources,
			YearFrom:    in.YearFrom,
			YearTo:      in.YearTo,
			QualitySort: in.QualitySort,
		})
		slog.Info("mcp adk tool wisdevSearchPapers",
			"service", "wisdev_agent_os",
			"component", "wisdev.mcp_adk_tool",
			"operation", "search_papers",
			"query_length", len(in.Query),
			"result_count", len(sr.Papers),
			"latency_ms", time.Since(start).Milliseconds(),
			"result", "success",
		)
		papers := make([]adkPaperSummary, 0, len(sr.Papers))
		for _, p := range sr.Papers {
			abs := p.Abstract
			if len(abs) > 500 {
				abs = abs[:500] + "…"
			}
			papers = append(papers, adkPaperSummary{
				Title: p.Title, Authors: p.Authors, Year: p.Year,
				Abstract: abs, DOI: p.DOI, ArxivID: p.ArxivID,
				Link: p.Link, CitationCount: p.CitationCount,
				Source: p.Source, Score: p.Score,
			})
		}
		return adkSearchPapersOutput{
			TotalFound: len(papers), Papers: papers,
			Providers: sr.Providers, LatencyMs: time.Since(start).Milliseconds(),
			MCPTool: MCPToolSearchPapers,
		}, nil
	})
	return tool
}

// ──────────────────────────────────────────────
// wisdevPaperLookup
// ──────────────────────────────────────────────

type adkPaperLookupInput struct {
	PaperID string `json:"paperId"`
}

type adkPaperLookupOutput struct {
	Paper   *adkPaperDetail `json:"paper,omitempty"`
	Found   bool            `json:"found"`
	MCPTool string          `json:"mcpTool"`
}

func (b *MCPADKBridge) paperLookupTool() adktool.Tool {
	tool, _ := functiontool.New(functiontool.Config{
		Name:        MCPToolPaperLookup,
		Description: "Fetch full metadata for a single paper by its ID (arXiv ID, DOI, or Semantic Scholar ID).",
	}, func(toolCtx adktool.Context, in adkPaperLookupInput) (adkPaperLookupOutput, error) {
		ctx := adkCtxToContext(toolCtx)
		if strings.TrimSpace(in.PaperID) == "" {
			return adkPaperLookupOutput{}, fmt.Errorf("paperId is required")
		}
		sr, err := search.HandleToolSearch(ctx, b.server.SearchRegistry, "paper_lookup", map[string]any{"paperId": in.PaperID})
		if err != nil || len(sr.Papers) == 0 {
			return adkPaperLookupOutput{Found: false, MCPTool: MCPToolPaperLookup}, nil
		}
		p := sr.Papers[0]
		return adkPaperLookupOutput{
			Found: true,
			Paper: &adkPaperDetail{
				Title: p.Title, Authors: p.Authors, Year: p.Year, Venue: p.Venue,
				Abstract: p.Abstract, DOI: p.DOI, ArxivID: p.ArxivID, Link: p.Link,
				PdfURL: p.PdfUrl, OpenAccessURL: p.OpenAccessUrl,
				CitationCount: p.CitationCount, ReferenceCount: p.ReferenceCount,
			},
			MCPTool: MCPToolPaperLookup,
		}, nil
	})
	return tool
}

// ──────────────────────────────────────────────
// wisdevEvidenceSearch
// ──────────────────────────────────────────────

type adkEvidenceSearchInput struct {
	Claim  string `json:"claim"`
	Limit  int    `json:"limit,omitempty"`
	Domain string `json:"domain,omitempty"`
}

type adkEvidenceSearchOutput struct {
	Claim    string        `json:"claim"`
	Evidence []adkEvidence `json:"evidence"`
	MCPTool  string        `json:"mcpTool"`
}

func (b *MCPADKBridge) evidenceSearchTool() adktool.Tool {
	tool, _ := functiontool.New(functiontool.Config{
		Name:        MCPToolEvidenceSearch,
		Description: "Find supporting or contradicting evidence for a specific claim or hypothesis.",
	}, func(toolCtx adktool.Context, in adkEvidenceSearchInput) (adkEvidenceSearchOutput, error) {
		ctx := adkCtxToContext(toolCtx)
		if strings.TrimSpace(in.Claim) == "" {
			return adkEvidenceSearchOutput{}, fmt.Errorf("claim is required")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 5
		}
		if limit > 20 {
			limit = 20
		}
		query := fmt.Sprintf("evidence for: %s", in.Claim)
		sr := search.ParallelSearch(ctx, b.server.SearchRegistry, query, search.SearchOpts{
			Limit: limit * 3, Domain: in.Domain, QualitySort: true,
		})
		evidence := make([]adkEvidence, 0, limit)
		for _, p := range sr.Papers {
			if len(evidence) >= limit {
				break
			}
			snippet := p.Abstract
			if len(snippet) > 500 {
				snippet = snippet[:500] + "…"
			}
			evidence = append(evidence, adkEvidence{
				PaperTitle: p.Title, Authors: p.Authors, Year: p.Year,
				Snippet: snippet, Link: p.Link, CitationCount: p.CitationCount,
			})
		}
		return adkEvidenceSearchOutput{Claim: in.Claim, Evidence: evidence, MCPTool: MCPToolEvidenceSearch}, nil
	})
	return tool
}

// ──────────────────────────────────────────────
// wisdevAuthorSearch
// ──────────────────────────────────────────────

type adkAuthorSearchInput struct {
	AuthorID string `json:"authorId"`
	Limit    int    `json:"limit,omitempty"`
}

type adkAuthorSearchOutput struct {
	AuthorID string            `json:"authorId"`
	Papers   []adkPaperSummary `json:"papers"`
	MCPTool  string            `json:"mcpTool"`
}

func (b *MCPADKBridge) authorSearchTool() adktool.Tool {
	tool, _ := functiontool.New(functiontool.Config{
		Name:        MCPToolAuthorSearch,
		Description: "Retrieve papers by a specific author using their provider-specific author ID.",
	}, func(toolCtx adktool.Context, in adkAuthorSearchInput) (adkAuthorSearchOutput, error) {
		ctx := adkCtxToContext(toolCtx)
		if strings.TrimSpace(in.AuthorID) == "" {
			return adkAuthorSearchOutput{}, fmt.Errorf("authorId is required")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		if limit > 50 {
			limit = 50
		}
		sr, err := search.HandleToolSearch(ctx, b.server.SearchRegistry, "author_lookup", map[string]any{
			"authorId": in.AuthorID, "limit": limit,
		})
		if err != nil {
			return adkAuthorSearchOutput{AuthorID: in.AuthorID, MCPTool: MCPToolAuthorSearch}, nil
		}
		papers := make([]adkPaperSummary, 0, len(sr.Papers))
		for _, p := range sr.Papers {
			papers = append(papers, adkPaperSummary{
				Title: p.Title, Authors: p.Authors, Year: p.Year,
				DOI: p.DOI, Link: p.Link, CitationCount: p.CitationCount,
			})
		}
		return adkAuthorSearchOutput{AuthorID: in.AuthorID, Papers: papers, MCPTool: MCPToolAuthorSearch}, nil
	})
	return tool
}

// ──────────────────────────────────────────────
// Context helper
// ──────────────────────────────────────────────

func adkCtxToContext(toolCtx adktool.Context) context.Context {
	if toolCtx == nil {
		return context.Background()
	}
	return toolCtx
}
