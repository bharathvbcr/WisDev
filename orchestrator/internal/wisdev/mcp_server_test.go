package wisdev

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

func TestMCPServerInitialize(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{}}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", rr.Code)
	}
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatal("result must be map")
	}
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("protocolVersion want %s got %v", mcpProtocolVersion, result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "wisdev-mcp" {
		t.Errorf("serverInfo.name want wisdev-mcp got %v", info["name"])
	}
	instructions, _ := result["instructions"].(string)
	if !strings.Contains(instructions, "wisdevSearchPapers") {
		t.Errorf("expected agent instructions mentioning wisdevSearchPapers, got %q", instructions)
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["prompts"]; !ok {
		t.Errorf("expected prompts capability, got %#v", caps)
	}
	if _, ok := caps["resources"]; !ok {
		t.Errorf("expected resources capability, got %#v", caps)
	}
}

func TestMCPServerPromptsListAndGet(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":10,"method":"prompts/list"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", rr.Code)
	}
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	prompts, _ := result["prompts"].([]any)
	if len(prompts) < 3 {
		t.Fatalf("expected at least 3 prompts, got %d", len(prompts))
	}

	rr = mcpPost(t, srv, `{"jsonrpc":"2.0","id":11,"method":"prompts/get","params":{"name":"wisdev_docgen","arguments":{"query":"RAG for papers","intent":"report"}}}`)
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("prompts/get error: %v", resp.Error)
	}
	getResult, _ := resp.Result.(map[string]any)
	messages, _ := getResult["messages"].([]any)
	if len(messages) == 0 {
		t.Fatal("expected prompt messages")
	}
}

func TestMCPServerToolsList(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", rr.Code)
	}
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) < 4 {
		t.Errorf("expected at least 4 tools, got %d", len(tools))
	}
	wantNames := map[string]bool{
		"wisdevSearchPapers":       false,
		"wisdevPaperLookup":        false,
		"wisdevEvidenceSearch":     false,
		"wisdevAuthorSearch":       false,
		"wisdevGenerateManuscript": false,
	}
	for _, toolRaw := range tools {
		if tool, ok := toolRaw.(map[string]any); ok {
			if name, ok := tool["name"].(string); ok {
				wantNames[name] = true
			}
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("tool %q not found in tools/list", name)
		}
	}
}

func TestMCPServerMethodNotFound(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":3,"method":"nonexistent"}`)
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != mcpErrMethodNotFound {
		t.Errorf("want %d got %d", mcpErrMethodNotFound, resp.Error.Code)
	}
}

func TestMCPServerInvalidJSON(t *testing.T) {
	srv := NewMCPServer(nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != mcpErrParse {
		t.Errorf("want parse error, got %v", resp.Error)
	}
}

func TestMCPServerGETCapabilities(t *testing.T) {
	srv := NewMCPServer(nil)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", rr.Code)
	}
	var result map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["name"] != "wisdev-mcp" {
		t.Errorf("name: got %v", result["name"])
	}
}

func TestMCPServerToolCallUnknownTool(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"unknownTool","arguments":{}}}`)
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != mcpErrMethodNotFound {
		t.Errorf("want method not found error, got %v", resp.Error)
	}
}

func TestMCPServerToolCallNoRegistry(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"wisdevSearchPapers","arguments":{"query":"transformers"}}}`)
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("expected tool-level error not JSON-RPC error: %v", resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	isError, _ := result["isError"].(bool)
	if !isError {
		t.Error("expected isError=true when registry is nil")
	}
}

func TestMCPServerLegacyScholarLMAlias(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"scholarlmSearchPapers","arguments":{"query":"transformers"}}}`)
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("legacy alias should resolve to canonical tool, got rpc error: %v", resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	isError, _ := result["isError"].(bool)
	if !isError {
		t.Error("expected tool-level error when registry is nil")
	}
}

func TestMCPServerPing(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestMCPServerMissingRequiredParams(t *testing.T) {
	srv := NewMCPServer(&search.ProviderRegistry{})
	cases := []struct {
		tool string
		args string
	}{
		{"wisdevSearchPapers", "{}"},
		{"wisdevEvidenceSearch", "{}"},
		{"wisdevPaperLookup", "{}"},
		{"wisdevAuthorSearch", "{}"},
		{"wisdevGenerateManuscript", "{}"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tc.tool + `","arguments":` + tc.args + `}}`
			rr := mcpPost(t, srv, body)
			var resp mcpResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			result, _ := resp.Result.(map[string]any)
			isError, _ := result["isError"].(bool)
			if !isError {
				t.Errorf("expected isError=true for missing required param in %s", tc.tool)
			}
		})
	}
}

func TestMCPADKBridgeBuildTools(t *testing.T) {
	bridge := NewMCPADKBridge(&search.ProviderRegistry{})
	tools := bridge.BuildADKTools()
	if len(tools) < 4 {
		t.Errorf("expected at least 4 ADK tools, got %d", len(tools))
	}
	wantNames := map[string]bool{
		"wisdevSearchPapers":   false,
		"wisdevPaperLookup":    false,
		"wisdevEvidenceSearch": false,
		"wisdevAuthorSearch":   false,
	}
	for _, tool := range tools {
		wantNames[tool.Name()] = true
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("ADK tool %q not registered", name)
		}
	}
}

func TestMCPGenerateManuscriptToolRegistered(t *testing.T) {
	if !isKnownMCPTool("wisdevGenerateManuscript") {
		t.Error("wisdevGenerateManuscript should be a known MCP tool")
	}
	// Aliases normalize to the canonical name.
	for _, alias := range []string{"wisdevDocGen", "scholarlmGenerateManuscript"} {
		if got := normalizeMCPToolName(alias); got != MCPToolGenerateManuscript {
			t.Errorf("alias %q normalized to %q, want %q", alias, got, MCPToolGenerateManuscript)
		}
	}
	// The tool definition exposes the words option and requires a query.
	def := mcpGenerateManuscriptTool()
	props, _ := def.InputSchema["properties"].(map[string]any)
	if _, ok := props["words"]; !ok {
		t.Error("wisdevGenerateManuscript should expose a 'words' option")
	}
	required, _ := def.InputSchema["required"].([]string)
	if len(required) != 1 || required[0] != "query" {
		t.Errorf("expected query to be the only required field, got %v", required)
	}
	// New DocGen params: intent, citationStyle, expanded format enum.
	if _, ok := props["intent"]; !ok {
		t.Error("wisdevGenerateManuscript should expose 'intent'")
	}
	if _, ok := props["citationStyle"]; !ok {
		t.Error("wisdevGenerateManuscript should expose 'citationStyle'")
	}
	formatProp, ok := props["format"].(map[string]any)
	if !ok {
		t.Fatal("format property missing or wrong type")
	}
	formatEnum, _ := formatProp["enum"].([]string)
	wantFormats := map[string]bool{"markdown": false, "json": false, "latex": false, "html": false}
	for _, f := range formatEnum {
		wantFormats[f] = true
	}
	for f, found := range wantFormats {
		if !found {
			t.Errorf("format enum missing %q (got %v)", f, formatEnum)
		}
	}
}

func TestMCPManuscriptConfigKnobs(t *testing.T) {
	index := knobByKey()
	for _, key := range []string{CfgManuscriptIntent, CfgManuscriptCitationStyle} {
		if _, ok := index[key]; !ok {
			t.Errorf("missing knob %q in registry", key)
		}
	}
	cfg := NewRuntimeConfig()
	if cfg.String(CfgManuscriptIntent) != "fullpaper" {
		t.Errorf("default intent=%q", cfg.String(CfgManuscriptIntent))
	}
	if cfg.String(CfgManuscriptCitationStyle) != "apa" {
		t.Errorf("default citation style=%q", cfg.String(CfgManuscriptCitationStyle))
	}
}

func TestMCPSectionWordBudget(t *testing.T) {
	p := &ManuscriptPipeline{TargetWords: 0}
	if got := p.sectionWordBudget(7); got != 0 {
		t.Errorf("no target -> budget 0, got %d", got)
	}
	p.TargetWords = 2100
	if got := p.sectionWordBudget(7); got != 300 {
		t.Errorf("2100/7 -> 300, got %d", got)
	}
	if got := p.sectionWordBudget(0); got != 0 {
		t.Errorf("no sections -> 0, got %d", got)
	}
}

func TestMCPArgHelpers(t *testing.T) {
	args := map[string]any{
		"str":   "  hello  ",
		"int":   float64(42),
		"bool":  true,
		"slice": []any{"a", "b"},
	}
	if got := mcpStringArg(args, "str"); got != "hello" {
		t.Errorf("string: want hello got %q", got)
	}
	if got := mcpIntArg(args, "int", 0); got != 42 {
		t.Errorf("int: want 42 got %d", got)
	}
	if got := mcpIntArg(args, "missing", 99); got != 99 {
		t.Errorf("int fallback: want 99 got %d", got)
	}
	if !mcpBoolArg(args, "bool", false) {
		t.Error("bool: want true")
	}
	if !mcpBoolArg(args, "missing", true) {
		t.Error("bool fallback: want true")
	}
	if got := mcpStringSliceArg(args, "slice"); len(got) != 2 {
		t.Errorf("slice: want 2 got %d", len(got))
	}
}

func mcpPost(t *testing.T, srv *MCPServer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// Ensure unused import is satisfied when registry funcs are needed.
var _ = context.Background
