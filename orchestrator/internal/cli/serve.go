package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

var runServeProcess = func(root string, stdout, stderr io.Writer) error {
	cmd := exec.Command("go", "run", "./cmd/server")
	cmd.Dir = root
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runServe(stdout, stderr io.Writer) error {
	root, err := findOrchestratorRoot()
	if err != nil {
		return printServeInstructions(stdout, err)
	}

	note(stderr, "Starting WisDev orchestrator from %s", root)
	return runServeProcess(root, stdout, stderr)
}

func printServeInstructions(stdout io.Writer, err error) error {
	userError(stdout, "Could not locate the orchestrator directory.")
	if err != nil {
		note(stdout, "  %v", err)
	}
	fmt.Fprintln(stdout)
	note(stdout, "  cd orchestrator && go run ./cmd/server")
	note(stdout, "  make serve")
	note(stdout, "  export WISDEV_ORCHESTRATOR_ROOT=/path/to/orchestrator")
	return fmt.Errorf("orchestrator root not found")
}
