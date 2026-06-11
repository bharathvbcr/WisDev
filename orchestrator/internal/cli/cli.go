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

	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/envload"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/telemetry"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

const defaultBaseURL = "http://127.0.0.1:8081"
const defaultRunProviders = "openalex,arxiv"

func Run(args []string, stdout, stderr io.Writer) error {
	envload.LoadDotEnvFiles()
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
	case "serve":
		return runServe(stdout, stderr)
	case "mcp":
		return runMCP(args[1:], stdout, stderr)
	case "mcp-config":
		return runMCPConfig(args[1:], stdout)
	case "doctor":
		return runDoctor(args[1:], stdout)
	case "providers":
		return runProviders(args[1:], stdout)
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "tui":
		return runTUI(args[1:], stdout, stderr)
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
	maxIterations := fs.Int("max-iterations", 3, "maximum local YOLO loop iterations")
	maxSearchTerms := fs.Int("max-search-terms", 6, "maximum local search terms")
	hitsPerSearch := fs.Int("hits-per-search", 5, "local hits per search")
	maxUniquePapers := fs.Int("max-unique-papers", 20, "maximum unique papers retained locally")
	disablePlanning := fs.Bool("disable-planning", false, "disable programmatic planning in local mode")
	disableHypotheses := fs.Bool("disable-hypotheses", false, "disable hypothesis generation in local mode")
	noEnhance := fs.Bool("no-enhance", false, "disable query grammar, typo, and acronym enhancement")
	longForm := fs.Bool("long-form", false, "synthesize extended Introduction and Background sections")
	stages := fs.Bool("stages", false, "stream research loop stage events to stderr during local runs")
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
	runLocal := !*remote
	if runLocal {
		return runLocalYOLO(stdout, stderr, localYOLOOptions{
			task:              task,
			jsonOut:           *jsonOut,
			quiet:             *quiet,
			verbose:           *verbose,
			showStages:        *stages || *verbose,
			offline:           *offline,
			providers:         splitCSV(*providers),
			timeout:           *timeout,
			domain:            *domain,
			projectID:         *projectID,
			maxIterations:     *maxIterations,
			maxSearchTerms:    *maxSearchTerms,
			hitsPerSearch:     *hitsPerSearch,
			maxUniquePapers:   *maxUniquePapers,
			disablePlanning:     *disablePlanning,
			disableHypotheses:   *disableHypotheses,
			disableQueryEnhance: *noEnhance,
			longFormReport:      *longForm,
		})
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
	task              string
	jsonOut           bool
	quiet             bool
	verbose           bool
	showStages        bool
	offline           bool
	providers         []string
	timeout           time.Duration
	domain            string
	projectID         string
	maxIterations     int
	maxSearchTerms    int
	hitsPerSearch     int
	maxUniquePapers   int
	disablePlanning     bool
	disableHypotheses   bool
	disableQueryEnhance bool
	longFormReport      bool
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

	if opts.jsonOut {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		return nil
	}

	return printYOLOResult(stdout, stderr, result, opts.quiet, opts.verbose)
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
		note(stderr, "WisDev MCP stdio ready (Ctrl+C to stop). Tools: wisdevSearchPapers, wisdevPaperLookup, wisdevEvidenceSearch, wisdevAuthorSearch")
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
