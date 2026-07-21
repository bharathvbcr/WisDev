//go:build integration
// +build integration

package wisdev

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResearchQuestRuntimeResumeRecoversBlockedCitationVerdictWithLiveRustGateway(t *testing.T) {
	rustURL, shutdown := startRustGatewayProcess(t)
	defer shutdown()

	t.Setenv("RUST_GATEWAY_INTERNAL_URL", rustURL)
	t.Setenv("INTERNAL_SERVICE_KEY", "wisdev-live-rust-key")

	retrieveCalls := 0
	hooks := stubQuestHooks(testQuestSources(1), CitationVerdict{})
	hooks.CitationFn = nil
	hooks.RetrieveFn = func(_ context.Context, quest *ResearchQuest) ([]Source, map[string]any, error) {
		retrieveCalls++
		if retrieveCalls == 1 {
			return []Source{{
					ID:    "live-rust-missing-id",
					Title: "Live Rust Missing Identifier",
				}}, map[string]any{
					"count":   1,
					"traceId": "live-rust-trace-1",
				}, nil
		}
		return []Source{{
				ID:    "live-rust-resolved",
				Title: "Live Rust Resolved Identifier",
				DOI:   "10.1000/test-1",
			}}, map[string]any{
				"count":   1,
				"traceId": "live-rust-trace-2",
			}, nil
	}

	runtime := newResearchQuestRuntimeWithMemoryStore(t, newTestMemoryStore(), hooks)

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:      "live-rust-user",
		Query:       "live rust broker recovery",
		QualityMode: "quality",
	})
	require.NoError(t, err)
	assert.Equal(t, QuestStatusBlocked, quest.Status)
	assert.False(t, quest.CitationVerdict.Promoted)
	assert.NotEmpty(t, quest.BlockingIssues)

	resumed, err := runtime.ResumeQuest(context.Background(), quest.QuestID, ResearchQuestRequest{
		ForceResume: true,
		Query:       "live rust broker recovery repaired",
	})
	require.NoError(t, err)
	assert.Equal(t, QuestStatusComplete, resumed.Status)
	assert.True(t, resumed.CitationVerdict.Promoted)
	assert.Equal(t, 2, retrieveCalls)
	if assert.NotEmpty(t, resumed.CitationAuthorities) {
		assert.Equal(t, "rust-citation-authority-broker", resumed.CitationAuthorities[0].ResolutionEngine)
	}
}

func startRustGatewayProcess(t *testing.T) (string, func()) {
	return startRustGatewayProcessWithTarget(t, "http://127.0.0.1:1")
}

func startRustGatewayProcessWithTarget(t *testing.T, goBackendURL string) (string, func()) {
	t.Helper()

	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Fatalf("cargo is required for live Rust gateway integration")
	}

	workingDir, err := os.Getwd()
	require.NoError(t, err)

	gatewayDir, err := filepath.Abs(filepath.Join(workingDir, "..", "..", "..", "rust_gateway", "crates", "gateway"))
	require.NoError(t, err)

	port := reserveTCPPort(t)
	var logs bytes.Buffer

	baseEnv := append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"FIREBASE_PROJECT_ID=local-dev",
		fmt.Sprintf("GO_BACKEND_URL=%s", goBackendURL),
		"INTERNAL_SERVICE_KEY=wisdev-live-rust-key",
		"CORS_ALLOWED_ORIGINS=http://localhost",
		"DATABASE_URL=",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:1",
		"REQUEST_TIMEOUT_SECONDS=5",
		"MAX_CONCURRENT_REQUESTS=10",
	)

	binaryPath := ensureRustGatewayBinary(t, gatewayDir, cargoPath, &logs)
	cmd := exec.Command(binaryPath)
	cmd.Dir = gatewayDir
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	cmd.Env = baseEnv

	require.NoError(t, cmd.Start())

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	healthURL := baseURL + "/health"
	client := &http.Client{Timeout: 1 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, reqErr := client.Get(healthURL)
		if reqErr == nil && resp != nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return baseURL, func() {
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
					_ = cmd.Wait()
				}
			}
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			t.Fatalf("rust gateway exited before becoming healthy:\n%s", logs.String())
		}
		time.Sleep(500 * time.Millisecond)
	}

	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	t.Fatalf("timed out waiting for rust gateway health:\n%s", logs.String())
	return "", func() {}
}

func ensureRustGatewayBinary(t *testing.T, gatewayDir string, cargoPath string, logs *bytes.Buffer) string {
	t.Helper()

	workspaceDir := filepath.Dir(filepath.Dir(gatewayDir))
	targetBinary := filepath.Join(workspaceDir, "target", "debug", "gateway.exe")
	buildCmd := exec.Command(cargoPath, "build", "-q")
	buildCmd.Dir = gatewayDir
	buildCmd.Stdout = logs
	buildCmd.Stderr = logs
	buildCmd.Env = os.Environ()
	buildErr := buildCmd.Run()
	if buildErr != nil {
		if _, statErr := os.Stat(targetBinary); statErr != nil {
			require.NoError(t, buildErr, "cargo build failed and no fallback gateway binary is available")
		}
	}

	tempBinary := filepath.Join(t.TempDir(), "gateway.exe")
	sourceBytes, readErr := os.ReadFile(targetBinary)
	require.NoError(t, readErr)
	require.NoError(t, os.WriteFile(tempBinary, sourceBytes, 0o755))
	return tempBinary
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return addr.Port
}
