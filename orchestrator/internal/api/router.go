package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/paper"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func registerPathAlias(mux *http.ServeMux, aliasPath string, targetPath string) {
	if strings.TrimSpace(aliasPath) == "" || aliasPath == targetPath {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if strings.Contains(strings.ToLower(fmt.Sprint(recovered)), "conflicts with pattern") {
				slog.Warn("skipping duplicate path alias registration", "alias_path", aliasPath, "target_path", targetPath)
				return
			}
			panic(recovered)
		}
	}()
	mux.HandleFunc(aliasPath, func(w http.ResponseWriter, r *http.Request) {
		cloned := r.Clone(r.Context())
		urlCopy := *r.URL
		urlCopy.Path = targetPath
		urlCopy.RawPath = targetPath
		cloned.URL = &urlCopy
		mux.ServeHTTP(w, cloned)
	})
}

func registerJSONPostAlias(
	mux *http.ServeMux,
	aliasPath string,
	targetPath string,
	buildPayload func(*http.Request) map[string]any,
) {
	mux.HandleFunc(aliasPath, func(w http.ResponseWriter, r *http.Request) {
		cloned := r.Clone(r.Context())
		urlCopy := *r.URL
		urlCopy.Path = targetPath
		urlCopy.RawPath = targetPath
		cloned.URL = &urlCopy
		cloned.Method = http.MethodPost

		payloadBytes, err := json.Marshal(buildPayload(r))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to marshal alias payload", map[string]any{
				"aliasPath":  aliasPath,
				"targetPath": targetPath,
				"error":      err.Error(),
			})
			return
		}

		cloned.Body = io.NopCloser(bytes.NewReader(payloadBytes))
		cloned.ContentLength = int64(len(payloadBytes))
		cloned.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payloadBytes)), nil
		}
		cloned.Header = r.Header.Clone()
		cloned.Header.Set("Content-Type", "application/json")

		mux.ServeHTTP(w, cloned)
	})
}

func wrapAcceptedOnSuccess(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		handler(rec, r)

		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		status := rec.Code
		if status == http.StatusOK {
			status = http.StatusAccepted
		}
		w.WriteHeader(status)
		_, _ = w.Write(rec.Body.Bytes())
	}
}

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
	mux.HandleFunc("/healthz", healthHandler.Liveness)
	mux.HandleFunc("/readiness", healthHandler.Readiness)
	mux.Handle("/metrics", telemetry.MetricsHandler())
	mux.HandleFunc("/internal/account/delete", internalOpsHandler.HandleAccountDelete)

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
	mux.HandleFunc("/search", searchHandler.HandleLegacySearch)
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

	// 5. Legacy RAG compatibility aliases. Canonical grounded-answer routes live
	// under /wisdev/research/* and are registered via RegisterWisDevRoutes.
	mux.HandleFunc("/rag/answer", ragHandler.HandleAnswer)
	mux.HandleFunc("/rag/section-context", ragHandler.HandleSectionContext)
	mux.HandleFunc("/rag/raptor/build", ragHandler.HandleRaptorBuild)
	mux.HandleFunc("/rag/raptor/query", ragHandler.HandleRaptorQuery)
	mux.HandleFunc("/rag/bm25/index", ragHandler.HandleBM25Index)
	mux.HandleFunc("/rag/bm25/search", ragHandler.HandleBM25Search)
	mux.HandleFunc("/rag/chunking/adaptive", ragHandler.HandleAdaptiveChunking)

	// 6. Gateway
	if agentGateway != nil {
		gatewayHandler.RegisterRoutes(mux)
	}

	// 6a. External Proxy for local Go-owned academic provider calls.
	mux.HandleFunc("/api/proxy/external", HandleExternalProxy)
	mux.HandleFunc("/external/proxy", HandleExternalProxy) // legacy alias

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

	// 9. YOLO Execution
	mux.HandleFunc("/agent/yolo/execute", YoloExecuteHandler)
	mux.HandleFunc("/agent/yolo/status", YoloStatusHandler)
	mux.HandleFunc("/agent/yolo/stream", YoloStreamHandler)
	mux.HandleFunc("/agent/yolo/cancel", yoloCancelHandler)
	// 9a. YOLO/WisDev Scheduling
	mux.HandleFunc("/wisdev/schedule", wisdevHandler.WisDevScheduleHandler)
	mux.HandleFunc("/wisdev/schedule/run/", wisdevHandler.WisDevScheduleRunHandler)

	registerPathAlias(mux, "/runtime/telemetry/delete", "/telemetry/delete-session")
	registerPathAlias(mux, "/ai", "/generate")

	mux.HandleFunc("/rag", func(w http.ResponseWriter, r *http.Request) {
		action := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
		if action == "embed" {
			llmHandler.HandleEmbed(w, r)
			return
		}
		if action == "embed-batch" || action == "embed_batch" {
			llmHandler.HandleEmbedBatch(w, r)
			return
		}
		WriteError(w, http.StatusNotFound, ErrNotFound, "unsupported rag action", map[string]any{
			"allowedActions": []string{"embed", "embed-batch"},
		})
	})

	registerPathAlias(mux, "/api/v1/export/markdown", "/export/markdown")
	registerPathAlias(mux, "/api/v1/export/html", "/export/html")
	registerPathAlias(mux, "/api/v1/export/latex", "/export/latex")
	registerPathAlias(mux, "/api/v1/images/generate", "/images/generate")
	registerPathAlias(mux, "/api/v1/papers/network", "/papers/network")

	registerPathAlias(mux, "/api/v1/ai", "/generate")
	registerPathAlias(mux, "/api/v1/ai/generate", "/generate")

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
		"wisdev-go", // span name prefix visible in Cloud Trace
	)

	return handler
}
