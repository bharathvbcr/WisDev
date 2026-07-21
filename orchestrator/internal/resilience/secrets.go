package resilience

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/googleapis/gax-go/v2"
)

// secretTTL is how long a fetched secret is trusted before re-fetching.
// This ensures rotated secrets take effect within this window.
const secretTTL = 5 * time.Minute

type secretClient interface {
	AccessSecretVersion(context.Context, *secretmanagerpb.AccessSecretVersionRequest, ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error)
	Close() error
}

type cachedSecret struct {
	value     string
	expiresAt time.Time
}

var (
	secretCache = make(map[string]cachedSecret)
	cacheMu     sync.RWMutex
	smClient    secretClient
	smMu        sync.Mutex
)

var newSecretManagerClient = func(ctx context.Context, opts ...option.ClientOption) (secretClient, error) {
	return secretmanager.NewClient(ctx, opts...)
}

var projectEnvKeys = [...]string{
	"GOOGLE_CLOUD_PROJECT",
	"GCLOUD_PROJECT",
	"GCP_PROJECT_ID",
	"CLOUDSDK_CORE_PROJECT",
}

var findDefaultCredentialsProjectID = func(ctx context.Context) (string, error) {
	creds, err := google.FindDefaultCredentials(ctx, secretmanager.DefaultAuthScopes()...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(creds.ProjectID), nil
}

var lookupGCloudProjectID = func() (string, error) {
	configDir := strings.TrimSpace(os.Getenv("CLOUDSDK_CONFIG"))
	if configDir == "" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			configDir = filepath.Join(appData, "gcloud")
		}
	}
	if configDir == "" {
		userConfigDir, err := userConfigDirFn()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(userConfigDir, "gcloud")
	}

	activeConfig := "default"
	if data, err := os.ReadFile(filepath.Join(configDir, "active_config")); err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			activeConfig = name
		}
	}

	configPath := filepath.Join(configDir, "configurations", "config_"+activeConfig)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	inCoreSection := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inCoreSection = strings.EqualFold(strings.TrimSpace(line[1:len(line)-1]), "core")
			continue
		}
		if !inCoreSection {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "project") {
			projectID := strings.TrimSpace(value)
			if projectID != "" {
				return projectID, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}

var userConfigDirFn = os.UserConfigDir

// MockProjectIDForTest allows tests to override the GCP project ID resolution.
func MockProjectIDForTest(mockID string) func() {
	origADC := findDefaultCredentialsProjectID
	origGCloud := lookupGCloudProjectID
	findDefaultCredentialsProjectID = func(ctx context.Context) (string, error) {
		return mockID, nil
	}
	lookupGCloudProjectID = func() (string, error) {
		return mockID, nil
	}
	return func() {
		findDefaultCredentialsProjectID = origADC
		lookupGCloudProjectID = origGCloud
	}
}

func secretCacheKey(projectID, name string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return name
	}
	return projectID + ":" + name
}

// ResolveGoogleCloudProjectID returns the first configured GCP project ID from
// the supported local/runtime environment variables.
func ResolveGoogleCloudProjectID() string {
	projectID, _ := ResolveGoogleCloudProjectIDWithSource(context.Background())
	return projectID
}

// ResolveGoogleCloudProjectIDWithSource returns the first configured GCP
// project ID and the source that supplied it.
func ResolveGoogleCloudProjectIDWithSource(ctx context.Context) (string, string) {
	for _, key := range projectEnvKeys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value, "env:" + key
		}
	}

	if projectID, err := lookupGCloudProjectID(); err == nil && strings.TrimSpace(projectID) != "" {
		return strings.TrimSpace(projectID), "gcloud_config"
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if projectID, err := findDefaultCredentialsProjectID(ctx); err == nil && strings.TrimSpace(projectID) != "" {
		return strings.TrimSpace(projectID), "adc"
	}

	return "", "none"
}

// GetSecret fetches a secret from env vars first, then GCP Secret Manager.
// Results are cached for secretTTL to support secret rotation.
func GetSecret(ctx context.Context, name string) (string, error) {
	// 1. Check environment variable first (local dev / Cloud Run injected secrets)
	if val := strings.TrimSpace(os.Getenv(name)); val != "" {
		return val, nil
	}

	return GetSecretFromManager(ctx, name)
}

// GetSecretFromManager fetches a secret from GCP Secret Manager only.
// Results are cached per project to avoid cross-project contamination in a
// long-lived process.
func GetSecretFromManager(ctx context.Context, name string) (string, error) {
	projectID, projectSource := ResolveGoogleCloudProjectIDWithSource(ctx)
	if projectID == "" {
		err := fmt.Errorf("GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT is not set; cannot fetch secret %q from Secret Manager", name)
		slog.Warn("secret manager project id missing", "secret_name", name, "project_source", projectSource, "error", err)
		return "", err
	}

	cacheKey := secretCacheKey(projectID, name)
	cacheMu.RLock()
	entry, ok := secretCache[cacheKey]
	cacheMu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.value, nil
	}

	client, err := getSMClient(ctx)
	if err != nil {
		slog.Error("secret manager client initialization failed", "secret_name", name, "project_id", projectID, "project_source", projectSource, "error", err)
		return "", err
	}

	secretPath := fmt.Sprintf("projects/%s/secrets/%s/versions/latest", projectID, name)
	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: secretPath,
	}

	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		var lastErr error
		for attempt := 2; attempt <= 3; attempt++ {
			lastErr = err
			delay := time.Duration(attempt-1) * 100 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				slog.Error("secret manager access canceled during retry", "secret_name", name, "project_id", projectID, "project_source", projectSource, "attempt", attempt, "error", ctx.Err())
				return "", fmt.Errorf("failed to access secret %s: %w", name, ctx.Err())
			case <-timer.C:
			}
			slog.Warn("secret manager access retrying", "secret_name", name, "project_id", projectID, "project_source", projectSource, "attempt", attempt, "error", err)
			result, err = client.AccessSecretVersion(ctx, req)
			if err == nil {
				lastErr = nil
				break
			}
		}
		if err != nil {
			if lastErr == nil {
				lastErr = err
			}
			slog.Error("secret manager access failed", "secret_name", name, "project_id", projectID, "project_source", projectSource, "error", lastErr)
			return "", fmt.Errorf("failed to access secret %s: %w", name, lastErr)
		}
	}

	secretVal := strings.TrimSpace(string(result.Payload.Data))

	cacheMu.Lock()
	secretCache[cacheKey] = cachedSecret{
		value:     secretVal,
		expiresAt: time.Now().Add(secretTTL),
	}
	cacheMu.Unlock()

	return secretVal, nil
}

// InvalidateSecret evicts a single entry from the cache, forcing a fresh fetch
// on the next call. Use this after a manual secret rotation.
func InvalidateSecret(name string) {
	cacheMu.Lock()
	for cacheKey := range secretCache {
		if cacheKey == name || strings.HasSuffix(cacheKey, ":"+name) {
			delete(secretCache, cacheKey)
		}
	}
	cacheMu.Unlock()
}

func getSMClient(ctx context.Context) (secretClient, error) {
	smMu.Lock()
	defer smMu.Unlock()

	if smClient != nil {
		return smClient, nil
	}

	client, err := newSecretManagerClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create secret manager client: %w", err)
	}

	smClient = client
	return smClient, nil
}

// CloseSecretManagerClient closes the shared Secret Manager gRPC connection.
// Should be called during process shutdown.
func CloseSecretManagerClient() error {
	smMu.Lock()
	defer smMu.Unlock()
	if smClient == nil {
		return nil
	}
	err := smClient.Close()
	smClient = nil
	return err
}
