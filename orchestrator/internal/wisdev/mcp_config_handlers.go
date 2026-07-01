// Package wisdev — MCP tool + resource handlers for the tuning surface.
//
// These handlers back the wisdevGetConfig / wisdevTuneConfig / wisdevResetConfig
// / wisdevListProviders / wisdevCapabilities tools and the MCP resources
// (wisdev://config, wisdev://providers, wisdev://capabilities). Together they
// let an external LLM discover, read, and change every runtime knob — the
// "tune anything" control surface — over both the HTTP and stdio transports.
package wisdev

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// defaultRuntimeConfig backs servers constructed without NewMCPServer (e.g.
// hand-built &MCPServer{} in tests) so config reads never panic.
var defaultRuntimeConfig = NewRuntimeConfig()

// cfg returns the server's live config, falling back to the shared defaults
// when a server was constructed without one.
func (s *MCPServer) cfg() *RuntimeConfig {
	if s.Config != nil {
		return s.Config
	}
	return defaultRuntimeConfig
}

// MCP resource URIs exposed by this runtime.
const (
	mcpResourceConfig       = "wisdev://config"
	mcpResourceProviders    = "wisdev://providers"
	mcpResourceCapabilities = "wisdev://capabilities"
)

// ───────────────── tuning tools ─────────────────

// configView merges a knob descriptor with its current value for output.
type configView struct {
	configKnob
	Current any `json:"current"`
}

// buildConfigViews returns the requested knobs (or all when keys is empty)
// merged with their current values, in registry order.
func (s *MCPServer) buildConfigViews(keys []string) ([]configView, []string) {
	snapshot := s.cfg().Snapshot()
	want := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		want[strings.TrimSpace(k)] = struct{}{}
	}
	index := knobByKey()
	var unknown []string
	for k := range want {
		if _, ok := index[k]; !ok && k != "" {
			unknown = append(unknown, k)
		}
	}
	views := make([]configView, 0, len(knobRegistry()))
	for _, knob := range knobRegistry() {
		if len(keys) > 0 {
			if _, ok := want[knob.Key]; !ok {
				continue
			}
		}
		views = append(views, configView{configKnob: knob, Current: snapshot[knob.Key]})
	}
	sort.Strings(unknown)
	return views, unknown
}

func (s *MCPServer) callGetConfig(args map[string]any) (*mcpToolResult, error) {
	keys := mcpStringSliceArg(args, "keys")
	views, unknown := s.buildConfigViews(keys)
	payload := map[string]any{
		"serverName":    s.ServerName,
		"serverVersion": s.ServerVersion,
		"knobCount":     len(views),
		"knobs":         views,
	}
	if len(unknown) > 0 {
		payload["unknownKeys"] = unknown
	}
	return jsonToolResult(payload)
}

func (s *MCPServer) callTuneConfig(args map[string]any) (*mcpToolResult, error) {
	rawSettings, ok := args["settings"].(map[string]any)
	if !ok || len(rawSettings) == 0 {
		return nil, fmt.Errorf("settings object is required (map of knob key -> value)")
	}
	applied, tuneErr := s.cfg().Tune(rawSettings)
	payload := map[string]any{
		"applied":      applied,
		"appliedCount": len(applied),
		"config":       s.cfg().Snapshot(),
	}
	if tuneErr != nil {
		payload["rejected"] = tuneErr.Error()
	}
	result, err := jsonToolResult(payload)
	if err != nil {
		return nil, err
	}
	// Surface validation problems as an MCP tool error so the LLM notices,
	// while still reporting which updates did apply.
	if tuneErr != nil {
		result.IsError = true
	}
	return result, nil
}

func (s *MCPServer) callResetConfig(args map[string]any) (*mcpToolResult, error) {
	keys := mcpStringSliceArg(args, "keys")
	resetErr := s.cfg().Reset(keys...)
	payload := map[string]any{
		"reset":  "complete",
		"config": s.cfg().Snapshot(),
	}
	if len(keys) > 0 {
		payload["reset"] = keys
	}
	if resetErr != nil {
		payload["rejected"] = resetErr.Error()
	}
	result, err := jsonToolResult(payload)
	if err != nil {
		return nil, err
	}
	if resetErr != nil {
		result.IsError = true
	}
	return result, nil
}

// ───────────────── introspection tools ─────────────────

type providerView struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains,omitempty"`
	Healthy bool     `json:"healthy"`
}

func (s *MCPServer) providerViews() []providerView {
	if s.SearchRegistry == nil {
		return nil
	}
	providers := s.SearchRegistry.All()
	views := make([]providerView, 0, len(providers))
	for _, p := range providers {
		views = append(views, providerView{
			Name:    p.Name(),
			Domains: p.Domains(),
			Healthy: p.Healthy(),
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}

func (s *MCPServer) callListProviders() (*mcpToolResult, error) {
	views := s.providerViews()
	return jsonToolResult(map[string]any{
		"providerCount": len(views),
		"providers":     views,
	})
}

// configGroups summarizes the tunable knobs grouped by capability for the
// capabilities overview.
func configGroups() map[string][]string {
	groups := map[string][]string{}
	for _, knob := range knobRegistry() {
		groups[knob.Group] = append(groups[knob.Group], knob.Key)
	}
	return groups
}

func (s *MCPServer) callCapabilities() (*mcpToolResult, error) {
	tools := s.allTools()
	toolViews := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		toolViews = append(toolViews, map[string]any{
			"name":        t.Name,
			"description": t.Description,
		})
	}
	return jsonToolResult(map[string]any{
		"serverName":      s.ServerName,
		"serverVersion":   s.ServerVersion,
		"protocolVersion": mcpProtocolVersion,
		"tools":           toolViews,
		"toolCount":       len(toolViews),
		"tunableGroups":   configGroups(),
		"tunableKnobs":    len(knobRegistry()),
		"resources":       []string{mcpResourceConfig, mcpResourceProviders, mcpResourceCapabilities},
		"transports":      []string{"http", "stdio"},
	})
}

// ───────────────── MCP resources ─────────────────

func (s *MCPServer) handleResourcesList() (any, *mcpError) {
	return map[string]any{
		"resources": []map[string]any{
			{"uri": mcpResourceConfig, "name": "WisDev runtime config", "description": "All tunable knobs with current values, types, and bounds.", "mimeType": "application/json"},
			{"uri": mcpResourceProviders, "name": "WisDev search providers", "description": "Registered academic search providers with domains and health.", "mimeType": "application/json"},
			{"uri": mcpResourceCapabilities, "name": "WisDev capabilities", "description": "Full MCP control surface: tools, tunable groups, resources.", "mimeType": "application/json"},
		},
	}, nil
}

type mcpResourceReadParams struct {
	URI string `json:"uri"`
}

func (s *MCPServer) handleResourcesRead(raw json.RawMessage) (any, *mcpError) {
	var params mcpResourceReadParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &mcpError{Code: mcpErrInvalidParams, Message: "invalid resources/read params: " + err.Error()}
		}
	}
	uri := strings.TrimSpace(params.URI)

	var payload any
	switch uri {
	case mcpResourceConfig:
		views, _ := s.buildConfigViews(nil)
		payload = map[string]any{"knobs": views}
	case mcpResourceProviders:
		payload = map[string]any{"providers": s.providerViews()}
	case mcpResourceCapabilities:
		payload = map[string]any{
			"tunableGroups": configGroups(),
			"toolCount":     len(s.allTools()),
		}
	default:
		return nil, &mcpError{Code: mcpErrInvalidParams, Message: "unknown resource uri: " + params.URI}
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, &mcpError{Code: mcpErrInternal, Message: err.Error()}
	}
	return map[string]any{
		"contents": []map[string]any{
			{"uri": uri, "mimeType": "application/json", "text": string(encoded)},
		},
	}, nil
}

// ───────────────── shared helper ─────────────────

// jsonToolResult marshals payload as pretty JSON inside an MCP text result.
func jsonToolResult(payload any) (*mcpToolResult, error) {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(encoded)}}}, nil
}
