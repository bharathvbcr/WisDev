package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

const defaultDemoQuery = "What evidence supports retrieval-augmented generation for scientific literature?"

func runDemo(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	offline := fs.Bool("offline", false, "run offline smoke instead of live search")
	providers := fs.String("provider", defaultRunProviders, "comma-separated providers for live demo")
	skipDoctor := fs.Bool("skip-doctor", false, "skip the doctor preflight scene")
	query := fs.String("query", defaultDemoQuery, "research question for the YOLO scene")
	if err := fs.Parse(args); err != nil {
		return err
	}

	task := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if task == "" {
		task = strings.TrimSpace(*query)
	}
	if task == "" {
		return fmt.Errorf("demo query is empty")
	}

	printSection(stderr, "WisDev hackathon demo")
	note(stderr, "Record this terminal at 1080p for Scene 3 in docs/hackathon/DEMO_SCRIPT.md")
	fmt.Fprintln(stderr)

	if !*skipDoctor {
		printSection(stderr, "Scene 1 — Doctor")
		report := buildDoctorReport(10 * time.Second)
		fmt.Fprintf(stderr, "  %s  providers: %s\n", statusGlyph(report.Checks[0].Status), report.Checks[0].Detail)
		fmt.Fprintf(stderr, "  %s  MCP tools: %s\n", statusGlyph(report.Checks[1].Status), report.Checks[1].Detail)
		fmt.Fprintf(stderr, "  %s  orchestrator: %s\n", statusGlyph(report.Checks[2].Status), report.Checks[2].Detail)
		fmt.Fprintln(stderr)
	}

	printSection(stderr, "Scene 2 — Narration hook")
	note(stderr, "  \"WisDev plans, searches academic sources, and synthesizes traceable evidence.\"")
	fmt.Fprintln(stderr)

	printSection(stderr, "Scene 3 — Local YOLO")
	if *offline {
		note(stderr, "  mode: offline smoke (no network)")
	} else {
		note(stderr, "  providers: %s", strings.TrimSpace(*providers))
	}
	fmt.Fprintln(stderr)

	yoloArgs := []string{"--local", "--verbose", task}
	if *offline {
		yoloArgs = append([]string{"--offline"}, yoloArgs...)
	} else if providers := strings.TrimSpace(*providers); providers != "" {
		yoloArgs = append([]string{"--provider", providers}, yoloArgs...)
	}

	return runYOLO(yoloArgs, stdout, stderr)
}
