package wisdev

import (
	"encoding/json"
	"net/http"
	"testing"
)

// decodeToolResult pulls the JSON payload out of an MCP tools/call text result.
func decodeToolResult(t *testing.T, rr interface{ Bytes() []byte }) (map[string]any, bool) {
	t.Helper()
	var resp mcpResponse
	if err := json.Unmarshal(rr.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected jsonrpc error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result not an object: %T", resp.Result)
	}
	isErr, _ := result["isError"].(bool)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in tool result")
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("decode tool payload: %v\n%s", err, text)
	}
	return payload, isErr
}

func TestRuntimeConfigDefaultsAndTune(t *testing.T) {
	c := NewRuntimeConfig()
	if got := c.Int(CfgSearchLimit); got != 10 {
		t.Fatalf("default search.limit want 10 got %d", got)
	}
	// Changed deliberately: citation-weighted re-ranking gives relevance only
	// 0.50 of ScoreQuality, so on a narrow technical query it surfaced famous
	// papers from adjacent fields. See TestQualitySortDefaultsOffOnAgentSurface
	// for the reasoning; it remains tunable per call and per session.
	if c.Bool(CfgSearchQualitySort) {
		t.Fatalf("default search.qualitySort want false on the agent surface")
	}

	applied, err := c.Tune(map[string]any{
		CfgSearchLimit:          float64(25), // JSON numbers decode as float64
		CfgSearchDefaultSources: []any{"openalex", "arxiv"},
		CfgManuscriptFormat:     "json",
	})
	if err != nil {
		t.Fatalf("unexpected tune error: %v", err)
	}
	if len(applied) != 3 {
		t.Fatalf("want 3 applied got %d", len(applied))
	}
	if c.Int(CfgSearchLimit) != 25 {
		t.Fatalf("search.limit not applied: %d", c.Int(CfgSearchLimit))
	}
	if got := c.Strings(CfgSearchDefaultSources); len(got) != 2 || got[0] != "openalex" {
		t.Fatalf("defaultSources not applied: %v", got)
	}
	if c.String(CfgManuscriptFormat) != "json" {
		t.Fatalf("manuscript.format not applied: %q", c.String(CfgManuscriptFormat))
	}
}

func TestRuntimeConfigValidation(t *testing.T) {
	c := NewRuntimeConfig()
	applied, err := c.Tune(map[string]any{
		CfgSearchLimit:      float64(999), // out of range (max 50)
		"search.bogus":      "x",          // unknown key
		CfgManuscriptFormat: "pdf",        // not in enum
		CfgEvidenceLimit:    float64(8),   // valid — should still apply
	})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if _, ok := applied[CfgEvidenceLimit]; !ok {
		t.Fatalf("valid update should still apply despite sibling errors: %v", applied)
	}
	if c.Int(CfgSearchLimit) != 10 {
		t.Fatalf("out-of-range update must not apply, got %d", c.Int(CfgSearchLimit))
	}
	if c.String(CfgManuscriptFormat) != "markdown" {
		t.Fatalf("enum-violating update must not apply, got %q", c.String(CfgManuscriptFormat))
	}
}

func TestRuntimeConfigReset(t *testing.T) {
	c := NewRuntimeConfig()
	if _, err := c.Tune(map[string]any{CfgSearchLimit: float64(40)}); err != nil {
		t.Fatalf("tune: %v", err)
	}
	if err := c.Reset(CfgSearchLimit); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if c.Int(CfgSearchLimit) != 10 {
		t.Fatalf("reset did not restore default: %d", c.Int(CfgSearchLimit))
	}
	if err := c.Reset("does.not.exist"); err == nil {
		t.Fatalf("expected error resetting unknown key")
	}
}

func TestMCPToolsListIncludesTuningTools(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	result, _ := resp.Result.(map[string]any)
	tools, _ := result["tools"].([]any)
	found := map[string]bool{}
	for _, raw := range tools {
		if tm, ok := raw.(map[string]any); ok {
			if name, ok := tm["name"].(string); ok {
				found[name] = true
			}
		}
	}
	for _, want := range []string{MCPToolGetConfig, MCPToolTuneConfig, MCPToolResetConfig, MCPToolListProviders, MCPToolCapabilities} {
		if !found[want] {
			t.Errorf("tools/list missing %s", want)
		}
	}
}

func TestMCPGetConfigTool(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wisdevGetConfig","arguments":{}}}`)
	payload, isErr := decodeToolResult(t, rr.Body)
	if isErr {
		t.Fatalf("unexpected error result")
	}
	knobs, _ := payload["knobs"].([]any)
	if len(knobs) != len(knobRegistry()) {
		t.Fatalf("want %d knobs got %d", len(knobRegistry()), len(knobs))
	}
}

func TestMCPTuneConfigAffectsServerState(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wisdevTuneConfig","arguments":{"settings":{"search.limit":33,"manuscript.genre":"research paper"}}}}`)
	payload, isErr := decodeToolResult(t, rr.Body)
	if isErr {
		t.Fatalf("unexpected error: %v", payload)
	}
	if srv.Config.Int(CfgSearchLimit) != 33 {
		t.Fatalf("tune did not affect server config: %d", srv.Config.Int(CfgSearchLimit))
	}
	if srv.Config.String(CfgManuscriptGenre) != "research paper" {
		t.Fatalf("genre not applied: %q", srv.Config.String(CfgManuscriptGenre))
	}
}

func TestMCPTuneConfigRejectsBadValue(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wisdevTuneConfig","arguments":{"settings":{"search.limit":9999}}}}`)
	payload, isErr := decodeToolResult(t, rr.Body)
	if !isErr {
		t.Fatalf("expected isError=true for out-of-range tune")
	}
	if _, ok := payload["rejected"]; !ok {
		t.Fatalf("expected rejected field, got %v", payload)
	}
	if srv.Config.Int(CfgSearchLimit) != 10 {
		t.Fatalf("bad value must not apply: %d", srv.Config.Int(CfgSearchLimit))
	}
}

func TestMCPResetConfigTool(t *testing.T) {
	srv := NewMCPServer(nil)
	if _, err := srv.Config.Tune(map[string]any{CfgSearchLimit: float64(40)}); err != nil {
		t.Fatalf("tune: %v", err)
	}
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wisdevResetConfig","arguments":{}}}`)
	if _, isErr := decodeToolResult(t, rr.Body); isErr {
		t.Fatalf("unexpected error result")
	}
	if srv.Config.Int(CfgSearchLimit) != 10 {
		t.Fatalf("reset tool did not restore default: %d", srv.Config.Int(CfgSearchLimit))
	}
}

func TestMCPCapabilitiesTool(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wisdevCapabilities","arguments":{}}}`)
	payload, isErr := decodeToolResult(t, rr.Body)
	if isErr {
		t.Fatalf("unexpected error result")
	}
	if _, ok := payload["tunableGroups"].(map[string]any); !ok {
		t.Fatalf("capabilities missing tunableGroups: %v", payload)
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != len(srv.allTools()) {
		t.Fatalf("capabilities tool count mismatch: %d vs %d", len(tools), len(srv.allTools()))
	}
}

func TestMCPResourcesListAndRead(t *testing.T) {
	srv := NewMCPServer(nil)
	rr := mcpPost(t, srv, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	var resp mcpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	result, _ := resp.Result.(map[string]any)
	resources, _ := result["resources"].([]any)
	if len(resources) != 3 {
		t.Fatalf("want 3 resources got %d", len(resources))
	}

	rr = mcpPost(t, srv, `{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"wisdev://config"}}`)
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	rr = mcpPost(t, srv, `{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"wisdev://nope"}}`)
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error for unknown resource uri")
	}
}

func TestMCPTuneConfigOverStdio(t *testing.T) {
	srv := NewMCPServer(nil)
	line := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"wisdevTune","arguments":{"settings":{"evidence.limit":12}}}}`)
	if _, _, err := srv.HandleStdioLine(nil, line); err != nil {
		t.Fatalf("stdio handle: %v", err)
	}
	if srv.Config.Int(CfgEvidenceLimit) != 12 {
		t.Fatalf("stdio tune did not apply: %d", srv.Config.Int(CfgEvidenceLimit))
	}
}
