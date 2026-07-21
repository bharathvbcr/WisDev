package cli

// guide.go implements `wisdev guide` (alias: commands): a one-screen guide to
// every CLI command, grouped by what the user is trying to do. `wisdev help
// <command>` stays the place for per-command detail; this is the map.

import (
	"fmt"
	"io"
)

func runGuide(stdout io.Writer) error {
	fmt.Fprintf(stdout, "%s %s\n\n",
		paint(stdout, ansiBold+scholarlmRedBold, "WisDev"),
		paint(stdout, ansiDim, "commands guide"),
	)

	printSection(stdout, "Research")
	note(stdout, `  wisdev "question"                Quick local research with openalex + arxiv (aliases: search, ask)`)
	note(stdout, `  wisdev yolo [flags] "question"   The research loop with full flag control`)
	note(stdout, `  wisdev max "question"            Everything dialed up: 12 iterations, all sources, long-form report`)
	note(stdout, `  wisdev docgen [-o paper.md] "q" Search + DocuGen: a grounded manuscript draft (alias: docugen)`)
	fmt.Fprintln(stdout)

	printSection(stdout, "Interactive")
	note(stdout, `  wisdev tui                       Full-screen terminal UI: providers, settings, live logs (alias: ui)`)
	fmt.Fprintln(stdout)

	printSection(stdout, "Integrations")
	note(stdout, `  wisdev mcp                       MCP stdio server for Cursor, Claude Code, and Codex`)
	note(stdout, `  wisdev setup --write PATH        Write MCP client config (alias of mcp-config)`)
	note(stdout, `  wisdev serve                     Start the HTTP orchestrator`)
	fmt.Fprintln(stdout)

	printSection(stdout, "Info & Maintenance")
	note(stdout, `  wisdev check                     Health check for providers and orchestrator (alias of doctor)`)
	note(stdout, `  wisdev sources                   List built-in search providers (alias of providers)`)
	note(stdout, `  wisdev version                   Show CLI version and binary path`)
	note(stdout, `  wisdev update [--check]          Self-update to the latest release (alias: upgrade)`)
	note(stdout, `  wisdev help <command>            Detailed help for one command`)
	note(stdout, `  wisdev guide                     This guide (alias: commands)`)
	fmt.Fprintln(stdout)

	printSection(stdout, "Common flags (search / yolo / max)")
	note(stdout, `  -q quiet  ·  -v verbose  ·  -j json  ·  --offline  ·  --stages`)
	note(stdout, `  --no-enhance  ·  --long-form  ·  --provider openalex,arxiv  ·  --max-iterations N`)
	fmt.Fprintln(stdout)

	printSection(stdout, "Environment")
	note(stdout, `  WISDEV_UNLEASHED=1    Lift budget/iteration caps (wisdev max sets this automatically)`)
	note(stdout, `  WISDEV_THEME          TUI theme: scholarlm, high-contrast, monochrome`)
	note(stdout, `  NO_COLOR / WISDEV_PLAIN=1    Plain output`)
	fmt.Fprintln(stdout)

	note(stdout, "Full flag-by-flag reference: docs/COMMANDS.md — https://github.com/bharathvbcr/WisDev/blob/main/docs/COMMANDS.md")
	fmt.Fprintln(stdout)

	printScholarLMBrandingProminent(stdout)
	return nil
}
