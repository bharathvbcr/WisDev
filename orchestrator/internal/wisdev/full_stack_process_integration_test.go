//go:build integration
// +build integration

package wisdev

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpawnedGoRustPythonStackStrictPaths(t *testing.T) {
	pythonURL, pythonGRPCAddr, stopPython := startPythonSidecarProcess(t)
	defer stopPython()

	rustURL, stopRust := startRustGatewayProcess(t)
	defer stopRust()

	goURL, stopGo := startGoOrchestratorProcess(t, pythonURL, pythonGRPCAddr, rustURL)
	defer stopGo()

	client := &http.Client{Timeout: 20 * time.Second}

	t.Run("python sidecar health exposes live grpc", func(t *testing.T) {
		resp, err := client.Get(pythonURL + "/health")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var payload map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
		assert.Equal(t, "ok", AsOptionalString(payload["status"]))
		assert.Equal(t, "ok", dependencyStatusByName(payload["dependencies"], "grpc_sidecar"))
	})

	t.Run("runtime health exposes strict citation gate", func(t *testing.T) {
		resp, err := client.Get(goURL + "/runtime/health")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var payload map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
		health := asMap(payload["health"])
		require.NotNil(t, health)
		citationBroker := asMap(health["citationBroker"])
		require.NotNil(t, citationBroker)
		assert.Equal(t, "strict", AsOptionalString(citationBroker["mode"]))
		assert.Equal(t, rustURL, AsOptionalString(citationBroker["gatewayUrl"]))
		assert.False(t, toBool(citationBroker["allowGoFallback"]))
	})

	t.Run("go paper extraction uses spawned python sidecar", func(t *testing.T) {
		pdfBody := buildMinimalPDF("ScholarLM spawned integration harness")
		req, err := newMultipartPDFRequest(goURL+"/paper/extract-pdf", "integration.pdf", pdfBody)
		require.NoError(t, err)
		req.Header.Set("X-Internal-Service-Key", "wisdev-live-rust-key")

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var payload map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
		assert.NotEqual(t, "fallback-local", AsOptionalString(payload["source"]))
		assert.NotEmpty(t, payload)
	})

	t.Run("rust authority broker resolves citations over live socket", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"items": []map[string]any{
				{
					"id":    "p1",
					"title": "Paper 1",
					"doi":   "10.1000/test-1",
				},
			},
		})
		require.NoError(t, err)

		req, err := http.NewRequest(http.MethodPost, rustURL+"/internal/citations/resolve", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Service-Key", "wisdev-live-rust-key")

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var payload map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
		assert.True(t, toBool(payload["ok"]))
		data := asMap(payload["data"])
		resolved := firstArtifactMaps(data["resolved"])
		require.Len(t, resolved, 1)
		assert.Equal(t, "10.1000/test-1", AsOptionalString(resolved[0]["doi"]))
		assert.NotEmpty(t, AsOptionalString(resolved[0]["provenance_hash"]))
	})
}

func startPythonSidecarProcess(t *testing.T) (string, string, func()) {
	t.Helper()

	pythonCmd, pythonPrefix := resolvePythonProcessCommand(t)

	workingDir, err := os.Getwd()
	require.NoError(t, err)
	sidecarDir, err := filepath.Abs(filepath.Join(workingDir, "..", "..", "..", "python_sidecar"))
	require.NoError(t, err)

	httpPort := reserveTCPPort(t)
	grpcPort := reserveTCPPort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	grpcAddr := fmt.Sprintf("127.0.0.1:%d", grpcPort)

	args := append(append([]string{}, pythonPrefix...), "main.py")
	cmd := exec.Command(pythonCmd, args...)
	cmd.Dir = sidecarDir

	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("HOST=%s", "127.0.0.1"),
		fmt.Sprintf("PORT=%d", httpPort),
		fmt.Sprintf("GRPC_PORT=%d", grpcPort),
		fmt.Sprintf("PYTHON_SIDECAR_GRPC_ADDR=%s", grpcAddr),
		"INTERNAL_SERVICE_KEY=wisdev-live-rust-key",
		"VERTEX_PROXY_URL=http://127.0.0.1:1",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:1",
		"PYTHONUNBUFFERED=1",
		"NO_PROXY=127.0.0.1,localhost",
	)

	require.NoError(t, cmd.Start())
	waitForHealthyURL(t, baseURL+"/health", cmd, &logs)

	return baseURL, grpcAddr, func() {
		stopManagedProcess(cmd)
	}
}

func startGoOrchestratorProcess(t *testing.T, pythonURL string, pythonGRPCAddr string, rustURL string) (string, func()) {
	t.Helper()

	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is required for spawned orchestrator integration")
	}

	workingDir, err := os.Getwd()
	require.NoError(t, err)
	orchestratorDir, err := filepath.Abs(filepath.Join(workingDir, "..", ".."))
	require.NoError(t, err)

	httpPort := reserveTCPPort(t)
	metricsPort := reserveTCPPort(t)
	internalGRPCPort := reserveTCPPort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)

	serverBinary := filepath.Join(t.TempDir(), "wisdev-agent-server.exe")
	buildCmd := exec.Command(goPath, "build", "-o", serverBinary, "./cmd/server")
	buildCmd.Dir = orchestratorDir
	buildOutput, buildErr := buildCmd.CombinedOutput()
	require.NoError(t, buildErr, "go build failed: %s", string(buildOutput))

	cmd := exec.Command(serverBinary)
	cmd.Dir = orchestratorDir

	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", httpPort),
		fmt.Sprintf("INTERNAL_METRICS_PORT=%d", metricsPort),
		fmt.Sprintf("GO_INTERNAL_GRPC_ADDR=127.0.0.1:%d", internalGRPCPort),
		fmt.Sprintf("PYTHON_SIDECAR_HTTP_URL=%s", pythonURL),
		fmt.Sprintf("PYTHON_SIDECAR_GRPC_ADDR=%s", pythonGRPCAddr),
		fmt.Sprintf("RUST_GATEWAY_INTERNAL_URL=%s", rustURL),
		"INTERNAL_SERVICE_KEY=wisdev-live-rust-key",
		fmt.Sprintf("%s=false", allowGoCitationFallbackEnv),
		"DATABASE_URL=",
		"UPSTASH_REDIS_URL=",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	)

	require.NoError(t, cmd.Start())
	waitForHealthyURL(t, baseURL+"/health", cmd, &logs)

	return baseURL, func() {
		stopManagedProcess(cmd)
	}
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

func waitForHealthyURL(t *testing.T, healthURL string, cmd *exec.Cmd, logs *bytes.Buffer) {
	t.Helper()

	client := &http.Client{Timeout: 1 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(healthURL)
		if err == nil && resp != nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			t.Fatalf("process exited before becoming healthy:\n%s", logs.String())
		}
		time.Sleep(500 * time.Millisecond)
	}

	stopManagedProcess(cmd)
	t.Fatalf("timed out waiting for health at %s:\n%s", healthURL, logs.String())
}

func stopManagedProcess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

func newMultipartPDFRequest(targetURL string, fileName string, pdfBytes []byte) (*http.Request, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(pdfBytes); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, targetURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}

func buildMinimalPDF(text string) []byte {
	escapedText := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text)
	stream := fmt.Sprintf("BT /F1 18 Tf 36 96 Td (%s) Tj ET", escapedText)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var out strings.Builder
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for idx, objectBody := range objects {
		objectNumber := idx + 1
		offsets[objectNumber] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", objectNumber, objectBody)
	}
	xrefOffset := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n", len(objects)+1)
	out.WriteString("0000000000 65535 f \n")
	for objectNumber := 1; objectNumber <= len(objects); objectNumber++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[objectNumber])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return []byte(out.String())
}

func dependencyStatusByName(raw any, name string) string {
	dependencies, ok := raw.([]any)
	if !ok {
		return ""
	}
	for _, item := range dependencies {
		dependency := asMap(item)
		if dependency == nil {
			continue
		}
		if AsOptionalString(dependency["name"]) == name {
			return AsOptionalString(dependency["status"])
		}
	}
	return ""
}


