//go:build integration
// +build integration

package testsupport

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const defaultInternalServiceKey = "go-integration-sidecar-key"

type ManagedPythonSidecar struct {
	BaseURL            string
	GRPCAddr           string
	InternalServiceKey string

	cmd  *exec.Cmd
	done chan error
	logs bytes.Buffer
}

func StartPythonSidecar(t *testing.T) *ManagedPythonSidecar {
	t.Helper()

	pythonCmd, pythonPrefix := resolvePythonProcessCommand(t)
	sidecarDir := locatePythonSidecarDir(t)

	httpPort := reserveTCPPort(t)
	grpcPort := reserveTCPPort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	grpcAddr := fmt.Sprintf("127.0.0.1:%d", grpcPort)

	args := append(append([]string{}, pythonPrefix...), "main.py")
	cmd := exec.Command(pythonCmd, args...)
	cmd.Dir = sidecarDir

	sidecar := &ManagedPythonSidecar{
		BaseURL:            baseURL,
		GRPCAddr:           grpcAddr,
		InternalServiceKey: defaultInternalServiceKey,
		cmd:                cmd,
		done:               make(chan error, 1),
	}

	cmd.Stdout = &sidecar.logs
	cmd.Stderr = &sidecar.logs
	cmd.Env = append(os.Environ(),
		"HOST=127.0.0.1",
		fmt.Sprintf("PORT=%d", httpPort),
		fmt.Sprintf("PYTHON_SIDECAR_GRPC_ADDR=%s", grpcAddr),
		fmt.Sprintf("INTERNAL_SERVICE_KEY=%s", sidecar.InternalServiceKey),
		"GOOGLE_API_KEY=test-key",
		"GEMINI_RUNTIME_MODE=native",
		"PYTHON_SIDECAR_WARM_PROBE=false",
		"UPSTASH_REDIS_URL=",
		"VERTEX_PROXY_URL=",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:1",
		"PYTHONUNBUFFERED=1",
		"NO_PROXY=127.0.0.1,localhost",
	)

	require.NoError(t, cmd.Start())
	go func() {
		sidecar.done <- cmd.Wait()
	}()

	waitForHealthyURL(t, baseURL+"/health", sidecar.done, &sidecar.logs)
	return sidecar
}

func (s *ManagedPythonSidecar) Stop() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}

	_ = s.cmd.Process.Kill()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
	}
}

func (s *ManagedPythonSidecar) Logs() string {
	if s == nil {
		return ""
	}
	return s.logs.String()
}

func resolvePythonProcessCommand(t *testing.T) (string, []string) {
	t.Helper()

	if pythonPath, err := exec.LookPath("python"); err == nil {
		return pythonPath, nil
	}
	if pyLauncher, err := exec.LookPath("py"); err == nil {
		return pyLauncher, []string{"-3"}
	}
	t.Fatalf("python is required for spawned sidecar integration")
	return "", nil
}

func locatePythonSidecarDir(t *testing.T) string {
	t.Helper()

	workingDir, err := os.Getwd()
	require.NoError(t, err)

	for dir := workingDir; ; dir = filepath.Dir(dir) {
		candidates := []string{
			filepath.Join(dir, "sidecar", "main.py"),
			filepath.Join(dir, "backend", "python_sidecar", "main.py"),
		}
		for _, candidate := range candidates {
			if _, statErr := os.Stat(candidate); statErr == nil {
				return filepath.Dir(candidate)
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	t.Fatalf("could not locate sidecar/main.py from %s", workingDir)
	return ""
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil && isLoopbackProviderUnavailableMessage(err.Error()) {
		t.Skipf("loopback sockets unavailable on this machine: %v", err)
	}
	require.NoError(t, err)
	defer ln.Close()

	return ln.Addr().(*net.TCPAddr).Port
}

func isLoopbackProviderUnavailableMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(normalized, "requested service provider could not be loaded or initialized")
}

func waitForHealthyURL(t *testing.T, healthURL string, done <-chan error, logs *bytes.Buffer) {
	t.Helper()

	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(45 * time.Second)
	lastErr := ""

	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("python sidecar exited before becoming healthy: %v\n%s", err, logs.String())
		default:
		}

		resp, err := client.Get(healthURL)
		if err == nil && resp != nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = resp.Status
		} else if err != nil {
			lastErr = err.Error()
		}

		time.Sleep(250 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for sidecar health at %s: %s\n%s", healthURL, lastErr, logs.String())
}
