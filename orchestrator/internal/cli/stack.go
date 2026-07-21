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

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/stackconfig"
)

func runStack(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return printStackUsage(stderr)
	}
	switch args[0] {
	case "ports":
		return runStackPorts(args[1:], stdout, stderr)
	case "start":
		return runStackStart(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		return printStackUsage(stdout)
	default:
		return fmt.Errorf("unknown stack subcommand %q (try: wisdev stack ports|start)", args[0])
	}
}

func printStackUsage(w io.Writer) error {
	fmt.Fprintln(w, "Usage: wisdev stack <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  ports [--write]   Allocate local stack ports (respects WISDEV_AUTO_PORT)")
	fmt.Fprintln(w, "  start             Allocate ports, write .wisdev/ports.env, start sidecar + serve")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Environment:")
	fmt.Fprintln(w, "  WISDEV_AUTO_PORT=1   Prefer manifest ports when free, else pick ephemeral ports")
	return nil
}

func runStackPorts(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("stack ports", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	write := fs.Bool("write", false, "write .wisdev/ports.env with the allocation")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var ports stackconfig.StackPorts
	var err error
	if *write {
		path := stackconfig.PortsEnvPath()
		ports, err = stackconfig.AllocateLocalStackPortsAndWrite(path)
		if err != nil {
			return err
		}
		note(stderr, "  ports saved → %s", absPathOrSelf(path))
	} else {
		ports, err = stackconfig.AllocateLocalStackPorts()
		if err != nil {
			return err
		}
	}
	env := ports.Env()
	if *jsonOut {
		encoded, err := json.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		return nil
	}
	for _, key := range []string{
		"PORT", "INTERNAL_METRICS_PORT", "GO_INTERNAL_GRPC_ADDR",
		"PYTHON_SIDECAR_HTTP_URL", "PYTHON_SIDECAR_GRPC_ADDR", "WISDEV_ORCHESTRATOR_URL",
	} {
		fmt.Fprintf(stdout, "%s=%s\n", key, env[key])
	}
	return nil
}

func runStackStart(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("stack start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	repoRoot, err := findWisdevArcRoot()
	if err != nil {
		return err
	}
	script := filepath.Join(repoRoot, "scripts", "start-stack.sh")
	if _, statErr := os.Stat(script); statErr != nil {
		return fmt.Errorf("stack launcher missing at %s: %w", script, statErr)
	}

	cmd := exec.Command(script)
	cmd.Dir = repoRoot
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func findWisdevArcRoot() (string, error) {
	if root, err := findOrchestratorRoot(); err == nil {
		parent := filepath.Dir(root)
		if isWisdevArcRootDir(parent) {
			return parent, nil
		}
		return root, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if isWisdevArcRootDir(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", fmt.Errorf("wisdev-arc root not found")
}

func isWisdevArcRootDir(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "scripts", "start-stack.sh")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "wisdev")); err == nil {
		return true
	}
	return false
}
