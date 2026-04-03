package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/internal/config"
	"github.com/wisdev-agent/wisdev-agent-os/internal/storage"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/api"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

var version = "dev"

func main() {
	ctx := context.Background()

	runCmd := flag.NewFlagSet("run", flag.ExitOnError)
	runConfig := runCmd.String("config", "wisdev.yaml", "path to wisdev.yaml config file")
	runQuery := runCmd.String("q", "", "research query")
	runMode := runCmd.String("mode", "autonomous", "agent mode: autonomous or guided")
	runPort := runCmd.Int("port", 8081, "HTTP listen port")
	runGRPCPort := runCmd.Int("grpc-port", 50052, "gRPC listen port")

	serverCmd := flag.NewFlagSet("server", flag.ExitOnError)
	serverConfig := serverCmd.String("config", "wisdev.yaml", "path to wisdev.yaml config file")
	serverPort := serverCmd.Int("port", 8081, "HTTP listen port")
	serverGRPCPort := serverCmd.Int("grpc-port", 50052, "gRPC listen port")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "wisdev - Open-source AI research agent\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  wisdev run [flags]     Run a research task\n")
		fmt.Fprintf(os.Stderr, "  wisdev server [flags]  Start the agent server\n")
		fmt.Fprintf(os.Stderr, "  wisdev version         Show version\n\n")
		fmt.Fprintf(os.Stderr, "Run flags:\n")
		runCmd.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nServer flags:\n")
		serverCmd.PrintDefaults()
	}

	if len(os.Args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("wisdev %s\n", version)
		return
	case "run":
		if err := runCmd.Parse(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error parsing run flags: %v\n", err)
			os.Exit(1)
		}
		if *runQuery == "" {
			fmt.Fprintf(os.Stderr, "error: -q flag is required\n")
			os.Exit(1)
		}
		runResearch(ctx, *runConfig, *runQuery, *runMode, *runPort, *runGRPCPort)
	case "server":
		if err := serverCmd.Parse(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error parsing server flags: %v\n", err)
			os.Exit(1)
		}
		startServer(ctx, *serverConfig, *serverPort, *serverGRPCPort)
	default:
		flag.Usage()
		os.Exit(1)
	}
}

func runResearch(ctx context.Context, configPath, query, mode string, port, grpcPort int) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		slog.Warn("config not found, using defaults", "error", err)
		cfg = &config.Config{}
		cfg.SetDefaults()
	}

	if port != 8081 {
		cfg.Server.HTTPPort = port
	}
	if grpcPort != 50052 {
		cfg.Server.GRPCPort = grpcPort
	}
	if mode != "" {
		cfg.Agent.Mode = mode
	}

	store, err := storage.NewProvider(cfg.Storage.Type, cfg.Storage.DSN)
	if err != nil {
		slog.Error("failed to initialize storage", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	slog.Info("starting research", "query", query, "mode", cfg.Agent.Mode, "storage", cfg.Storage.Type)

	sessionID := wisdev.NewTraceID()
	slog.Info("session created", "session_id", sessionID)

	slog.Info("research task queued (full execution loop in Phase 3)")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("shutting down gracefully...")
}

func startServer(ctx context.Context, configPath string, port, grpcPort int) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		slog.Warn("config not found, using defaults", "error", err)
		cfg = &config.Config{}
		cfg.SetDefaults()
	}

	if port != 8081 {
		cfg.Server.HTTPPort = port
	}
	if grpcPort != 50052 {
		cfg.Server.GRPCPort = grpcPort
	}

	if cfg.Observability.EnableOTEL {
		shutdown, err := telemetry.InitOTel(ctx, "", version)
		if err != nil {
			slog.Error("otel init failed", "error", err)
		} else {
			defer func() { _ = shutdown(ctx) }()
		}
	}

	store, err := storage.NewProvider(cfg.Storage.Type, cfg.Storage.DSN)
	if err != nil {
		slog.Error("failed to initialize storage", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	llmClient := llm.NewClient()
	searchRegistry := search.BuildRegistry()
	fastRegistry := search.BuildRegistry("semantic_scholar", "openalex")

	journal := wisdev.NewRuntimeJournal(nil)
	agentGateway := wisdev.NewAgentGateway(nil, nil, journal)

	router := api.NewRouter(api.ServerConfig{
		Version:        version,
		SearchRegistry: searchRegistry,
		FastRegistry:   fastRegistry,
		LLMClient:      llmClient,
		AgentGateway:   agentGateway,
		Journal:        journal,
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.HTTPPort),
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

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

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		slog.Info("wisdev server starting",
			"port", cfg.Server.HTTPPort,
			"grpc_port", cfg.Server.GRPCPort,
			"version", version,
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
	_ = srv.Shutdown(shutdownCtx)
	_ = internalSrv.Shutdown(shutdownCtx)
	slog.Info("shutdown complete")
}

func loadConfig(path string) (*config.Config, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return config.Load(path)
}
