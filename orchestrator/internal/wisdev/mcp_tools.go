package wisdev

import "strings"

// Canonical MCP tool names for the open-source WisDev runtime.
const (
	MCPToolSearchPapers   = "wisdevSearchPapers"
	MCPToolPaperLookup    = "wisdevPaperLookup"
	MCPToolEvidenceSearch = "wisdevEvidenceSearch"
	MCPToolAuthorSearch   = "wisdevAuthorSearch"
	MCPServerName         = "wisdev-mcp"
)

// normalizeMCPToolName maps legacy ScholarLM tool aliases to canonical WisDev names.
func normalizeMCPToolName(name string) string {
	switch strings.TrimSpace(name) {
	case "scholarlmSearchPapers", MCPToolSearchPapers:
		return MCPToolSearchPapers
	case "scholarlmPaperLookup", MCPToolPaperLookup:
		return MCPToolPaperLookup
	case "scholarlmEvidenceSearch", MCPToolEvidenceSearch:
		return MCPToolEvidenceSearch
	case "scholarlmAuthorSearch", MCPToolAuthorSearch:
		return MCPToolAuthorSearch
	default:
		return strings.TrimSpace(name)
	}
}

func isKnownMCPTool(name string) bool {
	switch normalizeMCPToolName(name) {
	case MCPToolSearchPapers, MCPToolPaperLookup, MCPToolEvidenceSearch, MCPToolAuthorSearch:
		return true
	default:
		return false
	}
}
