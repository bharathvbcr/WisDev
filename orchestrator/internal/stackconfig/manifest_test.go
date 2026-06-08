package stackconfig

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStackConfigResolution(t *testing.T) {
	t.Run("defaults from manifest", func(t *testing.T) {
		service, ok := Manifest.Services["go_orchestrator"]
		require.True(t, ok)
		grpcTarget, ok := Manifest.GRPCTargets["go_internal"]
		require.True(t, ok)
		expectedBaseURL := strings.TrimRight(service.DefaultBaseURL, "/")
		if overlayBaseURL := strings.TrimRight(CurrentOverlay().ServiceBaseURLs["go_orchestrator"], "/"); overlayBaseURL != "" {
			expectedBaseURL = overlayBaseURL
		}

		assert.Equal(t, "local", CurrentOverlayName())
		assert.Equal(t, expectedBaseURL, ResolveBaseURL("go_orchestrator"))
		assert.Equal(t, strings.TrimSpace(grpcTarget.DefaultAddress), ResolveGRPCTarget("go_internal"))
		assert.Equal(t, service.ListenPorts["http"], ResolveListenPort("go_orchestrator", "http"))
		assert.Equal(t, service.ListenPorts["metrics"], ResolveListenPort("go_orchestrator", "metrics"))
	})

	t.Run("current overlay and env fallbacks", func(t *testing.T) {
		t.Setenv("ENDPOINTS_MANIFEST_ENV", "")
		t.Setenv("INTERNAL_SERVICE_KEY", "override-key")
		t.Setenv("PORT", "9911")
		t.Setenv("INTERNAL_METRICS_PORT", "9912")
		t.Setenv("GO_INTERNAL_GRPC_ADDR", "127.0.0.1:9913")

		assert.Equal(t, "local", CurrentOverlayName())
		assert.Equal(t, "override-key", ResolveEnv("INTERNAL_SERVICE_KEY"))
		assert.Equal(t, "fallback-value", ResolveEnvWithFallback("MISSING_KEY", "fallback-value"))
		assert.Equal(t, "http://127.0.0.1:8081", ResolveBaseURL("go_orchestrator"))
		assert.Equal(t, "127.0.0.1:9913", ResolveGRPCTarget("go_internal"))
		assert.Equal(t, 9911, ResolveListenPort("go_orchestrator", "http"))
		assert.Equal(t, 9912, ResolveListenPort("go_orchestrator", "metrics"))
		assert.Equal(t, 9913, ResolveListenPort("go_orchestrator", "grpc"))
		assert.NoError(t, ValidateService("go_orchestrator"))
	})

	t.Run("unknown identifiers and service validation errors", func(t *testing.T) {
		originalLocal := Manifest.Overlays["local"]
		overlayCopy := ManifestOverlay{
			Env:             map[string]string{},
			ServiceBaseURLs: map[string]string{},
		}
		for k, v := range originalLocal.Env {
			overlayCopy.Env[k] = v
		}
		for k, v := range originalLocal.ServiceBaseURLs {
			overlayCopy.ServiceBaseURLs[k] = v
		}

		Manifest.Overlays["local"] = overlayCopy
		t.Cleanup(func() {
			Manifest.Overlays["local"] = originalLocal
		})

		t.Setenv("INTERNAL_SERVICE_KEY", "")
		assert.Error(t, ValidateService("unknown-service"))
		assert.Empty(t, ResolveBaseURL("unknown-service"))
		assert.Empty(t, ResolveGRPCTarget("unknown-target"))
		assert.Equal(t, 0, ResolveListenPort("unknown-service", "http"))

		Manifest.Overlays["local"] = ManifestOverlay{
			Env:             map[string]string{},
			ServiceBaseURLs: map[string]string{},
		}
		t.Setenv("INTERNAL_SERVICE_KEY", "")
		err := ValidateService("go_orchestrator")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing required manifest env")
	})

	t.Run("manifest env override", func(t *testing.T) {
		t.Setenv("ENDPOINTS_MANIFEST_ENV", "docker")
		assert.Equal(t, "docker", CurrentOverlayName())
	})
}

func TestResolveListenPortInvalidOverrides(t *testing.T) {
	t.Setenv("PORT", "not-a-number")
	t.Setenv("INTERNAL_METRICS_PORT", "not-a-number")
	t.Setenv("GO_INTERNAL_GRPC_ADDR", "missing-port")

	assert.Equal(t, 8081, ResolveListenPort("go_orchestrator", "http"))
	assert.Equal(t, 9090, ResolveListenPort("go_orchestrator", "metrics"))
	assert.Equal(t, 50053, ResolveListenPort("go_orchestrator", "grpc"))
}

func TestManifestHelpersUnknowns(t *testing.T) {
	t.Setenv("ENDPOINTS_MANIFEST_ENV", "does-not-exist")
	assert.Equal(t, "local", CurrentOverlayName())
	assert.Equal(t, Manifest.Overlays["local"], CurrentOverlay())
}

func TestResolveEnvUsesOverlayWhenUnset(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_KEY", "")
	assert.Equal(t, "dev-internal-key", ResolveEnv("INTERNAL_SERVICE_KEY"))
}

func TestResolveInternalServiceKey(t *testing.T) {
	t.Run("explicit env wins", func(t *testing.T) {
		t.Setenv("ENDPOINTS_MANIFEST_ENV", "")
		t.Setenv("INTERNAL_SERVICE_KEY", "explicit-key")

		assert.Equal(t, "explicit-key", ResolveInternalServiceKey())
	})

	t.Run("local overlay fallback", func(t *testing.T) {
		t.Setenv("ENDPOINTS_MANIFEST_ENV", "local")
		t.Setenv("INTERNAL_SERVICE_KEY", "")

		assert.Equal(t, "dev-internal-key", ResolveInternalServiceKey())
	})

	t.Run("cloud overlays do not use placeholder key", func(t *testing.T) {
		t.Setenv("ENDPOINTS_MANIFEST_ENV", "cloudrun")
		t.Setenv("INTERNAL_SERVICE_KEY", "")

		assert.Empty(t, ResolveInternalServiceKey())
	})
}

func TestManifestCanBeRead(t *testing.T) {
	require.NotZero(t, len(Manifest.Services))
	require.NotZero(t, len(Manifest.Overlays))
	require.NotZero(t, len(Manifest.GRPCTargets))
}

func TestManifestExcludesPrivateIntegrationRoutes(t *testing.T) {
	assert.NotContains(t, Manifest.HTTPRoutes["go_orchestrator"], "/internal/wisdev-bridge/*")
	assert.Len(t, Manifest.Services, 2)
	assert.NotContains(t, Manifest.HTTPRoutes["python_sidecar"], "/integrations/private-connector/*")
}

func TestResolveEnvWithFallbackWhenUnset(t *testing.T) {
	t.Setenv("SOME_MISSING_ENV", "")
	assert.Equal(t, "fallback", ResolveEnvWithFallback("SOME_MISSING_ENV", "fallback"))
}

func TestCurrentOverlayReturnsOverlay(t *testing.T) {
	t.Setenv("ENDPOINTS_MANIFEST_ENV", "docker")
	assert.Equal(t, Manifest.Overlays["docker"], CurrentOverlay())
}

func TestCurrentOverlayReturnsEmptyWhenMissing(t *testing.T) {
	originalOverlays := Manifest.Overlays
	overlays := make(map[string]ManifestOverlay, len(originalOverlays))
	for key, value := range originalOverlays {
		overlays[key] = value
	}
	delete(overlays, "local")
	Manifest.Overlays = overlays
	t.Cleanup(func() {
		Manifest.Overlays = originalOverlays
	})

	t.Setenv("ENDPOINTS_MANIFEST_ENV", "local")
	assert.Equal(t, ManifestOverlay{}, CurrentOverlay())
}

func TestResolveEnvUsesEnvWhenAvailable(t *testing.T) {
	t.Setenv("RESOLVE_ENV_WITH_FALLBACK", "from-env")
	assert.Equal(t, "from-env", ResolveEnvWithFallback("RESOLVE_ENV_WITH_FALLBACK", "fallback"))
}

func TestResolveBaseURLDefaultsToServiceBase(t *testing.T) {
	originalLocal := Manifest.Overlays["local"]
	t.Cleanup(func() {
		Manifest.Overlays["local"] = originalLocal
	})

	Manifest.Overlays["local"] = ManifestOverlay{
		Env:             map[string]string{},
		ServiceBaseURLs: map[string]string{},
	}
	t.Setenv("ENDPOINTS_MANIFEST_ENV", "local")

	assert.Equal(t, "http://127.0.0.1:8081", ResolveBaseURL("go_orchestrator"))
}

func TestMustParseManifestValidation(t *testing.T) {
	assert.PanicsWithValue(t, "failed to parse generated endpoints manifest: unexpected end of JSON input", func() {
		mustParseManifest(`{"version":3`)
	})

	assert.PanicsWithValue(t, "unsupported endpoints manifest version: got 2 want 4", func() {
		mustParseManifest(`{"version":2}`)
	})
}

func TestResolveGRPCTargetDefaultsWithoutEnvVar(t *testing.T) {
	original, ok := Manifest.GRPCTargets["go_internal"]
	require.True(t, ok)

	updated := original
	updated.EnvVar = ""
	Manifest.GRPCTargets["go_internal"] = updated
	t.Cleanup(func() {
		Manifest.GRPCTargets["go_internal"] = original
	})

	assert.Equal(t, "127.0.0.1:50053", ResolveGRPCTarget("go_internal"))
}

func TestStackConfigSanity(t *testing.T) {
	oldOverlay := Manifest.Overlays["local"]
	t.Cleanup(func() {
		Manifest.Overlays["local"] = oldOverlay
	})

	overlay := ManifestOverlay{
		Env:             map[string]string{},
		ServiceBaseURLs: map[string]string{},
	}
	for k, v := range oldOverlay.Env {
		overlay.Env[k] = v
	}
	for k, v := range oldOverlay.ServiceBaseURLs {
		overlay.ServiceBaseURLs[k] = v
	}
	overlay.Env["PYTHON_SIDECAR_HTTP_URL"] = ""
	Manifest.Overlays["local"] = overlay

	t.Setenv("INTERNAL_SERVICE_KEY", "")
	err := ValidateService("go_orchestrator")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PYTHON_SIDECAR_HTTP_URL")
	Manifest.Overlays["local"] = oldOverlay
	assert.Equal(t, "dev-internal-key", ResolveEnv("INTERNAL_SERVICE_KEY"))
}
