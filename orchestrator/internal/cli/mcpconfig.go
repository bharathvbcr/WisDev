package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type mcpClientConfig struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

type mcpServerEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Cwd     string   `json:"cwd,omitempty"`
}

func runMCPConfig(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mcp-config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	providers := fs.String("provider", "openalex,arxiv", "comma-separated provider names for wisdev mcp")
	writePath := fs.String("write", "", "write config to this path (e.g. .cursor/mcp.json)")
	useBinary := fs.Bool("binary", false, "use wisdev on PATH instead of go run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	root, err := findOrchestratorRoot()
	if err != nil {
		return err
	}

	entry := mcpServerEntry{Cwd: filepath.ToSlash(root)}
	if *useBinary {
		entry.Command = "wisdev"
		entry.Args = []string{"mcp", "--provider", strings.TrimSpace(*providers)}
	} else {
		entry.Command = "go"
		entry.Args = []string{
			"run", "./cmd/wisdev", "mcp",
			"--provider", strings.TrimSpace(*providers),
		}
	}

	config := mcpClientConfig{
		MCPServers: map[string]mcpServerEntry{
			"wisdev": entry,
		},
	}

	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	payload := string(encoded) + "\n"

	if strings.TrimSpace(*writePath) == "" {
		fmt.Fprint(stdout, payload)
		return nil
	}

	target := *writePath
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
	if err := os.WriteFile(target, []byte(payload), 0o644); err != nil {
		return err
	}
	note(stdout, "Wrote MCP client config to %s", target)
	return nil
}
