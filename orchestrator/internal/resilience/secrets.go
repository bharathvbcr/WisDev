package resilience

import (
	"context"
	"os"
	"sync"
)

var (
	secretCache = make(map[string]string)
	cacheMu     sync.RWMutex
)

// GetSecret returns a secret from environment variables.
// In the open-source version, all secrets must be provided via environment
// variables or the wisdev.yaml config file.
func GetSecret(ctx context.Context, name string) (string, error) {
	if val := os.Getenv(name); val != "" {
		return val, nil
	}

	cacheMu.RLock()
	val, ok := secretCache[name]
	cacheMu.RUnlock()
	if ok {
		return val, nil
	}

	return "", nil
}

// SetSecret caches a secret in memory for the lifetime of the process.
func SetSecret(name, value string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	secretCache[name] = value
}

// ClearSecrets removes all cached secrets (useful for testing).
func ClearSecrets() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	secretCache = make(map[string]string)
}
