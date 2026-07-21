package envload

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// maxDotEnvWalkDepth bounds how many parent directories the loader climbs
// looking for a `.wisdev/ports.env`. Without a bound, invoking the CLI from a
// directory with no repo-root marker anywhere between it and `/` would let a
// `.wisdev/ports.env` planted in any ancestor (e.g. a shared workspace parent)
// inject environment variables into this process.
const maxDotEnvWalkDepth = 8

// portsEnvAllowedKeys is the exact set of variables a generated
// `.wisdev/ports.env` may set — it mirrors stackconfig.StackPorts.Env plus the
// WISDEV_AUTO_PORT marker written by WritePortsEnv. Restricting ports.env to
// this allowlist means a hostile or stale ports.env cannot smuggle arbitrary
// variables (service URLs, model overrides, secrets) into the environment even
// if it is read; only real port-manifest keys take effect.
var portsEnvAllowedKeys = map[string]struct{}{
	"WISDEV_AUTO_PORT":             {},
	"PORT":                         {},
	"PYTHON_SIDECAR_PORT":          {},
	"INTERNAL_METRICS_PORT":        {},
	"GO_INTERNAL_GRPC_ADDR":        {},
	"PYTHON_SIDECAR_HTTP_URL":      {},
	"PYTHON_SIDECAR_GRPC_ADDR":     {},
	"WISDEV_ORCHESTRATOR_URL":      {},
	"PYTHON_SIDECAR_LLM_TRANSPORT": {},
}

// dotEnvSource is a candidate file plus how strictly its keys are trusted.
type dotEnvSource struct {
	path string
	// portsOnly restricts the file to portsEnvAllowedKeys. Set for
	// machine-generated `.wisdev/ports.env` port manifests, which must never be
	// able to set arbitrary variables.
	portsOnly bool
}

// LoadDotEnvFiles loads KEY=VALUE pairs from WisDev .env files without
// overriding variables already present in the process environment.
func LoadDotEnvFiles() {
	for _, src := range dotEnvCandidates() {
		loadDotEnvFile(src.path, src.portsOnly)
	}
}

func dotEnvCandidates() []dotEnvSource {
	candidates := make([]dotEnvSource, 0, 8)
	if explicit := strings.TrimSpace(os.Getenv("WISDEV_DOTENV")); explicit != "" {
		// An explicitly configured file is a full .env chosen by the operator;
		// it is trusted for arbitrary keys.
		candidates = append(candidates, dotEnvSource{path: explicit})
	}
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for depth := 0; depth <= maxDotEnvWalkDepth; depth++ {
			candidates = append(candidates, dotEnvSource{
				path:      filepath.Join(dir, ".wisdev", "ports.env"),
				portsOnly: true,
			})
			if isRepoDotEnvRoot(dir) {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		candidates = append(candidates, dotEnvSource{path: filepath.Join(cwd, ".env")})
		candidates = append(candidates, dotEnvSource{path: filepath.Join(cwd, "..", ".env")})
	}
	if exe, err := os.Executable(); err == nil {
		root := filepath.Dir(filepath.Dir(exe))
		candidates = append(candidates, dotEnvSource{
			path:      filepath.Join(root, ".wisdev", "ports.env"),
			portsOnly: true,
		})
		candidates = append(candidates, dotEnvSource{path: filepath.Join(root, ".env")})
	}
	return uniqueSources(candidates)
}

func isRepoDotEnvRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "orchestrator", "cmd", "wisdev")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "wisdev")); err == nil {
		return true
	}
	return false
}

func uniqueSources(sources []dotEnvSource) []dotEnvSource {
	seen := make(map[string]struct{}, len(sources))
	out := make([]dotEnvSource, 0, len(sources))
	for _, src := range sources {
		path := strings.TrimSpace(src.path)
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
		out = append(out, dotEnvSource{path: path, portsOnly: src.portsOnly})
	}
	return out
}

// loadDotEnvFile applies KEY=VALUE lines from path, skipping keys already set in
// the environment. When portsOnly is true, only keys in portsEnvAllowedKeys are
// honored; every other key is ignored.
func loadDotEnvFile(path string, portsOnly bool) {
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
		if portsOnly {
			if _, allowed := portsEnvAllowedKeys[key]; !allowed {
				continue
			}
		}
		value = strings.Trim(value, `"'`)
		if strings.TrimSpace(os.Getenv(key)) != "" {
			continue
		}
		_ = os.Setenv(key, value)
	}
}
