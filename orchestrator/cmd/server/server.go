package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/api"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

type Server struct {
	httpSrv     *http.Server
	internalSrv *http.Server
}

var (
	resolveProjectIDWithSource = resilience.ResolveGoogleCloudProjectIDWithSource
	initOTelFn                 = telemetry.InitOTel
	validateServiceFn          = stackconfig.ValidateService
	resolveListenPortFn        = stackconfig.ResolveListenPort
	resolveGRPCTargetFn        = stackconfig.ResolveGRPCTarget
	newDBPoolFn                = pgxpool.New
	parseRedisURLFn            = redis.ParseURL
	newRedisClientFn           = redis.NewClient
	normalizeDBProviderFn      = wisdev.NormalizeDBProvider
	newRuntimeJournalFn        = wisdev.NewRuntimeJournal
	newLLMClientFn             = llm.NewClient
	warmUpLLMClientFn          = func(ctx context.Context, client *llm.Client) error {
		if client == nil {
			return nil
		}
		timeout := 20 * time.Second
		attempts := 5
		if testing.Testing() {
			timeout = time.Second
			attempts = 1
		}
		warmCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return client.WarmUpWithRetry(warmCtx, attempts)
	}
	buildRegistryFn               = search.BuildRegistry
	newAgentGatewayFn             = wisdev.NewAgentGateway
	resolveTemporalConfigFn       = wisdev.ResolveTemporalConfig
	newTemporalClientFn           = wisdev.NewTemporalClient
	startTemporalWorkerFn         = wisdev.StartTemporalWorker
	newTemporalExecutionServiceFn = wisdev.NewTemporalExecutionService
	newVertexClientFn             = llm.NewVertexClient
	newRouterFn                   = api.NewRouter
	metricsHandlerFn              = telemetry.MetricsHandler
	startServerFn                 = func(srv *http.Server) error { return srv.ListenAndServe() }
	startGRPCServerFn             = wisdev.StartGRPCServer
	shutdownHTTPServerFn          = func(srv *http.Server, ctx context.Context) error { return srv.Shutdown(ctx) }
)

func Run(ctx context.Context, version string) error {
	projectID, projectSource := resolveProjectIDWithSource(ctx)
	slog.Info("go orchestrator booting",
		"version", version,
		"project_id_configured", projectID != "",
		"project_id_source", projectSource,
	)
	shutdownOTel, err := initOTelFn(ctx, projectID, version)
	if err != nil {
		slog.Error("otel init failed — proceeding with no-op provider", "error", err)
	} else {
		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			shutdownOTel(flushCtx)
		}()
	}

	if err := validateServiceFn("go_orchestrator"); err != nil {
		slog.Error("go orchestrator manifest validation failed", "error", err)
		return fmt.Errorf("go_orchestrator manifest validation failed: %w", err)
	}

	port := fmt.Sprintf("%d", resolveListenPortFn("go_orchestrator", "http"))
	internalPort := fmt.Sprintf("%d", resolveListenPortFn("go_orchestrator", "metrics"))
	grpcAddr := resolveGRPCTargetFn("go_internal")
	slog.Info("go orchestrator endpoints resolved",
		"http_port", port,
		"metrics_port", internalPort,
		"grpc_addr", grpcAddr,
	)

	var dbPool *pgxpool.Pool
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		slog.Info("postgres configuration detected")
		dbCtx, dbCancel := context.WithTimeout(ctx, 5*time.Second)
		dbPool, err = newDBPoolFn(dbCtx, dbURL)
		dbCancel()
		if err != nil {
			slog.Error("failed to create postgres pool", "error", err)
		} else {
			slog.Info("postgres pool initialized")
			defer dbPool.Close()
		}
	} else {
		slog.Info("postgres pool not configured; continuing without database connection")
	}

	var redisClient redis.UniversalClient
	if redisURL := os.Getenv("UPSTASH_REDIS_URL"); redisURL != "" {
		opt, err := parseRedisURLFn(redisURL)
		if err == nil {
			slog.Info("redis cache configured")
			redisClient = newRedisClientFn(opt)
			defer redisClient.Close()
		} else {
			slog.Warn("redis cache disabled due to invalid URL", "error", err)
		}
	} else {
		slog.Info("redis cache not configured; continuing without shared cache")
	}

	var dbProvider wisdev.DBProvider
	if dbPool != nil {
		dbProvider = dbPool
	}
	dbProvider = normalizeDBProviderFn(dbProvider)

	journal := newRuntimeJournalFn(dbProvider)
	llmClient := newLLMClientFn()
	if err := warmUpLLMClientFn(ctx, llmClient); err != nil {
		slog.Warn("llm sidecar warm-up did not complete before startup",
			"component", "cmd.server",
			"operation", "llm_warm_up",
			"error", err,
		)
	}
	searchRegistry := buildRegistryFn()
	searchRegistry.SetDB(dbProvider)
	searchRegistry.SetRedis(redisClient)
	wisdev.GlobalSearchRegistry = searchRegistry // Inject for legacy wisdev paths
	citationGate := wisdev.ResolveCitationBrokerGateConfig()
	slog.Info("go orchestrator runtime services initialized",
		"database_enabled", dbPool != nil,
		"redis_enabled", redisClient != nil,
		"citation_gate_mode", citationGate.Mode,
	)

	fastRegistry := buildRegistryFn("semantic_scholar", "openalex")
	fastRegistry.SetDB(dbProvider)
	fastRegistry.SetRedis(redisClient)

	var agentGateway *wisdev.AgentGateway
	var temporalClientCloser func()
	var temporalWorkerStop func()
	if os.Getenv("DISABLE_AGENT_GATEWAY") != "1" {
		agentGateway = newAgentGatewayFn(dbProvider, redisClient, journal, searchRegistry)
		if temporalCfg := resolveTemporalConfigFn(); temporalCfg.Enabled {
			temporalClient, temporalErr := newTemporalClientFn(temporalCfg)
			if temporalErr != nil {
				slog.Error("temporal client initialization failed", "error", temporalErr, "address", temporalCfg.Address)
				return fmt.Errorf("temporal client initialization failed: %w", temporalErr)
			}
			if temporalClient != nil {
				temporalClientCloser = temporalClient.Close
			}
			stopWorker, workerErr := startTemporalWorkerFn(agentGateway, temporalClient, temporalCfg)
			if workerErr != nil {
				temporalClient.Close()
				return fmt.Errorf("temporal worker startup failed: %w", workerErr)
			}
			temporalWorkerStop = stopWorker
			agentGateway.Execution = newTemporalExecutionServiceFn(agentGateway, temporalClient, temporalCfg)
			slog.Info("agent gateway temporal execution enabled", "address", temporalCfg.Address, "namespace", temporalCfg.Namespace, "taskQueue", temporalCfg.TaskQueue)
		}
		slog.Info("agent gateway enabled")
	} else {
		slog.Info("agent gateway disabled by configuration")
	}
	if temporalWorkerStop != nil {
		defer temporalWorkerStop()
	}
	if temporalClientCloser != nil {
		defer temporalClientCloser()
	}

	vertex, vertexErr := newVertexClientFn(ctx, projectID, "us-central1")
	if vertexErr != nil {
		slog.Warn("vertex client unavailable; continuing without Vertex integration", "error", vertexErr)
	} else {
		slog.Info("google genai client initialized", "backend", vertex.BackendName(), "credential_source", vertex.CredentialSource(), "location", "us-central1")
	}

	router := newRouterFn(api.ServerConfig{
		Version:        version,
		SearchRegistry: searchRegistry,
		FastRegistry:   fastRegistry,
		LLMClient:      llmClient,
		VertexClient:   vertex,
		AgentGateway:   agentGateway,
		DB:             dbProvider,
		Redis:          redisClient,
		Journal:        journal,
	})

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: router,
	}

	internalMux := http.NewServeMux()
	internalMux.Handle("/metrics", metricsHandlerFn())
	internalSrv := &http.Server{
		Addr:    ":" + internalPort,
		Handler: internalMux,
	}

	srvErrCh := make(chan error, 3)

	go func() {
		slog.Info(
			"public listener starting",
			"component", "http",
			"port",
			port,
			"citationGateMode",
			citationGate.Mode,
			"allowGoCitationFallback",
			citationGate.AllowGoFallback,
			"citationGateWarnings",
			citationGate.Warnings,
		)
		if err := startServerFn(srv); err != nil && err != http.ErrServerClosed {
			srvErrCh <- fmt.Errorf("public listener on :%s: %w", port, err)
		}
	}()

	go func() {
		slog.Info("internal metrics listener starting", "component", "metrics", "port", internalPort)
		if err := startServerFn(internalSrv); err != nil && err != http.ErrServerClosed {
			srvErrCh <- fmt.Errorf("internal listener on :%s: %w", internalPort, err)
		}
	}()

	// Start the internal gRPC server (SearchGateway + AgentGateway).
	// Only started when the agent gateway is enabled — gRPC is an internal
	// transport between services, not a public endpoint.
	if agentGateway != nil {
		go func() {
			slog.Info("gRPC server starting", "component", "grpc", "addr", grpcAddr)
			if err := startGRPCServerFn(grpcAddr, agentGateway, redisClient); err != nil {
				srvErrCh <- fmt.Errorf("gRPC listener on %s: %w", grpcAddr, err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received", "reason", ctx.Err())
	case srvErr := <-srvErrCh:
		return srvErr
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	slog.Info("graceful shutdown starting")
	if err := shutdownHTTPServerFn(srv, shutdownCtx); err != nil {
		slog.Error("error shutting down public server", "error", err)
	}
	if err := shutdownHTTPServerFn(internalSrv, shutdownCtx); err != nil {
		slog.Error("error shutting down internal server", "error", err)
	}
	slog.Info("go orchestrator shutdown complete")

	return nil
}
