package cli

import (
	"fmt"
	"io"
	"strings"
)

var commandHelp = map[string]string{
	"search": `Usage: wisdev "question"
       wisdev search [-q] [-v] [-j] [--offline] [--provider openalex,arxiv] "question"

Runs local research with default providers openalex,arxiv.
Aliases: ask`,
	"ask": `Alias for wisdev search.`,
	"run": `Legacy alias for wisdev search (optional search: prefix).`,
	"yolo": `Usage: wisdev yolo [-q] [-v] [-j] [--offline] [--no-enhance] "question"
       wisdev yolo --remote "question"

Local by default. AI structured query preparation (grammar, domain, seeds) is on unless --no-enhance.
Use --remote for the HTTP orchestrator.`,
	"mcp": `Usage: wisdev mcp [--provider openalex,arxiv]

MCP stdio server for Cursor, Claude Code, and Codex.`,
	"mcp-config": `Usage: wisdev setup [--write .cursor/mcp.json] [--binary]

Writes MCP client config. Alias: setup`,
	"setup": `Alias for wisdev mcp-config.`,
	"serve": `Usage: wisdev serve

Starts the HTTP orchestrator (go run ./cmd/server).`,
	"doctor": `Usage: wisdev check [--json]

Health check for providers, MCP tools, and orchestrator.
Alias: check`,
	"check": `Alias for wisdev doctor.`,
	"providers": `Usage: wisdev sources [--json]

Lists built-in search providers. Alias: sources`,
	"sources": `Alias for wisdev providers.`,
	"version": `Usage: wisdev version`,
	"demo": `Usage: wisdev demo [--offline] ["question"]

Hackathon demo: check + narrated local research.`,
	"tui": `Usage: wisdev tui [--offline] [--demo] [--autostart] [--query "question"] [--output path.md]

Interactive terminal UI for research questions, provider selection, and live run logs.
--demo pre-fills the hackathon query, enables offline mode, and autostarts. Alias: ui

Environment Variables:
  WISDEV_THEME  Set color theme (scholarlm, default, high-contrast, monochrome)

ScholarLM (https://scholarlm-vbcr.web.app) — full research UI, cloud sync, and team workflows.`,
	"ui": `Alias for wisdev tui.`,
}

func runHelp(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}
	command := strings.ToLower(strings.TrimSpace(args[0]))
	text, ok := commandHelp[command]
	if !ok {
		if suggestion := suggestCommand(command); suggestion != "" {
			return fmt.Errorf("unknown help topic %q. Did you mean %q? Try: wisdev help %s", args[0], suggestion, suggestion)
		}
		return fmt.Errorf("unknown help topic %q", args[0])
	}
	fmt.Fprintln(stdout, text)
	return nil
}
