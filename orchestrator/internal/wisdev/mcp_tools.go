package wisdev

import "strings"

// Canonical MCP tool names for the open-source WisDev runtime.
const (
	MCPToolSearchPapers       = "wisdevSearchPapers"
	MCPToolPaperLookup        = "wisdevPaperLookup"
	MCPToolEvidenceSearch     = "wisdevEvidenceSearch"
	MCPToolAuthorSearch       = "wisdevAuthorSearch"
	MCPToolGenerateManuscript = "wisdevGenerateManuscript"

	// Tuning / introspection tools: let an external LLM discover, read, and
	// change every runtime knob, and inspect providers and capabilities.
	MCPToolGetConfig     = "wisdevGetConfig"
	MCPToolTuneConfig    = "wisdevTuneConfig"
	MCPToolResetConfig   = "wisdevResetConfig"
	MCPToolListProviders = "wisdevListProviders"
	MCPToolCapabilities  = "wisdevCapabilities"

	MCPServerName = "wisdev-mcp"
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
	case "scholarlmGenerateManuscript", "wisdevDocGen", MCPToolGenerateManuscript:
		return MCPToolGenerateManuscript
	case "wisdevConfig", MCPToolGetConfig:
		return MCPToolGetConfig
	case "wisdevSetConfig", "wisdevTune", MCPToolTuneConfig:
		return MCPToolTuneConfig
	case MCPToolResetConfig:
		return MCPToolResetConfig
	case "wisdevProviders", MCPToolListProviders:
		return MCPToolListProviders
	case "wisdevDescribeCapabilities", MCPToolCapabilities:
		return MCPToolCapabilities
	default:
		return strings.TrimSpace(name)
	}
}

func isKnownMCPTool(name string) bool {
	switch normalizeMCPToolName(name) {
	case MCPToolSearchPapers, MCPToolPaperLookup, MCPToolEvidenceSearch, MCPToolAuthorSearch, MCPToolGenerateManuscript,
		MCPToolGetConfig, MCPToolTuneConfig, MCPToolResetConfig, MCPToolListProviders, MCPToolCapabilities:
		return true
	default:
		return false
	}
}
