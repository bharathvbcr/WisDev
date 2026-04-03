package resilience

import (
	"context"
	"os"
	"testing"
)

func TestGetSecretFromEnv(t *testing.T) {
	os.Setenv("TEST_SECRET", "secret-value")
	defer os.Unsetenv("TEST_SECRET")

	val, err := GetSecret(context.Background(), "TEST_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if val != "secret-value" {
		t.Errorf("expected secret-value, got %s", val)
	}
}

func TestGetSecretNotSet(t *testing.T) {
	val, err := GetSecret(context.Background(), "NONEXISTENT_SECRET_12345")
	if err != nil {
		t.Fatal(err)
	}
	if val != "" {
		t.Errorf("expected empty string, got %s", val)
	}
}

func TestSetSecret(t *testing.T) {
	defer ClearSecrets()

	SetSecret("cached_secret", "cached-value")
	val, err := GetSecret(context.Background(), "cached_secret")
	if err != nil {
		t.Fatal(err)
	}
	if val != "cached-value" {
		t.Errorf("expected cached-value, got %s", val)
	}
}

func TestEnvTakesPrecedenceOverCache(t *testing.T) {
	defer ClearSecrets()

	os.Setenv("OVERRIDE_SECRET", "env-value")
	defer os.Unsetenv("OVERRIDE_SECRET")

	SetSecret("OVERRIDE_SECRET", "cache-value")
	val, err := GetSecret(context.Background(), "OVERRIDE_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if val != "env-value" {
		t.Errorf("expected env-value (env takes precedence), got %s", val)
	}
}

func TestClearSecrets(t *testing.T) {
	SetSecret("WISDEV_TEST_CLEAR_ME", "value")
	ClearSecrets()

	val, _ := GetSecret(context.Background(), "WISDEV_TEST_CLEAR_ME")
	if val != "" {
		t.Error("expected empty after ClearSecrets")
	}
}

func TestIsDegraded(t *testing.T) {
	ctx := context.Background()
	if IsDegraded(ctx) {
		t.Error("expected not degraded by default")
	}

	ctx2 := SetDegraded(ctx, true)
	if !IsDegraded(ctx2) {
		t.Error("expected degraded after SetDegraded(true)")
	}

	ctx3 := SetDegraded(ctx, false)
	if IsDegraded(ctx3) {
		t.Error("expected not degraded after SetDegraded(false)")
	}
}
