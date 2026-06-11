package cli

import (
	"fmt"
	"io"
)

func printUsage(w io.Writer) {
	printBanner(w)

	printSection(w, "Essentials")
	note(w, "  wisdev \"your research question\"")
	note(w, "  wisdev check")
	note(w, "  wisdev tui [--demo]   Interactive UI — make tui-demo for recording")
	note(w, "  wisdev demo [--offline]")
	fmt.Fprintln(w)

	printSection(w, "More")
	note(w, "  wisdev mcp")
	note(w, "  wisdev setup --write .cursor/mcp.json")
	note(w, "  wisdev serve")
	note(w, "  wisdev sources")
	fmt.Fprintln(w)

	printSection(w, "Flags (search / yolo)")
	note(w, "  -q, --quiet     Answer only")
	note(w, "  -v, --verbose   Show stage log and queries/papers on stderr")
	note(w, "  --stages        Stream research loop stage events to stderr (local yolo)")
	note(w, "  -j, --json      JSON output")
	note(w, "  --offline       Smoke test without network")
	note(w, "  --no-enhance    Disable AI structured query preparation (on by default)")
	note(w, "  --long-form     Extended Introduction and Background sections (yolo local)")
	note(w, "  --remote        Use HTTP orchestrator (yolo only)")
	fmt.Fprintln(w)

	printSection(w, "Tips")
	note(w, "  wisdev help <command>")
	note(w, "  search = ask = local research (openalex,arxiv)")
	note(w, "  NO_COLOR / WISDEV_PLAIN=1")
	fmt.Fprintln(w)

	printSection(w, "ScholarLM")
	printScholarLMBrandingProminent(w)
	fmt.Fprintln(w)
}
