package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

const defaultBaseURL = "http://127.0.0.1:8081"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "wisdev:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "yolo":
		return runYOLO(args[1:], stdout)
	case "serve":
		fmt.Fprintln(stdout, "Run the orchestrator server with: go run ./cmd/server")
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `WisDev Agent OS

Usage:
  wisdev yolo [--url http://127.0.0.1:8081] [--json] "task"
  wisdev yolo --local [--offline] [--provider openalex,arxiv] [--json] "task"
  wisdev serve

Environment:
  WISDEV_ORCHESTRATOR_URL  local orchestrator base URL

By default the CLI preserves the extracted HTTP compatibility surface. Use
--local to run through the embedded pkg/wisdev facade without starting the
orchestrator server.`)
}

func runYOLO(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("yolo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	baseURL := fs.String("url", envOrDefault("WISDEV_ORCHESTRATOR_URL", defaultBaseURL), "orchestrator base URL")
	jsonOut := fs.Bool("json", false, "emit raw JSON response")
	timeout := fs.Duration("timeout", 5*time.Minute, "request timeout")
	local := fs.Bool("local", false, "run through the embedded WisDev agent instead of HTTP")
	offline := fs.Bool("offline", false, "disable network-backed search providers in local mode")
	providers := fs.String("provider", "", "comma-separated built-in provider names for local mode")
	domain := fs.String("domain", "", "research domain hint for local mode")
	projectID := fs.String("project-id", "", "project or session id for local mode")
	maxIterations := fs.Int("max-iterations", 3, "maximum local YOLO loop iterations")
	maxSearchTerms := fs.Int("max-search-terms", 6, "maximum local search terms")
	hitsPerSearch := fs.Int("hits-per-search", 5, "local hits per search")
	maxUniquePapers := fs.Int("max-unique-papers", 20, "maximum unique papers retained locally")
	disablePlanning := fs.Bool("disable-planning", false, "disable programmatic planning in local mode")
	disableHypotheses := fs.Bool("disable-hypotheses", false, "disable hypothesis generation in local mode")
	if err := fs.Parse(args); err != nil {
		return err
	}

	task := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if task == "" {
		return errors.New("missing YOLO task")
	}
	if *local {
		return runLocalYOLO(stdout, localYOLOOptions{
			task:              task,
			jsonOut:           *jsonOut,
			offline:           *offline,
			providers:         splitCSV(*providers),
			timeout:           *timeout,
			domain:            *domain,
			projectID:         *projectID,
			maxIterations:     *maxIterations,
			maxSearchTerms:    *maxSearchTerms,
			hitsPerSearch:     *hitsPerSearch,
			maxUniquePapers:   *maxUniquePapers,
			disablePlanning:   *disablePlanning,
			disableHypotheses: *disableHypotheses,
		})
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
	for _, endpoint := range endpoints {
		responseBody, status, err := postJSON(*baseURL, endpoint, body, *timeout)
		if err != nil {
			lastErr = err
			continue
		}
		if status >= 200 && status < 300 {
			if *jsonOut {
				fmt.Fprintln(stdout, string(responseBody))
				return nil
			}
			return printResponse(stdout, responseBody)
		}
		lastErr = fmt.Errorf("%s returned HTTP %d: %s", endpoint, status, strings.TrimSpace(string(responseBody)))
	}

	return fmt.Errorf("YOLO request failed against %s: %w", strings.TrimRight(*baseURL, "/"), lastErr)
}

type localYOLOOptions struct {
	task              string
	jsonOut           bool
	offline           bool
	providers         []string
	timeout           time.Duration
	domain            string
	projectID         string
	maxIterations     int
	maxSearchTerms    int
	hitsPerSearch     int
	maxUniquePapers   int
	disablePlanning   bool
	disableHypotheses bool
}

func runLocalYOLO(stdout io.Writer, opts localYOLOOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	agentOpts := []agent.Option{}
	if opts.offline {
		agentOpts = append(agentOpts, agent.WithNoSearchProviders())
	} else if len(opts.providers) > 0 {
		agentOpts = append(agentOpts, agent.WithProviderNames(opts.providers...))
	}

	result, err := agent.NewAgent(agentOpts...).RunYOLO(ctx, agent.YOLORequest{
		Task:              opts.task,
		Domain:            opts.domain,
		ProjectID:         opts.projectID,
		MaxIterations:     opts.maxIterations,
		MaxSearchTerms:    opts.maxSearchTerms,
		HitsPerSearch:     opts.hitsPerSearch,
		MaxUniquePapers:   opts.maxUniquePapers,
		DisablePlanning:   opts.disablePlanning,
		DisableHypotheses: opts.disableHypotheses,
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

	if strings.TrimSpace(result.FinalAnswer) != "" {
		fmt.Fprintln(stdout, result.FinalAnswer)
		return nil
	}
	fmt.Fprintf(stdout, "WisDev YOLO completed: iterations=%d papers=%d converged=%t stopReason=%s\n",
		result.Iterations,
		result.PapersFound,
		result.Converged,
		firstNonEmpty(result.StopReason, "not_reported"),
	)
	return nil
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
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
