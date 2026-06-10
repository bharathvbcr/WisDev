package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/paper"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/rag"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/telemetry"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

type ServerConfig struct {
	Version        string
	SearchRegistry *search.ProviderRegistry
	FastRegistry   *search.ProviderRegistry
	LLMClient      *llm.Client
	VertexClient   *llm.VertexClient
	AgentGateway   *wisdev.AgentGateway
	DB             wisdev.DBProvider
	Redis          redis.UniversalClient
	Journal        *wisdev.RuntimeJournal
}

func ensureAgentGateway(cfg ServerConfig) *wisdev.AgentGateway {
	if cfg.AgentGateway != nil {
		resolveUnifiedResearchRuntime(cfg.AgentGateway)
		if cfg.AgentGateway.QuestRuntime == nil {
			cfg.AgentGateway.QuestRuntime = wisdev.NewResearchQuestRuntime(cfg.AgentGateway)
		}
		return cfg.AgentGateway
	}

	journal := cfg.Journal
	if journal == nil {
		journal = wisdev.NewRuntimeJournal(cfg.DB)
	}

	gateway := wisdev.NewAgentGateway(cfg.DB, cfg.Redis, journal, cfg.SearchRegistry)
	runtimeDepsChanged := false
	if cfg.LLMClient != nil {
		// Wire the VertexClient for native controlled generation (official
		// response_mime_type + response_json_schema structured output). This
		// bypasses the Python sidecar proxy path which drops the JSON schema.
		if cfg.VertexClient != nil {
			cfg.LLMClient.VertexDirect = cfg.VertexClient
			slog.Info("structured output: native VertexClient wired for schema-constrained generation")
		} else {
			slog.Warn("structured output: cfg.VertexClient is nil — falling back to Python sidecar for structured output; JSON schema may not be enforced")
		}
		gateway.LLMClient = cfg.LLMClient
		gateway.Brain = wisdev.NewBrainCapabilities(cfg.LLMClient)
		gateway.Gate = rag.NewEvidenceGate(cfg.LLMClient)
		gateway.Executor = wisdev.NewPlanExecutor(gateway.Registry, gateway.PolicyConfig, cfg.LLMClient, gateway.Brain, cfg.Redis, gateway.PythonExecute, gateway.ADKRuntime, gateway.SearchRegistry)
		runtimeDepsChanged = true
	}
	if cfg.SearchRegistry != nil {
		gateway.SearchRegistry = cfg.SearchRegistry
		gateway.Loop = wisdev.NewAutonomousLoop(cfg.SearchRegistry, gateway.LLMClient)
		if _, ok := gateway.Executor.(*wisdev.PlanExecutor); ok || gateway.Executor == nil {
			gateway.Executor = wisdev.NewPlanExecutor(gateway.Registry, gateway.PolicyConfig, gateway.LLMClient, gateway.Brain, cfg.Redis, gateway.PythonExecute, gateway.ADKRuntime, cfg.SearchRegistry)
		}
		runtimeDepsChanged = true
	}
	if runtimeDepsChanged {
		gateway.Runtime = nil
	}
	resolveUnifiedResearchRuntime(gateway)
	if gateway.QuestRuntime == nil {
		gateway.QuestRuntime = wisdev.NewResearchQuestRuntime(gateway)
	}
	return gateway
}

func NewRouter(cfg ServerConfig) http.Handler {
	mux := http.NewServeMux()
	agentGateway := ensureAgentGateway(cfg)

	// Initialize engines
	ragEngine := buildRAGEngine(cfg, agentGateway)
	ragHandler := NewRAGHandler(ragEngine).WithAgentGateway(agentGateway)

	// Share the session manager from the agent gateway when available so the
	// canonical guided-session handlers operate on the same session store.
	var wisdevSessions *wisdev.SessionManager
	if agentGateway != nil && agentGateway.WisdevSessions != nil {
		wisdevSessions = agentGateway.WisdevSessions
	} else {
		wisdevSessions = wisdev.NewSessionManager("")
	}
	wisdevGuided := wisdev.NewGuidedFlow()

	var wisdevAutonomous *wisdev.AutonomousLoop
	if agentGateway != nil {
		wisdevAutonomous = agentGateway.Loop
	} else {
		wisdevAutonomous = wisdev.NewAutonomousLoop(cfg.SearchRegistry, cfg.LLMClient)
	}
	GlobalYoloLoop = wisdevAutonomous
	GlobalYoloGateway = agentGateway

	wisdevWorker := wisdev.NewAutonomousWorker(wisdevAutonomous)
	var brainCaps *wisdev.BrainCapabilities
	if cfg.LLMClient != nil {
		brainCaps = wisdev.NewBrainCapabilities(cfg.LLMClient)
	}
	compiler := wisdev.NewPaper2SkillCompiler(cfg.LLMClient)
	wisdevHandler := NewWisDevHandler(wisdevSessions, wisdevGuided, wisdevWorker, agentGateway, brainCaps, compiler, ragHandler)

	paperProfiler := paper.NewProfiler(cfg.LLMClient)
	pythonBaseURL := wisdev.ResolvePythonBase()
	paperHandler := NewPaperHandler(paperProfiler, pythonBaseURL)

	analysisHandler := NewAnalysisHandler(cfg.LLMClient, nil)
	var citationGrounder *CitationGrounder
	if strings.TrimSpace(os.Getenv("SEMANTIC_SCHOLAR_API_KEY")) != "" {
		citationGrounder = NewCitationGrounder(search.NewSemanticScholarProvider())
	}
	synthesisHandler := NewSynthesisHandler(cfg.LLMClient, citationGrounder)
	llmHandler := NewLLMHandler(cfg.LLMClient)

	healthHandler := NewHealthHandler(cfg.LLMClient)

	searchHandler := NewSearchHandler(cfg.SearchRegistry, cfg.FastRegistry, cfg.Redis)
	var searchIntelligence *search.SearchIntelligence
	if cfg.SearchRegistry != nil {
		searchIntelligence = cfg.SearchRegistry.GetIntelligence()
	}
	topicTreeHandler := NewTopicTreeHandler(cfg.Redis, searchIntelligence)
	gatewayHandler := NewGatewayHandler(agentGateway)
	imageHandler := NewImageHandler(cfg.VertexClient)
	internalOpsHandler := NewInternalOpsHandler(cfg.DB, cfg.Journal)

	// 0. Operational Endpoints
	RegisterRuntimeManifestRoutes(mux, cfg.LLMClient)
	RegisterPaperclipIntegrationRoutes(mux)
	mux.HandleFunc("/healthz", healthHandler.Liveness)
	mux.HandleFunc("/readiness", healthHandler.Readiness)
	mux.Handle("/metrics", telemetry.MetricsHandler())
	mux.HandleFunc("/internal/account/delete", internalOpsHandler.HandleAccountDelete)
	mux.HandleFunc("/internal/billing/stripe/webhook", internalOpsHandler.HandleStripeBillingSync)
	mux.HandleFunc("/internal/billing/subscription", internalOpsHandler.HandleStripeBillingSync)

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		status := "healthy"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "version": cfg.Version})
	})

	// 1. Canonical WisDev routes.
	RegisterWisDevRoutes(mux, agentGateway, ragHandler, nil)
	mux.HandleFunc("/wisdev/paper2skill", wisdevHandler.HandlePaper2Skill)

	// 2. Paper Insight Routes
	mux.HandleFunc("/paper/extract-pdf", paperHandler.HandleExtractPDF)
	mux.HandleFunc("/paper/profile", paperHandler.HandleProfile)
	mux.HandleFunc("/export/markdown", paperHandler.HandleExportMarkdown)
	mux.HandleFunc("/export/html", paperHandler.HandleExportHTML)
	mux.HandleFunc("/export/latex", paperHandler.HandleExportLaTeX)
	mux.HandleFunc("/papers/count", paperHandler.HandleCount)
	mux.HandleFunc("/papers/related", searchHandler.HandleRelatedArticles)
	mux.HandleFunc("/papers/network", paperHandler.HandleGetNetwork)

	// 3. Search Endpoints
	mux.HandleFunc("/search/click", searchHandler.HandleRecordClick)
	mux.HandleFunc("/search/parallel", searchHandler.HandleParallelSearch)
	mux.HandleFunc("/search/hybrid", searchHandler.HandleHybridSearch)
	mux.HandleFunc("/search/opensearch-hybrid", HandleOpenSearchHybrid)
	mux.HandleFunc("/search/batch", searchHandler.HandleBatchSearch)
	mux.HandleFunc("/search/tools", searchHandler.HandleSearchTools)
	mux.HandleFunc("/search/tool", searchHandler.HandleToolSearch)
	mux.HandleFunc("/expand/aggressive", searchHandler.HandleAggressiveExpansion)
	mux.HandleFunc("/expand/splade", searchHandler.HandleSPLADEExpansion)

	// 4. Full Paper Endpoints (Managed by RegisterWisDevRoutes)

	// 4a. Image Endpoints
	mux.HandleFunc("/images/generate", imageHandler.HandleGenerate)

	// 4b. Analysis & Synthesis Endpoints
	mux.HandleFunc("/analysis", analysisHandler.HandleAnalysis)
	mux.HandleFunc("/synthesis", synthesisHandler.HandleSynthesis)
	mux.HandleFunc("/generate", llmHandler.HandleGenerate)
	mux.HandleFunc("/llm/embed", llmHandler.HandleEmbed)
	mux.HandleFunc("/llm/embed/batch", llmHandler.HandleEmbedBatch)

	// 5. WisDev RAG utility routes.
	mux.HandleFunc("/wisdev/rag/answer", ragHandler.HandleAnswer)
	mux.HandleFunc("/wisdev/rag/section-context", ragHandler.HandleSectionContext)
	mux.HandleFunc("/wisdev/rag/raptor/build", ragHandler.HandleRaptorBuild)
	mux.HandleFunc("/wisdev/rag/raptor/query", ragHandler.HandleRaptorQuery)
	mux.HandleFunc("/wisdev/rag/bm25/index", ragHandler.HandleBM25Index)
	mux.HandleFunc("/wisdev/rag/bm25/search", ragHandler.HandleBM25Search)
	mux.HandleFunc("/wisdev/rag/chunking/adaptive", ragHandler.HandleAdaptiveChunking)
	mux.HandleFunc("/extraction/paper", llmHandler.HandlePaperExtraction)

	// 6. Gateway
	if agentGateway != nil {
		gatewayHandler.RegisterRoutes(mux)
	}

	// 6a. External Proxy (mirrors Rust gateway's external_proxy_handler
	// for local dev when bypassing Rust via VITE_GO_ORCHESTRATOR_URL).
	mux.HandleFunc("/api/proxy/external", HandleExternalProxy)

	// 7. Vector Operations
	mux.HandleFunc("/vector/batch-similarity", HandleBatchSimilarity)
	mux.HandleFunc("/vector/fuse", HandleFuseResults)
	mux.HandleFunc("/query/categories", searchHandler.HandleQueryCategories)
	mux.HandleFunc("/query/field", searchHandler.HandleQueryField)
	mux.HandleFunc("/query/introduction", searchHandler.HandleQueryIntroduction)
	mux.HandleFunc("/summarization/batch", searchHandler.HandleBatchSummaries)
	mux.HandleFunc("/source/related", searchHandler.HandleRelatedArticles)

	// 8. Topic Tree
	mux.HandleFunc("/topic-tree/generate", topicTreeHandler.HandleTopicTreeGenerate)
	mux.HandleFunc("/topic-tree/children", topicTreeHandler.HandleTopicTreeChildren)
	mux.HandleFunc("/topic-tree/edges", handleTopicTreeEdges)
	mux.HandleFunc("/topic-tree/queries", topicTreeHandler.HandleTopicTreeQueries)
	mux.HandleFunc("/topic-tree/refine-queries", topicTreeHandler.HandleTopicTreeRefineQueries)

	// 9. YOLO/WisDev Scheduling
	mux.HandleFunc("/wisdev/schedule", wisdevHandler.WisDevScheduleHandler)
	mux.HandleFunc("/wisdev/schedule/run/", wisdevHandler.WisDevScheduleRunHandler)

	// Register MCP (Model Context Protocol) routes.
	// Exposes the WisDev academic search tools via JSON-RPC 2.0 at /wisdev/mcp
	// so external AI agents, ADK clients, and IDEs can call them over HTTP.
	if agentGateway != nil {
		agentGateway.RegisterMCPRoutes(mux, wisdev.MCPRouteConfig{})
	}

	// Build middleware chain.
	handler := otelhttp.NewHandler(
		telemetry.MetricsMiddleware(
			PanicRecoveryMiddleware(
				telemetry.RequestLogger(
					CORSMiddleware(
						RequestTraceContextMiddleware(
							InternalServiceMiddleware(
								AuthMiddleware(
									ResilienceMiddleware(cfg.LLMClient)(mux),
								),
							),
						),
					),
				),
			),
		),
		"scholarlm-go", // span name prefix visible in Cloud Trace
	)

	return handler
}
