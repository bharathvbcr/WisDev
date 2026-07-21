package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type mcpClientConfig struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

type mcpServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

func runMCPConfig(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mcp-config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	providers := fs.String("provider", "openalex,arxiv", "comma-separated provider names for wisdev mcp")
	writePath := fs.String("write", "", "write config to this path (e.g. .cursor/mcp.json or ~/.cursor/mcp.json)")
	useBinary := fs.Bool("binary", false, "use absolute path to the wisdev binary instead of go run")
	replace := fs.Bool("replace", false, "overwrite the entire mcpServers map instead of merging the wisdev entry")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	entry, err := buildMCPServerEntry(strings.TrimSpace(*providers), *useBinary)
	if err != nil {
		return err
	}

	config := mcpClientConfig{
		MCPServers: map[string]mcpServerEntry{
			"wisdev": entry,
		},
	}

	target := strings.TrimSpace(*writePath)
	if target == "" {
		encoded, encErr := json.MarshalIndent(config, "", "  ")
		if encErr != nil {
			return encErr
		}
		fmt.Fprint(stdout, string(encoded)+"\n")
		return nil
	}

	if !filepath.IsAbs(target) {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return cwdErr
		}
		target = filepath.Join(cwd, target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	if !*replace {
		if merged, mergeErr := mergeMCPConfigFile(target, entry); mergeErr != nil {
			return mergeErr
		} else if merged != nil {
			config = *merged
		}
	}

	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(target, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	note(stdout, "Wrote MCP client config to %s", target)
	return nil
}

func buildMCPServerEntry(providers string, useBinary bool) (mcpServerEntry, error) {
	if providers == "" {
		providers = "openalex,arxiv"
	}
	mcpArgs := []string{"mcp", "--provider", providers}

	if useBinary {
		bin, err := resolveWisdevBinaryPath()
		if err != nil {
			return mcpServerEntry{}, err
		}
		entry := mcpServerEntry{
			Command: bin,
			Args:    mcpArgs,
			Env:     mcpClientEnv(),
		}
		return entry, nil
	}

	root, err := findOrchestratorRoot()
	if err != nil {
		return mcpServerEntry{}, err
	}
	return mcpServerEntry{
		Command: "go",
		Args: []string{
			"run", "./cmd/wisdev", "mcp",
			"--provider", providers,
		},
		Cwd: filepath.ToSlash(root),
		Env: mcpClientEnv(),
	}, nil
}

func resolveWisdevBinaryPath() (string, error) {
	if exe, err := os.Executable(); err == nil {
		resolved, resErr := filepath.EvalSymlinks(exe)
		if resErr == nil {
			exe = resolved
		}
		base := strings.TrimSuffix(filepath.Base(exe), ".exe")
		if base == "wisdev" {
			return filepath.Clean(exe), nil
		}
	}
	if path, err := exec.LookPath("wisdev"); err == nil {
		abs, absErr := filepath.Abs(path)
		if absErr != nil {
			return filepath.Clean(path), nil
		}
		return abs, nil
	}
	return "", fmt.Errorf("wisdev binary not found on PATH; install with scripts/install.sh or go install ./cmd/wisdev, or omit --binary to use go run")
}

func mcpClientEnv() map[string]string {
	// Cursor / Claude Desktop often launch MCP with a minimal PATH that omits
	// ~/go/bin and ~/.local/bin. Prefer an absolute --binary command; keep a
	// compact PATH prefix so child tools remain resolvable without embedding
	// the entire interactive shell PATH into mcp.json.
	home := os.Getenv("HOME")
	parts := []string{
		filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".local", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
	}
	return map[string]string{
		"PATH": strings.Join(parts, string(os.PathListSeparator)),
	}
}

func mergeMCPConfigFile(target string, wisdevEntry mcpServerEntry) (*mcpClientConfig, error) {
	raw, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}

	var existing mcpClientConfig
	if err := json.Unmarshal(raw, &existing); err != nil {
		return nil, fmt.Errorf("existing MCP config at %s is not valid JSON (use --replace to overwrite): %w", target, err)
	}
	if existing.MCPServers == nil {
		existing.MCPServers = map[string]mcpServerEntry{}
	}
	existing.MCPServers["wisdev"] = wisdevEntry
	return &existing, nil
}
