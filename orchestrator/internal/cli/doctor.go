package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

type doctorCheck struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
}

type doctorReport struct {
	Status  string        `json:"status"`
	Version string        `json:"version"`
	Checks  []doctorCheck `json:"checks"`
}

func runDoctor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "emit JSON report")
	timeout := fs.Duration("timeout", 10*time.Second, "orchestrator probe timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	report := buildDoctorReport(*timeout)
	if *jsonOut {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		return nil
	}

	fmt.Fprintf(stdout, "%s doctor: %s  %s\n\n",
		paint(stdout, ansiBold+scholarlmRedBold, "WisDev"),
		statusLabel(stdout, report.Status),
		paint(stdout, ansiDim, report.Version),
	)
	for _, check := range report.Checks {
		detail := check.Detail
		if check.ID == "search-providers" && !strings.Contains(detail, "no search") {
			if idx := strings.Index(detail, "("); idx > 0 && strings.Contains(detail, ", ") {
				prefix := detail[:idx]
				rest := strings.TrimSuffix(strings.TrimPrefix(detail[idx:], "("), ")")
				names := strings.Split(rest, ", ")
				detail = fmt.Sprintf("%s (%s)", strings.TrimSpace(prefix), truncateList(names, 6))
			}
		}
		fmt.Fprintf(stdout, "  %s  %-24s %s\n", statusGlyph(check.Status), check.Name+":", detail)
	}

	fmt.Fprintln(stdout)
	switch report.Status {
	case "pass":
		note(stdout, "All checks passed. Try: wisdev search \"your research question\"")
	case "warn":
		note(stdout, "Warnings only. Start the server with: wisdev serve")
	default:
		userError(stdout, "Fix failing checks before running research tasks.")
	}
	printScholarLMBrandingProminent(stdout)
	if report.Status == "fail" {
		return fmt.Errorf("doctor found failing checks")
	}
	return nil
}

func buildDoctorReport(timeout time.Duration) doctorReport {
	checks := []doctorCheck{}

	registry := search.BuildRegistry()
	providerNames := make([]string, 0, len(registry.All()))
	for _, provider := range registry.All() {
		providerNames = append(providerNames, provider.Name())
	}
	sort.Strings(providerNames)
	providerStatus := "pass"
	providerDetail := fmt.Sprintf("%d built-in providers (%s)", len(providerNames), strings.Join(providerNames, ", "))
	if len(providerNames) == 0 {
		providerStatus = "fail"
		providerDetail = "no search providers registered"
	}
	checks = append(checks, doctorCheck{
		ID: "search-providers", Name: "Search providers", Status: providerStatus, Detail: providerDetail,
	})

	mcpSrv := internalwisdev.NewMCPServer(registry)
	toolNames := make([]string, 0, 4)
	for _, tool := range mcpSrv.ListTools() {
		toolNames = append(toolNames, tool.Name)
	}
	sort.Strings(toolNames)
	mcpStatus := "pass"
	mcpDetail := fmt.Sprintf("%d MCP tools (%s)", len(toolNames), strings.Join(toolNames, ", "))
	if len(toolNames) < 4 {
		mcpStatus = "fail"
		mcpDetail = fmt.Sprintf("expected 4 MCP tools, got %d", len(toolNames))
	}
	checks = append(checks, doctorCheck{
		ID: "mcp-tools", Name: "MCP tools", Status: mcpStatus, Detail: mcpDetail,
	})

	baseURL := strings.TrimRight(envOrDefault("WISDEV_ORCHESTRATOR_URL", defaultBaseURL), "/")
	probeStatus, probeDetail := probeOrchestrator(baseURL, timeout)
	checks = append(checks, doctorCheck{
		ID: "orchestrator", Name: "Orchestrator reachability", Status: probeStatus, Detail: probeDetail,
	})

	llmStatus, llmDetail := probeResearchLLMProviders(timeout)
	checks = append(checks, doctorCheck{
		ID: "llm-providers", Name: "LLM providers", Status: llmStatus, Detail: llmDetail,
	})

	overall := "pass"
	for _, check := range checks {
		if check.Status == "fail" {
			overall = "fail"
			break
		}
		if check.Status == "warn" && overall == "pass" {
			overall = "warn"
		}
	}

	return doctorReport{
		Status:  overall,
		Version: Version,
		Checks:  checks,
	}
}

func probeOrchestrator(baseURL string, timeout time.Duration) (string, string) {
	if strings.TrimSpace(baseURL) == "" {
		return "warn", "WISDEV_ORCHESTRATOR_URL is empty"
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	endpoints := []string{"/readiness", "/health", "/wisdev/mcp/status"}
	var lastDetail string
	for _, endpoint := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+endpoint, nil)
		if err != nil {
			lastDetail = err.Error()
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastDetail = fmt.Sprintf("%s%s unreachable (%v)", baseURL, endpoint, err)
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return "pass", fmt.Sprintf("%s%s -> HTTP %d", baseURL, endpoint, resp.StatusCode)
		}
		lastDetail = fmt.Sprintf("%s%s -> HTTP %d", baseURL, endpoint, resp.StatusCode)
	}

	return "warn", lastDetail + " (start with: make serve)"
}

func probeResearchLLMProviders(timeout time.Duration) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := llm.NewClient()
	llm.WireDirectProviders(ctx, client)
	status := client.FullDirectProviderStatus(ctx)
	mode, _ := status["mode"].(llm.LLMProviderMode)
	chain, _ := status["chain"].(string)
	if chain == "" {
		chain = "sidecar"
	}

	local, _ := status["local"].(map[string]any)
	cloud, _ := status["cloud"].(map[string]any)
	configured, _ := status["configured"].(bool)

	switch mode {
	case llm.LLMProviderModeLocal:
		if local == nil {
			return "warn", "mode=local but no OpenAI-compatible/Ollama endpoint wired; set WISDEV_LLM_BASE_URL or WISDEV_LLM_PROVIDER=ollama"
		}
		return summarizeLocalLLMCheck(local)
	case llm.LLMProviderModeCloud:
		if cloud == nil {
			return "warn", "mode=cloud but Vertex/Gemini unavailable; run gcloud auth application-default login or set GOOGLE_CLOUD_PROJECT"
		}
		return "pass", fmt.Sprintf("mode=cloud chain=%s (%s)", chain, cloud["credentialSource"])
	case llm.LLMProviderModeHybrid:
		if !configured {
			return "warn", "mode=hybrid but no direct providers wired; configure Ollama (WISDEV_LLM_BASE_URL) and/or Vertex credentials"
		}
		parts := []string{fmt.Sprintf("mode=hybrid tier-split chain=%s", chain)}
		if local != nil {
			localStatus, localDetail := summarizeLocalLLMCheck(local)
			parts = append(parts, "local="+localDetail)
			if localStatus == "warn" && cloud == nil {
				return "warn", strings.Join(parts, "; ")
			}
		}
		if cloud != nil {
			parts = append(parts, fmt.Sprintf("cloud=%s", cloud["credentialSource"]))
		} else {
			parts = append(parts, "cloud=unavailable (local-only fallback)")
		}
		overall := "pass"
		if local != nil {
			if localStatus, _ := summarizeLocalLLMCheck(local); localStatus == "warn" && cloud == nil {
				overall = "warn"
			}
		}
		return overall, strings.Join(parts, "; ")
	default:
		if configured {
			return "pass", fmt.Sprintf("chain=%s", chain)
		}
		return "warn", "no direct LLM providers wired; synthesis will use orchestrator sidecar or heuristic fallback"
	}
}

func summarizeLocalLLMCheck(local map[string]any) (string, string) {
	backend, _ := local["backend"].(string)
	model, _ := local["model"].(string)
	cred, _ := local["credentialSource"].(string)
	healthy, _ := local["healthy"].(bool)
	if !healthy {
		errText, _ := local["error"].(string)
		return "warn", fmt.Sprintf("%s/%s unreachable (%s); run: ollama serve && ollama pull %s", backend, model, errText, model)
	}
	if available, ok := local["modelAvailable"].(bool); ok && !available {
		errText, _ := local["modelError"].(string)
		return "warn", fmt.Sprintf("%s reachable but model %s missing (%s); run: ollama pull %s", backend, model, errText, model)
	}
	detail := fmt.Sprintf("%s/%s via %s", backend, model, cred)
	if resolved, ok := local["resolvedModel"].(string); ok && resolved != "" && resolved != model {
		detail += " (resolved=" + resolved + ")"
	}
	return "pass", detail
}

func probeLocalLLM(baseURL, model string, timeout time.Duration) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client := llm.NewOpenAICompatibleClient(baseURL, model, "")
	if err := client.HealthCheck(ctx); err != nil {
		return "warn", fmt.Sprintf("%s unreachable (%v); run: ollama serve && ollama pull %s", baseURL, err, model)
	}
	if available, detail := client.ModelAvailable(ctx); !available {
		return "warn", fmt.Sprintf("%s reachable but model %s missing (%s); run: ollama pull %s", baseURL, model, detail, model)
	} else if detail != "" && detail != model {
		return "pass", fmt.Sprintf("%s healthy (model=%s, resolved=%s)", baseURL, model, detail)
	}
	return "pass", fmt.Sprintf("%s healthy (model=%s)", baseURL, model)
}
