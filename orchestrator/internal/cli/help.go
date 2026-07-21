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
	"yolo": `Usage: wisdev yolo [-q] [-v] [-j] [--offline] [--no-enhance] [--long-form] [--docgen] "question"
       wisdev yolo --remote "question"

Local by default. AI structured query preparation (grammar, domain, seeds) is on unless --no-enhance.
--long-form synthesizes extended Introduction and Background sections.
--docgen also generates a grounded manuscript from the retrieved papers (same
engine as ` + "`wisdev docgen`" + `). Zero-config: the manuscript auto-saves to
manuscript-<topic>-<timestamp>.md (override with --doc-output FILE) with an
auto citation floor of 10 distinct sources (override with --doc-min-citations N).
Optional: --doc-format markdown|latex|json, --doc-words N, --doc-flow LIST,
--doc-review-rounds N (agentic generate→review→revise loop), and --doc-genre STR.
Local mode only.
Use --remote for the HTTP orchestrator.`,
	"mcp": `Usage: wisdev mcp [--provider openalex,arxiv]

MCP stdio server for Cursor, Claude Code, and Codex.`,
	"mcp-config": `Usage: wisdev setup [--write .cursor/mcp.json] [--binary] [--replace]
       wisdev setup --write ~/.cursor/mcp.json --binary

Writes (or merges) an MCP client config for Cursor / Claude Code / Claude Desktop.
  --binary   use the absolute path to this wisdev binary (recommended)
  --replace  overwrite the whole mcpServers map (default merges the "wisdev" entry)
Alias: setup`,
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
	"max": `Usage: wisdev max [flags] "question"

Local research with every quality knob dialed up: unleashed budgets
(WISDEV_UNLEASHED=1 for this run), 12 loop iterations, all built-in search
providers, 20 search terms, up to 80 unique papers, long-form synthesis,
live stage log, and a 30 minute timeout. AI query enhancement, hypotheses,
and planning stay on.

Any yolo flag still works and overrides the preset, e.g.:
  wisdev max --max-iterations 6 "question"
  wisdev max -q "question"`,
	"docgen": `Usage: wisdev docgen [flags] "your topic"
       wisdev docgen --offline "your topic"
       wisdev docgen -o paper.md "your topic"

Runs local research to gather grounded papers, then drives the manuscript
pipeline (the same engine behind the /full-paper route) to produce a grounded
manuscript draft: ordered sections, visuals, a peer-review critique, and a
reference list. Section prose is enriched by the Python sidecar when one is
reachable and falls back to grounded scaffolds otherwise (so --offline works).
Zero-config by default: with no flags it targets an auto citation floor of 10
distinct sources and streams live stage progress (drafting, review rounds,
fact-check) to stderr while it runs.

  -o, --output PATH   Write the manuscript to a file (format inferred from extension)
  -f, --format FMT    Output format: markdown | latex | json (default: from -o, else markdown)
  -j, --json          Emit the raw manuscript pipeline result as JSON
  -q, --quiet         Print only the manuscript markdown
  -v, --verbose       Show search queries and pipeline stages on stderr
  --offline           Skip network providers (grounded scaffold only)
  --provider LIST     Comma-separated providers for retrieval (e.g. pubmed,arxiv)
  --domain NAME       Research domain hint (e.g. medicine, cs)
  --python-url URL    Python sidecar base URL for section enrichment
  --max-unique-papers N   Cap on papers fed into the manuscript (default 24)
  --words N           Target total word count (split across sections; 0 = model default)
  --min-citations N   Minimum distinct sources to cite (raises the retrieval floor)
  --flow LIST         Comma-separated section flow, e.g. introduction,methods,results,discussion
  --review-rounds N   Max rounds of the agentic generate→review→revise loop (0 = default 2, max 5)
  --genre STR         Manuscript genre, e.g. "research paper" (default: narrative literature review)

Section drafting runs an agentic generate → review → revise loop: each round
re-reviews the draft and rewrites flagged sections, stopping early on convergence.

Alias: docugen`,
	"docugen": `Alias for wisdev docgen.`,
	"guide": `Usage: wisdev guide

One-screen guide to every wisdev command, grouped by task.
Alias: commands`,
	"commands": `Alias for wisdev guide.`,
	"version":  `Usage: wisdev version`,
	"update": `Usage: wisdev update [--check] [--version vX.Y.Z] [--force]

Self-update the wisdev binary from the latest GitHub release
(https://github.com/bharathvbcr/WisDev/releases). Downloads the asset for
this OS/architecture, verifies it against the release SHA256SUMS, and swaps
it in place of the running binary.

  --check          Only report whether an update is available
  --version TAG    Install a specific release tag instead of the latest
  --force          Replace a dev/source build or downgrade to an older tag

Environment Variables:
  WISDEV_REPO  GitHub repo to fetch releases from (default: bharathvbcr/WisDev)

Alias: upgrade`,
	"upgrade": `Alias for wisdev update.`,
	"tui": `Usage: wisdev tui [--offline] [--autostart] [--query "question"] [--output path.md]

Interactive terminal UI for research questions, provider selection, and live run logs. Alias: ui

Environment Variables:
  WISDEV_THEME  Set color theme (scholarlm, default, high-contrast, monochrome)

ScholarLM (https://scholarlm-vbc.web.app) — full research UI, cloud sync, and team workflows.`,
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
