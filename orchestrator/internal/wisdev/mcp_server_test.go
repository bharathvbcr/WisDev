package wisdev

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		"wisdevSearchPapers":   false,
		"wisdevPaperLookup":    false,
		"wisdevEvidenceSearch": false,
		"wisdevAuthorSearch":   false,
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

func TestMCPArgHelpers(t *testing.T) {
	args := map[string]any{
		"str":  "  hello  ",
		"int":  float64(42),
		"bool": true,
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
