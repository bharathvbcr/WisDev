package stackconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const supportedManifestVersion = 4

type StackManifest struct {
	Version              int                           `json:"version"`
	Environment          string                        `json:"environment"`
	CanonicalRequestFlow []string                      `json:"canonicalRequestFlow"`
	Ports                map[string]int                `json:"ports"`
	Services             map[string]ManifestService    `json:"services"`
	Dependencies         []ManifestDependency          `json:"dependencies"`
	HTTPRoutes           map[string][]string           `json:"httpRoutes"`
	GRPCTargets          map[string]ManifestGRPCTarget `json:"grpcTargets"`
	AuthMode             map[string]string             `json:"authMode"`
	RequiredEnv          []string                      `json:"requiredEnv"`
	CanonicalEnvVars     []string                      `json:"canonicalEnvVars"`
	Overlays             map[string]ManifestOverlay    `json:"overlays"`
}

type ManifestService struct {
	DisplayName    string         `json:"displayName"`
	Path           string         `json:"path"`
	Entrypoint     string         `json:"entrypoint"`
	Transport      string         `json:"transport"`
	DefaultBaseURL string         `json:"defaultBaseUrl"`
	HealthRoutes   []string       `json:"healthRoutes"`
	RequiredEnv    []string       `json:"requiredEnv"`
	ListenPorts    map[string]int `json:"listenPorts"`
}

type ManifestDependency struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Transport   string `json:"transport"`
	Description string `json:"description"`
}

type ManifestGRPCTarget struct {
	EnvVar         string `json:"envVar"`
	Transport      string `json:"transport"`
	Source         string `json:"source"`
	Target         string `json:"target"`
	DefaultAddress string `json:"defaultAddress"`
}

type ManifestOverlay struct {
	Env             map[string]string `json:"env"`
	ServiceBaseURLs map[string]string `json:"serviceBaseUrls"`
}

func mustParseManifest(raw string) StackManifest {
	var manifest StackManifest
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		panic(fmt.Sprintf("failed to parse generated endpoints manifest: %v", err))
	}
	if manifest.Version != supportedManifestVersion {
		panic(fmt.Sprintf("unsupported endpoints manifest version: got %d want %d", manifest.Version, supportedManifestVersion))
	}
	return manifest
}

func CurrentOverlayName() string {
	raw := strings.TrimSpace(os.Getenv("ENDPOINTS_MANIFEST_ENV"))
	if raw == "" {
		return "local"
	}
	if _, ok := Manifest.Overlays[raw]; ok {
		return raw
	}
	return "local"
}

func CurrentOverlay() ManifestOverlay {
	overlay, ok := Manifest.Overlays[CurrentOverlayName()]
	if !ok {
		return ManifestOverlay{}
	}
	return overlay
}

func ResolveEnv(key string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return strings.TrimSpace(CurrentOverlay().Env[key])
}

func ResolveEnvWithFallback(key, fallback string) string {
	if resolved := ResolveEnv(key); resolved != "" {
		return resolved
	}
	return fallback
}

func ResolveInternalServiceKey() string {
	if key := strings.TrimSpace(os.Getenv("INTERNAL_SERVICE_KEY")); key != "" {
		return key
	}
	switch CurrentOverlayName() {
	case "local", "docker":
		return strings.TrimSpace(CurrentOverlay().Env["INTERNAL_SERVICE_KEY"])
	default:
		return ""
	}
}

func ResolveBaseURL(serviceID string) string {
	if overlayURL := strings.TrimRight(CurrentOverlay().ServiceBaseURLs[serviceID], "/"); overlayURL != "" {
		return overlayURL
	}
	if service, ok := Manifest.Services[serviceID]; ok {
		return strings.TrimRight(service.DefaultBaseURL, "/")
	}
	return ""
}

func ResolveGRPCTarget(targetID string) string {
	target, ok := Manifest.GRPCTargets[targetID]
	if !ok {
		return ""
	}
	if target.EnvVar != "" {
		if resolved := ResolveEnv(target.EnvVar); resolved != "" {
			return resolved
		}
	}
	return strings.TrimSpace(target.DefaultAddress)
}

func resolveConfiguredPort(raw string, preferred int) int {
	if IsAutoPortSpec(raw) && AutoPortEnabled() {
		if picked, err := PickListenPort(preferred); err == nil {
			return picked
		}
	}
	if parsed, err := strconv.Atoi(raw); err == nil {
		if AutoPortEnabled() && !portAvailable(parsed) {
			if picked, pickErr := PickListenPort(preferred); pickErr == nil {
				return picked
			}
		}
		return parsed
	}
	return preferred
}

func ResolveListenPort(serviceID, portName string) int {
	service, ok := Manifest.Services[serviceID]
	if !ok {
		return 0
	}
	if portName == "http" {
		raw := strings.TrimSpace(os.Getenv("PORT"))
		if raw == "" && AutoPortEnabled() {
			if picked, err := PickListenPort(service.ListenPorts["http"]); err == nil {
				return picked
			}
		}
		if raw != "" {
			return resolveConfiguredPort(raw, service.ListenPorts["http"])
		}
	}
	if portName == "metrics" {
		raw := strings.TrimSpace(os.Getenv("INTERNAL_METRICS_PORT"))
		if raw == "" && AutoPortEnabled() {
			if picked, err := PickListenPort(service.ListenPorts["metrics"]); err == nil {
				return picked
			}
		}
		if raw != "" {
			return resolveConfiguredPort(raw, service.ListenPorts["metrics"])
		}
	}
	if portName == "grpc" && serviceID == "go_orchestrator" {
		if raw := ResolveEnv("GO_INTERNAL_GRPC_ADDR"); raw != "" {
			if _, portRaw, ok := strings.Cut(raw, ":"); ok {
				preferred := service.ListenPorts["grpc"]
				if parsed, err := strconv.Atoi(portRaw); err == nil {
					if AutoPortEnabled() && !portAvailable(parsed) {
						if picked, pickErr := PickListenPort(preferred); pickErr == nil {
							return picked
						}
					}
					return parsed
				}
			}
		}
	}
	return service.ListenPorts[portName]
}

func ValidateService(serviceID string) error {
	service, ok := Manifest.Services[serviceID]
	if !ok {
		return fmt.Errorf("unknown service id %q", serviceID)
	}
	for _, envKey := range service.RequiredEnv {
		if strings.TrimSpace(ResolveEnv(envKey)) == "" {
			return fmt.Errorf("missing required manifest env %s for %s", envKey, serviceID)
		}
	}
	return nil
}
