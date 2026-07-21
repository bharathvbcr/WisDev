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
//   - initialize            (returns agent instructions + tool/resource/prompt caps)
//   - tools/list
//   - tools/call
//   - resources/list, resources/read  (wisdev://config, providers, capabilities)
//   - prompts/list, prompts/get
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
		Name: MCPToolSearchPapers,
		Description: "PRIMARY literature search for Claude Code / Cursor agents. Fan-out across 15+ academic providers (OpenAlex, arXiv, Semantic Scholar, PubMed, Europe PMC, Crossref, DBLP, …) and return ranked papers (title, abstract, authors, year, DOI, citations). " +
			"Use for open-ended topic searches and source gathering. Prefer wisdevPaperLookup when you already have a DOI/arXiv ID; prefer wisdevEvidenceSearch for claim-grounded quotable snippets; prefer wisdevGenerateManuscript when the user wants a drafted document. " +
			"Legacy alias on tools/call: scholarlmSearchPapers.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":        map[string]any{"type": "string", "description": "Academic search query (natural language or keyword)", "minLength": 1},
				"limit":        map[string]any{"type": "integer", "description": "Max results (1-50, default 10)", "minimum": 1, "maximum": 50},
				"sources":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Provider hints: openalex, arxiv, semantic_scholar, pubmed, europe_pmc, crossref, dblp, etc. Call wisdevListProviders if unsure."},
				"domain":       map[string]any{"type": "string", "description": "Research domain hint for provider routing (e.g. biomed, cs, physics)"},
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
		Name: MCPToolPaperLookup,
		Description: "Fetch full metadata for ONE known paper by ID (arXiv like '2310.07862', DOI, or provider corpusId). Returns title, abstract, authors, year, venue, citation count, and open-access PDF URL when available. " +
			"Use when the user pastes a DOI/arXiv link or you already have an ID from a prior search. Not for open topic search — use wisdevSearchPapers. Legacy alias: scholarlmPaperLookup.",
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
		Name: MCPToolEvidenceSearch,
		Description: "Claim-grounded evidence retrieval for agents that need citable snippets. Searches papers, extracts relevant passages, and returns evidence with citations. " +
			"Best for 'what supports/contradicts X?', hypothesis grounding, and verification — not for drafting a full manuscript (use wisdevGenerateManuscript) or browsing a topic list (use wisdevSearchPapers). Legacy alias: scholarlmEvidenceSearch.",
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
		Name: MCPToolGenerateManuscript,
		Description: "DocGen: retrieve papers then generate a grounded ScholarDoc-style document. Intents: fullpaper (default; plan→write→verify→peer-review), report (fast thematic synthesis), litreview (thematic literature review). " +
			"Citation styles: apa|mla|chicago|vancouver|ieee|harvard|nature. Formats: markdown|json|latex|html (docx is CLI-only via `wisdev docgen`). " +
			"Use when the user wants a drafted paper/report/review — not for search-only answers (wisdevSearchPapers) or the multi-iteration YOLO loop (CLI: wisdev \"question\"). " +
			"Aliases on tools/call: scholarlmGenerateManuscript, wisdevDocGen.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":        map[string]any{"type": "string", "description": "Research question / manuscript topic", "minLength": 1},
				"maxPapers":    map[string]any{"type": "integer", "description": "Max papers to ground the manuscript on (1-80, default 30)", "minimum": 1, "maximum": 80},
				"words":        map[string]any{"type": "integer", "description": "Target total word count for the manuscript (split across sections). 0 or omitted lets the model choose length.", "minimum": 0, "maximum": 20000},
				"minCitations": map[string]any{"type": "integer", "description": "Minimum number of distinct sources the manuscript should cite. Raises the retrieval floor and instructs the writers to cite broadly. Omit for the default floor of 10; pass an explicit 0 for no minimum.", "minimum": 0, "maximum": 200},
				"reviewRounds": map[string]any{"type": "integer", "description": "Max rounds of the agentic generate→review→revise loop (each round re-reviews and rewrites flagged sections, stopping early when it converges). 0 = default (2). Max 5. Ignored for report/litreview.", "minimum": 0, "maximum": 5},
				"genre":        map[string]any{"type": "string", "description": "Manuscript genre for fullpaper, e.g. 'narrative literature review' (default) or 'research paper'. Controls voice and reviewer grading."},
				"instructions": map[string]any{"type": "string", "description": "Free-text author steering applied to every section (tone, target audience, emphasis, terminology, structural preferences). Overrides default style guidance but never grounding/attribution/no-fabrication rules."},
				"flow":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Ordered section flow for fullpaper, e.g. [\"abstract\",\"introduction\",\"methods\",\"results\",\"discussion\",\"conclusion\"]. Known ids reuse tuned briefs; unknown ids become generic synthesis sections. Omit for default plan."},
				"sources":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Provider hints: openalex, arxiv, semantic_scholar, pubmed, europe_pmc, crossref, dblp, etc."},
				"domain":       map[string]any{"type": "string", "description": "Research domain hint for provider routing"},
				"intent":        map[string]any{"type": "string", "description": "Document type: 'fullpaper' (default), 'report' (fast synthesis), or 'litreview'. report/litreview ignore fullpaper-only knobs (flow, reviewRounds, genre).", "enum": []string{"fullpaper", "report", "litreview"}},
				"citationStyle": map[string]any{"type": "string", "description": "Bibliography citation style (default 'apa').", "enum": []string{"apa", "mla", "chicago", "vancouver", "ieee", "harvard", "nature"}},
				"format":        map[string]any{"type": "string", "description": "Output format: 'markdown' (default), 'json', 'latex', or 'html'. fullpaper+json returns raw ManuscriptPipelineResult; other intents return the Document envelope.", "enum": []string{"markdown", "json", "latex", "html"}},
			},
			"required": []string{"query"},
		},
	}
}

func mcpAuthorSearchTool() mcpTool {
	return mcpTool{
		Name: MCPToolAuthorSearch,
		Description: "List papers by a provider author ID (Semantic Scholar authorId or OpenAlex author ID). Use after you already have an authorId from a prior paper result — not for name-string search. Legacy alias: scholarlmAuthorSearch.",
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
		Name: MCPToolGetConfig,
		Description: "Discover and read every runtime knob (type, range/enum, default, current value). Call before wisdevTuneConfig. Groups: search.*, evidence.*, author.*, manuscript.*, server.*. Also available as resource wisdev://config.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keys": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional subset of knob keys (e.g. ['search.limit','manuscript.genre']). Omit for all knobs."},
			},
		},
	}
}

func mcpTuneConfigTool() mcpTool {
	return mcpTool{
		Name: MCPToolTuneConfig,
		Description: "Change runtime defaults that subsequent search/evidence/author/manuscript calls in this MCP session inherit (no restart). Validate keys via wisdevGetConfig first. Unknown/out-of-range keys are rejected; valid keys in the same call still apply.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"settings": map[string]any{"type": "object", "description": "Map of knob key → new value, e.g. {\"search.limit\": 25, \"search.defaultSources\": [\"openalex\",\"arxiv\"], \"manuscript.genre\": \"research paper\", \"manuscript.intent\": \"report\"}."},
			},
			"required": []string{"settings"},
		},
	}
}

func mcpResetConfigTool() mcpTool {
	return mcpTool{
		Name:        MCPToolResetConfig,
		Description: "Reset WisDev runtime knobs to built-in defaults. Pass 'keys' for a subset, or omit to reset everything. Use after exploratory tuning.",
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
		Description: "List registered academic search providers (canonical name, domains, health). Call once per session to learn valid 'sources' / search.defaultSources values before searching.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func mcpCapabilitiesTool() mcpTool {
	return mcpTool{
		Name:        MCPToolCapabilities,
		Description: "One-shot overview of this MCP control surface: tools, tunable config groups, and resources (wisdev://config|providers|capabilities). Prefer this at session start if unsure which tool to call.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

// mcpServerInstructions is returned on initialize so Claude Code / Cursor agents
// get routing guidance without reading external docs.
const mcpServerInstructions = `WisDev academic research MCP (stdio). Prefer wisdev* tool names; scholarlm* aliases still work on tools/call.

Tool routing:
- Topic / literature search → wisdevSearchPapers
- Known DOI / arXiv / paper ID → wisdevPaperLookup
- Claim verification / quotable snippets → wisdevEvidenceSearch
- Author publication list (by authorId) → wisdevAuthorSearch
- Draft a paper / report / lit review → wisdevGenerateManuscript (DocGen; intents fullpaper|report|litreview)
- Discover providers / knobs → wisdevListProviders, wisdevCapabilities, wisdevGetConfig / wisdevTuneConfig

Resources: wisdev://config, wisdev://providers, wisdev://capabilities.
This is NOT the multi-iteration YOLO research loop — for that use the CLI: wisdev "question".
Do not invent citations; quote IDs/DOIs returned by tools.`

type mcpPrompt struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Arguments   []mcpPromptArg `json:"arguments,omitempty"`
}

type mcpPromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

func mcpPrompts() []mcpPrompt {
	return []mcpPrompt{
		{
			Name:        "wisdev_literature_search",
			Description: "Search academic literature on a topic and summarize grounded findings with citations.",
			Arguments: []mcpPromptArg{
				{Name: "query", Description: "Research topic or question", Required: true},
				{Name: "minCitations", Description: "Optional citation floor for quality filtering", Required: false},
			},
		},
		{
			Name:        "wisdev_evidence_check",
			Description: "Find claim-grounded evidence snippets for or against an assertion.",
			Arguments: []mcpPromptArg{
				{Name: "claim", Description: "Claim or hypothesis to verify", Required: true},
			},
		},
		{
			Name:        "wisdev_docgen",
			Description: "Generate a grounded document (fullpaper, report, or litreview) via wisdevGenerateManuscript.",
			Arguments: []mcpPromptArg{
				{Name: "query", Description: "Manuscript topic / research question", Required: true},
				{Name: "intent", Description: "fullpaper | report | litreview (default fullpaper)", Required: false},
				{Name: "citationStyle", Description: "apa | mla | chicago | vancouver | ieee | harvard | nature", Required: false},
			},
		},
	}
}

// ──────────────────────────────────────────────
// Manuscript generation injection
// ──────────────────────────────────────────────
//
// internal/docgen imports internal/wisdev (it drives the manuscript pipeline),
// so internal/wisdev cannot import internal/docgen without a cycle. To give the
// MCP manuscript tool the full DocGen surface (report / litreview intents,
// citation styles, latex/html rendering) we accept an injected generator that a
// higher layer able to import internal/docgen (the CLI) wires up via
// SetMCPManuscriptGenerator. When no generator is injected the tool falls back
// to the built-in full-paper pipeline path, so library consumers and tests keep
// working unchanged.

// MCPManuscriptOptions carries the resolved inputs for the injected generator.
type MCPManuscriptOptions struct {
	Query         string
	Intent        string
	CitationStyle string
	Format        string
	Papers        []search.Paper
	PythonURL     string
	TargetWords   int
	MinCitations  int
	SectionFlow   []string
	ReviewRounds  int
	Genre         string
	// Instructions is free-text author steering (tone, audience, emphasis).
	// Applied to the built-in pipeline as CustomInstructions and to DocGen as VoiceInstructions.
	Instructions string
}

// MCPManuscriptGenerator generates and renders a document for the MCP tool.
type MCPManuscriptGenerator func(ctx context.Context, opts MCPManuscriptOptions) (rendered string, pipeline ManuscriptPipelineResult, err error)

var mcpManuscriptGenerator MCPManuscriptGenerator

// SetMCPManuscriptGenerator injects the DocGen-backed manuscript generator. It
// is called once from a layer that can import internal/docgen (e.g. the CLI).
func SetMCPManuscriptGenerator(fn MCPManuscriptGenerator) { mcpManuscriptGenerator = fn }

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
		ServerVersion:  "1.2.0",
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
		return s.handlePromptsList()
	case "prompts/get":
		return s.handlePromptsGet(req.Params)
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
			"tools":     map[string]any{"listChanged": false},
			"resources": map[string]any{"listChanged": false},
			"prompts":   map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    s.ServerName,
			"version": s.ServerVersion,
		},
		"instructions": mcpServerInstructions,
	}, nil
}

func (s *MCPServer) handlePromptsList() (any, *mcpError) {
	return map[string]any{"prompts": mcpPrompts()}, nil
}

type mcpPromptGetParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
}

func (s *MCPServer) handlePromptsGet(raw json.RawMessage) (any, *mcpError) {
	var params mcpPromptGetParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &mcpError{Code: mcpErrInvalidParams, Message: "invalid prompts/get params: " + err.Error()}
		}
	}
	name := strings.TrimSpace(params.Name)
	args := params.Arguments
	if args == nil {
		args = map[string]string{}
	}

	var text string
	switch name {
	case "wisdev_literature_search":
		query := strings.TrimSpace(args["query"])
		if query == "" {
			return nil, &mcpError{Code: mcpErrInvalidParams, Message: "argument 'query' is required"}
		}
		minCit := strings.TrimSpace(args["minCitations"])
		text = "Use WisDev MCP tools to research: " + query + "\n" +
			"1. Optionally call wisdevListProviders or wisdevCapabilities once.\n" +
			"2. Call wisdevSearchPapers with query=\"" + query + "\""
		if minCit != "" {
			text += " and minCitations=" + minCit
		}
		text += ".\n3. Summarize findings and cite returned DOIs/IDs only — do not invent citations."
	case "wisdev_evidence_check":
		claim := strings.TrimSpace(args["claim"])
		if claim == "" {
			return nil, &mcpError{Code: mcpErrInvalidParams, Message: "argument 'claim' is required"}
		}
		text = "Verify this claim with WisDev evidence tools: " + claim + "\n" +
			"1. Call wisdevEvidenceSearch with claim=\"" + claim + "\".\n" +
			"2. Optionally follow up with wisdevPaperLookup on the strongest sources.\n" +
			"3. Report supporting vs contradicting snippets with citations from tool results only."
	case "wisdev_docgen":
		query := strings.TrimSpace(args["query"])
		if query == "" {
			return nil, &mcpError{Code: mcpErrInvalidParams, Message: "argument 'query' is required"}
		}
		intent := strings.TrimSpace(args["intent"])
		if intent == "" {
			intent = "fullpaper"
		}
		style := strings.TrimSpace(args["citationStyle"])
		if style == "" {
			style = "apa"
		}
		text = "Generate a grounded document with wisdevGenerateManuscript.\n" +
			"query=\"" + query + "\", intent=\"" + intent + "\", citationStyle=\"" + style + "\", format=\"markdown\".\n" +
			"Return the rendered document; do not invent bibliography entries beyond tool output."
	default:
		return nil, &mcpError{Code: mcpErrInvalidParams, Message: "unknown prompt: " + name}
	}

	return map[string]any{
		"description": "WisDev MCP prompt: " + name,
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": map[string]any{
					"type": "text",
					"text": text,
				},
			},
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
	reviewRounds := mcpIntArg(args, "reviewRounds", c.Int(CfgManuscriptReviewRounds))
	if reviewRounds < 0 {
		reviewRounds = 0
	}
	genre := strings.TrimSpace(mcpStringArg(args, "genre"))
	if genre == "" {
		genre = c.String(CfgManuscriptGenre)
	}
	intent := strings.TrimSpace(mcpStringArg(args, "intent"))
	if intent == "" {
		intent = c.String(CfgManuscriptIntent)
	}
	if intent == "" {
		intent = "fullpaper"
	}
	citationStyle := strings.TrimSpace(mcpStringArg(args, "citationStyle"))
	if citationStyle == "" {
		citationStyle = c.String(CfgManuscriptCitationStyle)
	}
	if citationStyle == "" {
		citationStyle = "apa"
	}
	format := strings.TrimSpace(mcpStringArg(args, "format"))
	if format == "" {
		format = c.String(CfgManuscriptFormat)
	}
	if format == "" {
		format = "markdown"
	}
	customInstructions := strings.TrimSpace(mcpStringArg(args, "instructions"))

	sr := search.ParallelSearch(ctx, s.SearchRegistry, query, search.SearchOpts{
		Limit:       maxPapers,
		Domain:      domain,
		Sources:     sources,
		QualitySort: true,
	})

	// When a DocGen-backed generator is injected (wired from the CLI), use it
	// for the full intent/format/citation-style surface. Otherwise fall back to
	// the built-in full-paper pipeline path so library consumers keep working.
	if mcpManuscriptGenerator != nil {
		rendered, _, err := mcpManuscriptGenerator(ctx, MCPManuscriptOptions{
			Query:         query,
			Intent:        intent,
			CitationStyle: citationStyle,
			Format:        format,
			Papers:        sr.Papers,
			PythonURL:     ResolvePythonBase(),
			TargetWords:   targetWords,
			MinCitations:  minCitations,
			SectionFlow:   sectionFlow,
			ReviewRounds:  reviewRounds,
			Genre:         genre,
			Instructions:  customInstructions,
		})
		if err != nil {
			return nil, fmt.Errorf("manuscript generation failed: %w", err)
		}
		return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: rendered}}}, nil
	}

	// Built-in fallback: full-paper pipeline only (no report/litreview intents).
	pipeline := NewManuscriptPipeline(ResolvePythonBase())
	pipeline.Checkpoints = NewFileCheckpointStore("")
	pipeline.TargetWords = targetWords
	pipeline.MinCitations = minCitations
	pipeline.SectionFlow = sectionFlow
	pipeline.ReviewRounds = reviewRounds
	pipeline.Genre = genre
	pipeline.CustomInstructions = customInstructions
	jobID := fmt.Sprintf("mcp_docgen_%d", time.Now().UnixMilli())
	result, err := pipeline.Run(ctx, jobID, query, sr.Papers)
	if err != nil {
		return nil, fmt.Errorf("manuscript generation failed: %w", err)
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
	return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: strings.TrimRight(b.String(), "\n")}}}, nil
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
