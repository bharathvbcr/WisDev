package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/redis/go-redis/v9"
	temporalclient "go.temporal.io/sdk/client"
)

func TestRun(t *testing.T) {
	// Existing behavior path: gateway disabled, no external stores.
	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := Run(ctx, "test-version")
	assert.NoError(t, err)
}

func TestRun_WarmsUpLLMClientBeforeServing(t *testing.T) {
	originalWarmUpLLMClientFn := warmUpLLMClientFn
	t.Cleanup(func() {
		warmUpLLMClientFn = originalWarmUpLLMClientFn
	})

	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "")

	warmed := false
	warmUpLLMClientFn = func(ctx context.Context, client *llm.Client) error {
		require.NotNil(t, client)
		require.NotNil(t, ctx)
		warmed = true
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := Run(ctx, "warmup")
	require.NoError(t, err)
	assert.True(t, warmed)
}

func TestRun_ValidationFailure(t *testing.T) {
	original := stackconfig.Manifest.Services["go_orchestrator"]
	replaced := original
	replaced.RequiredEnv = append([]string{}, original.RequiredEnv...)
	replaced.RequiredEnv = append(replaced.RequiredEnv, "MISSING_MANIFEST_ENV")
	stackconfig.Manifest.Services["go_orchestrator"] = replaced
	t.Cleanup(func() {
		stackconfig.Manifest.Services["go_orchestrator"] = original
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := Run(ctx, "validation-error")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "go_orchestrator manifest validation failed")
}

func TestRun_OTelInitFailurePath(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "invalid-project-id")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "no-such-file.json")

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	err := Run(ctx, "otel-init-failure")
	assert.NoError(t, err)
}

func TestRun_RedisConfigured(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "redis://localhost:6379/0")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := Run(ctx, "redis-configured")
	assert.NoError(t, err)
}

func TestRun_InternalMetricsListenerError(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "-1")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := Run(ctx, "invalid-internal-listener-2")
	require.Error(t, err)
	assert.Regexp(t, "internal listener on", err.Error())
}

func TestRun_TemporalEnabledButClientUnavailable(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "")
	t.Setenv("TEMPORAL_ENABLED", "1")
	t.Setenv("TEMPORAL_ADDRESS", "127.0.0.1:12345")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := Run(ctx, "temporal-client-unavailable")
	require.Error(t, err)
	assert.Regexp(t, "temporal client initialization failed", err.Error())
}

func TestRun_InvalidPublicPort(t *testing.T) {
	t.Setenv("PORT", "-1")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := Run(ctx, "invalid-listen")
	require.Error(t, err)
	assert.Regexp(t, "public listener on :-1", err.Error())
}

func TestRun_InternalMetricsListenerFailure(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "1")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := Run(ctx, "invalid-internal-listener")
	if err != nil {
		assert.Regexp(t, "internal listener on", err.Error())
	}
}

func TestRun_InvalidDatabaseURL(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "1")
	t.Setenv("DATABASE_URL", "://bad-url")
	t.Setenv("UPSTASH_REDIS_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	err := Run(ctx, "invalid-db-url")
	assert.NoError(t, err)
}

func TestRun_InvalidRedisURL(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "::::bad-redis")

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	err := Run(ctx, "invalid-redis-url")
	assert.NoError(t, err)
}

func TestRun_AgentGatewayEnabledAndGRPCListenFailure(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("GO_INTERNAL_GRPC_ADDR", "invalid-grpc-addr")
	t.Setenv("DISABLE_AGENT_GATEWAY", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := Run(ctx, "gateway-grpc-listen-fail")
	require.Error(t, err)
	assert.Regexp(t, "gRPC listener on invalid-grpc-addr", err.Error())
}

func TestMainErrorPath(t *testing.T) {
	originalRun := runFn
	originalExit := exitFn
	t.Cleanup(func() {
		runFn = originalRun
		exitFn = originalExit
	})

	var exitCode int
	runFn = func(context.Context, string) error { return errors.New("test-fail") }
	exitFn = func(code int) {
		exitCode = code
	}

	main()

	assert.Equal(t, 1, exitCode)
}

func TestMainSuccessPath(t *testing.T) {
	originalRun := runFn
	originalExit := exitFn
	t.Cleanup(func() {
		runFn = originalRun
		exitFn = originalExit
	})

	called := false
	runFn = func(context.Context, string) error { return nil }
	exitFn = func(int) {
		called = true
	}

	main()

	assert.False(t, called)
}

func TestResolveListenPortDefaultsInRun(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "1")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("UPSTASH_REDIS_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	err := Run(ctx, fmt.Sprintf("version-%d", time.Now().UnixNano()))
	assert.NoError(t, err)
}

func TestRun_AgentGatewayEnabledHappyPath(t *testing.T) {
	originalResolveProjectIDWithSource := resolveProjectIDWithSource
	originalInitOTelFn := initOTelFn
	originalValidateServiceFn := validateServiceFn
	originalResolveListenPortFn := resolveListenPortFn
	originalResolveGRPCTargetFn := resolveGRPCTargetFn
	originalNewVertexClientFn := newVertexClientFn
	originalNewAgentGatewayFn := newAgentGatewayFn
	originalResolveTemporalConfigFn := resolveTemporalConfigFn
	originalStartServerFn := startServerFn
	originalStartGRPCServerFn := startGRPCServerFn
	t.Cleanup(func() {
		resolveProjectIDWithSource = originalResolveProjectIDWithSource
		initOTelFn = originalInitOTelFn
		validateServiceFn = originalValidateServiceFn
		resolveListenPortFn = originalResolveListenPortFn
		resolveGRPCTargetFn = originalResolveGRPCTargetFn
		newVertexClientFn = originalNewVertexClientFn
		newAgentGatewayFn = originalNewAgentGatewayFn
		resolveTemporalConfigFn = originalResolveTemporalConfigFn
		startServerFn = originalStartServerFn
		startGRPCServerFn = originalStartGRPCServerFn
	})

	resolveProjectIDWithSource = func(context.Context) (string, string) {
		return "demo-project", "test"
	}
	initOTelFn = func(context.Context, string, string) (telemetry.ShutdownFunc, error) {
		return telemetry.ShutdownFunc(func(context.Context) error { return nil }), nil
	}
	validateServiceFn = func(string) error { return nil }
	resolveListenPortFn = func(string, string) int { return 0 }
	resolveGRPCTargetFn = func(string) string { return "127.0.0.1:0" }
	newVertexClientFn = func(context.Context, string, string) (*llm.VertexClient, error) {
		return &llm.VertexClient{}, nil
	}
	newAgentGatewayFn = func(db wisdev.DBProvider, rdb redis.UniversalClient, journal *wisdev.RuntimeJournal, searchReg ...*search.ProviderRegistry) *wisdev.AgentGateway {
		return &wisdev.AgentGateway{}
	}
	resolveTemporalConfigFn = func() wisdev.TemporalConfig {
		return wisdev.TemporalConfig{Enabled: false}
	}
	startServerFn = func(*http.Server) error { return nil }
	startGRPCServerFn = func(string, *wisdev.AgentGateway, redis.UniversalClient) error { return nil }

	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/wisdev?sslmode=disable")
	t.Setenv("UPSTASH_REDIS_URL", "redis://localhost:6379/0")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := Run(ctx, "gateway-enabled")
	require.NoError(t, err)
}

func TestRun_TemporalEnabledHappyPath(t *testing.T) {
	originalResolveProjectIDWithSource := resolveProjectIDWithSource
	originalInitOTelFn := initOTelFn
	originalValidateServiceFn := validateServiceFn
	originalResolveListenPortFn := resolveListenPortFn
	originalResolveGRPCTargetFn := resolveGRPCTargetFn
	originalNewVertexClientFn := newVertexClientFn
	originalNewAgentGatewayFn := newAgentGatewayFn
	originalResolveTemporalConfigFn := resolveTemporalConfigFn
	originalNewTemporalClientFn := newTemporalClientFn
	originalStartTemporalWorkerFn := startTemporalWorkerFn
	originalStartServerFn := startServerFn
	originalStartGRPCServerFn := startGRPCServerFn
	t.Cleanup(func() {
		resolveProjectIDWithSource = originalResolveProjectIDWithSource
		initOTelFn = originalInitOTelFn
		validateServiceFn = originalValidateServiceFn
		resolveListenPortFn = originalResolveListenPortFn
		resolveGRPCTargetFn = originalResolveGRPCTargetFn
		newVertexClientFn = originalNewVertexClientFn
		newAgentGatewayFn = originalNewAgentGatewayFn
		resolveTemporalConfigFn = originalResolveTemporalConfigFn
		newTemporalClientFn = originalNewTemporalClientFn
		startTemporalWorkerFn = originalStartTemporalWorkerFn
		startServerFn = originalStartServerFn
		startGRPCServerFn = originalStartGRPCServerFn
	})

	resolveProjectIDWithSource = func(context.Context) (string, string) {
		return "demo-project", "test"
	}
	initOTelFn = func(context.Context, string, string) (telemetry.ShutdownFunc, error) {
		return telemetry.ShutdownFunc(func(context.Context) error { return nil }), nil
	}
	validateServiceFn = func(string) error { return nil }
	resolveListenPortFn = func(string, string) int { return 0 }
	resolveGRPCTargetFn = func(string) string { return "127.0.0.1:0" }
	newVertexClientFn = func(context.Context, string, string) (*llm.VertexClient, error) {
		return &llm.VertexClient{}, nil
	}
	newAgentGatewayFn = func(db wisdev.DBProvider, rdb redis.UniversalClient, journal *wisdev.RuntimeJournal, searchReg ...*search.ProviderRegistry) *wisdev.AgentGateway {
		return &wisdev.AgentGateway{}
	}
	resolveTemporalConfigFn = func() wisdev.TemporalConfig {
		return wisdev.TemporalConfig{
			Enabled:   true,
			Address:   "127.0.0.1:7233",
			Namespace: "default",
			TaskQueue: "wisdev-test",
		}
	}
	newTemporalClientFn = func(wisdev.TemporalConfig) (temporalclient.Client, error) {
		return temporalclient.NewLazyClient(temporalclient.Options{HostPort: "127.0.0.1:7233"})
	}
	startTemporalWorkerFn = func(*wisdev.AgentGateway, temporalclient.Client, wisdev.TemporalConfig) (func(), error) {
		return func() {}, nil
	}
	startServerFn = func(*http.Server) error { return nil }
	startGRPCServerFn = func(string, *wisdev.AgentGateway, redis.UniversalClient) error { return nil }

	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/wisdev?sslmode=disable")
	t.Setenv("UPSTASH_REDIS_URL", "redis://localhost:6379/0")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := Run(ctx, "temporal-enabled")
	require.NoError(t, err)
}

func TestRun_TemporalWorkerStartupFailure(t *testing.T) {
	originalResolveProjectIDWithSource := resolveProjectIDWithSource
	originalInitOTelFn := initOTelFn
	originalValidateServiceFn := validateServiceFn
	originalResolveListenPortFn := resolveListenPortFn
	originalResolveGRPCTargetFn := resolveGRPCTargetFn
	originalNewVertexClientFn := newVertexClientFn
	originalNewAgentGatewayFn := newAgentGatewayFn
	originalResolveTemporalConfigFn := resolveTemporalConfigFn
	originalNewTemporalClientFn := newTemporalClientFn
	originalStartTemporalWorkerFn := startTemporalWorkerFn
	originalStartServerFn := startServerFn
	originalStartGRPCServerFn := startGRPCServerFn
	t.Cleanup(func() {
		resolveProjectIDWithSource = originalResolveProjectIDWithSource
		initOTelFn = originalInitOTelFn
		validateServiceFn = originalValidateServiceFn
		resolveListenPortFn = originalResolveListenPortFn
		resolveGRPCTargetFn = originalResolveGRPCTargetFn
		newVertexClientFn = originalNewVertexClientFn
		newAgentGatewayFn = originalNewAgentGatewayFn
		resolveTemporalConfigFn = originalResolveTemporalConfigFn
		newTemporalClientFn = originalNewTemporalClientFn
		startTemporalWorkerFn = originalStartTemporalWorkerFn
		startServerFn = originalStartServerFn
		startGRPCServerFn = originalStartGRPCServerFn
	})

	resolveProjectIDWithSource = func(context.Context) (string, string) {
		return "demo-project", "test"
	}
	initOTelFn = func(context.Context, string, string) (telemetry.ShutdownFunc, error) {
		return telemetry.ShutdownFunc(func(context.Context) error { return nil }), nil
	}
	validateServiceFn = func(string) error { return nil }
	resolveListenPortFn = func(string, string) int { return 0 }
	resolveGRPCTargetFn = func(string) string { return "127.0.0.1:0" }
	newVertexClientFn = func(context.Context, string, string) (*llm.VertexClient, error) {
		return &llm.VertexClient{}, nil
	}
	newAgentGatewayFn = func(db wisdev.DBProvider, rdb redis.UniversalClient, journal *wisdev.RuntimeJournal, searchReg ...*search.ProviderRegistry) *wisdev.AgentGateway {
		return &wisdev.AgentGateway{}
	}
	resolveTemporalConfigFn = func() wisdev.TemporalConfig {
		return wisdev.TemporalConfig{
			Enabled:   true,
			Address:   "127.0.0.1:7233",
			Namespace: "default",
			TaskQueue: "wisdev-test",
		}
	}
	newTemporalClientFn = func(wisdev.TemporalConfig) (temporalclient.Client, error) {
		return temporalclient.NewLazyClient(temporalclient.Options{HostPort: "127.0.0.1:7233"})
	}
	startTemporalWorkerFn = func(*wisdev.AgentGateway, temporalclient.Client, wisdev.TemporalConfig) (func(), error) {
		return nil, errors.New("worker failed")
	}
	startServerFn = func(*http.Server) error { return nil }
	startGRPCServerFn = func(string, *wisdev.AgentGateway, redis.UniversalClient) error { return nil }

	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/wisdev?sslmode=disable")
	t.Setenv("UPSTASH_REDIS_URL", "redis://localhost:6379/0")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := Run(ctx, "temporal-worker-failure")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporal worker startup failed")
}

func TestRun_ShutdownErrorsAreLogged(t *testing.T) {
	originalResolveProjectIDWithSource := resolveProjectIDWithSource
	originalInitOTelFn := initOTelFn
	originalValidateServiceFn := validateServiceFn
	originalResolveListenPortFn := resolveListenPortFn
	originalResolveGRPCTargetFn := resolveGRPCTargetFn
	originalNewVertexClientFn := newVertexClientFn
	originalNewAgentGatewayFn := newAgentGatewayFn
	originalResolveTemporalConfigFn := resolveTemporalConfigFn
	originalStartServerFn := startServerFn
	originalStartGRPCServerFn := startGRPCServerFn
	originalShutdownHTTPServerFn := shutdownHTTPServerFn
	t.Cleanup(func() {
		resolveProjectIDWithSource = originalResolveProjectIDWithSource
		initOTelFn = originalInitOTelFn
		validateServiceFn = originalValidateServiceFn
		resolveListenPortFn = originalResolveListenPortFn
		resolveGRPCTargetFn = originalResolveGRPCTargetFn
		newVertexClientFn = originalNewVertexClientFn
		newAgentGatewayFn = originalNewAgentGatewayFn
		resolveTemporalConfigFn = originalResolveTemporalConfigFn
		startServerFn = originalStartServerFn
		startGRPCServerFn = originalStartGRPCServerFn
		shutdownHTTPServerFn = originalShutdownHTTPServerFn
	})

	resolveProjectIDWithSource = func(context.Context) (string, string) {
		return "demo-project", "test"
	}
	initOTelFn = func(context.Context, string, string) (telemetry.ShutdownFunc, error) {
		return telemetry.ShutdownFunc(func(context.Context) error { return nil }), nil
	}
	validateServiceFn = func(string) error { return nil }
	resolveListenPortFn = func(string, string) int { return 0 }
	resolveGRPCTargetFn = func(string) string { return "127.0.0.1:0" }
	newVertexClientFn = func(context.Context, string, string) (*llm.VertexClient, error) {
		return &llm.VertexClient{}, nil
	}
	newAgentGatewayFn = func(db wisdev.DBProvider, rdb redis.UniversalClient, journal *wisdev.RuntimeJournal, searchReg ...*search.ProviderRegistry) *wisdev.AgentGateway {
		return &wisdev.AgentGateway{}
	}
	resolveTemporalConfigFn = func() wisdev.TemporalConfig {
		return wisdev.TemporalConfig{Enabled: false}
	}
	startServerFn = func(*http.Server) error { return nil }
	startGRPCServerFn = func(string, *wisdev.AgentGateway, redis.UniversalClient) error { return nil }
	shutdownHTTPServerFn = func(*http.Server, context.Context) error {
		return errors.New("shutdown failed")
	}

	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/wisdev?sslmode=disable")
	t.Setenv("UPSTASH_REDIS_URL", "redis://localhost:6379/0")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := Run(ctx, "shutdown-errors")
	require.NoError(t, err)
}

func TestRun_ServerClosedIsIgnored(t *testing.T) {
	originalResolveProjectIDWithSource := resolveProjectIDWithSource
	originalInitOTelFn := initOTelFn
	originalValidateServiceFn := validateServiceFn
	originalResolveListenPortFn := resolveListenPortFn
	originalResolveGRPCTargetFn := resolveGRPCTargetFn
	originalNewVertexClientFn := newVertexClientFn
	originalNewAgentGatewayFn := newAgentGatewayFn
	originalResolveTemporalConfigFn := resolveTemporalConfigFn
	originalStartServerFn := startServerFn
	originalStartGRPCServerFn := startGRPCServerFn
	t.Cleanup(func() {
		resolveProjectIDWithSource = originalResolveProjectIDWithSource
		initOTelFn = originalInitOTelFn
		validateServiceFn = originalValidateServiceFn
		resolveListenPortFn = originalResolveListenPortFn
		resolveGRPCTargetFn = originalResolveGRPCTargetFn
		newVertexClientFn = originalNewVertexClientFn
		newAgentGatewayFn = originalNewAgentGatewayFn
		resolveTemporalConfigFn = originalResolveTemporalConfigFn
		startServerFn = originalStartServerFn
		startGRPCServerFn = originalStartGRPCServerFn
	})

	resolveProjectIDWithSource = func(context.Context) (string, string) {
		return "demo-project", "test"
	}
	initOTelFn = func(context.Context, string, string) (telemetry.ShutdownFunc, error) {
		return telemetry.ShutdownFunc(func(context.Context) error { return nil }), nil
	}
	validateServiceFn = func(string) error { return nil }
	resolveListenPortFn = func(string, string) int { return 0 }
	resolveGRPCTargetFn = func(string) string { return "127.0.0.1:0" }
	newVertexClientFn = func(context.Context, string, string) (*llm.VertexClient, error) {
		return &llm.VertexClient{}, nil
	}
	newAgentGatewayFn = func(db wisdev.DBProvider, rdb redis.UniversalClient, journal *wisdev.RuntimeJournal, searchReg ...*search.ProviderRegistry) *wisdev.AgentGateway {
		return &wisdev.AgentGateway{}
	}
	resolveTemporalConfigFn = func() wisdev.TemporalConfig {
		return wisdev.TemporalConfig{Enabled: false}
	}
	startServerFn = func(*http.Server) error { return http.ErrServerClosed }
	startGRPCServerFn = func(string, *wisdev.AgentGateway, redis.UniversalClient) error { return nil }

	t.Setenv("PORT", "0")
	t.Setenv("INTERNAL_METRICS_PORT", "0")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
	t.Setenv("DISABLE_AGENT_GATEWAY", "")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/wisdev?sslmode=disable")
	t.Setenv("UPSTASH_REDIS_URL", "redis://localhost:6379/0")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := Run(ctx, "server-closed")
	require.NoError(t, err)
}
