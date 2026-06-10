package cli

import (
	"fmt"
	"io"
	"strings"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

func printYOLOResult(stdout, stderr io.Writer, result *agent.YOLOResult, quiet, verbose bool) error {
	if result == nil {
		return fmt.Errorf("empty YOLO result")
	}

	if quiet {
		if strings.TrimSpace(result.FinalAnswer) != "" {
			fmt.Fprintln(stdout, result.FinalAnswer)
		}
		return nil
	}

	if strings.TrimSpace(result.FinalAnswer) != "" {
		printSection(stderr, "Answer")
		fmt.Fprintln(stdout, result.FinalAnswer)
		fmt.Fprintln(stderr)
	}

	printSection(stderr, "Run summary")
	fmt.Fprintf(stderr, "  %s iterations: %d\n", statusGlyph("ok"), result.Iterations)
	fmt.Fprintf(stderr, "  %s papers found: %d\n", statusGlyph("ok"), result.PapersFound)
	if result.Converged {
		fmt.Fprintf(stderr, "  %s converged: yes\n", statusGlyph("ok"))
	} else if reason := strings.TrimSpace(result.StopReason); reason != "" {
		fmt.Fprintf(stderr, "  %s stop reason: %s\n", statusGlyph("warn"), reason)
	}

	if verbose && len(result.ExecutedQueries) > 0 {
		fmt.Fprintln(stderr)
		printSection(stderr, "Executed queries")
		limit := len(result.ExecutedQueries)
		if limit > 8 {
			limit = 8
		}
		for i := 0; i < limit; i++ {
			fmt.Fprintf(stderr, "  %d. %s\n", i+1, result.ExecutedQueries[i])
		}
		if len(result.ExecutedQueries) > limit {
			note(stderr, "  … +%d more queries", len(result.ExecutedQueries)-limit)
		}
	}

	if verbose && len(result.Papers) > 0 {
		fmt.Fprintln(stderr)
		printSection(stderr, "Top papers")
		limit := len(result.Papers)
		if limit > 5 {
			limit = 5
		}
		for i := 0; i < limit; i++ {
			paper := result.Papers[i]
			fmt.Fprintf(stderr, "  %d. %s\n", i+1, formatPaperBibliographyTerminal(paper))
		}
		if len(result.Papers) > limit {
			note(stderr, "  … +%d more papers", len(result.Papers)-limit)
		}
	}

	if strings.TrimSpace(result.FinalAnswer) == "" {
		fmt.Fprintf(stdout, "WisDev YOLO completed: iterations=%d papers=%d converged=%t stopReason=%s\n",
			result.Iterations,
			result.PapersFound,
			result.Converged,
			firstNonEmpty(result.StopReason, "not_reported"),
		)
	}

	fmt.Fprintln(stderr)
	printScholarLMBrandingProminent(stderr)
	return nil
}
