// Package wisdev — MCP HTTP gateway handlers.
//
// This file wires the MCPServer and MCPADKBridge into the AgentGateway so that
// external MCP clients (Claude Desktop, Cursor, ADK agents, etc.) can reach the
// WisDev MCP tools over HTTP.
//
// Routes added by RegisterMCPRoutes (called once during server startup):
//   POST /wisdev/mcp        — main JSON-RPC 2.0 MCP endpoint
//   GET  /wisdev/mcp        — MCP capability probe (returns server metadata)
//   GET  /wisdev/mcp/tools  — convenience REST listing of all exposed tools
//   GET  /wisdev/mcp/status — live status: ADK wiring, registry size, latency probe
package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// MCPRouteConfig holds configuration for the MCP route group.
// All fields are optional; sensible defaults are used when omitted.
type MCPRouteConfig struct {
	// BasePath is the URL prefix for all MCP routes. Defaults to "/wisdev/mcp".
	BasePath string
}

// RegisterMCPRoutes mounts the MCP endpoint group on mux.
// It creates a new MCPServer and MCPADKBridge backed by gw.SearchRegistry.
//
// Usage in your server startup:
//
//	mux := http.NewServeMux()
//	gw.RegisterMCPRoutes(mux, MCPRouteConfig{})
func (gw *AgentGateway) RegisterMCPRoutes(mux *http.ServeMux, cfg MCPRouteConfig) {
	base := cfg.BasePath
	if base == "" {
		base = "/wisdev/mcp"
	}

	mcpSrv := NewMCPServer(gw.SearchRegistry)
	mcpBridge := NewMCPADKBridge(gw.SearchRegistry)
	adkTools := mcpBridge.BuildADKTools()

	slog.Info("mcp routes registered",
		"service", "go_orchestrator",
		"runtime", "go",
		"component", "wisdev.mcp_gateway",
		"operation", "register_routes",
		"base_path", base,
		"mcp_tools", len(mcpSrv.allTools()),
		"adk_tools", len(adkTools),
		"stage", "startup",
	)

	// Main MCP JSON-RPC 2.0 endpoint (POST) + capability probe (GET).
	mux.Handle(base, mcpSrv.Handler())

	// Convenience REST endpoint: GET /wisdev/mcp/tools
	mux.HandleFunc(base+"/tools", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		type toolListing struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		}
		tools := mcpSrv.allTools()
		listings := make([]toolListing, 0, len(tools))
		for _, tool := range tools {
			listings = append(listings, toolListing{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tools":           listings,
			"count":           len(listings),
			"protocolVersion": mcpProtocolVersion,
			"serverName":      mcpSrv.ServerName,
			"serverVersion":   mcpSrv.ServerVersion,
		})
	})

	// Live status endpoint: GET /wisdev/mcp/status
	mux.HandleFunc(base+"/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		start := time.Now()
		providerCount := 0
		if gw.SearchRegistry != nil {
			providerCount = len(gw.SearchRegistry.All())
		}

		type adkToolInfo struct {
			Name string `json:"name"`
		}
		adkToolInfos := make([]adkToolInfo, 0, len(adkTools))
		for _, t := range adkTools {
			adkToolInfos = append(adkToolInfos, adkToolInfo{Name: t.Name()})
		}

		llmStatus := map[string]any{"configured": false}
		if gw.LLMClient != nil {
			probeCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			llmStatus = gw.LLMClient.DirectProviderStatus(probeCtx)
			cancel()
		}

		adkBridge := map[string]any{
			"enabled":   true,
			"toolCount": len(adkTools),
			"tools":     adkToolInfos,
			"runtime":   officialADKModule,
		}
		if gw.ADKRuntime != nil {
			if meta := gw.ADKRuntime.Metadata(); meta != nil {
				if backend := strings.TrimSpace(fmt.Sprint(meta["modelBackend"])); backend != "" {
					adkBridge["modelBackend"] = backend
				}
				if source := strings.TrimSpace(fmt.Sprint(meta["credentialSource"])); source != "" {
					adkBridge["credentialSource"] = source
				}
				adkBridge["ready"] = meta["ready"]
				adkBridge["status"] = meta["status"]
			}
		}

		status := map[string]any{
			"status":          "ok",
			"serverName":      mcpSrv.ServerName,
			"serverVersion":   mcpSrv.ServerVersion,
			"protocolVersion": mcpProtocolVersion,
			"llmDirect":       llmStatus,
			"mcpTools": map[string]any{
				"count": len(mcpSrv.allTools()),
				"names": func() []string {
					names := make([]string, 0, len(mcpSrv.allTools()))
					for _, t := range mcpSrv.allTools() {
						names = append(names, t.Name)
					}
					return names
				}(),
			},
			"adkBridge": adkBridge,
			"searchRegistry": map[string]any{
				"providerCount": providerCount,
				"providers":     gw.SearchRegistry.All(),
			},
			"latencyMs": time.Since(start).Milliseconds(),
		}

		slog.Info("mcp status probe",
			"service", "go_orchestrator",
			"component", "wisdev.mcp_gateway",
			"operation", "status",
			"provider_count", providerCount,
			"mcp_tools", len(mcpSrv.allTools()),
			"adk_tools", len(adkTools),
			"latency_ms", time.Since(start).Milliseconds(),
		)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})
}

// MCPStatus returns a lightweight status map for embedding in /wisdev/health
// or similar aggregate health endpoints.
func (gw *AgentGateway) MCPStatus() map[string]any {
	providerCount := 0
	if gw.SearchRegistry != nil {
		providerCount = len(gw.SearchRegistry.All())
	}
	bridge := NewMCPADKBridge(gw.SearchRegistry)
	adkTools := bridge.BuildADKTools()
	srv := NewMCPServer(gw.SearchRegistry)
	llmStatus := map[string]any{"configured": false}
	if gw.LLMClient != nil {
		probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		llmStatus = gw.LLMClient.DirectProviderStatus(probeCtx)
		cancel()
	}
	return map[string]any{
		"mcpEnabled":      true,
		"protocolVersion": mcpProtocolVersion,
		"mcpToolCount":    len(srv.allTools()),
		"adkToolCount":    len(adkTools),
		"providerCount":   providerCount,
		"llmDirect":       llmStatus,
	}
}
