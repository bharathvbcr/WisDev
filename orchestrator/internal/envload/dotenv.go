package envload

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadDotEnvFiles loads KEY=VALUE pairs from WisDev .env files without
// overriding variables already present in the process environment.
func LoadDotEnvFiles() {
	for _, path := range dotEnvCandidates() {
		loadDotEnvFile(path)
	}
}

func dotEnvCandidates() []string {
	candidates := make([]string, 0, 4)
	if explicit := strings.TrimSpace(os.Getenv("WISDEV_DOTENV")); explicit != "" {
		candidates = append(candidates, explicit)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, ".env"))
		candidates = append(candidates, filepath.Join(cwd, "..", ".env"))
	}
	if exe, err := os.Executable(); err == nil {
		root := filepath.Dir(filepath.Dir(exe))
		candidates = append(candidates, filepath.Join(root, ".env"))
	}
	return uniquePaths(candidates)
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func loadDotEnvFile(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		value = strings.Trim(value, `"'`)
		if strings.TrimSpace(os.Getenv(key)) != "" {
			continue
		}
		_ = os.Setenv(key, value)
	}
}
