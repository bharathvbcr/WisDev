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
//   - ping
//
// Exposed tools:
//   - wisdevSearchPapers   – parallel multi-provider academic search
//   - wisdevPaperLookup    – single-paper metadata retrieval by ID
//   - wisdevEvidenceSearch – RAG-grounded evidence retrieval
//   - wisdevAuthorSearch   – papers by author ID
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

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
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
				"query":       map[string]any{"type": "string", "description": "Academic search query", "minLength": 1},
				"limit":       map[string]any{"type": "integer", "description": "Max results (1-50, default 10)", "minimum": 1, "maximum": 50},
				"sources":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Provider hints: openalex, arxiv, semantic_scholar, pubmed, europe_pmc, crossref, dblp, etc."},
				"domain":      map[string]any{"type": "string", "description": "Research domain hint for provider routing"},
				"yearFrom":    map[string]any{"type": "integer", "description": "Start year filter (inclusive)"},
				"yearTo":      map[string]any{"type": "integer", "description": "End year filter (inclusive)"},
				"qualitySort": map[string]any{"type": "boolean", "description": "Sort by citation-weighted quality score (default true)"},
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
}

// NewMCPServer constructs an MCPServer backed by the given search registry.
func NewMCPServer(registry *search.ProviderRegistry) *MCPServer {
	return &MCPServer{
		SearchRegistry: registry,
		ServerName:     MCPServerName,
		ServerVersion:  "1.0.0",
		timeout:        30 * time.Second,
	}
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

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
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
		return map[string]any{"resources": []any{}}, nil
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
		"service", "wisdev_agent_os",
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
	default:
		return nil, &mcpError{Code: mcpErrMethodNotFound, Message: "unknown tool: " + params.Name}
	}

	latency := time.Since(start)
	if callErr != nil {
		slog.Warn("mcp tool call error",
			"service", "wisdev_agent_os",
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
		"service", "wisdev_agent_os",
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
	limit := mcpIntArg(args, "limit", 10)
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	opts := search.SearchOpts{
		Limit:       limit,
		Domain:      mcpStringArg(args, "domain"),
		Sources:     mcpStringSliceArg(args, "sources"),
		YearFrom:    mcpIntArg(args, "yearFrom", 0),
		YearTo:      mcpIntArg(args, "yearTo", 0),
		QualitySort: mcpBoolArg(args, "qualitySort", true),
	}
	sr := search.ParallelSearch(ctx, s.SearchRegistry, query, opts)

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
	limit := mcpIntArg(args, "limit", 5)
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	query := fmt.Sprintf("evidence for: %s", claim)
	sr := search.ParallelSearch(ctx, s.SearchRegistry, query, search.SearchOpts{
		Limit:       limit * 3,
		Domain:      mcpStringArg(args, "domain"),
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
	limit := mcpIntArg(args, "limit", 20)
	if limit <= 0 || limit > 50 {
		limit = 20
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
