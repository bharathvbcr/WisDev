// Package wisdev — MCP server implementation for the open-source WisDev runtime.
//
// Exposes WisDev academic search and retrieval tools as a Model Context Protocol
// (MCP) server.
//
// MCP is an open standard (JSON-RPC 2.0 over HTTP SSE or stdio) that lets
// external AI agents, Claude Desktop, Cursor, and ADK agents securely call
// tools provided by this server.
//
// Canonical protocol spec: https://modelcontextprotocol.io/spec
//
// Supported MCP methods:
//   - initialize
//   - tools/list
//   - tools/call
//   - resources/list, resources/read  (wisdev://config, providers, capabilities)
//   - prompts/list
//   - ping
//
// Exposed action tools:
//   - wisdevSearchPapers       – parallel multi-provider academic search
//   - wisdevPaperLookup        – single-paper metadata retrieval by ID
//   - wisdevEvidenceSearch     – RAG-grounded evidence retrieval
//   - wisdevAuthorSearch       – papers by author ID
//   - wisdevGenerateManuscript – grounded DocGen manuscript pipeline
//
// Exposed tuning / introspection tools (let an external LLM tune anything):
//   - wisdevGetConfig     – discover + read every runtime knob
//   - wisdevTuneConfig    – change runtime defaults (validated)
//   - wisdevResetConfig   – restore knob defaults
//   - wisdevListProviders – list registered search providers
//   - wisdevCapabilities  – full control-surface overview
//
// Tuned knobs (see mcp_config.go) become the per-call defaults the action tools
// inherit, so tuning takes effect immediately on both HTTP and stdio transports.
//
// Legacy scholarlm* names remain accepted on tools/call for compatibility.
package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// ──────────────────────────────────────────────
// MCP JSON-RPC 2.0 envelope types
// ──────────────────────────────────────────────

const mcpProtocolVersion = "2024-11-05"

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *mcpError `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP error codes (per spec).
const (
	mcpErrParse          = -32700
	mcpErrInvalidRequest = -32600
	mcpErrMethodNotFound = -32601
	mcpErrInvalidParams  = -32602
	mcpErrInternal       = -32603
)

// ──────────────────────────────────────────────
// Tool schema definitions
// ──────────────────────────────────────────────

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func mcpSearchPapersTool() mcpTool {
	return mcpTool{
		Name:        MCPToolSearchPapers,
		Description: "Search academic papers across 15+ providers (OpenAlex, arXiv, Semantic Scholar, PubMed, Europe PMC, Crossref, DBLP, etc.). Returns ranked papers with titles, abstracts, authors, years, DOIs, and citation counts. Use this to find evidence-grounded sources for research questions.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":        map[string]any{"type": "string", "description": "Academic search query", "minLength": 1},
				"limit":        map[string]any{"type": "integer", "description": "Max results (1-50, default 10)", "minimum": 1, "maximum": 50},
				"sources":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Provider hints: openalex, arxiv, semantic_scholar, pubmed, europe_pmc, crossref, dblp, etc."},
				"domain":       map[string]any{"type": "string", "description": "Research domain hint for provider routing"},
				"yearFrom":     map[string]any{"type": "integer", "description": "Start year filter (inclusive)"},
				"yearTo":       map[string]any{"type": "integer", "description": "End year filter (inclusive)"},
				"minCitations": map[string]any{"type": "integer", "description": "Only return papers with at least this many citations (quality filter). 0 = no minimum.", "minimum": 0},
				"qualitySort":  map[string]any{"type": "boolean", "description": "Sort by citation-weighted quality score (default true)"},
			},
			"required": []string{"query"},
		},
	}
}

func mcpPaperLookupTool() mcpTool {
	return mcpTool{
		Name:        MCPToolPaperLookup,
		Description: "Fetch full metadata for a single paper by its provider-specific ID (e.g. arXiv ID like '2310.07862', DOI, or Semantic Scholar corpusId). Returns title, abstract, authors, year, venue, citation count, and open-access PDF URL.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paperId": map[string]any{"type": "string", "description": "Paper ID (arXiv ID, DOI, or provider-specific ID)", "minLength": 1},
			},
			"required": []string{"paperId"},
		},
	}
}

func mcpEvidenceSearchTool() mcpTool {
	return mcpTool{
		Name:        MCPToolEvidenceSearch,
		Description: "RAG-grounded evidence retrieval: searches for papers, extracts relevant passages, and returns evidence snippets with citations. Ideal for claim verification, hypothesis grounding, or finding supporting/contradicting evidence for a specific assertion.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"claim":  map[string]any{"type": "string", "description": "Claim or hypothesis to find evidence for", "minLength": 1},
				"limit":  map[string]any{"type": "integer", "description": "Max evidence snippets (1-20, default 5)", "minimum": 1, "maximum": 20},
				"domain": map[string]any{"type": "string", "description": "Research domain"},
			},
			"required": []string{"claim"},
		},
	}
}

func mcpGenerateManuscriptTool() mcpTool {
	return mcpTool{
		Name:        MCPToolGenerateManuscript,
		Description: "Generate a grounded, citation-backed manuscript (literature-review style) for a research question. Retrieves papers across providers, then runs the WisDev DocGen manuscript pipeline (plan → write → blind-verify → peer-review) and returns the drafted sections as Markdown (or raw JSON). This is the docGen capability — use wisdevSearchPapers instead for research-only retrieval without a manuscript.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":        map[string]any{"type": "string", "description": "Research question / manuscript topic", "minLength": 1},
				"maxPapers":    map[string]any{"type": "integer", "description": "Max papers to ground the manuscript on (1-80, default 30)", "minimum": 1, "maximum": 80},
				"words":        map[string]any{"type": "integer", "description": "Target total word count for the manuscript (split across sections). 0 or omitted lets the model choose length.", "minimum": 0, "maximum": 20000},
				"minCitations": map[string]any{"type": "integer", "description": "Minimum number of distinct sources the manuscript should cite. Raises the retrieval floor and instructs the writers to cite broadly. 0 = no minimum.", "minimum": 0, "maximum": 200},
				"reviewRounds": map[string]any{"type": "integer", "description": "Max rounds of the agentic generate→review→revise loop (each round re-reviews and rewrites flagged sections, stopping early when it converges). 0 = default (2). Max 5.", "minimum": 0, "maximum": 5},
				"genre":        map[string]any{"type": "string", "description": "Manuscript genre, e.g. 'narrative literature review' (default) or 'research paper'. Controls the writers' voice and how the reviewer grades it (a research paper's first-person voice is not penalized)."},
				"flow":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Ordered section flow for the draft, e.g. [\"abstract\",\"introduction\",\"methods\",\"results\",\"discussion\",\"conclusion\"]. Known ids reuse the tuned section briefs; unknown ids become generic synthesis sections in the given order. Omit for the default plan."},
				"sources":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Provider hints: openalex, arxiv, semantic_scholar, pubmed, europe_pmc, crossref, dblp, etc."},
				"domain":       map[string]any{"type": "string", "description": "Research domain hint for provider routing"},
				"format":       map[string]any{"type": "string", "description": "Output format: 'markdown' (default) or 'json'", "enum": []string{"markdown", "json"}},
			},
			"required": []string{"query"},
		},
	}
}

func mcpAuthorSearchTool() mcpTool {
	return mcpTool{
		Name:        MCPToolAuthorSearch,
		Description: "Retrieve papers by a specific author using their provider-specific author ID. Returns a list of that author's papers with metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"authorId": map[string]any{"type": "string", "description": "Author ID (Semantic Scholar authorId or OpenAlex author ID)", "minLength": 1},
				"limit":    map[string]any{"type": "integer", "description": "Max results (1-50, default 20)", "minimum": 1, "maximum": 50},
			},
			"required": []string{"authorId"},
		},
	}
}

func mcpGetConfigTool() mcpTool {
	return mcpTool{
		Name:        MCPToolGetConfig,
		Description: "Inspect the WisDev runtime's tunable configuration. Returns every tunable knob with its type, allowed range/enum, default, current value, and a description of what it controls. Call this first to discover what wisdevTuneConfig can change. Optionally pass 'keys' to fetch only specific knobs.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keys": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional subset of knob keys to return (e.g. ['search.limit','manuscript.genre']). Omit for all knobs."},
			},
		},
	}
}

func mcpTuneConfigTool() mcpTool {
	return mcpTool{
		Name:        MCPToolTuneConfig,
		Description: "Tune the WisDev runtime. Pass a 'settings' object of knob-key → value to change runtime defaults that subsequent search/evidence/author/manuscript calls inherit. Values are validated against each knob's type and range/enum (discover them via wisdevGetConfig). Unknown keys or out-of-range values are reported and rejected; valid updates in the same call still apply. Returns the applied changes and the full updated config.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"settings": map[string]any{"type": "object", "description": "Map of knob key → new value, e.g. {\"search.limit\": 25, \"search.defaultSources\": [\"openalex\",\"arxiv\"], \"manuscript.genre\": \"research paper\"}."},
			},
			"required": []string{"settings"},
		},
	}
}

func mcpResetConfigTool() mcpTool {
	return mcpTool{
		Name:        MCPToolResetConfig,
		Description: "Reset WisDev runtime knobs back to their built-in defaults. Pass 'keys' to reset a subset, or omit to reset everything. Returns the resulting config.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keys": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional subset of knob keys to reset. Omit to reset all knobs to defaults."},
			},
		},
	}
}

func mcpListProvidersTool() mcpTool {
	return mcpTool{
		Name:        MCPToolListProviders,
		Description: "List the academic search providers registered in this runtime, with their canonical name, specialised domains, and current health. Use this to discover valid values for the 'sources' argument and the 'search.defaultSources' knob.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func mcpCapabilitiesTool() mcpTool {
	return mcpTool{
		Name:        MCPToolCapabilities,
		Description: "Describe everything this WisDev MCP runtime can do: the callable tools, the tunable configuration groups, and the introspectable resources. Use this for a one-shot overview of the full control surface.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

// ──────────────────────────────────────────────
// MCPServer
// ──────────────────────────────────────────────

// MCPServer exposes WisDev academic search tools via the Model Context Protocol.
// It implements JSON-RPC 2.0 over HTTP POST and supports the tools/list and
// tools/call methods required by MCP clients and ADK tool connectors.
type MCPServer struct {
	SearchRegistry *search.ProviderRegistry
	ServerName     string
	ServerVersion  string
	timeout        time.Duration
	// Config holds the live, externally-tunable runtime knobs. The tuning
	// tools (wisdevGetConfig/TuneConfig/ResetConfig) read and mutate it, and
	// the action tools read it for their per-call defaults.
	Config *RuntimeConfig
}

// NewMCPServer constructs an MCPServer backed by the given search registry.
func NewMCPServer(registry *search.ProviderRegistry) *MCPServer {
	return &MCPServer{
		SearchRegistry: registry,
		ServerName:     MCPServerName,
		ServerVersion:  "1.1.0",
		timeout:        30 * time.Second,
		Config:         NewRuntimeConfig(),
	}
}

// effectiveTimeout resolves the per-request handler timeout, honoring the
// externally-tunable server.timeoutSeconds knob and falling back to the
// constructed default.
func (s *MCPServer) effectiveTimeout() time.Duration {
	if s.Config != nil {
		if secs := s.Config.Int(CfgServerTimeoutSeconds); secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return s.timeout
}

// Handler returns an http.Handler for the MCP endpoint.
// Mount it at /mcp or /wisdev/mcp in the server routing table.
func (s *MCPServer) Handler() http.Handler {
	return http.HandlerFunc(s.ServeHTTP)
}

// ServeHTTP handles a single MCP JSON-RPC 2.0 request over HTTP POST.
func (s *MCPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":            s.ServerName,
			"version":         s.ServerVersion,
			"protocolVersion": mcpProtocolVersion,
			"tools":           len(s.allTools()),
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mcpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, mcpErrParse, "parse error: "+err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		s.writeError(w, req.ID, mcpErrInvalidRequest, "jsonrpc must be 2.0")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.effectiveTimeout())
	defer cancel()

	slog.Info("mcp request",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "wisdev.mcp_server",
		"operation", "mcp_dispatch",
		"method", req.Method,
		"id", fmt.Sprint(req.ID),
	)

	result, mcpErr := s.dispatch(ctx, req)
	if mcpErr != nil {
		s.writeError(w, req.ID, mcpErr.Code, mcpErr.Message)
		return
	}
	s.writeResult(w, req.ID, result)
}

func (s *MCPServer) dispatch(ctx context.Context, req mcpRequest) (any, *mcpError) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.Params)
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolsCall(ctx, req.Params)
	case "resources/list":
		return s.handleResourcesList()
	case "resources/read":
		return s.handleResourcesRead(req.Params)
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	case "ping":
		return map[string]any{}, nil
	default:
		return nil, &mcpError{Code: mcpErrMethodNotFound, Message: "method not found: " + req.Method}
	}
}

type mcpInitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      map[string]any `json:"clientInfo,omitempty"`
}

func (s *MCPServer) handleInitialize(raw json.RawMessage) (any, *mcpError) {
	var params mcpInitializeParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &mcpError{Code: mcpErrInvalidParams, Message: "invalid initialize params: " + err.Error()}
		}
	}
	return map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    s.ServerName,
			"version": s.ServerVersion,
		},
	}, nil
}

func (s *MCPServer) handleToolsList() (any, *mcpError) {
	return map[string]any{"tools": s.allTools()}, nil
}

func (s *MCPServer) allTools() []mcpTool {
	return []mcpTool{
		mcpSearchPapersTool(),
		mcpPaperLookupTool(),
		mcpEvidenceSearchTool(),
		mcpAuthorSearchTool(),
		mcpGenerateManuscriptTool(),
		mcpGetConfigTool(),
		mcpTuneConfigTool(),
		mcpResetConfigTool(),
		mcpListProvidersTool(),
		mcpCapabilitiesTool(),
	}
}

// ListTools returns the MCP tools exposed by this server.
func (s *MCPServer) ListTools() []mcpTool {
	return s.allTools()
}

type mcpToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *MCPServer) handleToolsCall(ctx context.Context, raw json.RawMessage) (any, *mcpError) {
	var params mcpToolCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &mcpError{Code: mcpErrInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}

	toolName := normalizeMCPToolName(params.Name)
	args := params.Arguments
	if args == nil {
		args = map[string]any{}
	}

	if !isKnownMCPTool(toolName) {
		return nil, &mcpError{Code: mcpErrMethodNotFound, Message: "unknown tool: " + params.Name}
	}

	slog.Info("mcp tools/call",
		"service", "wisdev_arc",
		"runtime", "go",
		"component", "wisdev.mcp_server",
		"operation", "tool_call",
		"tool", toolName,
		"stage", "dispatching",
	)

	start := time.Now()
	var result *mcpToolResult
	var callErr error

	switch toolName {
	case MCPToolSearchPapers:
		result, callErr = s.callSearchPapers(ctx, args)
	case MCPToolPaperLookup:
		result, callErr = s.callPaperLookup(ctx, args)
	case MCPToolEvidenceSearch:
		result, callErr = s.callEvidenceSearch(ctx, args)
	case MCPToolAuthorSearch:
		result, callErr = s.callAuthorSearch(ctx, args)
	case MCPToolGenerateManuscript:
		result, callErr = s.callGenerateManuscript(ctx, args)
	case MCPToolGetConfig:
		result, callErr = s.callGetConfig(args)
	case MCPToolTuneConfig:
		result, callErr = s.callTuneConfig(args)
	case MCPToolResetConfig:
		result, callErr = s.callResetConfig(args)
	case MCPToolListProviders:
		result, callErr = s.callListProviders()
	case MCPToolCapabilities:
		result, callErr = s.callCapabilities()
	default:
		return nil, &mcpError{Code: mcpErrMethodNotFound, Message: "unknown tool: " + params.Name}
	}

	latency := time.Since(start)
	if callErr != nil {
		slog.Warn("mcp tool call error",
			"service", "wisdev_arc",
			"component", "wisdev.mcp_server",
			"tool", toolName,
			"latency_ms", latency.Milliseconds(),
			"error_code", "tool_call_failed",
			"error", callErr.Error(),
		)
		return &mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "error: " + callErr.Error()}},
			IsError: true,
		}, nil
	}

	slog.Info("mcp tool call completed",
		"service", "wisdev_arc",
		"component", "wisdev.mcp_server",
		"tool", toolName,
		"latency_ms", latency.Milliseconds(),
		"result", "success",
	)
	return result, nil
}

func (s *MCPServer) callSearchPapers(ctx context.Context, args map[string]any) (*mcpToolResult, error) {
	if s.SearchRegistry == nil {
		return nil, fmt.Errorf("search registry not configured")
	}
	query := mcpStringArg(args, "query")
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	c := s.cfg()
	limit := mcpIntArg(args, "limit", c.Int(CfgSearchLimit))
	if limit <= 0 || limit > 50 {
		limit = c.Int(CfgSearchLimit)
	}

	sources := mcpStringSliceArg(args, "sources")
	if len(sources) == 0 {
		sources = c.Strings(CfgSearchDefaultSources)
	}
	domain := mcpStringArg(args, "domain")
	if domain == "" {
		domain = c.String(CfgSearchDefaultDomain)
	}

	opts := search.SearchOpts{
		Limit:            limit,
		Domain:           domain,
		Sources:          sources,
		YearFrom:         mcpIntArg(args, "yearFrom", c.Int(CfgSearchYearFrom)),
		YearTo:           mcpIntArg(args, "yearTo", c.Int(CfgSearchYearTo)),
		QualitySort:      mcpBoolArg(args, "qualitySort", c.Bool(CfgSearchQualitySort)),
		ExpandQuery:      mcpBoolArg(args, "expandQuery", c.Bool(CfgSearchExpandQuery)),
		DynamicProviders: mcpBoolArg(args, "dynamicProviders", c.Bool(CfgSearchDynamicProviders)),
	}
	sr := search.ParallelSearch(ctx, s.SearchRegistry, query, opts)

	// Optional granular quality filter: keep only papers at/above a citation floor.
	if minCitations := mcpIntArg(args, "minCitations", c.Int(CfgSearchMinCitations)); minCitations > 0 {
		filtered := sr.Papers[:0]
		for _, p := range sr.Papers {
			if p.CitationCount >= minCitations {
				filtered = append(filtered, p)
			}
		}
		sr.Papers = filtered
	}

	lines := make([]string, 0, len(sr.Papers)+3)
	lines = append(lines, fmt.Sprintf("Found %d papers for query: %q", len(sr.Papers), query))
	lines = append(lines, fmt.Sprintf("Providers queried: %v | Latency: %dms", sr.Providers, sr.LatencyMs))
	lines = append(lines, "")
	for i, p := range sr.Papers {
		abstract := p.Abstract
		if len(abstract) > 300 {
			abstract = abstract[:300] + "…"
		}
		lines = append(lines, fmt.Sprintf(
			"[%d] %s\n    Authors: %s | Year: %d | Citations: %d\n    DOI: %s | ArXivID: %s\n    Abstract: %s\n    Link: %s",
			i+1, p.Title, strings.Join(p.Authors, ", "),
			p.Year, p.CitationCount, p.DOI, p.ArxivID, abstract, p.Link,
		))
	}
	return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: strings.Join(lines, "\n")}}}, nil
}

func (s *MCPServer) callPaperLookup(ctx context.Context, args map[string]any) (*mcpToolResult, error) {
	if s.SearchRegistry == nil {
		return nil, fmt.Errorf("search registry not configured")
	}
	paperID := mcpStringArg(args, "paperId")
	if paperID == "" {
		return nil, fmt.Errorf("paperId is required")
	}
	sr, err := search.HandleToolSearch(ctx, s.SearchRegistry, "paper_lookup", map[string]any{"paperId": paperID})
	if err != nil {
		return nil, fmt.Errorf("paper lookup failed: %w", err)
	}
	if len(sr.Papers) == 0 {
		return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No paper found for ID: " + paperID}}}, nil
	}
	p := sr.Papers[0]
	text := fmt.Sprintf(
		"Title: %s\nAuthors: %s\nYear: %d | Venue: %s\nCitations: %d | References: %d\nDOI: %s | ArXivID: %s\nOpen Access: %s\nPDF: %s\nAbstract:\n%s",
		p.Title, strings.Join(p.Authors, ", "),
		p.Year, p.Venue, p.CitationCount, p.ReferenceCount,
		p.DOI, p.ArxivID, p.OpenAccessUrl, p.PdfUrl, p.Abstract,
	)
	return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: text}}}, nil
}

func (s *MCPServer) callEvidenceSearch(ctx context.Context, args map[string]any) (*mcpToolResult, error) {
	if s.SearchRegistry == nil {
		return nil, fmt.Errorf("search registry not configured")
	}
	claim := mcpStringArg(args, "claim")
	if claim == "" {
		return nil, fmt.Errorf("claim is required")
	}
	c := s.cfg()
	limit := mcpIntArg(args, "limit", c.Int(CfgEvidenceLimit))
	if limit <= 0 || limit > 20 {
		limit = c.Int(CfgEvidenceLimit)
	}
	domain := mcpStringArg(args, "domain")
	if domain == "" {
		domain = c.String(CfgSearchDefaultDomain)
	}
	query := fmt.Sprintf("evidence for: %s", claim)
	sr := search.ParallelSearch(ctx, s.SearchRegistry, query, search.SearchOpts{
		Limit:       limit * 3,
		Domain:      domain,
		QualitySort: true,
	})
	lines := make([]string, 0, len(sr.Papers)+3)
	lines = append(lines, fmt.Sprintf("Evidence search for claim: %q", claim))
	lines = append(lines, fmt.Sprintf("Retrieved %d supporting sources", len(sr.Papers)))
	lines = append(lines, "")
	shown := 0
	for _, p := range sr.Papers {
		if shown >= limit {
			break
		}
		snippet := p.Abstract
		if len(snippet) > 400 {
			snippet = snippet[:400] + "…"
		}
		lines = append(lines, fmt.Sprintf(
			"Source %d: %s (%d)\n  Authors: %s | Citations: %d\n  Evidence: %s\n  Link: %s",
			shown+1, p.Title, p.Year, strings.Join(p.Authors, ", "), p.CitationCount, snippet, p.Link,
		))
		shown++
	}
	if shown == 0 {
		lines = append(lines, "No supporting evidence found.")
	}
	return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: strings.Join(lines, "\n")}}}, nil
}

func (s *MCPServer) callAuthorSearch(ctx context.Context, args map[string]any) (*mcpToolResult, error) {
	if s.SearchRegistry == nil {
		return nil, fmt.Errorf("search registry not configured")
	}
	authorID := mcpStringArg(args, "authorId")
	if authorID == "" {
		return nil, fmt.Errorf("authorId is required")
	}
	c := s.cfg()
	limit := mcpIntArg(args, "limit", c.Int(CfgAuthorLimit))
	if limit <= 0 || limit > 50 {
		limit = c.Int(CfgAuthorLimit)
	}
	sr, err := search.HandleToolSearch(ctx, s.SearchRegistry, "author_lookup", map[string]any{
		"authorId": authorID,
		"limit":    limit,
	})
	if err != nil {
		return nil, fmt.Errorf("author search failed: %w", err)
	}
	lines := make([]string, 0, len(sr.Papers)+1)
	lines = append(lines, fmt.Sprintf("Papers by author ID %s (%d results):", authorID, len(sr.Papers)))
	lines = append(lines, "")
	for i, p := range sr.Papers {
		lines = append(lines, fmt.Sprintf("[%d] %s (%d) — Citations: %d | DOI: %s",
			i+1, p.Title, p.Year, p.CitationCount, p.DOI))
	}
	return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: strings.Join(lines, "\n")}}}, nil
}

// callGenerateManuscript is the MCP docGen capability: it retrieves grounding
// papers, runs the manuscript pipeline, and returns the drafted manuscript as
// Markdown (or raw JSON). The optional `words` argument sets a manuscript-wide
// target word count forwarded to the section writers.
func (s *MCPServer) callGenerateManuscript(ctx context.Context, args map[string]any) (*mcpToolResult, error) {
	if s.SearchRegistry == nil {
		return nil, fmt.Errorf("search registry not configured")
	}
	query := mcpStringArg(args, "query")
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	c := s.cfg()
	maxPapers := mcpIntArg(args, "maxPapers", c.Int(CfgManuscriptMaxPapers))
	if maxPapers <= 0 || maxPapers > 80 {
		maxPapers = c.Int(CfgManuscriptMaxPapers)
	}
	targetWords := mcpIntArg(args, "words", c.Int(CfgManuscriptTargetWords))
	if targetWords < 0 {
		targetWords = 0
	}
	minCitations := mcpIntArg(args, "minCitations", c.Int(CfgManuscriptMinCitations))
	if minCitations < 0 {
		minCitations = 0
	}
	// Retrieve at least as many papers as the requested citation floor (capped) so
	// the manuscript can actually cite that many distinct sources.
	if minCitations > maxPapers {
		maxPapers = minCitations
		if maxPapers > 80 {
			maxPapers = 80
		}
	}
	sectionFlow := mcpStringSliceArg(args, "flow")
	if len(sectionFlow) == 0 {
		sectionFlow = c.Strings(CfgManuscriptSectionFlow)
	}
	sources := mcpStringSliceArg(args, "sources")
	if len(sources) == 0 {
		sources = c.Strings(CfgSearchDefaultSources)
	}
	domain := mcpStringArg(args, "domain")
	if domain == "" {
		domain = c.String(CfgSearchDefaultDomain)
	}

	sr := search.ParallelSearch(ctx, s.SearchRegistry, query, search.SearchOpts{
		Limit:       maxPapers,
		Domain:      domain,
		Sources:     sources,
		QualitySort: true,
	})

	reviewRounds := mcpIntArg(args, "reviewRounds", c.Int(CfgManuscriptReviewRounds))
	if reviewRounds < 0 {
		reviewRounds = 0
	}
	genre := strings.TrimSpace(mcpStringArg(args, "genre"))
	if genre == "" {
		genre = c.String(CfgManuscriptGenre)
	}

	pipeline := NewManuscriptPipeline(ResolvePythonBase())
	pipeline.TargetWords = targetWords
	pipeline.MinCitations = minCitations
	pipeline.SectionFlow = sectionFlow
	pipeline.ReviewRounds = reviewRounds
	pipeline.Genre = genre
	jobID := fmt.Sprintf("mcp_docgen_%d", time.Now().UnixMilli())
	result, err := pipeline.Run(ctx, jobID, query, sr.Papers)
	if err != nil {
		return nil, fmt.Errorf("manuscript generation failed: %w", err)
	}

	format := strings.TrimSpace(mcpStringArg(args, "format"))
	if format == "" {
		format = c.String(CfgManuscriptFormat)
	}
	if strings.EqualFold(format, "json") {
		encoded, encErr := json.MarshalIndent(result, "", "  ")
		if encErr != nil {
			return nil, encErr
		}
		return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(encoded)}}}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", query)
	fmt.Fprintf(&b, "_Grounded manuscript generated from %d retrieved papers", len(sr.Papers))
	if targetWords > 0 {
		fmt.Fprintf(&b, "; target ~%d words", targetWords)
	}
	if minCitations > 0 {
		fmt.Fprintf(&b, "; ≥%d citations", minCitations)
	}
	if len(sectionFlow) > 0 {
		fmt.Fprintf(&b, "; custom flow (%d sections)", len(result.SectionDrafts))
	}
	b.WriteString("._\n\n")
	for _, section := range result.SectionDrafts {
		title := strings.TrimSpace(section.Title)
		if title == "" {
			title = section.SectionID
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", title, strings.TrimSpace(section.Content))
	}
	return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: strings.TrimSpace(b.String())}}}, nil
}

// ──────────────────────────────────────────────
// HTTP response helpers
// ──────────────────────────────────────────────

func (s *MCPServer) writeResult(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *MCPServer) writeError(w http.ResponseWriter, id any, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	if code == mcpErrParse || code == mcpErrInvalidRequest {
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: msg}})
}

// ──────────────────────────────────────────────
// Argument helpers
// ──────────────────────────────────────────────

func mcpStringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func mcpIntArg(args map[string]any, key string, fallback int) int {
	switch v := args[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int64:
		return int(v)
	default:
		return fallback
	}
}

func mcpBoolArg(args map[string]any, key string, fallback bool) bool {
	v, ok := args[key].(bool)
	if !ok {
		return fallback
	}
	return v
}

func mcpStringSliceArg(args map[string]any, key string) []string {
	switch v := args[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}
