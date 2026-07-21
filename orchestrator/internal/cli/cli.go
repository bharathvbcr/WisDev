package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/citations"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/docgen"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/envload"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/telemetry"
	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

const defaultBaseURL = "http://127.0.0.1:8081"
const defaultRunProviders = "openalex,arxiv"

func Run(args []string, stdout, stderr io.Writer) error {
	envload.LoadDotEnvFiles()
	ensureDocGenWired()
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		if len(args) == 1 {
			printUsage(stdout)
			return nil
		}
		return runHelp(args[1:], stdout)
	}
	if args[0] == "--version" || args[0] == "-v" || args[0] == "version" {
		return runVersion(stdout)
	}

	args = normalizeInvocation(args)

	switch args[0] {
	case "run":
		return runShortcut(args[1:], stdout, stderr)
	case "search":
		return runShortcut(args[1:], stdout, stderr)
	case "yolo":
		return runYOLO(args[1:], stdout, stderr)
	case "max":
		return runMax(args[1:], stdout, stderr)
	case "docgen":
		return runDocGen(args[1:], stdout, stderr)
	case "serve":
		return runServe(stdout, stderr)
	case "stack":
		return runStack(args[1:], stdout, stderr)
	case "mcp":
		return runMCP(args[1:], stdout, stderr)
	case "mcp-config":
		return runMCPConfig(args[1:], stdout)
	case "doctor":
		return runDoctor(args[1:], stdout)
	case "providers":
		return runProviders(args[1:], stdout)
	case "tui":
		return runTUI(args[1:], stdout, stderr)
	case "update":
		return runUpdate(args[1:], stdout, stderr)
	case "guide":
		return runGuide(stdout)
	default:
		printUsage(stderr)
		if suggestion := suggestCommand(args[0]); suggestion != "" {
			userError(stderr, "Unknown command %q. Did you mean %q? Try: wisdev help %s", args[0], suggestion, suggestion)
		}
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runShortcut(args []string, stdout, stderr io.Writer) error {
	normalized := normalizeRunArgs(args)
	yoloArgs := append([]string{"--local", "--provider", defaultRunProviders}, normalized...)
	return runYOLO(yoloArgs, stdout, stderr)
}

// runMax runs a local research query with every quality knob maxed out:
// it forces unleashed budgets/iterations for this process and cranks the loop
// depth, search breadth, paper retention, long-form synthesis, and timeout.
// Enhancements (query rewrite, hypotheses, planning) are on by default and left
// enabled. Any flags the user supplies are appended last and therefore override
// these presets. Usage: wisdev max "research question".
func runMax(args []string, stdout, stderr io.Writer) error {
	// Guarantee maximum elaboration even if WISDEV_UNLEASHED is not set in the
	// environment: this raises the autonomous-loop iteration floor and lifts the
	// budget/token/timeout caps for this run.
	_ = os.Setenv("WISDEV_UNLEASHED", "1")
	normalized := normalizeRunArgs(args)
	maxArgs := append([]string{
		"--local",
		"--long-form",
		"--stages",
		"--max-iterations", "12",
		"--max-search-terms", "20",
		"--hits-per-search", "12",
		"--max-unique-papers", "80",
		"--timeout", "30m",
	}, normalized...)
	return runYOLO(maxArgs, stdout, stderr)
}

func normalizeRunArgs(args []string) []string {
	normalized := append([]string(nil), args...)
	for i, arg := range normalized {
		if strings.HasPrefix(arg, "-") {
			continue
		}

		lower := strings.ToLower(arg)
		if lower == "search:" || lower == "search" {
			return append(normalized[:i], normalized[i+1:]...)
		}
		if strings.HasPrefix(lower, "search:") {
			normalized[i] = strings.TrimSpace(arg[len("search:"):])
			if normalized[i] == "" {
				return append(normalized[:i], normalized[i+1:]...)
			}
			return normalized
		}
	}
	return normalized
}

// defaultLocalMaxIterations picks the default local YOLO iteration ceiling.
// In unleashed mode (WISDEV_UNLEASHED) it defaults high so a research run is
// elaborate and multi-pass; otherwise it returns the caller's standard default.
func defaultLocalMaxIterations(base int) int {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WISDEV_UNLEASHED"))) {
	case "1", "true", "yes", "on":
		if base > 12 {
			return base
		}
		return 12
	default:
		return base
	}
}

func runYOLO(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("yolo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	baseURL := fs.String("url", envOrDefault("WISDEV_ORCHESTRATOR_URL", defaultBaseURL), "orchestrator base URL")
	jsonOut := fs.Bool("json", false, "emit raw JSON response")
	quiet := fs.Bool("quiet", false, "print only the final answer")
	verbose := fs.Bool("verbose", false, "print queries and top papers on stderr")
	timeout := fs.Duration("timeout", 5*time.Minute, "request timeout")
	local := fs.Bool("local", false, "run locally (default; kept for compatibility)")
	remote := fs.Bool("remote", false, "use remote orchestrator HTTP API")
	offline := fs.Bool("offline", false, "disable network-backed search providers in local mode")
	fs.BoolVar(jsonOut, "j", false, "emit raw JSON response")
	fs.BoolVar(quiet, "q", false, "print only the final answer")
	fs.BoolVar(verbose, "v", false, "print queries and top papers on stderr")
	providers := fs.String("provider", "", "comma-separated built-in provider names for local mode")
	domain := fs.String("domain", "", "research domain hint for local mode")
	projectID := fs.String("project-id", "", "project or session id for local mode")
	maxIterations := fs.Int("max-iterations", defaultLocalMaxIterations(3), "maximum local YOLO loop iterations")
	maxSearchTerms := fs.Int("max-search-terms", 6, "maximum local search terms")
	hitsPerSearch := fs.Int("hits-per-search", 5, "local hits per search")
	maxUniquePapers := fs.Int("max-unique-papers", 20, "maximum unique papers retained locally")
	disablePlanning := fs.Bool("disable-planning", false, "disable programmatic planning in local mode")
	disableHypotheses := fs.Bool("disable-hypotheses", false, "disable hypothesis generation in local mode")
	noEnhance := fs.Bool("no-enhance", false, "disable query grammar, typo, and acronym enhancement")
	longForm := fs.Bool("long-form", false, "synthesize extended Introduction and Background sections")
	stages := fs.Bool("stages", false, "stream research loop stage events to stderr during local runs")
	docGen := fs.Bool("docgen", false, "after research, generate a grounded manuscript from the retrieved papers (same engine as `wisdev docgen`)")
	docFormat := fs.String("doc-format", "markdown", "manuscript format when --docgen is set: markdown|latex|html|docx|json")
	docIntent := fs.String("doc-intent", "", "document type when --docgen is set: report|litreview|fullpaper (default: fullpaper)")
	docCitationStyle := fs.String("doc-citation-style", "", "bibliography citation style when --docgen is set: apa|mla|chicago|vancouver|ieee|harvard|nature (default: apa)")
	docOutput := fs.String("doc-output", "", "write the generated manuscript to this file instead of stdout (implies --docgen)")
	docWords := fs.Int("doc-words", 0, "target total word count for the --docgen manuscript (0 = model default)")
	docMinCitations := fs.Int("doc-min-citations", 0, "minimum distinct sources the --docgen manuscript should cite (0 = no minimum)")
	docFlow := fs.String("doc-flow", "", "comma-separated section flow for --docgen, e.g. introduction,methods,results,discussion (empty = default)")
	docReviewRounds := fs.Int("doc-review-rounds", 0, "max rounds of the agentic generate→review→revise loop for --docgen (0 = default 2, max 5)")
	docGenre := fs.String("doc-genre", "", "manuscript genre for --docgen, e.g. \"research paper\" (default: narrative literature review)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	task := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if task == "" {
		return errors.New("missing research question")
	}
	if *local && *remote {
		return errors.New("use either --local or --remote, not both")
	}

	// --doc-output implies --docgen; resolve the manuscript format up front so an
	// invalid value fails fast before the research loop runs.
	withDocGen := *docGen || strings.TrimSpace(*docOutput) != ""
	resolvedDocFormat := "markdown"
	resolvedDocIntent := ""
	resolvedDocStyle := ""
	if withDocGen {
		var fmtErr error
		resolvedDocFormat, fmtErr = resolveDocGenFormat(*docFormat, *docOutput, false)
		if fmtErr != nil {
			return fmtErr
		}
		parsedDocIntent, intentErr := docgen.ParseIntent(*docIntent)
		if intentErr != nil {
			return intentErr
		}
		resolvedDocIntent = string(parsedDocIntent)
		parsedDocStyle, styleErr := citations.ParseStyle(*docCitationStyle)
		if styleErr != nil {
			return styleErr
		}
		resolvedDocStyle = string(parsedDocStyle)
	}

	// Smart default: apply a sane citation floor when --docgen is on and the
	// caller passed no --doc-min-citations. The explicit flag always wins.
	autoDocMin := false
	if withDocGen && *docMinCitations == 0 {
		*docMinCitations = smartDocGenMinCitations
		autoDocMin = true
	}

	// Smart default: without --doc-output the manuscript would be appended to the
	// research report on stdout, where it is easy to lose. Auto-save it to a
	// slugged filename instead (suppressed for --json, whose payload embeds the
	// manuscript). --doc-output always wins.
	docOutputPath := strings.TrimSpace(*docOutput)
	autoDocOutput := false
	if withDocGen && docOutputPath == "" && !*jsonOut {
		docOutputPath = docGenDefaultFilename(task, resolvedDocFormat, time.Now())
		autoDocOutput = true
	}

	// Retrieve at least as many papers as the requested docGen citation floor so the
	// manuscript can cite that many distinct sources.
	maxUnique := *maxUniquePapers
	if withDocGen && *docMinCitations > maxUnique {
		maxUnique = *docMinCitations
	}

	runLocal := !*remote
	if runLocal {
		return runLocalYOLO(stdout, stderr, localYOLOOptions{
			task:                task,
			jsonOut:             *jsonOut,
			quiet:               *quiet,
			verbose:             *verbose,
			showStages:          *stages || *verbose,
			offline:             *offline,
			providers:           splitCSV(*providers),
			timeout:             *timeout,
			domain:              *domain,
			projectID:           *projectID,
			maxIterations:       *maxIterations,
			maxSearchTerms:      *maxSearchTerms,
			hitsPerSearch:       *hitsPerSearch,
			maxUniquePapers:     maxUnique,
			disablePlanning:     *disablePlanning,
			disableHypotheses:   *disableHypotheses,
			disableQueryEnhance: *noEnhance,
			longFormReport:      *longForm,
			withDocGen:          withDocGen,
			docFormat:           resolvedDocFormat,
			docIntent:           resolvedDocIntent,
			docCitationStyle:    resolvedDocStyle,
			docOutputPath:       docOutputPath,
			docTargetWords:      *docWords,
			docMinCitations:     *docMinCitations,
			docSectionFlow:      splitCSV(*docFlow),
			docReviewRounds:     *docReviewRounds,
			docGenre:            strings.TrimSpace(*docGenre),
			autoDocMinCitations: autoDocMin,
			autoDocOutput:       autoDocOutput,
		})
	}

	if withDocGen {
		return errors.New("--docgen is only supported in local mode; drop --remote or use `wisdev docgen`")
	}

	if !*jsonOut && !*quiet {
		note(stderr, "Submitting YOLO task to %s", strings.TrimRight(*baseURL, "/"))
	}

	payload := map[string]any{
		"task":          task,
		"query":         task,
		"mode":          "yolo",
		"executionMode": "yolo",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoints := []string{
		"/api/wisdev/yolo",
		"/wisdev/yolo",
		"/api/wisdev/execute",
	}

	var lastErr error
	progressErr := runWithProgress(stderr, "Waiting for orchestrator response", func() error {
		for _, endpoint := range endpoints {
			responseBody, status, postErr := postJSON(*baseURL, endpoint, body, *timeout)
			if postErr != nil {
				lastErr = postErr
				continue
			}
			if status >= 200 && status < 300 {
				if *jsonOut {
					fmt.Fprintln(stdout, string(responseBody))
					return nil
				}
				if *quiet {
					return printResponse(stdout, responseBody)
				}
				return printResponse(stdout, responseBody)
			}
			lastErr = fmt.Errorf("%s returned HTTP %d: %s", endpoint, status, strings.TrimSpace(string(responseBody)))
		}
		return fmt.Errorf("YOLO request failed against %s: %w", strings.TrimRight(*baseURL, "/"), lastErr)
	})
	return progressErr
}

type localYOLOOptions struct {
	task                string
	jsonOut             bool
	quiet               bool
	verbose             bool
	showStages          bool
	offline             bool
	providers           []string
	timeout             time.Duration
	domain              string
	projectID           string
	maxIterations       int
	maxSearchTerms      int
	hitsPerSearch       int
	maxUniquePapers     int
	disablePlanning     bool
	disableHypotheses   bool
	disableQueryEnhance bool
	longFormReport      bool
	withDocGen          bool
	docFormat           string
	docIntent           string
	docCitationStyle    string
	docOutputPath       string
	docTargetWords      int
	docMinCitations     int
	docSectionFlow      []string
	docReviewRounds     int
	docGenre            string
	autoDocMinCitations bool // docMinCitations came from the smart default, not a flag
	autoDocOutput       bool // docOutputPath was derived (no --doc-output given)
}

func runLocalYOLO(stdout, stderr io.Writer, opts localYOLOOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if !opts.jsonOut && !opts.quiet {
		printSection(stderr, "WisDev YOLO")
		note(stderr, "  task: %s", opts.task)
		if opts.offline {
			note(stderr, "  mode: offline smoke")
		} else if len(opts.providers) > 0 {
			note(stderr, "  providers: %s", strings.Join(opts.providers, ", "))
		} else {
			note(stderr, "  providers: all built-in")
		}
		if opts.withDocGen {
			note(stderr, "  docgen: on (manuscript format: %s)", opts.docFormat)
			if opts.autoDocOutput {
				note(stderr, "  docgen output: %s (auto — override with --doc-output)", opts.docOutputPath)
			}
			if opts.autoDocMinCitations {
				note(stderr, "  docgen citations: auto floor of %d distinct sources (override with --doc-min-citations)", opts.docMinCitations)
			}
		}
		if opts.showStages {
			printSection(stderr, "Stages")
		}
		fmt.Fprintln(stderr)
	}

	agentOpts := []agent.Option{}
	llmClient := resolveResearchLLMClient()
	agentOpts = append(agentOpts, agent.WithLLMClient(llmClient))
	if opts.offline {
		agentOpts = append(agentOpts, agent.WithNoSearchProviders())
	} else if len(opts.providers) > 0 {
		agentOpts = append(agentOpts, agent.WithProviderNames(opts.providers...))
	}

	var result *agent.YOLOResult
	err := withQuietAgentLogs(func() error {
		progressLabel := "Running local YOLO loop"
		if opts.showStages {
			progressLabel = "Running local YOLO loop (stage log below)"
		}
		return runWithProgress(stderr, progressLabel, func() error {
			var runErr error
			runErr = withGlobalResearchLLMClient(llmClient, func() error {
				var innerErr error
				yoloReq := agent.YOLORequest{
					Task:                opts.task,
					OriginalQuery:       opts.task,
					Domain:              opts.domain,
					ProjectID:           opts.projectID,
					MaxIterations:       opts.maxIterations,
					MaxSearchTerms:      opts.maxSearchTerms,
					HitsPerSearch:       opts.hitsPerSearch,
					MaxUniquePapers:     opts.maxUniquePapers,
					DisablePlanning:     opts.disablePlanning,
					DisableHypotheses:   opts.disableHypotheses,
					DisableQueryEnhance: opts.disableQueryEnhance,
					LongFormReport:      opts.longFormReport,
				}
				if opts.showStages {
					yoloReq.OnProgress = func(event agent.ProgressEvent) {
						printCLIProgressEvent(stderr, event)
					}
				}
				result, innerErr = agent.NewAgent(agentOpts...).RunYOLO(ctx, yoloReq)
				return innerErr
			})
			return runErr
		})
	})
	if err != nil {
		return err
	}

	// Optionally turn the research result into a grounded manuscript using the same
	// pipeline as `wisdev docgen`. This reuses the papers already retrieved above, so
	// no second search pass runs. The document is appended after the research output
	// (or written to --doc-output when requested).
	var (
		manuscript     string
		manuscriptDone bool
	)
	if opts.withDocGen {
		ctx2, cancel2 := context.WithTimeout(context.Background(), opts.timeout)
		defer cancel2()
		rendered, _, docErr := generateManuscriptFromResearch(ctx2, stderr, opts.task, result, opts.docIntent, opts.docFormat, opts.docCitationStyle, "", opts.offline, manuscriptControls{
			targetWords:  opts.docTargetWords,
			minCitations: opts.docMinCitations,
			sectionFlow:  opts.docSectionFlow,
			reviewRounds: opts.docReviewRounds,
			genre:        opts.docGenre,
		})
		if docErr != nil {
			return fmt.Errorf("manuscript generation failed: %w", docErr)
		}
		manuscript = rendered
		manuscriptDone = true
	}

	if opts.jsonOut {
		payload := any(result)
		if manuscriptDone {
			payload = map[string]any{
				"research":   result,
				"manuscript": json.RawMessage(manuscriptRawJSON(manuscript, opts.docFormat)),
			}
		}
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		return nil
	}

	if err := printYOLOResult(stdout, stderr, result, opts.quiet, opts.verbose); err != nil {
		return err
	}

	if manuscriptDone {
		if opts.docOutputPath != "" {
			if werr := os.WriteFile(opts.docOutputPath, []byte(manuscript), 0o644); werr != nil {
				return fmt.Errorf("failed to write manuscript to %s: %w", opts.docOutputPath, werr)
			}
			note(stderr, "  manuscript (%s) saved → %s", opts.docFormat, absPathOrSelf(opts.docOutputPath))
		} else {
			if !opts.quiet {
				printSection(stderr, "DocuGen manuscript")
			}
			fmt.Fprintln(stdout)
			fmt.Fprintln(stdout, manuscript)
		}
	}
	return nil
}

// manuscriptRawJSON returns a JSON value for embedding the rendered manuscript in
// a combined --json payload: for the "json" format the manuscript already IS JSON
// (embed it verbatim), otherwise it is a markdown/latex string and gets quoted.
func manuscriptRawJSON(rendered, format string) []byte {
	if format == "json" {
		return []byte(rendered)
	}
	encoded, err := json.Marshal(rendered)
	if err != nil {
		return []byte(`""`)
	}
	return encoded
}

func postJSON(baseURL, endpoint string, body []byte, timeout time.Duration) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	url := strings.TrimRight(baseURL, "/") + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return responseBody, resp.StatusCode, nil
}

func printResponse(stdout io.Writer, body []byte) error {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		fmt.Fprintln(stdout, string(body))
		return nil
	}

	for _, key := range []string{"summary", "answer", "result", "message"} {
		if value, ok := decoded[key]; ok {
			fmt.Fprintln(stdout, value)
			return nil
		}
	}

	pretty, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(pretty))
	return nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func runMCP(args []string, stdout, stderr io.Writer) error {
	return runMCPWithIO(args, os.Stdin, stdout, stderr)
}

func runMCPWithIO(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	offline := fs.Bool("offline", false, "disable network-backed search providers")
	providers := fs.String("provider", "", "comma-separated built-in provider names")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	var registry *search.ProviderRegistry
	switch {
	case *offline:
		registry = search.NewProviderRegistry()
	case strings.TrimSpace(*providers) != "":
		registry = search.BuildRegistry(splitCSV(*providers)...)
	default:
		registry = search.BuildRegistry()
	}

	if stderr != nil {
		note(stderr, "WisDev MCP stdio ready (Ctrl+C to stop). Action tools: wisdevSearchPapers, wisdevPaperLookup, wisdevEvidenceSearch, wisdevAuthorSearch, wisdevGenerateManuscript. Tuning tools: wisdevGetConfig, wisdevTuneConfig, wisdevResetConfig, wisdevListProviders, wisdevCapabilities")
	}

	// MCP stdio reserves stdout for JSON-RPC frames; the telemetry package's
	// init() points both its package logger and the slog default at stdout,
	// so reroute both here.
	logSink := stderr
	if logSink == nil {
		logSink = io.Discard
	}
	previousLogger := telemetry.Logger()
	telemetry.SetLogger(slog.New(slog.NewJSONHandler(logSink, nil)))
	defer telemetry.SetLogger(previousLogger)

	srv := internalwisdev.NewMCPServer(registry)
	return srv.RunStdio(context.Background(), stdin, stdout)
}

func splitCSV(value string) []string {
	fields := strings.Split(value, ",")
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
