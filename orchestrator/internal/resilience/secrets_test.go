package resilience

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/googleapis/gax-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/api/option"
)

type mockSecretClient struct {
	mock.Mock
}

var (
	originalLookupGCloudProjectID           = lookupGCloudProjectID
	originalFindDefaultCredentialsProjectID = findDefaultCredentialsProjectID
	originalNewSecretManagerClient          = newSecretManagerClient
)

func (m *mockSecretClient) AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*secretmanagerpb.AccessSecretVersionResponse), args.Error(1)
}

func (m *mockSecretClient) Close() error {
	return m.Called().Error(0)
}

func TestGetSecret(t *testing.T) {
	is := assert.New(t)
	t.Cleanup(func() {
		lookupGCloudProjectID = originalLookupGCloudProjectID
		findDefaultCredentialsProjectID = originalFindDefaultCredentialsProjectID
	})

	// Helper to reset state
	reset := func() {
		cacheMu.Lock()
		secretCache = make(map[string]cachedSecret)
		cacheMu.Unlock()

		smMu.Lock()
		smClient = nil
		smMu.Unlock()

		lookupGCloudProjectID = func() (string, error) { return "", nil }
		findDefaultCredentialsProjectID = func(ctx context.Context) (string, error) { return "", nil }

		os.Unsetenv("TEST_SECRET")
		os.Unsetenv("GOOGLE_CLOUD_PROJECT")
		os.Unsetenv("GCLOUD_PROJECT")
		os.Unsetenv("GCP_PROJECT_ID")
		os.Unsetenv("CLOUDSDK_CORE_PROJECT")
	}

	t.Run("fetch from environment variable", func(t *testing.T) {
		reset()
		t.Setenv("TEST_SECRET", "env-value")

		val, err := GetSecret(context.Background(), "TEST_SECRET")
		is.NoError(err)
		is.Equal("env-value", val)
	})

	t.Run("fetch from cache", func(t *testing.T) {
		reset()
		t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
		cacheMu.Lock()
		secretCache[secretCacheKey("test-project", "CACHED_SECRET")] = cachedSecret{
			value:     "cached-value",
			expiresAt: time.Now().Add(secretTTL),
		}
		cacheMu.Unlock()

		val, err := GetSecret(context.Background(), "CACHED_SECRET")
		is.NoError(err)
		is.Equal("cached-value", val)
	})

	t.Run("fetch from secret manager and cache it", func(t *testing.T) {
		reset()
		t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
		m := new(mockSecretClient)
		smClient = m

		expectedValue := "sm-value"
		m.On("AccessSecretVersion", mock.Anything, mock.Anything).Return(&secretmanagerpb.AccessSecretVersionResponse{
			Payload: &secretmanagerpb.SecretPayload{
				Data: []byte(expectedValue),
			},
		}, nil)

		val, err := GetSecret(context.Background(), "SM_SECRET")
		is.NoError(err)
		is.Equal(expectedValue, val)

		// Verify it was cached
		cacheMu.RLock()
		cachedEntry := secretCache[secretCacheKey("test-project", "SM_SECRET")]
		cacheMu.RUnlock()
		is.Equal(expectedValue, cachedEntry.value)
		is.True(cachedEntry.expiresAt.After(time.Now()))

		m.AssertExpectations(t)
	})

	t.Run("fetch from secret manager using gcloud project fallback", func(t *testing.T) {
		reset()
		t.Setenv("GCLOUD_PROJECT", "fallback-project")
		m := new(mockSecretClient)
		smClient = m

		m.On("AccessSecretVersion", mock.Anything, mock.Anything).Return(&secretmanagerpb.AccessSecretVersionResponse{
			Payload: &secretmanagerpb.SecretPayload{
				Data: []byte("fallback-value"),
			},
		}, nil)

		val, err := GetSecret(context.Background(), "FALLBACK_SECRET")
		is.NoError(err)
		is.Equal("fallback-value", val)
		m.AssertExpectations(t)
	})

	t.Run("handle secret manager error", func(t *testing.T) {
		reset()
		t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
		m := new(mockSecretClient)
		smClient = m

		m.On("AccessSecretVersion", mock.Anything, mock.Anything).Return(nil, errors.New("sm-error"))

		val, err := GetSecret(context.Background(), "FAIL_SECRET")
		is.Error(err)
		is.Empty(val)
		is.Contains(err.Error(), "failed to access secret")

		m.AssertExpectations(t)
	})

	t.Run("retries transient secret manager bootstrap failures", func(t *testing.T) {
		reset()
		t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
		m := new(mockSecretClient)
		smClient = m

		m.On("AccessSecretVersion", mock.Anything, mock.Anything).Return(nil, errors.New("temporary IAM unavailable")).Once()
		m.On("AccessSecretVersion", mock.Anything, mock.Anything).Return(&secretmanagerpb.AccessSecretVersionResponse{
			Payload: &secretmanagerpb.SecretPayload{Data: []byte("eventual-value")},
		}, nil).Once()

		val, err := GetSecret(context.Background(), "RETRY_SECRET")
		is.NoError(err)
		is.Equal("eventual-value", val)
		m.AssertExpectations(t)
	})

	t.Run("missing project id", func(t *testing.T) {
		reset()
		val, err := GetSecret(context.Background(), "SOME_SECRET")
		is.Error(err)
		is.Empty(val)
		is.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT is not set")
	})
}

func TestResolveGoogleCloudProjectID(t *testing.T) {
	t.Run("prefers google cloud project", func(t *testing.T) {
		t.Setenv("GOOGLE_CLOUD_PROJECT", "primary")
		t.Setenv("GCLOUD_PROJECT", "secondary")
		t.Setenv("GCP_PROJECT_ID", "tertiary")
		assert.Equal(t, "primary", ResolveGoogleCloudProjectID())
	})

	t.Run("falls back through supported env vars", func(t *testing.T) {
		t.Setenv("GOOGLE_CLOUD_PROJECT", "")
		t.Setenv("GCLOUD_PROJECT", "secondary")
		t.Setenv("GCP_PROJECT_ID", "tertiary")
		assert.Equal(t, "secondary", ResolveGoogleCloudProjectID())
	})

	t.Run("uses cloudsdk core project", func(t *testing.T) {
		t.Setenv("GOOGLE_CLOUD_PROJECT", "")
		t.Setenv("GCLOUD_PROJECT", "")
		t.Setenv("GCP_PROJECT_ID", "")
		t.Setenv("CLOUDSDK_CORE_PROJECT", "cloudsdk-project")
		assert.Equal(t, "cloudsdk-project", ResolveGoogleCloudProjectID())
	})

	t.Run("uses gcloud config when env is missing", func(t *testing.T) {
		oldLookup := lookupGCloudProjectID
		oldADC := findDefaultCredentialsProjectID
		t.Cleanup(func() {
			lookupGCloudProjectID = oldLookup
			findDefaultCredentialsProjectID = oldADC
		})

		lookupGCloudProjectID = func() (string, error) { return "gcloud-project", nil }
		findDefaultCredentialsProjectID = func(ctx context.Context) (string, error) {
			t.Fatalf("unexpected ADC lookup")
			return "", nil
		}

		t.Setenv("GOOGLE_CLOUD_PROJECT", "")
		t.Setenv("GCLOUD_PROJECT", "")
		t.Setenv("GCP_PROJECT_ID", "")
		t.Setenv("CLOUDSDK_CORE_PROJECT", "")

		projectID, source := ResolveGoogleCloudProjectIDWithSource(context.Background())
		assert.Equal(t, "gcloud-project", projectID)
		assert.Equal(t, "gcloud_config", source)
	})

	t.Run("uses adc when env and gcloud config are missing", func(t *testing.T) {
		oldLookup := lookupGCloudProjectID
		oldADC := findDefaultCredentialsProjectID
		t.Cleanup(func() {
			lookupGCloudProjectID = oldLookup
			findDefaultCredentialsProjectID = oldADC
		})

		lookupGCloudProjectID = func() (string, error) { return "", nil }
		findDefaultCredentialsProjectID = func(ctx context.Context) (string, error) { return "adc-project", nil }

		t.Setenv("GOOGLE_CLOUD_PROJECT", "")
		t.Setenv("GCLOUD_PROJECT", "")
		t.Setenv("GCP_PROJECT_ID", "")
		t.Setenv("CLOUDSDK_CORE_PROJECT", "")

		projectID, source := ResolveGoogleCloudProjectIDWithSource(context.Background())
		assert.Equal(t, "adc-project", projectID)
		assert.Equal(t, "adc", source)
	})

	t.Run("MockProjectIDForTest", func(t *testing.T) {
		restore := MockProjectIDForTest("mocked-project")
		defer restore()

		projectID := ResolveGoogleCloudProjectID()
		assert.Equal(t, "mocked-project", projectID)
	})
}

func TestInvalidateSecret(t *testing.T) {
	cacheMu.Lock()
	secretCache["TO_INVALIDATE"] = cachedSecret{value: "v", expiresAt: time.Now().Add(time.Hour)}
	cacheMu.Unlock()

	InvalidateSecret("TO_INVALIDATE")

	cacheMu.RLock()
	_, ok := secretCache["TO_INVALIDATE"]
	cacheMu.RUnlock()
	assert.False(t, ok)
}

func TestSecretCacheKey(t *testing.T) {
	assert.Equal(t, "name", secretCacheKey("", "name"))
	assert.Equal(t, "project:name", secretCacheKey(" project ", "name"))
}

func TestLookupGCloudProjectID(t *testing.T) {
	lookupGCloudProjectID = originalLookupGCloudProjectID
	findDefaultCredentialsProjectID = func(context.Context) (string, error) { return "", nil }
	t.Cleanup(func() {
		lookupGCloudProjectID = originalLookupGCloudProjectID
		findDefaultCredentialsProjectID = originalFindDefaultCredentialsProjectID
	})

	dir := t.TempDir()
	configDir := filepath.Join(dir, "configurations")
	assert.NoError(t, os.MkdirAll(configDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "active_config"), []byte("custom\n"), 0o600))
	assert.NoError(t, os.WriteFile(filepath.Join(configDir, "config_custom"), []byte(`
[core]
project = parsed-project
`), 0o600))

	t.Setenv("CLOUDSDK_CONFIG", dir)
	projectID, err := lookupGCloudProjectID()
	assert.NoError(t, err)
	assert.Equal(t, "parsed-project", projectID)
}

func TestLookupGCloudProjectIDAppDataFallback(t *testing.T) {
	oldUserConfigDirFn := userConfigDirFn
	t.Cleanup(func() {
		userConfigDirFn = oldUserConfigDirFn
	})

	dir := t.TempDir()
	configDir := filepath.Join(dir, "gcloud", "configurations")
	assert.NoError(t, os.MkdirAll(configDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "gcloud", "configurations", "config_default"), []byte(`
[core]
project = appdata-project
`), 0o600))

	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("APPDATA", dir)
	userConfigDirFn = func() (string, error) {
		t.Fatalf("unexpected user config dir lookup")
		return "", nil
	}

	projectID, err := lookupGCloudProjectID()
	assert.NoError(t, err)
	assert.Equal(t, "appdata-project", projectID)
}

func TestLookupGCloudProjectIDUserConfigDirAndScannerError(t *testing.T) {
	oldUserConfigDirFn := userConfigDirFn
	t.Cleanup(func() {
		userConfigDirFn = oldUserConfigDirFn
	})

	dir := t.TempDir()
	configDir := filepath.Join(dir, "gcloud", "configurations")
	assert.NoError(t, os.MkdirAll(configDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "gcloud", "active_config"), []byte("default\n"), 0o600))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "gcloud", "configurations", "config_default"), []byte(`
[other]
noop = value
[core]
not-a-pair
project = user-config-project
`), 0o600))

	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("APPDATA", "")
	userConfigDirFn = func() (string, error) {
		return dir, nil
	}

	projectID, err := lookupGCloudProjectID()
	assert.NoError(t, err)
	assert.Equal(t, "user-config-project", projectID)

	assert.NoError(t, os.WriteFile(filepath.Join(dir, "gcloud", "configurations", "config_default"), []byte(
		"[core]\n"+strings.Repeat("a", 70000),
	), 0o600))

	_, err = lookupGCloudProjectID()
	assert.Error(t, err)
}

func TestLookupGCloudProjectIDMissingConfigFile(t *testing.T) {
	lookupGCloudProjectID = originalLookupGCloudProjectID
	oldUserConfigDirFn := userConfigDirFn
	t.Cleanup(func() {
		userConfigDirFn = oldUserConfigDirFn
	})

	dir := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("APPDATA", "")
	userConfigDirFn = func() (string, error) {
		return dir, nil
	}

	projectID, err := lookupGCloudProjectID()
	assert.Error(t, err)
	assert.Empty(t, projectID)
}

func TestLookupGCloudProjectIDNoProjectInCoreSection(t *testing.T) {
	lookupGCloudProjectID = originalLookupGCloudProjectID
	oldUserConfigDirFn := userConfigDirFn
	t.Cleanup(func() {
		userConfigDirFn = oldUserConfigDirFn
	})

	dir := t.TempDir()
	configDir := filepath.Join(dir, "gcloud", "configurations")
	assert.NoError(t, os.MkdirAll(configDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "gcloud", "active_config"), []byte("default\n"), 0o600))
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "gcloud", "configurations", "config_default"), []byte(`
[core]
not-project = nope
`), 0o600))

	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("APPDATA", "")
	userConfigDirFn = func() (string, error) {
		return dir, nil
	}

	projectID, err := lookupGCloudProjectID()
	assert.NoError(t, err)
	assert.Empty(t, projectID)
}

func TestFindDefaultCredentialsProjectID(t *testing.T) {
	findDefaultCredentialsProjectID = originalFindDefaultCredentialsProjectID
	oldFindDefaultCredentialsProjectID := findDefaultCredentialsProjectID
	t.Cleanup(func() {
		findDefaultCredentialsProjectID = oldFindDefaultCredentialsProjectID
	})

	key, err := rsa.GenerateKey(rand.Reader, 1024)
	assert.NoError(t, err)

	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	creds := map[string]string{
		"type":                        "service_account",
		"project_id":                  "adc-project",
		"private_key_id":              "test-key-id",
		"private_key":                 string(pemKey),
		"client_email":                "adc@test-project.iam.gserviceaccount.com",
		"client_id":                   "1234567890",
		"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
		"token_uri":                   "https://oauth2.googleapis.com/token",
		"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
		"client_x509_cert_url":        "https://www.googleapis.com/robot/v1/metadata/x509/adc%40test-project.iam.gserviceaccount.com",
	}
	data, err := json.Marshal(creds)
	assert.NoError(t, err)

	dir := t.TempDir()
	credPath := filepath.Join(dir, "adc.json")
	assert.NoError(t, os.WriteFile(credPath, data, 0o600))

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credPath)
	projectID, err := findDefaultCredentialsProjectID(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "adc-project", projectID)
}

func TestCloseSecretManagerClient(t *testing.T) {
	m := new(mockSecretClient)
	m.On("Close").Return(nil)
	smClient = m

	err := CloseSecretManagerClient()
	assert.NoError(t, err)
	assert.Nil(t, smClient)
	m.AssertExpectations(t)
}

func TestGetSMClient(t *testing.T) {
	t.Cleanup(func() {
		newSecretManagerClient = originalNewSecretManagerClient
		smClient = nil
	})

	t.Run("returns existing client", func(t *testing.T) {
		existing := new(mockSecretClient)
		smClient = existing

		got, err := getSMClient(context.Background())
		assert.NoError(t, err)
		assert.Same(t, existing, got)
	})

	t.Run("creates a new client when missing", func(t *testing.T) {
		smClient = nil
		expected := new(mockSecretClient)
		newSecretManagerClient = func(ctx context.Context, opts ...option.ClientOption) (secretClient, error) {
			return expected, nil
		}

		got, err := getSMClient(context.Background())
		assert.NoError(t, err)
		assert.Same(t, expected, got)
	})

	t.Run("returns factory errors", func(t *testing.T) {
		smClient = nil
		newSecretManagerClient = func(ctx context.Context, opts ...option.ClientOption) (secretClient, error) {
			return nil, errors.New("boom")
		}

		got, err := getSMClient(context.Background())
		assert.Error(t, err)
		assert.Nil(t, got)
	})
}

func TestGetSecretFromManagerClientInitError(t *testing.T) {
	oldLookup := lookupGCloudProjectID
	oldADC := findDefaultCredentialsProjectID
	oldFactory := newSecretManagerClient
	t.Cleanup(func() {
		lookupGCloudProjectID = oldLookup
		findDefaultCredentialsProjectID = oldADC
		newSecretManagerClient = oldFactory
		smClient = nil
		cacheMu.Lock()
		secretCache = make(map[string]cachedSecret)
		cacheMu.Unlock()
	})

	lookupGCloudProjectID = func() (string, error) { return "", nil }
	findDefaultCredentialsProjectID = func(context.Context) (string, error) { return "", nil }
	newSecretManagerClient = func(context.Context, ...option.ClientOption) (secretClient, error) {
		return nil, errors.New("client boom")
	}

	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")

	val, err := GetSecretFromManager(context.Background(), "BROKEN_SECRET")
	assert.Error(t, err)
	assert.Empty(t, val)
	assert.Contains(t, err.Error(), "failed to create secret manager client")
}
