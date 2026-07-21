package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func findOrchestratorRoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv("WISDEV_ORCHESTRATOR_ROOT")); root != "" {
		if isOrchestratorRoot(root) {
			return filepath.Clean(root), nil
		}
		return "", errors.New("WISDEV_ORCHESTRATOR_ROOT does not look like an orchestrator directory")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for dir := cwd; ; dir = filepath.Dir(dir) {
		if isOrchestratorRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	return "", errors.New("orchestrator root not found (expected cmd/server and cmd/wisdev); set WISDEV_ORCHESTRATOR_ROOT or cd into orchestrator/")
}

func isOrchestratorRoot(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	_, serverErr := os.Stat(filepath.Join(dir, "cmd", "server", "main.go"))
	_, cliErr := os.Stat(filepath.Join(dir, "cmd", "wisdev", "main.go"))
	return serverErr == nil && cliErr == nil
}

func historyFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wisdev", "history.jsonl")
}

func sessionFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wisdev", "session.json")
}

