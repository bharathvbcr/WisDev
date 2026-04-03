package wisdev

import "testing"

func TestResolvePythonBase(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_URL", "http://python-sidecar.local/")
	t.Setenv("SIDECAR_API_URL", "http://sidecar.invalid")
	t.Setenv("VITE_CLOUDRUN_URL", "http://frontend.invalid")

	if got := ResolvePythonBase(); got != "http://python-sidecar.local" {
		t.Fatalf("ResolvePythonBase() = %q, want %q", got, "http://python-sidecar.local")
	}
}

func TestResolvePythonBaseDefaultsToLocalSidecar(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_URL", "")
	t.Setenv("SIDECAR_API_URL", "http://sidecar.invalid")
	t.Setenv("VITE_CLOUDRUN_URL", "http://frontend.invalid")

	if got := ResolvePythonBase(); got != defaultPythonSidecarURL {
		t.Fatalf("ResolvePythonBase() = %q, want %q", got, defaultPythonSidecarURL)
	}
}
