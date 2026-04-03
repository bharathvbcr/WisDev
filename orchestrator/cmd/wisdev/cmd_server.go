package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/api"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/storage"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
)

func handleServer(args []string) {
	serverCmd := flag.NewFlagSet("server", flag.ExitOnError)
	serverConfig := serverCmd.String("config", "wisdev.yaml", "path to wisdev.yaml config file")
	serverPort := serverCmd.Int("port", 8081, "HTTP listen port")
	serverGRPCPort := serverCmd.Int("grpc-port", 50052, "gRPC listen port")

	if err := serverCmd.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing flags: %v\n", err)
		os.Exit(1)
	}

	cfg, err := loadConfig(*serverConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("config error: %v\n", err)))
		os.Exit(1)
	}

	if *serverPort != 8081 {
		cfg.Server.HTTPPort = *serverPort
	}
	if *serverGRPCPort != 50052 {
		cfg.Server.GRPCPort = *serverGRPCPort
	}

	printBanner()
	printConfig(cfg)
	fmt.Println()

	ctx := context.Background()

	if cfg.Observability.EnableOTEL {
		shutdown, err := telemetry.InitOTel(ctx, "", version)
		if err != nil {
			fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("otel init failed: %v\n", err)))
		} else {
			defer func() { _ = shutdown(ctx) }()
		}
	}

	store, err := storage.NewProvider(cfg.Storage.Type, cfg.Storage.DSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("storage error: %v\n", err)))
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
		if err := internalSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "internal metrics server error: %v\n", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	fmt.Println(styleSuccess.Render(fmt.Sprintf("Server listening on :%d", cfg.Server.HTTPPort)))
	fmt.Println(styleDim.Render("Press Ctrl+C to stop"))
	fmt.Println()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, styleError.Render(fmt.Sprintf("server error: %v\n", err)))
			os.Exit(1)
		}
	}()

	<-stop
	fmt.Println()
	fmt.Println(styleDim.Render("Shutting down..."))
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = internalSrv.Shutdown(shutdownCtx)
	fmt.Println(styleSuccess.Render("Server stopped."))
}
