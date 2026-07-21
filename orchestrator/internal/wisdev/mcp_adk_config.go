// Package wisdev — ADK function-tool wrappers for the MCP tuning surface.
//
// These expose the same get/tune/reset/list/capabilities control surface as the
// JSON-RPC MCP tools (mcp_config_handlers.go) to in-process Google ADK agents,
// so an ADK-driven external LLM can also discover and tune every runtime knob.
// They operate on the bridge's shared MCPServer.Config.
package wisdev

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

type adkGetConfigInput struct {
	Keys []string `json:"keys,omitempty"`
}

type adkConfigOutput struct {
	Knobs    []configView   `json:"knobs,omitempty"`
	Config   map[string]any `json:"config,omitempty"`
	Applied  map[string]any `json:"applied,omitempty"`
	Rejected string         `json:"rejected,omitempty"`
	MCPTool  string         `json:"mcpTool"`
}

func (b *MCPADKBridge) getConfigTool() adktool.Tool {
	tool, _ := functiontool.New(functiontool.Config{
		Name:        MCPToolGetConfig,
		Description: "Inspect the WisDev runtime's tunable configuration: every knob with type, range/enum, default, and current value. Call before tuning to discover what can change.",
	}, func(_ agent.Context, in adkGetConfigInput) (adkConfigOutput, error) {
		views, _ := b.server.buildConfigViews(in.Keys)
		return adkConfigOutput{Knobs: views, MCPTool: MCPToolGetConfig}, nil
	})
	return tool
}

type adkTuneConfigInput struct {
	Settings map[string]any `json:"settings"`
}

func (b *MCPADKBridge) tuneConfigTool() adktool.Tool {
	tool, _ := functiontool.New(functiontool.Config{
		Name:        MCPToolTuneConfig,
		Description: "Tune the WisDev runtime. Pass settings (knob key -> value) to change defaults that subsequent search/evidence/author/manuscript calls inherit. Values are validated against each knob's type and range/enum.",
	}, func(_ agent.Context, in adkTuneConfigInput) (adkConfigOutput, error) {
		if len(in.Settings) == 0 {
			return adkConfigOutput{}, fmt.Errorf("settings is required (map of knob key -> value)")
		}
		applied, tuneErr := b.server.cfg().Tune(in.Settings)
		out := adkConfigOutput{Applied: applied, Config: b.server.cfg().Snapshot(), MCPTool: MCPToolTuneConfig}
		if tuneErr != nil {
			out.Rejected = tuneErr.Error()
		}
		return out, nil
	})
	return tool
}

type adkResetConfigInput struct {
	Keys []string `json:"keys,omitempty"`
}

func (b *MCPADKBridge) resetConfigTool() adktool.Tool {
	tool, _ := functiontool.New(functiontool.Config{
		Name:        MCPToolResetConfig,
		Description: "Reset WisDev runtime knobs to their built-in defaults. Pass keys to reset a subset, or omit to reset all.",
	}, func(_ agent.Context, in adkResetConfigInput) (adkConfigOutput, error) {
		resetErr := b.server.cfg().Reset(in.Keys...)
		out := adkConfigOutput{Config: b.server.cfg().Snapshot(), MCPTool: MCPToolResetConfig}
		if resetErr != nil {
			out.Rejected = resetErr.Error()
		}
		return out, nil
	})
	return tool
}

type adkListProvidersInput struct{}

type adkListProvidersOutput struct {
	ProviderCount int            `json:"providerCount"`
	Providers     []providerView `json:"providers"`
	MCPTool       string         `json:"mcpTool"`
}

func (b *MCPADKBridge) listProvidersTool() adktool.Tool {
	tool, _ := functiontool.New(functiontool.Config{
		Name:        MCPToolListProviders,
		Description: "List the registered academic search providers with name, specialised domains, and health. Use to discover valid 'sources' values and the search.defaultSources knob.",
	}, func(_ agent.Context, _ adkListProvidersInput) (adkListProvidersOutput, error) {
		views := b.server.providerViews()
		return adkListProvidersOutput{ProviderCount: len(views), Providers: views, MCPTool: MCPToolListProviders}, nil
	})
	return tool
}
