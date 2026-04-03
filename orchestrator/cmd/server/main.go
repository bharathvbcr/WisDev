package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/api"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

var version = "dev"

func main() {
	ctx := context.Background()

	// ── Observability bootstrap (must come first) ────────────────────────────
	// InitOTel registers the global TracerProvider and MeterProvider pointed at
	// GCP OpenTelemetry + Cloud Monitoring. All subsequent instrumented code
	// resolves exporters via otel.GetTracerProvider() / otel.GetMeterProvider().
	projectID := os.Getenv("VERTEX_PROJECT")
	shutdownOTel, err := telemetry.InitOTel(ctx, projectID, version)
	if err != nil {
		slog.Error("otel init failed — proceeding with no-op provider", "error", err)
		// Non-fatal: the service still runs; traces just won't export.
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := shutdownOTel(flushCtx); shutdownErr != nil {
			slog.Error("otel shutdown error", "error", shutdownErr)
		}
	}()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	// ── Postgres connection pool ─────────────────────────────────────────────
	var dbPool *pgxpool.Pool
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		dbCtx, dbCancel := context.WithTimeout(ctx, 10*time.Second)
		defer dbCancel()
		var dbErr error
		dbPool, dbErr = pgxpool.New(dbCtx, dbURL)
		if dbErr != nil {
			slog.Error("failed to create postgres pool", "error", dbErr)
		} else {
			slog.Info("postgres pool initialized")
			defer dbPool.Close()
		}
	}

	// ── Redis client ─────────────────────────────────────────────────────────
	var redisClient redis.UniversalClient
	if redisURL := os.Getenv("UPSTASH_REDIS_URL"); redisURL != "" {
		opt, err := redis.ParseURL(redisURL)
		if err == nil {
			redisClient = redis.NewClient(opt)
			slog.Info("redis client initialized")
			defer redisClient.Close()
		} else {
			slog.Error("failed to parse redis url", "error", err)
		}
	}

	// ── WisDev Journal ───────────────────────────────────────────────────────
	journal := wisdev.NewRuntimeJournal(dbPool)

	// ── Application dependencies ─────────────────────────────────────────────
	llmClient := llm.NewClient()
	api.WireLegacySearch(redisClient)
	searchRegistry := search.BuildRegistry()
	searchRegistry.SetRedis(redisClient)
	fastRegistry := search.BuildRegistry("semantic_scholar", "openalex")
	fastRegistry.SetRedis(redisClient)

	var agentGateway *wisdev.AgentGateway
	if os.Getenv("DISABLE_AGENT_GATEWAY") == "1" {
		slog.Warn("agent gateway disabled via DISABLE_AGENT_GATEWAY=1")
	} else {
		agentGateway = wisdev.NewAgentGateway(dbPool, redisClient, journal)
	}

	vertex, vertexErr := llm.NewVertexClient(ctx, projectID, "us-central1")
	if vertexErr != nil {
		slog.Error("failed to initialize vertex ai", "error", vertexErr)
	}

	var dbProvider wisdev.DBProvider
	if dbPool != nil {
		dbProvider = dbPool
	}

	// ── Internal Service Key check ──────────────────────────────────────────
	internalKey := os.Getenv("INTERNAL_SERVICE_KEY")
	if internalKey == "" {
		slog.Warn("INTERNAL_SERVICE_KEY is not set! Service-to-service authentication is disabled.")
	} else {
		slog.Info("INTERNAL_SERVICE_KEY is configured; service-to-service auth enabled.")
	}

	// ── HTTP server ──────────────────────────────────────────────────────────
	router := api.NewRouter(api.ServerConfig{
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
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	// ── gRPC Gateways (WisDev) ───────────────────────────────────────────────
	grpcPort := os.Getenv("GRPC_PORT")
	if grpcPort == "" {
		grpcPort = "50052"
	}
	go func() {
		slog.Info("WisDev gRPC gateways starting", "port", grpcPort)
		if err := wisdev.StartGRPCServer(":"+grpcPort, agentGateway, redisClient); err != nil {
			slog.Error("wisdev grpc server error", "error", err)
		}
	}()

	// ── Metrics & health endpoints ───────────────────────────────────────────
	// /metrics is served on a separate internal mux (not exposed to the public
	// router) so it doesn't require auth and isn't accidentally exposed externally.
	internalMux := http.NewServeMux()
	internalMux.Handle("/metrics", telemetry.MetricsHandler())
	internalMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	internalSrv := &http.Server{
		Addr:        ":9090",
		Handler:     internalMux,
		ReadTimeout: 5 * time.Second,
	}
	go func() {
		slog.Info("internal metrics server starting", "port", "9090")
		if err := internalSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("internal metrics server error", "error", err)
		}
	}()

	// ── Graceful shutdown ────────────────────────────────────────────────────
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		slog.Info("ScholarLM Go orchestrator starting",
			"port", port,
			"grpc_port", grpcPort,
			"version", version,
			"project", projectID,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "error", err)
			os.Exit(1)
		}
	}()

	<-stop
	slog.Info("shutting down gracefully...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http server shutdown failed", "error", err)
		os.Exit(1)
	}
	if err := internalSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("internal server shutdown error", "error", err)
	}
	slog.Info("shutdown complete")
}
